package otlp

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/eduardorarruda/revoada/gateway/internal/model"
	"github.com/eduardorarruda/revoada/gateway/internal/pg"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	cpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	mpb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// maxOTLPBody limita o tamanho do corpo aceito (aplicado ao conteúdo DESCOMPRIMIDO).
const maxOTLPBody = 16 << 20 // 16 MiB

// readOTLPBody lê o corpo da requisição OTLP/HTTP, descomprimindo transparentemente
// quando Content-Encoding: gzip (muitos SDKs OTel ligam gzip por padrão). O limite de
// 16 MiB é aplicado ao conteúdo já descomprimido.
func readOTLPBody(r *http.Request) ([]byte, error) {
	if enc := r.Header.Get("Content-Encoding"); enc == "gzip" {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, err
		}
		defer func() { _ = gz.Close() }()
		return io.ReadAll(io.LimitReader(gz, maxOTLPBody))
	}
	return io.ReadAll(io.LimitReader(r.Body, maxOTLPBody))
}

// isJSONContentType decide, de forma tolerante, se o corpo está em OTLP/JSON. Aceita
// "application/json" com quaisquer parâmetros (ex.: "; charset=utf-8"); qualquer outra
// coisa (vazio, application/x-protobuf, inválido) é tratada como protobuf.
func isJSONContentType(ct string) bool {
	mediatype, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return mediatype == "application/json"
}

// Defaults de amostragem de traces (usados quando não há env ou o Receiver é criado
// sem config — ex.: struct literal em testes).
const (
	defaultSampleFrac = 0.2    // 20% dos traces "normais" mantidos
	defaultSlowMs     = 1000.0 // 1s: acima disso o trace é sempre mantido
)

// loadTraceSampling lê a config de amostragem das envs REVOADA_TRACE_SAMPLE (fração
// 0.0–1.0, default 0.2) e REVOADA_TRACE_SAMPLE_SLOW_MS (float ms, default 1000).
// Valores inválidos ou fora de faixa caem no default.
func loadTraceSampling() (sampleFrac, slowMs float64) {
	sampleFrac, slowMs = defaultSampleFrac, defaultSlowMs
	if v := os.Getenv("REVOADA_TRACE_SAMPLE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			sampleFrac = f
		}
	}
	if v := os.Getenv("REVOADA_TRACE_SAMPLE_SLOW_MS"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			slowMs = f
		}
	}
	return sampleFrac, slowMs
}

// Publisher publica lotes de métricas (implementado por queue.Queue).
type Publisher interface {
	PublishMetrics(ctx context.Context, metrics []model.Metric) error
}

// Authenticator resolve a chave de ingestão (tabela agents).
type Authenticator interface {
	Authenticate(ctx context.Context, key string) (pg.Agent, error)
}

// LogSink grava lotes de logs (implementado por chhttp.Client).
type LogSink interface {
	InsertLogs(ctx context.Context, rows []model.LogRecord) error
}

// SpanSink grava lotes de spans (implementado por chhttp.Client, P5.3).
type SpanSink interface {
	InsertSpans(ctx context.Context, rows []model.Span) error
}

// LogEnqueuer enfileira logs no batcher assíncrono (implementado por
// chbatch.Batcher[model.LogRecord]). Enqueue não bloqueia: false => backpressure.
type LogEnqueuer interface {
	Enqueue(rows []model.LogRecord) bool
}

// SpanEnqueuer enfileira spans no batcher assíncrono (chbatch.Batcher[model.Span]).
type SpanEnqueuer interface {
	Enqueue(rows []model.Span) bool
}

// HostSink registra o inventário de host (implementado por pg.Store). Sem ele, hosts
// que reportam por OTLP não aparecem no Infraestrutura.
type HostSink interface {
	UpsertHost(ctx context.Context, h model.Host) error
}

// Receiver ingere métricas, logs e traces OTLP (HTTP e gRPC).
type Receiver struct {
	log       *slog.Logger
	auth      Authenticator
	pub       Publisher
	logs      LogSink
	spans     SpanSink
	logBatch  LogEnqueuer  // se != nil, logs vão assíncronos (202/429); senão, sync
	spanBatch SpanEnqueuer // idem para spans
	hosts     HostSink
	now       func() time.Time

	// Amostragem de traces (configurável via env; ver loadTraceSampling).
	sampleFrac float64 // fração 0.0–1.0 dos traces "normais" mantidos
	slowMs     float64 // acima deste tempo (ms) o trace é sempre mantido

	// dec guarda a decisão de amostragem POR TRACE numa janela curta, para que um
	// trace partido em vários lotes não seja julgado de novo a cada requisição (ver
	// tracedecision.go). Criado sob demanda: há Receivers montados por struct literal.
	decMu sync.Mutex
	dec   *traceDecider

	// hostBind: política de amarração de host (anti-spoofing). Off por padrão.
	hostBind HostBindMode

	// clock é a faixa de timestamps aceita (ver guard.go). Zero-value = sem guarda,
	// para não quebrar Receivers montados por struct literal em teste; NewReceiver
	// sempre preenche.
	clock clockWindow
	// series é o teto de cardinalidade por serverkey (ver guard.go). nil = desligado.
	series *seriesLimiter
}

// window devolve a faixa de relógio efetiva (default seguro quando o Receiver foi
// montado por struct literal, como nos testes).
func (rc *Receiver) window() clockWindow {
	if rc.clock.future <= 0 || rc.clock.past <= 0 {
		return clockWindow{future: defaultClockFuture, past: defaultClockPast}
	}
	return rc.clock
}

// SetHostBinding define a política de amarração de host (anti-spoofing). Off por
// padrão; ligar exige que agents.hostname reflita o hostname real de cada chave.
func (rc *Receiver) SetHostBinding(m HostBindMode) { rc.hostBind = m }

// NewReceiver cria o receiver.
func NewReceiver(log *slog.Logger, auth Authenticator, pub Publisher) *Receiver {
	sampleFrac, slowMs := loadTraceSampling()
	log.Info("otlp traces sampling", "sample_frac", sampleFrac, "slow_ms", slowMs)
	sl := newSeriesLimiter(maxSeriesPerKeyWindow, seriesWindow)
	seriesSeenGauge.set(sl)
	rc := &Receiver{
		log: log, auth: auth, pub: pub, now: time.Now,
		sampleFrac: sampleFrac, slowMs: slowMs,
		clock:  clockWindow{future: defaultClockFuture, past: defaultClockPast},
		series: sl,
	}
	log.Info("otlp guardas de ingestão",
		"clock_future", rc.clock.future, "clock_past", rc.clock.past,
		"max_points_req", maxPointsPerRequest, "max_labels_point", maxLabelsPerPoint,
		"max_series_key_janela", maxSeriesPerKeyWindow, "janela", seriesWindow)
	return rc
}

// SetSinks liga os destinos de logs e traces (ClickHouse direto). Opcional: sem
// eles, /v1/logs e /v1/traces respondem 503.
func (rc *Receiver) SetSinks(logs LogSink, spans SpanSink) {
	rc.logs = logs
	rc.spans = spans
}

// SetHostSink liga o inventário de host (popula a tabela `hosts` a partir do OTLP).
func (rc *Receiver) SetHostSink(h HostSink) { rc.hosts = h }

// SetLogBatcher e SetSpanBatcher ligam a ingestão ASSÍNCRONA de logs/traces. Com eles,
// os handlers enfileiram e respondem 202 (ou 429 sob backpressure) em vez de gravar
// no ClickHouse de forma síncrona. Sem eles, mantém-se o caminho síncrono (fallback).
func (rc *Receiver) SetLogBatcher(b LogEnqueuer)   { rc.logBatch = b }
func (rc *Receiver) SetSpanBatcher(b SpanEnqueuer) { rc.spanBatch = b }

// authTenant foi removida: era uma segunda autenticação que NÃO devolvia o hostname
// vinculado à chave, e por isso não dava para aplicar a amarração de host nem preencher
// o `host` das linhas. Ninguém a chamava mais (todos os caminhos usam authAgent), e
// deixá-la de pé era um convite a reintroduzir a linha de telemetria sem dono.

// authAgent resolve o tenant a partir da chave e também devolve o hostname vinculado a ela, para
// a amarração de host (anti-spoofing) no caminho de métricas. O authCache dedup a
// consulta, então chamar isto em vez de authTenant não custa uma query a mais.
func (rc *Receiver) authAgent(ctx context.Context, key string) (tenant, host string, status int, ok bool) {
	if key == "" {
		return "", "", http.StatusUnauthorized, false
	}
	agent, err := rc.auth.Authenticate(ctx, key)
	switch {
	case errors.Is(err, pg.ErrUnknownAgent):
		return "", "", http.StatusUnauthorized, false
	case err != nil:
		return "", "", http.StatusInternalServerError, false
	case agent.Revoked:
		return "", "", http.StatusForbidden, false
	}
	return agent.TenantID, agent.Hostname, http.StatusOK, true
}

// ingestOutcome é o resultado de uma requisição de métricas: o status HTTP a
// responder e, quando parcial, quantas linhas foram recusadas e por quê.
type ingestOutcome struct {
	status   int    // 200 | 413 | 429 | 503
	rejected int64  // linhas recusadas (vai no partial_success do OTLP)
	message  string // motivo legível ("clock_future=3, label_cap=1")
}

// countDataPoints conta os data points do request SEM converter. Serve para aplicar o
// teto por requisição antes de gastar memória com a conversão de um payload absurdo.
func countDataPoints(req *cpb.ExportMetricsServiceRequest) int {
	n := 0
	for _, rm := range req.GetResourceMetrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				switch data := m.GetData().(type) {
				case *mpb.Metric_Gauge:
					n += len(data.Gauge.GetDataPoints())
				case *mpb.Metric_Sum:
					n += len(data.Sum.GetDataPoints())
				}
			}
		}
	}
	return n
}

// process converte e publica. `bound` é o hostname vinculado à chave (amarração
// anti-spoofing; "" desliga) e `key` é a serverkey (balde do teto de cardinalidade).
//
// Ordem deliberada: teto de pontos (antes de converter) → amarração de host →
// inventário → conversão → guarda de relógio + teto de labels → teto de séries →
// publicação. Cada recusa é contada em /metrics e devolvida ao cliente no
// partial_success — nada é descartado em silêncio.
func (rc *Receiver) process(ctx context.Context, tenant, bound, key string, req *cpb.ExportMetricsServiceRequest) (ingestOutcome, error) {
	now := rc.now().UTC()

	// Teto de data points por requisição (ver maxPointsPerRequest em guard.go).
	if n := countDataPoints(req); n > maxPointsPerRequest {
		rejectedRequests.With(signalMetrics, reasonPointsCap).Inc()
		rejectedPoints.With(signalMetrics, reasonPointsCap).Add(int64(n))
		rc.log.Warn("otlp: requisição acima do teto de data points",
			"host", bound, "pontos", n, "teto", maxPointsPerRequest)
		return ingestOutcome{
			status:   http.StatusRequestEntityTooLarge,
			rejected: int64(n),
			message:  "acima do teto de data points por requisição",
		}, nil
	}

	// Amarração de host (no-op por padrão): impede uma chave de reportar métricas
	// como se fossem de outro host. Aplicada ANTES da conversão/inventário.
	applyHostBindMetrics(rc.hostBind, bound, req)
	// Registra/atualiza o inventário do host. O sink é COALESCENTE (ver HostCoalescer):
	// isto é uma escrita em memória, não um UPDATE no Postgres — o caminho quente da
	// ingestão não pode depender do PG (medido: 50 hosts distintos = 307 POST/s contra
	// 118 POST/s com 1 host único, 2,6× de queda só por contenção de lock na mesma linha).
	if rc.hosts != nil {
		for _, rm := range req.GetResourceMetrics() {
			if h, ok := hostFromResource(tenant, rm, now); ok {
				if err := rc.hosts.UpsertHost(ctx, h); err != nil {
					rc.log.Warn("otlp: upsert de host", "host", h.Hostname, "err", err)
				}
			}
		}
	}

	points := FromResourceMetrics(tenant, req.GetResourceMetrics())
	for i := range points {
		if points[i].TS.IsZero() {
			points[i].TS = now
		}
	}
	// ANTES do filtro: o host cujo relógio está de fato quebrado tem TODOS os pontos
	// recusados pela guarda, e medir depois o deixava sem série nenhuma — o único que
	// realmente precisava aparecer era o único invisível. Ver skew.go.
	observeClockSkew(points, now, bound)
	points, fps, rejected := filterMetrics(points, now, rc.window())
	nRejected, msg := rejectionSummary(rejected)
	if nRejected > 0 {
		rc.log.Warn("otlp: linhas de métrica recusadas", "host", bound, "motivos", msg)
	}

	// Teto de séries distintas por serverkey na janela (429: problema é do emissor,
	// e o agente já sabe re-bufferar em erro).
	if rc.series != nil && !rc.series.observe(key, fps) {
		rejectedRequests.With(signalMetrics, reasonSeriesCap).Inc()
		rejectedPoints.With(signalMetrics, reasonSeriesCap).Add(int64(len(points)))
		rc.log.Warn("otlp: teto de cardinalidade por chave estourado",
			"host", bound, "teto", maxSeriesPerKeyWindow, "janela", seriesWindow)
		return ingestOutcome{
			status:   http.StatusTooManyRequests,
			rejected: int64(len(points)) + nRejected,
			message:  "teto de séries distintas por chave estourado",
		}, nil
	}

	if err := rc.pub.PublishMetrics(ctx, points); err != nil {
		return ingestOutcome{status: http.StatusServiceUnavailable}, err
	}
	return ingestOutcome{status: http.StatusOK, rejected: nRejected, message: msg}, nil
}

// HTTPHandler trata POST /v1/metrics (application/x-protobuf ou application/json).
func (rc *Receiver) HTTPHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-Revoada-Key")
		tenant, bound, status, ok := rc.authAgent(r.Context(), key)
		if !ok {
			http.Error(w, http.StatusText(status), status)
			return
		}
		body, err := readOTLPBody(r)
		if err != nil {
			http.Error(w, "corpo inválido", http.StatusBadRequest)
			return
		}
		req := &cpb.ExportMetricsServiceRequest{}
		if isJSONContentType(r.Header.Get("Content-Type")) {
			err = protojson.Unmarshal(body, req)
		} else { // application/x-protobuf (padrão OTLP/HTTP)
			err = proto.Unmarshal(body, req)
		}
		if err != nil {
			http.Error(w, "payload OTLP inválido: "+err.Error(), http.StatusBadRequest)
			return
		}
		out, err := rc.process(r.Context(), tenant, bound, key, req)
		switch out.status {
		case http.StatusServiceUnavailable:
			rc.log.Warn("otlp: publicando métricas", "err", err)
			http.Error(w, "erro publicando", out.status)
			return
		case http.StatusRequestEntityTooLarge, http.StatusTooManyRequests:
			http.Error(w, out.message, out.status)
			return
		}
		// Resposta OTLP. Quando houve recusa parcial (relógio fora de faixa, teto de
		// labels), o contrato do OTLP manda dizer QUANTAS linhas caíram e por quê no
		// partial_success — é assim que a recusa deixa de ser silenciosa para o emissor,
		// sem transformar um lote parcialmente ruim em erro e loop de reenvio.
		resp := &cpb.ExportMetricsServiceResponse{}
		if out.rejected > 0 {
			resp.PartialSuccess = &cpb.ExportMetricsPartialSuccess{
				RejectedDataPoints: out.rejected,
				ErrorMessage:       out.message,
			}
		}
		b, _ := proto.Marshal(resp)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
	}
}

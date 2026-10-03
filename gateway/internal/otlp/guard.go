package otlp

import (
	"hash/fnv"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/eduardorarruda/revoada/gateway/internal/metrics"
	"github.com/eduardorarruda/revoada/gateway/internal/model"
)

// ---------------------------------------------------------------------------
// Contadores de recusa. Nenhum descarte deste arquivo é silencioso: tudo aparece
// em /metrics (e o handler ainda devolve partial_success ao cliente OTLP).
// ---------------------------------------------------------------------------

var (
	// rejectedPoints conta LINHAS recusadas (data point, log record ou span).
	// signal ∈ {metrics, logs, spans}; reason ∈ {clock_future, clock_too_old, label_cap}.
	rejectedPoints = metrics.NewCounterVec(
		"revoada_ingest_rejected_points_total",
		"Linhas de telemetria recusadas na ingestão, por sinal e motivo.",
		"signal", "reason")

	// rejectedRequests conta REQUISIÇÕES recusadas inteiras (429/413).
	// reason ∈ {points_cap, series_cap}.
	rejectedRequests = metrics.NewCounterVec(
		"revoada_ingest_rejected_requests_total",
		"Requisições de ingestão recusadas inteiras, por sinal e motivo.",
		"signal", "reason")

	// acceptedPoints dá o denominador: sem ele, "1000 recusados" não diz se é
	// ruído ou se a ingestão inteira está no chão.
	acceptedPoints = metrics.NewCounterVec(
		"revoada_ingest_accepted_points_total",
		"Linhas de telemetria aceitas na ingestão, por sinal.",
		"signal")

	// seriesTracked é a cardinalidade viva vista pelo teto de séries (janela corrente).
	seriesSeenGauge = &seriesGauge{}
)

const (
	signalMetrics = "metrics"
	signalLogs    = "logs"
	signalSpans   = "spans"

	reasonClockFuture = "clock_future"
	reasonClockOld    = "clock_too_old"
	reasonLabelCap    = "label_cap"
	reasonPointsCap   = "points_cap"
	reasonSeriesCap   = "series_cap"
)

// ---------------------------------------------------------------------------
// 1) Guarda de relógio
// ---------------------------------------------------------------------------

// clockWindow é a faixa de timestamps aceita na ingestão.
//
// POR QUE: até esta versão o ts vinha do cliente e era gravado como veio (só o ts
// ZERADO ganhava "agora"). Medido em dev com serverkey real: ts = agora+1h → HTTP 200
// e gravado; ts = agora+10 anos → HTTP 200 e gravado em 2036-08-06. O estrago é a
// jusante e é grave: as consultas de "último valor" do painel usam argMax(value, ts),
// então o ponto do futuro VENCE PARA SEMPRE; ao mesmo tempo o gráfico e o avaliador
// de alertas filtram ts < now e param de enxergar a série, o avaliador auto-resolve o
// alerta com "sem dados" e dispara notificação de RESOLVIDO para um problema vivo.
//
// Do outro lado: um ponto de 30 dias atrás foi aceito com 200 e apagado pelo TTL de 7
// dias da tabela `metrics` logo em seguida — trabalho puro (parse, NATS, insert, merge)
// para uma linha que já nasce morta.
type clockWindow struct {
	future time.Duration // tolerância para frente
	past   time.Duration // tolerância para trás
}

// Tolerâncias padrão.
//
//   - FUTURO = 2 min. Cobre o desvio de relógio normal de uma VM sem NTP apertado e o
//     tempo de voo do lote; qualquer coisa além disso é relógio quebrado, não latência.
//     O agente coleta a cada 15s, então 2 min é 8 ticks de folga.
//
//   - PASSADO = 7 dias, que é exatamente o TTL da tabela `metrics` (ver
//     deploy/migrations/clickhouse/001_metrics.up.sql). Abaixo disso a linha bruta é
//     apagada pelo TTL no primeiro merge — aceitar é gastar CPU para nada. A folga é
//     grande para o caso legítimo: o WAL em disco do agente guarda 5000 lotes a ticks
//     de 15s ≈ 20,8 h de fila (agent/internal/config: defaultBufferFiles), então uma
//     queda de quase um dia ainda drena inteira, com ~8× de margem.
const (
	defaultClockFuture = 2 * time.Minute
	defaultClockPast   = 7 * 24 * time.Hour
)

// check classifica um ts. Devolve "" quando o ponto está na faixa.
func (w clockWindow) check(ts, now time.Time) string {
	if ts.After(now.Add(w.future)) {
		return reasonClockFuture
	}
	if ts.Before(now.Add(-w.past)) {
		return reasonClockOld
	}
	return ""
}

// ---------------------------------------------------------------------------
// 2) Teto de cardinalidade
// ---------------------------------------------------------------------------

// Tetos de cardinalidade.
//
// POR QUE: não havia teto NENHUM — nem de séries por requisição, nem de labels por
// ponto, nem de séries novas por chave. Medido em dev: UMA requisição de 391 KiB com
// 3000 data points criou 3000 séries distintas em 0,08 s; com o teto de corpo de 16 MiB
// isso daria ~85 mil séries por requisição, a 500 r/s. É a forma mais barata de matar o
// ClickHouse (cada série vira uma chave nova em ORDER BY (tenant, metric, labels, ts)
// nos rollups) e NÃO aparece como "muitas requisições" em nenhum gráfico.
//
// Dimensionamento (folga deliberada para o caso legítimo):
//
//   - maxPointsPerRequest = 50 000. O caso legítimo mais pesado que conhecemos é um host
//     de containers: ~300 containers × ~15 métricas + ~200 métricas de host ≈ 4,7 mil
//     pontos por tick. 50 mil é ~10× isso. O writer já assume "≈10k linhas no teto" por
//     mensagem NATS (queue/nats.go), e o lote é fragmentado, então 50k não estoura nada
//     a jusante.
//
//   - maxLabelsPerPoint = 64. As convenções OTel (resource semconv + atributos de ponto)
//     ficam na casa de 15–25 chaves; o agente do painel usa menos de 20. 64 é ~3× o pior
//     caso real. Acima disso o ponto é recusado INTEIRO — nunca truncado: truncar labels
//     mudaria a IDENTIDADE da série (o fingerprint do alerta sai do mapa de labels
//     inteiro) e recriaria, calada, a mesma falha que o dropHostInventory corrigiu.
//
//   - maxSeriesPerKeyWindow = 20 000 séries distintas por serverkey por hora. Um host de
//     containers em regime tem ~5 mil séries vivas; 20 mil absorve o churn de um dia de
//     deploys sem encostar no teto. Estourar devolve 429 (o agente re-bufferar é o
//     comportamento certo: o problema é do emissor).
const (
	maxPointsPerRequest   = 50000
	maxLabelsPerPoint     = 64
	maxSeriesPerKeyWindow = 20000
	seriesWindow          = time.Hour
	maxSeriesKeysTracked  = 2000 // tetos de chaves rastreadas (memória do próprio limitador)
)

// seriesLimiter conta séries DISTINTAS por serverkey dentro de uma janela em cascata.
//
// A memória é limitada por construção: o conjunto por chave nunca passa de
// maxSeriesPerKeyWindow entradas (ao chegar lá o gateway já está devolvendo 429), e o
// número de chaves rastreadas é limitado por maxSeriesKeysTracked com despejo da chave
// mais antiga. Em regime, o conjunto tem o tamanho da cardinalidade REAL do host
// (milhares), não o do teto.
type seriesLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	maxKeys int
	now     func() time.Time
	buckets map[string]*seriesBucket
}

type seriesBucket struct {
	seen     map[uint64]struct{}
	resetAt  time.Time
	lastUsed time.Time
}

func newSeriesLimiter(limit int, window time.Duration) *seriesLimiter {
	if limit < 1 {
		limit = maxSeriesPerKeyWindow
	}
	if window <= 0 {
		window = seriesWindow
	}
	return &seriesLimiter{
		limit: limit, window: window, maxKeys: maxSeriesKeysTracked,
		now: time.Now, buckets: map[string]*seriesBucket{},
	}
}

// observe registra os fingerprints de série de uma requisição para a chave key.
// Devolve false quando a chave estourou o teto na janela corrente (=> 429).
func (sl *seriesLimiter) observe(key string, fps []uint64) bool {
	now := sl.now()
	sl.mu.Lock()
	defer sl.mu.Unlock()

	b, ok := sl.buckets[key]
	if !ok {
		sl.evictLocked(now)
		b = &seriesBucket{seen: make(map[uint64]struct{}), resetAt: now.Add(sl.window)}
		sl.buckets[key] = b
	}
	if now.After(b.resetAt) { // janela em cascata: zera e recomeça
		b.seen = make(map[uint64]struct{})
		b.resetAt = now.Add(sl.window)
	}
	b.lastUsed = now
	for _, fp := range fps {
		if _, dup := b.seen[fp]; dup {
			continue
		}
		if len(b.seen) >= sl.limit {
			return false
		}
		b.seen[fp] = struct{}{}
	}
	return true
}

// evictLocked mantém o número de chaves rastreadas sob o teto, removendo a menos
// recentemente usada (o limitador não pode virar, ele próprio, um vazamento).
func (sl *seriesLimiter) evictLocked(now time.Time) {
	if len(sl.buckets) < sl.maxKeys {
		return
	}
	var oldestKey string
	var oldest time.Time
	for k, b := range sl.buckets {
		if oldestKey == "" || b.lastUsed.Before(oldest) {
			oldestKey, oldest = k, b.lastUsed
		}
	}
	delete(sl.buckets, oldestKey)
}

// stats devolve (chaves rastreadas, séries somadas) — exposto em /metrics.
func (sl *seriesLimiter) stats() (keys, series int) {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	for _, b := range sl.buckets {
		series += len(b.seen)
	}
	return len(sl.buckets), series
}

// seriesGauge liga o seriesLimiter ativo aos medidores do /metrics (registrados uma
// única vez, no init, e apontando para o limitador corrente).
type seriesGauge struct {
	mu sync.RWMutex
	sl *seriesLimiter
}

func (g *seriesGauge) set(sl *seriesLimiter) {
	g.mu.Lock()
	g.sl = sl
	g.mu.Unlock()
}

func (g *seriesGauge) read() *seriesLimiter {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.sl
}

func init() {
	metrics.NewGaugeFunc("revoada_ingest_series_tracked",
		"Séries distintas rastreadas pelo teto de cardinalidade na janela corrente.",
		func() float64 {
			sl := seriesSeenGauge.read()
			if sl == nil {
				return 0
			}
			_, series := sl.stats()
			return float64(series)
		})
	metrics.NewGaugeFunc("revoada_ingest_series_keys_tracked",
		"Serverkeys rastreadas pelo teto de cardinalidade.",
		func() float64 {
			sl := seriesSeenGauge.read()
			if sl == nil {
				return 0
			}
			keys, _ := sl.stats()
			return float64(keys)
		})
}

// seriesFingerprint identifica uma série (métrica + conjunto de labels) de forma
// determinística e independente da ordem do mapa.
func seriesFingerprint(m model.Metric) uint64 {
	keys := make([]string, 0, len(m.Labels))
	for k := range m.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := fnv.New64a()
	_, _ = h.Write([]byte(m.Metric))
	for _, k := range keys {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(k))
		_, _ = h.Write([]byte{1})
		_, _ = h.Write([]byte(m.Labels[k]))
	}
	return h.Sum64()
}

// ---------------------------------------------------------------------------
// Aplicação: filtro de métricas
// ---------------------------------------------------------------------------

// filterMetrics aplica, em uma passada, a guarda de relógio e o teto de labels por
// ponto. Devolve as métricas aceitas, os fingerprints das séries aceitas e o mapa
// motivo→quantidade das recusadas (para o partial_success e para o log).
func filterMetrics(in []model.Metric, now time.Time, w clockWindow) (out []model.Metric, fps []uint64, rejected map[string]int) {
	out = in[:0:0]
	fps = make([]uint64, 0, len(in))
	rejected = map[string]int{}
	for _, m := range in {
		if len(m.Labels) > maxLabelsPerPoint {
			rejected[reasonLabelCap]++
			rejectedPoints.With(signalMetrics, reasonLabelCap).Inc()
			continue
		}
		if r := w.check(m.TS, now); r != "" {
			rejected[r]++
			rejectedPoints.With(signalMetrics, r).Inc()
			continue
		}
		out = append(out, m)
		fps = append(fps, seriesFingerprint(m))
	}
	acceptedPoints.With(signalMetrics).Add(int64(len(out)))
	return out, fps, rejected
}

// rejectionSummary transforma o mapa de recusas numa mensagem estável para o campo
// error_message do partial_success do OTLP (o cliente vê o motivo, não só o número).
func rejectionSummary(rejected map[string]int) (int64, string) {
	if len(rejected) == 0 {
		return 0, ""
	}
	reasons := make([]string, 0, len(rejected))
	var total int64
	for r, n := range rejected {
		reasons = append(reasons, r+"="+strconv.Itoa(n))
		total += int64(n)
	}
	sort.Strings(reasons)
	msg := "linhas recusadas pelo gateway: "
	for i, r := range reasons {
		if i > 0 {
			msg += ", "
		}
		msg += r
	}
	return total, msg
}

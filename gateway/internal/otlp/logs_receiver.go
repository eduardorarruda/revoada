package otlp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/gateway/internal/model"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	clpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
)

// severityNum mapeia um rótulo de severidade textual para o número OTLP aproximado.
func severityNum(text string) uint8 {
	switch strings.ToUpper(strings.TrimSpace(text)) {
	case "FATAL", "CRIT", "CRITICAL":
		return 21
	case "ERROR", "ERR":
		return 17
	case "WARN", "WARNING":
		return 13
	case "INFO", "NOTICE":
		return 9
	case "DEBUG":
		return 5
	case "TRACE":
		return 1
	default:
		return 0
	}
}

// jsonLogLine é uma linha do formato NDJSON amigável a agentes (Vector, scripts).
type jsonLogLine struct {
	TS       string            `json:"ts"` // RFC3339 opcional
	Service  string            `json:"service"`
	Severity string            `json:"severity"`
	Body     string            `json:"body"`
	Message  string            `json:"message"` // alias comum de body
	Labels   map[string]string `json:"labels"`
	TraceID  string            `json:"trace_id"`
	SpanID   string            `json:"span_id"`
}

// maxNDJSONLines limita quantas linhas um único POST /ingest/logs pode trazer.
// Mesmo raciocínio do teto de data points: sem teto, um corpo de 16 MiB vira dezenas
// de milhares de linhas numa requisição só. 20 mil linhas por POST é ~10× o lote que o
// agente manda num tick de logtail.
const maxNDJSONLines = 20000

// batchSpread é o intervalo TOTAL que um lote de logs sem timestamp próprio ocupa
// para trás, a partir de "agora".
//
// POR QUE: o gateway carimbava rc.now() UMA vez por requisição e aplicava ao lote
// inteiro — medido: 12 linhas com ts idêntico 14:36:03.101, e aí `ORDER BY ts` não
// reconstrói a ordem original, o visualizador embaralha as linhas e um stacktrace
// aparece antes da mensagem que o causou. A coluna ts é DateTime64(3) (milissegundo),
// então o deslocamento mínimo que SOBREVIVE à gravação é 1 ms.
//
// O deslocamento é para TRÁS (a última linha fica em "agora") por dois motivos: não
// inventar registro no futuro — que a guarda de relógio recusaria, com razão — e
// porque a última linha lida é de fato a mais recente.
const batchSpread = time.Second

// batchTimestamps devolve n timestamps monotonicamente crescentes terminando em `now`,
// com no mínimo 1 ms de separação enquanto couber em batchSpread. Lotes maiores que
// batchSpread/1ms compartilham milissegundo (a ordem dentro do ms se perde, mas o
// grosso da ordem é preservado) — é o melhor possível sem ts por linha do emissor.
func batchTimestamps(now time.Time, n int) []time.Time {
	out := make([]time.Time, n)
	if n == 0 {
		return out
	}
	step := time.Millisecond
	if total := time.Duration(n-1) * step; total > batchSpread {
		step = batchSpread / time.Duration(n-1)
	}
	for i := 0; i < n; i++ {
		out[i] = now.Add(-time.Duration(n-1-i) * step)
	}
	return out
}

// JSONLogsHandler trata POST /ingest/logs: NDJSON (uma linha JSON por log), pensado
// para Vector (http sink) e scripts, sem precisar codificar OTLP.
func (rc *Receiver) JSONLogsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if rc.logs == nil {
			http.Error(w, "ingestão de logs desabilitada", http.StatusServiceUnavailable)
			return
		}
		// authAgent (e não authTenant): precisamos do hostname vinculado à chave para
		// amarrar/preencher o host das linhas — /ingest/logs nem chamava ensureHostLabel.
		tenant, bound, st, ok := rc.authAgent(r.Context(), r.Header.Get("X-Revoada-Key"))
		if !ok {
			http.Error(w, http.StatusText(st), st)
			return
		}
		now := rc.now().UTC()
		var rows []model.LogRecord
		var semTS []int // índices das linhas sem ts próprio (recebem o escalonamento)
		sc := bufio.NewScanner(io.LimitReader(r.Body, 16<<20))
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			if len(rows) >= maxNDJSONLines {
				rejectedRequests.With(signalLogs, reasonPointsCap).Inc()
				http.Error(w, "acima do teto de linhas por requisição", http.StatusRequestEntityTooLarge)
				return
			}
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var jl jsonLogLine
			if err := json.Unmarshal([]byte(line), &jl); err != nil {
				continue // linha malformada: ignora (não derruba o lote)
			}
			body := jl.Body
			if body == "" {
				body = jl.Message
			}
			var ts time.Time
			if jl.TS != "" {
				// O ts por linha do emissor SEMPRE vence — é a ordem real da origem.
				if parsed, err := time.Parse(time.RFC3339Nano, jl.TS); err == nil {
					ts = parsed.UTC()
				} else if parsed, err := time.Parse(time.RFC3339, jl.TS); err == nil {
					ts = parsed.UTC()
				}
			}
			labels := jl.Labels
			if labels == nil {
				labels = map[string]string{}
			}
			rec := model.LogRecord{
				TenantID: tenant, TS: ts, Service: jl.Service,
				Body: body, Labels: labels, TraceID: jl.TraceID, SpanID: jl.SpanID,
			}
			applySeverity(&rec, jl.Severity, 0)
			if ts.IsZero() {
				semTS = append(semTS, len(rows))
			}
			rows = append(rows, rec)
		}
		// Linhas sem ts próprio ganham um deslocamento monotônico dentro do lote, para
		// que ORDER BY ts reconstrua a ordem em que chegaram.
		stamps := batchTimestamps(now, len(semTS))
		for i, idx := range semTS {
			rows[idx].TS = stamps[i]
		}

		rows, rejected := rc.prepareLogs(bound, rows, now)
		code, err := rc.ingestLogs(r.Context(), rows)
		switch code {
		case http.StatusTooManyRequests:
			http.Error(w, "ingestão sobrecarregada, retente", code)
			return
		case http.StatusServiceUnavailable:
			rc.log.Warn("ingest: gravando logs", "err", err)
			http.Error(w, "erro gravando logs", code)
			return
		}
		writeJSONOK(w, code, len(rows), rejected)
	}
}

// prepareLogs aplica, às linhas já convertidas, a política de host (amarração +
// preenchimento) e a guarda de relógio. Devolve as linhas aceitas e o mapa
// motivo→quantidade das recusadas.
func (rc *Receiver) prepareLogs(bound string, rows []model.LogRecord, now time.Time) ([]model.LogRecord, map[string]int) {
	w := rc.window()
	rejected := map[string]int{}
	out := rows[:0]
	for i := range rows {
		if applyHostBindLabels(rc.hostBind, bound, rows[i].Labels) {
			rejected["host_bind"]++
			hostDropped.Inc()
			continue
		}
		if len(rows[i].Labels) > maxLabelsPerPoint {
			rejected[reasonLabelCap]++
			rejectedPoints.With(signalLogs, reasonLabelCap).Inc()
			continue
		}
		if r := w.check(rows[i].TS, now); r != "" {
			rejected[r]++
			rejectedPoints.With(signalLogs, r).Inc()
			continue
		}
		out = append(out, rows[i])
	}
	acceptedPoints.With(signalLogs).Add(int64(len(out)))
	if len(rejected) > 0 {
		_, msg := rejectionSummary(rejected)
		rc.log.Warn("ingestão: linhas de log recusadas", "host", bound, "motivos", msg)
	}
	return out, rejected
}

func writeJSONOK(w http.ResponseWriter, status, n int, rejected map[string]int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status) // 200 no sucesso (síncrono ou enfileirado)
	// A recusa aparece na RESPOSTA, não só no /metrics: quem manda o lote precisa
	// saber que parte dele não entrou (e por quê) sem ter acesso ao /metrics.
	body := `{"ingested":` + itoa(n)
	if total, msg := rejectionSummary(rejected); total > 0 {
		body += `,"rejected":` + itoa(int(total)) + `,"reason":"` + msg + `"`
	}
	_, _ = w.Write([]byte(body + "}"))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// buildLogRows converte o request OTLP em linhas; aplica um "agora" ESCALONADO aos
// records sem timestamp (preservando a ordem do lote) e, em seguida, a política de
// host e a guarda de relógio. Devolve as linhas aceitas e o mapa de recusas.
func (rc *Receiver) buildLogRows(tenant, bound string, req *clpb.ExportLogsServiceRequest) ([]model.LogRecord, map[string]int) {
	rows := FromResourceLogs(tenant, req.GetResourceLogs())
	now := rc.now().UTC()
	var semTS []int
	for i := range rows {
		if rows[i].TS.IsZero() {
			semTS = append(semTS, i)
		}
	}
	stamps := batchTimestamps(now, len(semTS))
	for i, idx := range semTS {
		rows[idx].TS = stamps[i]
	}
	return rc.prepareLogs(bound, rows, now)
}

// ingestLogs entrega as linhas ao destino: ASSÍNCRONO via batcher quando configurado
// (enfileira e responde 200; 429 sob backpressure para o cliente re-tentar); senão,
// insert SÍNCRONO no ClickHouse (200/503). Devolve o status HTTP a responder e o erro.
// Sucesso é sempre 200 (não 202): o revoada-agent e os SDKs OTLP tratam apenas 200 como OK.
func (rc *Receiver) ingestLogs(ctx context.Context, rows []model.LogRecord) (int, error) {
	if rc.logBatch != nil {
		if rc.logBatch.Enqueue(rows) {
			return http.StatusOK, nil
		}
		return http.StatusTooManyRequests, nil
	}
	if err := rc.logs.InsertLogs(ctx, rows); err != nil {
		return http.StatusServiceUnavailable, err
	}
	return http.StatusOK, nil
}

// parseLogsRequest decodifica o corpo OTLP em ExportLogsServiceRequest. Protobuf:
// proto.Unmarshal direto. JSON: usa a "ponte pdata" (plogotlp) que interpreta
// traceId/spanId em HEX conforme o spec OTLP/JSON, evitando a corrupção base64 do
// protojson; re-serializa em proto e desempacota na struct clpb do pipeline.
func parseLogsRequest(body []byte, isJSON bool) (*clpb.ExportLogsServiceRequest, error) {
	req := &clpb.ExportLogsServiceRequest{}
	if !isJSON {
		if err := proto.Unmarshal(body, req); err != nil {
			return nil, err
		}
		return req, nil
	}
	er := plogotlp.NewExportRequest()
	if err := er.UnmarshalJSON(body); err != nil {
		return nil, err
	}
	pbytes, err := er.MarshalProto()
	if err != nil {
		return nil, err
	}
	if err := proto.Unmarshal(pbytes, req); err != nil {
		return nil, err
	}
	return req, nil
}

// LogsHTTPHandler trata POST /v1/logs (protobuf ou JSON).
func (rc *Receiver) LogsHTTPHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if rc.logs == nil {
			http.Error(w, "ingestão de logs desabilitada", http.StatusServiceUnavailable)
			return
		}
		tenant, bound, st, ok := rc.authAgent(r.Context(), r.Header.Get("X-Revoada-Key"))
		if !ok {
			http.Error(w, http.StatusText(st), st)
			return
		}
		body, err := readOTLPBody(r)
		if err != nil {
			http.Error(w, "corpo inválido", http.StatusBadRequest)
			return
		}
		req, err := parseLogsRequest(body, isJSONContentType(r.Header.Get("Content-Type")))
		if err != nil {
			http.Error(w, "payload OTLP inválido: "+err.Error(), http.StatusBadRequest)
			return
		}
		rows, rejected := rc.buildLogRows(tenant, bound, req)
		code, err := rc.ingestLogs(r.Context(), rows)
		switch code {
		case http.StatusTooManyRequests:
			http.Error(w, "ingestão sobrecarregada, retente", code)
			return
		case http.StatusServiceUnavailable:
			rc.log.Warn("otlp: gravando logs", "err", err)
			http.Error(w, "erro gravando logs", code)
			return
		}
		resp := &clpb.ExportLogsServiceResponse{}
		if total, msg := rejectionSummary(rejected); total > 0 {
			resp.PartialSuccess = &clpb.ExportLogsPartialSuccess{
				RejectedLogRecords: total, ErrorMessage: msg,
			}
		}
		b, _ := proto.Marshal(resp)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(code) // 200 no sucesso (contrato OTLP: sucesso = 200 + corpo protobuf)
		_, _ = w.Write(b)
	}
}

// --- gRPC LogsService ---

type grpcLogsServer struct {
	clpb.UnimplementedLogsServiceServer
	rc *Receiver
}

func (g *grpcLogsServer) Export(ctx context.Context, req *clpb.ExportLogsServiceRequest) (*clpb.ExportLogsServiceResponse, error) {
	if g.rc.logs == nil {
		return nil, status.Error(codes.Unavailable, "ingestão de logs desabilitada")
	}
	key := ""
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get("x-revoada-key"); len(v) > 0 {
			key = v[0]
		}
	}
	tenant, bound, httpStatus, ok := g.rc.authAgent(ctx, key)
	if !ok {
		return nil, grpcAuthError(httpStatus)
	}
	rows, rejected := g.rc.buildLogRows(tenant, bound, req)
	st, err := g.rc.ingestLogs(ctx, rows)
	switch st {
	case http.StatusTooManyRequests:
		return nil, status.Error(codes.ResourceExhausted, "ingestão sobrecarregada")
	case http.StatusServiceUnavailable:
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	resp := &clpb.ExportLogsServiceResponse{}
	if total, msg := rejectionSummary(rejected); total > 0 {
		resp.PartialSuccess = &clpb.ExportLogsPartialSuccess{
			RejectedLogRecords: total, ErrorMessage: msg,
		}
	}
	return resp, nil
}

// grpcAuthError traduz o status HTTP interno em erro gRPC.
func grpcAuthError(httpStatus int) error {
	switch httpStatus {
	case http.StatusUnauthorized:
		return status.Error(codes.Unauthenticated, "chave desconhecida")
	case http.StatusForbidden:
		return status.Error(codes.PermissionDenied, "chave revogada")
	default:
		return status.Error(codes.Internal, "erro de autenticação")
	}
}

// ServeGRPCAll sobe métricas, logs e traces no mesmo listener gRPC, com o MESMO rate
// limit e o MESMO teto global das rotas HTTP (ver grpclimit.go: sem eles, :4317 era a
// porta sem freio). rl/gate nil sobem sem o respectivo controle — só para testes.
func (rc *Receiver) ServeGRPCAll(ctx context.Context, addr string, rl Limiter, gate Gate) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(rateLimitUnary(rl, gate)),
		grpc.StreamInterceptor(rateLimitStream(rl, gate)),
	)
	registerMetricsService(srv, rc)
	clpb.RegisterLogsServiceServer(srv, &grpcLogsServer{rc: rc})
	registerTracesService(srv, rc)

	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()

	rc.log.Info("OTLP/gRPC ouvindo (metrics+logs+traces)", "addr", addr)
	if err := srv.Serve(lis); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

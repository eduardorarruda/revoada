package otlp

import (
	"context"
	"hash/fnv"
	"net/http"

	"github.com/eduardorarruda/revoada/gateway/internal/model"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	ctpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

// Amostragem tail-based (aproximada, por lote): mantém 100% dos spans de um trace
// que tenha algum span com erro OU mais lento que rc.slowMs; os demais traces são
// mantidos com probabilidade rc.sampleFrac. Tail sampling entre lotes/serviços exige
// um coletor OTel dedicado — documentado como follow-up.

// sampleSpans decide quais spans persistir agrupando por trace_id, usando a config
// de amostragem do Receiver. frac=0 é honrado (mantém só erro/lento); só valor fora
// de [0,1] cai no default.
//
// A decisão é PEGAJOSA por trace_id numa janela curta (ver tracedecision.go): decidir
// por LOTE fazia o mesmo trace ser julgado de novo a cada requisição, e um trace
// partido em dois lotes perdia a raiz e era apresentado com um filho no lugar dela.
// Quando o trace vira "manter" DEPOIS de já ter perdido spans, os spans retidos
// carregam o rótulo SpanPartialLabel — o painel precisa dizer que aquilo é um pedaço.
func (rc *Receiver) sampleSpans(spans []model.Span) []model.Span {
	// frac=0 é um valor VÁLIDO e honrado (mantém só traces "interessantes": erro/lento).
	// Só valores fora de [0,1] caem no default (nunca ocorre via loadTraceSampling; guarda
	// contra construção manual do Receiver com campo inválido).
	frac := rc.sampleFrac
	if frac < 0 || frac > 1 {
		frac = defaultSampleFrac
	}
	slow := rc.slowMs
	if slow <= 0 {
		slow = defaultSlowMs
	}
	// limiar em [0,100): fração 0.2 => mantém buckets < 20.
	pct := uint32(frac * 100)
	// classifica cada trace: interessante (erro/lento) ou não.
	interesting := map[string]bool{}
	seen := map[string]bool{}
	for _, s := range spans {
		seen[s.TraceID] = true
		if s.StatusCode == "ERROR" || s.DurationMs >= slow {
			interesting[s.TraceID] = true
		}
	}
	dec := rc.decider()
	keep := map[string]bool{}
	partial := map[string]bool{}
	for tid := range seen {
		// `fresh` é a decisão que valeria se o trace fosse novo: o bucket determinístico.
		keep[tid], partial[tid] = dec.decide(tid, interesting[tid], traceBucket(tid) < pct)
	}
	out := spans[:0:0]
	for _, s := range spans {
		if !keep[s.TraceID] {
			continue
		}
		if partial[s.TraceID] {
			// Marcar o span é a única forma de a informação chegar à tela: o gateway não
			// tem como reescrever os spans que já gravou, e o consumidor precisa saber
			// que a raiz pode não ser a raiz e que a duração é um PISO.
			if s.Labels == nil {
				s.Labels = map[string]string{}
			}
			s.Labels[SpanPartialLabel] = "1"
		}
		out = append(out, s)
	}
	return out
}

// decider devolve a janela de decisão de amostragem, criando-a sob demanda — o
// Receiver é montado por struct literal em vários testes e não pode exigir NewReceiver
// só para amostrar spans.
func (rc *Receiver) decider() *traceDecider {
	rc.decMu.Lock()
	defer rc.decMu.Unlock()
	if rc.dec == nil {
		rc.dec = newTraceDecider(traceDecisionTTL, maxTraceDecisions)
	}
	return rc.dec
}

// traceBucket mapeia o trace_id em [0,100) de forma determinística (mesmo trace,
// mesma decisão — não fragmenta o trace).
func traceBucket(traceID string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(traceID))
	return h.Sum32() % 100
}

// buildSpanRows converte o request OTLP em spans (timestamp default, política de host,
// guarda de relógio e amostragem). Devolve os spans aceitos e o mapa de recusas.
func (rc *Receiver) buildSpanRows(tenant, bound string, req *ctpb.ExportTraceServiceRequest) ([]model.Span, map[string]int) {
	spans := FromResourceSpans(tenant, req.GetResourceSpans())
	now := rc.now().UTC()
	w := rc.window()
	rejected := map[string]int{}
	kept := spans[:0]
	for i := range spans {
		if spans[i].TS.IsZero() {
			spans[i].TS = now
		}
		// Mesma política de host das métricas: um span também é telemetria com dono, e
		// sem isto uma chave podia pendurar spans no host de outra máquina — e span sem
		// host ficava invisível para usuário com escopo por servidor.
		if applyHostBindLabels(rc.hostBind, bound, spans[i].Labels) {
			rejected["host_bind"]++
			hostDropped.Inc()
			continue
		}
		if len(spans[i].Labels) > maxLabelsPerPoint {
			rejected[reasonLabelCap]++
			rejectedPoints.With(signalSpans, reasonLabelCap).Inc()
			continue
		}
		if r := w.check(spans[i].TS, now); r != "" {
			rejected[r]++
			rejectedPoints.With(signalSpans, r).Inc()
			continue
		}
		kept = append(kept, spans[i])
	}
	if len(rejected) > 0 {
		_, msg := rejectionSummary(rejected)
		rc.log.Warn("otlp: spans recusados", "host", bound, "motivos", msg)
	}
	out := rc.sampleSpans(kept)
	acceptedPoints.With(signalSpans).Add(int64(len(out)))
	return out, rejected
}

// ingestSpans entrega os spans: ASSÍNCRONO via batcher quando configurado (enfileira e
// responde 200; 429 sob backpressure); senão, insert SÍNCRONO (200/503). Sucesso é sempre
// 200 (não 202): os SDKs OTLP tratam apenas 200 como OK. Devolve o status HTTP e o erro.
func (rc *Receiver) ingestSpans(ctx context.Context, spans []model.Span) (int, error) {
	if rc.spanBatch != nil {
		if rc.spanBatch.Enqueue(spans) {
			return http.StatusOK, nil
		}
		return http.StatusTooManyRequests, nil
	}
	if err := rc.spans.InsertSpans(ctx, spans); err != nil {
		return http.StatusServiceUnavailable, err
	}
	return http.StatusOK, nil
}

// parseTraceRequest decodifica o corpo OTLP em ExportTraceServiceRequest.
//
// Protobuf: proto.Unmarshal direto (já correto). JSON: usa a "ponte pdata" — o
// decoder OTLP/JSON oficial (ptraceotlp) interpreta traceId/spanId/parentSpanId como
// HEX (conforme o spec OTLP/JSON), ao contrário de protojson que os trataria como
// base64 e corromperia os IDs silenciosamente. O resultado é re-serializado em proto
// e desempacotado na mesma struct ctpb que o resto do pipeline consome.
func parseTraceRequest(body []byte, isJSON bool) (*ctpb.ExportTraceServiceRequest, error) {
	req := &ctpb.ExportTraceServiceRequest{}
	if !isJSON {
		if err := proto.Unmarshal(body, req); err != nil {
			return nil, err
		}
		return req, nil
	}
	er := ptraceotlp.NewExportRequest()
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

// TracesHTTPHandler trata POST /v1/traces (protobuf ou JSON).
func (rc *Receiver) TracesHTTPHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if rc.spans == nil {
			http.Error(w, "ingestão de traces desabilitada", http.StatusServiceUnavailable)
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
		req, err := parseTraceRequest(body, isJSONContentType(r.Header.Get("Content-Type")))
		if err != nil {
			http.Error(w, "payload OTLP inválido: "+err.Error(), http.StatusBadRequest)
			return
		}
		spans, rejected := rc.buildSpanRows(tenant, bound, req)
		code, err := rc.ingestSpans(r.Context(), spans)
		switch code {
		case http.StatusTooManyRequests:
			http.Error(w, "ingestão sobrecarregada, retente", code)
			return
		case http.StatusServiceUnavailable:
			rc.log.Warn("otlp: gravando traces", "err", err)
			http.Error(w, "erro gravando traces", code)
			return
		}
		resp := &ctpb.ExportTraceServiceResponse{}
		if total, msg := rejectionSummary(rejected); total > 0 {
			resp.PartialSuccess = &ctpb.ExportTracePartialSuccess{
				RejectedSpans: total, ErrorMessage: msg,
			}
		}
		b, _ := proto.Marshal(resp)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(code) // 200 no sucesso (contrato OTLP: sucesso = 200 + corpo protobuf)
		_, _ = w.Write(b)
	}
}

// --- gRPC TraceService ---

type grpcTracesServer struct {
	ctpb.UnimplementedTraceServiceServer
	rc *Receiver
}

func (g *grpcTracesServer) Export(ctx context.Context, req *ctpb.ExportTraceServiceRequest) (*ctpb.ExportTraceServiceResponse, error) {
	if g.rc.spans == nil {
		return nil, status.Error(codes.Unavailable, "ingestão de traces desabilitada")
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
	spans, rejected := g.rc.buildSpanRows(tenant, bound, req)
	st, err := g.rc.ingestSpans(ctx, spans)
	switch st {
	case http.StatusTooManyRequests:
		return nil, status.Error(codes.ResourceExhausted, "ingestão sobrecarregada")
	case http.StatusServiceUnavailable:
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	resp := &ctpb.ExportTraceServiceResponse{}
	if total, msg := rejectionSummary(rejected); total > 0 {
		resp.PartialSuccess = &ctpb.ExportTracePartialSuccess{
			RejectedSpans: total, ErrorMessage: msg,
		}
	}
	return resp, nil
}

func registerTracesService(srv *grpc.Server, rc *Receiver) {
	ctpb.RegisterTraceServiceServer(srv, &grpcTracesServer{rc: rc})
}

package otlp

import (
	"strconv"
	"time"

	"github.com/eduardorarruda/revoada/core/genai"
	"github.com/eduardorarruda/revoada/gateway/internal/model"
	tpb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// maxStacktraceLen limita o tamanho do stacktrace preservado nos labels do span.
const maxStacktraceLen = 4000

// applySpanEvents preserva informação dos span events nos labels (CONTRATO FIXO com
// a UI/outros consumidores). Grava `span.events` = total de events; e, do PRIMEIRO
// event chamado `exception`, extrai exception.type/message/stacktrace. Só cria chaves
// quando há valor.
func applySpanEvents(labels map[string]string, events []*tpb.Span_Event) {
	if len(events) == 0 {
		return
	}
	labels["span.events"] = strconv.Itoa(len(events))
	for _, ev := range events {
		if ev.GetName() != "exception" {
			continue
		}
		for _, kv := range ev.GetAttributes() {
			v := anyValueToString(kv.GetValue())
			if v == "" {
				continue
			}
			switch kv.GetKey() {
			case "exception.type":
				labels["exception.type"] = v
			case "exception.message":
				labels["exception.message"] = v
			case "exception.stacktrace":
				if len(v) > maxStacktraceLen {
					v = v[:maxStacktraceLen]
				}
				labels["exception.stacktrace"] = v
			}
		}
		return // apenas o primeiro event "exception"
	}
}

func spanKind(k tpb.Span_SpanKind) string {
	switch k {
	case tpb.Span_SPAN_KIND_SERVER:
		return "SERVER"
	case tpb.Span_SPAN_KIND_CLIENT:
		return "CLIENT"
	case tpb.Span_SPAN_KIND_PRODUCER:
		return "PRODUCER"
	case tpb.Span_SPAN_KIND_CONSUMER:
		return "CONSUMER"
	default:
		return "INTERNAL"
	}
}

func statusCode(c tpb.Status_StatusCode) string {
	switch c {
	case tpb.Status_STATUS_CODE_OK:
		return "OK"
	case tpb.Status_STATUS_CODE_ERROR:
		return "ERROR"
	default:
		return "UNSET"
	}
}

// FromResourceSpans converte ResourceSpans OTLP em spans internos.
func FromResourceSpans(tenant string, rss []*tpb.ResourceSpans) []model.Span {
	var out []model.Span
	for _, rs := range rss {
		resAttrs := attrsToLabelsTexto(nil, rs.GetResource().GetAttributes())
		service := resAttrs["service.name"]
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				start := sp.GetStartTimeUnixNano()
				end := sp.GetEndTimeUnixNano()
				var when time.Time
				if start != 0 {
					when = time.Unix(0, int64(start)).UTC()
				}
				durMs := 0.0
				if end >= start && start != 0 {
					durMs = float64(end-start) / 1e6
				}
				labels := attrsToLabelsTexto(resAttrs, sp.GetAttributes())
				ensureHostLabel(labels)
				applySpanEvents(labels, sp.GetEvents())
				chamada := reconhecerIA(sp, labels)
				out = append(out, model.Span{
					TenantID:   tenant,
					TS:         when,
					TraceID:    hexOrEmpty(sp.GetTraceId()),
					SpanID:     hexOrEmpty(sp.GetSpanId()),
					ParentID:   hexOrEmpty(sp.GetParentSpanId()),
					Service:    service,
					Name:       sp.GetName(),
					Kind:       spanKind(sp.GetKind()),
					DurationMs: durMs,
					StatusCode: statusCode(sp.GetStatus().GetCode()),
					StatusMsg:  sp.GetStatus().GetMessage(),
					Labels:     labels,
					GenAI:      chamada,
				})
			}
		}
	}
	return out
}

// reconhecerIA normaliza o span quando ele é uma chamada de IA (core/genai) e, nesse
// caso, TIRA dos rótulos as chaves de conteúdo (prompt, resposta, argumentos). O
// conteúdo segue só dentro da Chamada; se e como ele é gravado é decisão do modo de
// conteúdo (genai.go). Duas consequências deliberadas: com o conteúdo desligado, o
// prompt não sobra em spans.labels, que todo leitor vê por 15 dias; e o span do
// OpenInference, que achata cada mensagem em várias chaves, não estoura mais o teto
// de rótulos e deixa de ser recusado inteiro.
func reconhecerIA(sp *tpb.Span, labels map[string]string) *genai.Chamada {
	ch, ok := genai.Normalizar(genai.Span{
		Nome:      sp.GetName(),
		Erro:      sp.GetStatus().GetCode() == tpb.Status_STATUS_CODE_ERROR,
		Atributos: labels,
		Eventos:   eventosIA(sp.GetEvents()),
	})
	if !ok {
		return nil
	}
	for k := range labels {
		if genai.EhChaveDeConteudo(k) {
			delete(labels, k)
		}
	}
	return &ch
}

// eventosIA converte só os eventos que podem carregar conteúdo de IA.
func eventosIA(evs []*tpb.Span_Event) []genai.Evento {
	var out []genai.Evento
	for _, ev := range evs {
		if len(ev.GetName()) < 7 || ev.GetName()[:7] != "gen_ai." {
			continue
		}
		out = append(out, genai.Evento{Nome: ev.GetName(), Atributos: attrsToLabelsTexto(nil, ev.GetAttributes())})
	}
	return out
}

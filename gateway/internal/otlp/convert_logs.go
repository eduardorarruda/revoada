package otlp

import (
	"encoding/hex"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/gateway/internal/metrics"
	"github.com/eduardorarruda/revoada/gateway/internal/model"
	lpb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// SeverityUnknown é o nível DECLARADO para severidade ausente ou não reconhecida.
//
// POR QUE existe: o gateway aceitava qualquer string crua e gravava severity_num=0;
// severidade ausente virava severity=” (string vazia). Medido em dev: uma linha com
// severity="trololo" foi gravada como TROLOLO/0 e outra sem severidade como ”/0. A
// linha existe na tabela, mas some do filtro por nível da UI e nunca conta no
// histograma de erros (countIf(severity_num >= 17)) — é o zero silencioso da faixa de
// logs: o painel mostra "0 erros" porque não sabe classificar, não porque não houve.
// Com um rótulo declarado a linha continua encontrável e o desconhecido fica VISÍVEL
// como desconhecido.
const SeverityUnknown = "UNKNOWN"

// severityText mapeia o SeverityNumber OTLP (1..24) para um rótulo curto.
func severityText(n int32) string {
	switch {
	case n >= 21:
		return "FATAL"
	case n >= 17:
		return "ERROR"
	case n >= 13:
		return "WARN"
	case n >= 9:
		return "INFO"
	case n >= 5:
		return "DEBUG"
	case n >= 1:
		return "TRACE"
	default:
		return SeverityUnknown
	}
}

// normalizeSeverity resolve o par (texto, número) para um valor sempre CLASSIFICADO.
// Devolve (rótulo, número, textoOriginalDescartado).
//
// Regras, nesta ordem:
//   - número válido (>0): manda. O texto do emissor é preservado quando existe.
//   - sem número mas com texto conhecido (ERROR, err, warning, crit…): deriva o número
//     e canoniza o rótulo — é isto que faz a linha entrar no histograma de erros.
//   - texto desconhecido: vira UNKNOWN/0 e o texto original volta no terceiro retorno,
//     para o chamador guardar em label. Não fica na coluna `severity` porque ela é
//     LowCardinality(String): aceitar string arbitrária ali seria dar ao emissor um
//     canal de explosão de cardinalidade numa coluna que o ClickHouse assume pequena.
//   - nada: UNKNOWN/0. Nunca string vazia.
func normalizeSeverity(text string, num uint8) (sev string, n uint8, raw string) {
	t := strings.ToUpper(strings.TrimSpace(text))
	if num > 0 {
		if t != "" && severityNum(t) == 0 {
			// texto livre com número válido: classifica pelo número, guarda o texto.
			return severityText(int32(num)), num, t
		}
		if t != "" {
			return t, num, ""
		}
		return severityText(int32(num)), num, ""
	}
	if k := severityNum(t); k > 0 {
		return severityText(int32(k)), k, ""
	}
	if t == "" {
		return SeverityUnknown, 0, ""
	}
	return SeverityUnknown, 0, t
}

// severityRawLabel guarda o texto de severidade não reconhecido enviado pelo emissor.
// A informação não se perde, mas fica no Map de labels (valor String livre) em vez da
// coluna LowCardinality.
const severityRawLabel = "log.severity_raw"

// applySeverity normaliza a severidade de uma linha, preservando o texto original em
// label quando ele não é reconhecido.
func applySeverity(rec *model.LogRecord, text string, num uint8) {
	sev, n, raw := normalizeSeverity(text, num)
	rec.Severity, rec.SeverityNum = sev, n
	if raw == "" {
		return
	}
	if rec.Labels == nil {
		rec.Labels = map[string]string{}
	}
	rec.Labels[severityRawLabel] = raw
	severityUnclassified.Inc()
}

var severityUnclassified = metrics.NewCounter(
	"revoada_ingest_log_severity_unclassified_total",
	"Linhas de log cuja severidade não foi reconhecida (gravadas como UNKNOWN).")

// FromResourceLogs converte ResourceLogs OTLP em linhas internas de log.
// service vem de resource.attributes["service.name"]; severity do SeverityText
// (ou derivado do número); labels = atributos de recurso + do log record.
func FromResourceLogs(tenant string, rls []*lpb.ResourceLogs) []model.LogRecord {
	var out []model.LogRecord
	for _, rl := range rls {
		resAttrs := attrsToLabelsTexto(nil, rl.GetResource().GetAttributes())
		service := resAttrs["service.name"]
		for _, sl := range rl.GetScopeLogs() {
			for _, lr := range sl.GetLogRecords() {
				labels := attrsToLabelsTexto(resAttrs, lr.GetAttributes())
				ensureHostLabel(labels)
				ts := lr.GetTimeUnixNano()
				if ts == 0 {
					ts = lr.GetObservedTimeUnixNano()
				}
				var when time.Time
				if ts != 0 {
					when = time.Unix(0, int64(ts)).UTC()
				}
				rec := model.LogRecord{
					TenantID: tenant,
					TS:       when,
					Service:  service,
					Body:     anyValueToString(lr.GetBody()),
					Labels:   labels,
					TraceID:  hexOrEmpty(lr.GetTraceId()),
					SpanID:   hexOrEmpty(lr.GetSpanId()),
				}
				// Severidade sempre CLASSIFICADA: nunca string vazia, nunca texto livre
				// na coluna LowCardinality (ver normalizeSeverity).
				applySeverity(&rec, lr.GetSeverityText(), uint8(lr.GetSeverityNumber()))
				out = append(out, rec)
			}
		}
	}
	return out
}

func hexOrEmpty(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return hex.EncodeToString(b)
}

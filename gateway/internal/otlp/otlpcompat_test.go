package otlp

import (
	"bytes"
	"compress/gzip"
	"testing"

	"github.com/eduardorarruda/revoada/gateway/internal/model"
	ctpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	cmnpb "go.opentelemetry.io/proto/otlp/common/v1"
	rpb "go.opentelemetry.io/proto/otlp/resource/v1"
	tpb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

const (
	testTraceIDHex = "5b8efff798038103d269b633813fc60c"
	testSpanIDHex  = "eee19b7ec3c1b174"
)

// otlpJSONTrace é um corpo OTLP/JSON mínimo com IDs em HEX (como todo SDK oficial
// emite). O ponto crítico: traceId/spanId em hex NÃO podem ser tratados como base64.
const otlpJSONTrace = `{
  "resourceSpans": [{
    "resource": { "attributes": [
      { "key": "service.name", "value": { "stringValue": "checkout" } },
      { "key": "host.name", "value": { "stringValue": "web-01" } }
    ]},
    "scopeSpans": [{
      "spans": [{
        "traceId": "5b8efff798038103d269b633813fc60c",
        "spanId": "eee19b7ec3c1b174",
        "name": "GET /cart",
        "kind": 2,
        "startTimeUnixNano": "1700000000000000000",
        "endTimeUnixNano": "1700000000500000000"
      }]
    }]
  }]
}`

// Teste MAIS importante: round-trip do trace_id em OTLP/JSON via a ponte pdata.
// Com o antigo protojson, o hex seria decodificado como base64 e o TraceID sairia
// corrompido. Aqui exercitamos a MESMA função que o handler usa (parseTraceRequest).
func TestParseTraceRequest_JSONHexIDsRoundTrip(t *testing.T) {
	req, err := parseTraceRequest([]byte(otlpJSONTrace), true)
	if err != nil {
		t.Fatalf("parseTraceRequest JSON falhou: %v", err)
	}
	spans := FromResourceSpans("tenantX", req.GetResourceSpans())
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span, veio %d", len(spans))
	}
	s := spans[0]
	if s.TraceID != testTraceIDHex {
		t.Errorf("trace_id corrompido: got %q, want %q", s.TraceID, testTraceIDHex)
	}
	if s.SpanID != testSpanIDHex {
		t.Errorf("span_id corrompido: got %q, want %q", s.SpanID, testSpanIDHex)
	}
	if s.Service != "checkout" {
		t.Errorf("service errado: %q", s.Service)
	}
	// host.name deve ter sido normalizado para host.
	if s.Labels["host"] != "web-01" {
		t.Errorf("host normalizado errado: %v", s.Labels)
	}
}

// Sanidade: o ramo protobuf continua funcionando e produz os mesmos IDs.
func TestParseTraceRequest_ProtobufMatchesJSON(t *testing.T) {
	tid := mustHex(t, testTraceIDHex)
	sid := mustHex(t, testSpanIDHex)
	orig := &ctpb.ExportTraceServiceRequest{ResourceSpans: []*tpb.ResourceSpans{{
		ScopeSpans: []*tpb.ScopeSpans{{Spans: []*tpb.Span{{
			TraceId: tid, SpanId: sid, Name: "x",
		}}}},
	}}}
	body, err := proto.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	req, err := parseTraceRequest(body, false)
	if err != nil {
		t.Fatalf("parseTraceRequest protobuf falhou: %v", err)
	}
	got := FromResourceSpans("t", req.GetResourceSpans())
	if len(got) != 1 || got[0].TraceID != testTraceIDHex {
		t.Fatalf("protobuf trace_id errado: %+v", got)
	}
}

// gzip: um corpo protobuf comprimido deve parsear igual ao não comprimido, via a
// lógica compartilhada de descompressão (aqui replicamos o que readOTLPBody faz).
func TestGzipRoundTrip(t *testing.T) {
	tid := mustHex(t, testTraceIDHex)
	orig := &ctpb.ExportTraceServiceRequest{ResourceSpans: []*tpb.ResourceSpans{{
		ScopeSpans: []*tpb.ScopeSpans{{Spans: []*tpb.Span{{TraceId: tid, Name: "g"}}}},
	}}}
	raw, err := proto.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	// descomprime como readOTLPBody faria.
	zr, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer func() { _ = zr.Close() }()
	decompressed := new(bytes.Buffer)
	if _, err := decompressed.ReadFrom(zr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decompressed.Bytes(), raw) {
		t.Fatal("gzip round-trip alterou o corpo")
	}
	req, err := parseTraceRequest(decompressed.Bytes(), false)
	if err != nil {
		t.Fatalf("parse do corpo descomprimido falhou: %v", err)
	}
	if got := FromResourceSpans("t", req.GetResourceSpans()); len(got) != 1 || got[0].TraceID != testTraceIDHex {
		t.Fatalf("gzip: trace_id errado: %+v", got)
	}
}

// Content-Type com charset deve ser tratado como JSON.
func TestIsJSONContentType(t *testing.T) {
	jsonCases := []string{
		"application/json",
		"application/json; charset=utf-8",
		"application/json;charset=UTF-8",
		"Application/JSON",
	}
	for _, ct := range jsonCases {
		if !isJSONContentType(ct) {
			t.Errorf("%q deveria ser JSON", ct)
		}
	}
	protoCases := []string{"", "application/x-protobuf", "application/octet-stream", "text/plain"}
	for _, ct := range protoCases {
		if isJSONContentType(ct) {
			t.Errorf("%q NÃO deveria ser JSON", ct)
		}
	}
}

// host.name -> host na conversão de spans (sem host explícito).
func TestFromResourceSpans_HostNameNormalized(t *testing.T) {
	rss := []*tpb.ResourceSpans{{
		Resource: &rpb.Resource{Attributes: []*cmnpb.KeyValue{
			strAttr("host.name", "web-01"),
		}},
		ScopeSpans: []*tpb.ScopeSpans{{Spans: []*tpb.Span{{Name: "s"}}}},
	}}
	got := FromResourceSpans("t", rss)
	if len(got) != 1 {
		t.Fatalf("esperava 1 span, veio %d", len(got))
	}
	if got[0].Labels["host"] != "web-01" {
		t.Errorf("host não normalizado: %v", got[0].Labels)
	}
}

// host explícito não deve ser sobrescrito por host.name.
func TestFromResourceSpans_HostExplicitWins(t *testing.T) {
	rss := []*tpb.ResourceSpans{{
		Resource: &rpb.Resource{Attributes: []*cmnpb.KeyValue{
			strAttr("host", "explicit"),
			strAttr("host.name", "web-01"),
		}},
		ScopeSpans: []*tpb.ScopeSpans{{Spans: []*tpb.Span{{Name: "s"}}}},
	}}
	got := FromResourceSpans("t", rss)
	if got[0].Labels["host"] != "explicit" {
		t.Errorf("host explícito foi sobrescrito: %v", got[0].Labels)
	}
}

// span event "exception" deve virar labels exception.type/message/stacktrace + span.events.
func TestFromResourceSpans_ExceptionEvent(t *testing.T) {
	rss := []*tpb.ResourceSpans{{
		ScopeSpans: []*tpb.ScopeSpans{{Spans: []*tpb.Span{{
			Name: "boom",
			Events: []*tpb.Span_Event{
				{Name: "log", Attributes: []*cmnpb.KeyValue{strAttr("k", "v")}},
				{Name: "exception", Attributes: []*cmnpb.KeyValue{
					strAttr("exception.type", "RuntimeError"),
					strAttr("exception.message", "kaboom"),
					strAttr("exception.stacktrace", "line1\nline2"),
				}},
			},
		}}}},
	}}
	got := FromResourceSpans("t", rss)
	if len(got) != 1 {
		t.Fatalf("esperava 1 span, veio %d", len(got))
	}
	l := got[0].Labels
	if l["exception.type"] != "RuntimeError" {
		t.Errorf("exception.type errado: %q", l["exception.type"])
	}
	if l["exception.message"] != "kaboom" {
		t.Errorf("exception.message errado: %q", l["exception.message"])
	}
	if l["exception.stacktrace"] != "line1\nline2" {
		t.Errorf("exception.stacktrace errado: %q", l["exception.stacktrace"])
	}
	if l["span.events"] != "2" {
		t.Errorf("span.events errado: %q", l["span.events"])
	}
}

// Sem events, não deve criar a chave span.events nem chaves de exception.
func TestFromResourceSpans_NoEventsNoKeys(t *testing.T) {
	rss := []*tpb.ResourceSpans{{
		ScopeSpans: []*tpb.ScopeSpans{{Spans: []*tpb.Span{{Name: "s"}}}},
	}}
	l := FromResourceSpans("t", rss)[0].Labels
	if _, ok := l["span.events"]; ok {
		t.Errorf("span.events não deveria existir: %v", l)
	}
	if _, ok := l["exception.type"]; ok {
		t.Errorf("exception.type não deveria existir: %v", l)
	}
}

// Logs OTLP/JSON: trace_id hex também deve sobreviver via a ponte plogotlp.
const otlpJSONLogs = `{
  "resourceLogs": [{
    "resource": { "attributes": [
      { "key": "service.name", "value": { "stringValue": "api" } },
      { "key": "host.name", "value": { "stringValue": "log-01" } }
    ]},
    "scopeLogs": [{
      "logRecords": [{
        "timeUnixNano": "1700000000000000000",
        "severityNumber": 17,
        "body": { "stringValue": "boom" },
        "traceId": "5b8efff798038103d269b633813fc60c",
        "spanId": "eee19b7ec3c1b174"
      }]
    }]
  }]
}`

func TestParseLogsRequest_JSONHexIDsRoundTrip(t *testing.T) {
	req, err := parseLogsRequest([]byte(otlpJSONLogs), true)
	if err != nil {
		t.Fatalf("parseLogsRequest JSON falhou: %v", err)
	}
	rows := FromResourceLogs("t", req.GetResourceLogs())
	if len(rows) != 1 {
		t.Fatalf("esperava 1 log, veio %d", len(rows))
	}
	r := rows[0]
	if r.TraceID != testTraceIDHex {
		t.Errorf("log trace_id corrompido: got %q, want %q", r.TraceID, testTraceIDHex)
	}
	if r.SpanID != testSpanIDHex {
		t.Errorf("log span_id corrompido: got %q", r.SpanID)
	}
	if r.Severity != "ERROR" || r.Body != "boom" {
		t.Errorf("log convertido errado: %+v", r)
	}
	if r.Labels["host"] != "log-01" {
		t.Errorf("host do log não normalizado: %v", r.Labels)
	}
}

// Amostragem: valida que sampleSpans respeita rc.sampleFrac/slowMs, incluindo o caso
// frac=0 (honrado: mantém só erro/lento) e frac=1 (mantém tudo).
func TestSampleSpans_ConfigAndDefaults(t *testing.T) {
	errSpan := model.Span{TraceID: "aaa", StatusCode: "ERROR"}
	// Trace com erro é sempre mantido, independentemente da fração.
	rc := &Receiver{sampleFrac: 0, slowMs: 1000}
	if got := rc.sampleSpans([]model.Span{errSpan}); len(got) != 1 {
		t.Errorf("trace com erro deveria ser mantido, veio %d", len(got))
	}
	// frac=0 EXPLÍCITO: trace normal (sem erro, rápido) é DESCARTADO (não volta a 0.2).
	normal := model.Span{TraceID: "normal", DurationMs: 5}
	if got := rc.sampleSpans([]model.Span{normal}); len(got) != 0 {
		t.Errorf("frac=0 deveria descartar trace normal, manteve %d", len(got))
	}
	// slowMs baixo => trace lento vira "interessante" e é mantido mesmo com frac ~0.
	rc2 := &Receiver{sampleFrac: 0.0001, slowMs: 10}
	slow := model.Span{TraceID: "slow", DurationMs: 50}
	if got := rc2.sampleSpans([]model.Span{slow}); len(got) != 1 {
		t.Errorf("trace lento (>slowMs) deveria ser mantido, veio %d", len(got))
	}
	// sampleFrac=1.0 => mantém tudo.
	rc3 := &Receiver{sampleFrac: 1.0, slowMs: 1000}
	many := []model.Span{{TraceID: "x1"}, {TraceID: "x2"}, {TraceID: "x3"}}
	if got := rc3.sampleSpans(many); len(got) != 3 {
		t.Errorf("sampleFrac=1.0 deveria manter todos, veio %d", len(got))
	}
	// Valor fora de faixa cai no default (não quebra amostragem).
	rc4 := &Receiver{sampleFrac: 5, slowMs: 1000}
	if got := rc4.sampleSpans([]model.Span{errSpan}); len(got) != 1 {
		t.Errorf("frac inválido deveria usar default e manter erro, veio %d", len(got))
	}
}

func TestLoadTraceSampling(t *testing.T) {
	t.Setenv("REVOADA_TRACE_SAMPLE", "0.5")
	t.Setenv("REVOADA_TRACE_SAMPLE_SLOW_MS", "250")
	frac, slow := loadTraceSampling()
	if frac != 0.5 || slow != 250 {
		t.Errorf("env não aplicada: frac=%v slow=%v", frac, slow)
	}
	// valor inválido cai no default.
	t.Setenv("REVOADA_TRACE_SAMPLE", "abc")
	t.Setenv("REVOADA_TRACE_SAMPLE_SLOW_MS", "")
	frac, slow = loadTraceSampling()
	if frac != defaultSampleFrac || slow != defaultSlowMs {
		t.Errorf("default não aplicado: frac=%v slow=%v", frac, slow)
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b := make([]byte, len(s)/2)
	for i := 0; i < len(b); i++ {
		var hi, lo byte
		hi = hexNibble(s[2*i])
		lo = hexNibble(s[2*i+1])
		b[i] = hi<<4 | lo
	}
	return b
}

func hexNibble(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}

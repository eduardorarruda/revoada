package otlp

import (
	"testing"

	cmnpb "go.opentelemetry.io/proto/otlp/common/v1"
	lpb "go.opentelemetry.io/proto/otlp/logs/v1"
	rpb "go.opentelemetry.io/proto/otlp/resource/v1"
)

func TestSeverityText(t *testing.T) {
	// 0 é UNKNOWN (nível DECLARADO), não UNSET nem "": a linha precisa continuar
	// encontrável no filtro por nível mesmo quando o emissor não classificou.
	cases := map[int32]string{1: "TRACE", 5: "DEBUG", 9: "INFO", 13: "WARN", 17: "ERROR", 21: "FATAL", 0: SeverityUnknown}
	for num, want := range cases {
		if got := severityText(num); got != want {
			t.Errorf("severityText(%d)=%s, quer %s", num, got, want)
		}
	}
}

// TestNormalizeSeverity: severidade nunca sai vazia nem com texto livre na coluna
// LowCardinality. Era o zero silencioso da faixa de logs (severity=” / num=0).
func TestNormalizeSeverity(t *testing.T) {
	cases := []struct {
		text    string
		num     uint8
		wantSev string
		wantNum uint8
		wantRaw string
	}{
		{"", 0, SeverityUnknown, 0, ""},
		{"   ", 0, SeverityUnknown, 0, ""},
		{"trololo", 0, SeverityUnknown, 0, "TROLOLO"},
		{"error", 0, "ERROR", 17, ""},
		{"warning", 0, "WARN", 13, ""},
		{"crit", 0, "FATAL", 21, ""},
		{"", 17, "ERROR", 17, ""},
		{"ERROR", 17, "ERROR", 17, ""},
		{"minha-tag", 17, "ERROR", 17, "MINHA-TAG"},
	}
	for _, c := range cases {
		sev, num, raw := normalizeSeverity(c.text, c.num)
		if sev != c.wantSev || num != c.wantNum || raw != c.wantRaw {
			t.Errorf("normalizeSeverity(%q,%d)=(%q,%d,%q), quer (%q,%d,%q)",
				c.text, c.num, sev, num, raw, c.wantSev, c.wantNum, c.wantRaw)
		}
		if sev == "" {
			t.Errorf("severidade vazia para (%q,%d)", c.text, c.num)
		}
	}
}

func TestSeverityNumRoundTrip(t *testing.T) {
	// texto → número → texto deve cair no mesmo balde.
	for _, s := range []string{"ERROR", "WARN", "INFO", "DEBUG", "TRACE", "FATAL"} {
		if got := severityText(int32(severityNum(s))); got != s {
			t.Errorf("round-trip %s virou %s", s, got)
		}
	}
}

func TestFromResourceLogs(t *testing.T) {
	str := func(s string) *cmnpb.AnyValue {
		return &cmnpb.AnyValue{Value: &cmnpb.AnyValue_StringValue{StringValue: s}}
	}
	rls := []*lpb.ResourceLogs{{
		Resource: &rpb.Resource{Attributes: []*cmnpb.KeyValue{
			{Key: "service.name", Value: str("api")},
		}},
		ScopeLogs: []*lpb.ScopeLogs{{LogRecords: []*lpb.LogRecord{{
			SeverityNumber: lpb.SeverityNumber(17),
			Body:           str("boom"),
			Attributes:     []*cmnpb.KeyValue{{Key: "k", Value: str("v")}},
			TraceId:        []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
		}}}},
	}}
	rows := FromResourceLogs("t1", rls)
	if len(rows) != 1 {
		t.Fatalf("esperava 1 linha, obteve %d", len(rows))
	}
	r := rows[0]
	if r.Service != "api" || r.Severity != "ERROR" || r.Body != "boom" || r.Labels["k"] != "v" {
		t.Errorf("conversão errada: %+v", r)
	}
	if r.TraceID != "0102030405060708090a0b0c0d0e0f10" {
		t.Errorf("trace_id hex errado: %s", r.TraceID)
	}
	if r.Labels["service.name"] != "api" {
		t.Errorf("label de recurso deveria estar presente: %+v", r.Labels)
	}
}

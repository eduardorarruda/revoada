package scrape

import (
	"testing"
	"time"
)

func TestParseExposition(t *testing.T) {
	body := `# HELP node_cpu_seconds_total CPU
# TYPE node_cpu_seconds_total counter
node_cpu_seconds_total{cpu="0",mode="idle"} 12345.6
node_cpu_seconds_total{cpu="1",mode="idle"} 7.0 1700000000000
up 1
malformed_line_without_value
go_goroutines 42
`
	now := time.Unix(1700000123, 0).UTC()
	ms := parseExposition("default", body, map[string]string{"job": "node"}, now)

	if len(ms) != 4 { // 2x node_cpu + up + go_goroutines (malformed ignorada)
		t.Fatalf("esperava 4 métricas, veio %d", len(ms))
	}
	byKey := map[string]float64{}
	for _, m := range ms {
		if m.Labels["job"] != "node" {
			t.Errorf("label extra 'job' ausente em %s", m.Metric)
		}
		key := m.Metric
		if c := m.Labels["cpu"]; c != "" {
			key += "/cpu=" + c
		}
		byKey[key] = m.Value
	}
	if byKey["node_cpu_seconds_total/cpu=0"] != 12345.6 {
		t.Errorf("valor cpu0 errado: %v", byKey["node_cpu_seconds_total/cpu=0"])
	}
	if byKey["up"] != 1 || byKey["go_goroutines"] != 42 {
		t.Errorf("up/go_goroutines errados: %v %v", byKey["up"], byKey["go_goroutines"])
	}
}

func TestParseLineTimestamp(t *testing.T) {
	now := time.Unix(1700000123, 0).UTC()
	_, _, _, ts, ok := parseLine(`m{a="b"} 1 1700000000000`, now)
	if !ok {
		t.Fatal("linha válida não parseou")
	}
	if ts.UnixMilli() != 1700000000000 {
		t.Errorf("timestamp explícito ignorado: %v", ts)
	}
}

func TestSplitTopLevelCommas(t *testing.T) {
	// vírgula dentro de aspas não deve dividir
	parts := splitTopLevelCommas(`a="x,y",b="z"`)
	if len(parts) != 2 {
		t.Fatalf("esperava 2 pares, veio %d: %v", len(parts), parts)
	}
}

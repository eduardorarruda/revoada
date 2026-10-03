package otlpsend

import (
	"testing"

	"github.com/eduardorarruda/revoada/agent/internal/collect"
	mpb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// firstDataPoint devolve o primeiro NumberDataPoint da métrica dada.
func firstDataPoint(m *mpb.Metric) *mpb.NumberDataPoint {
	switch d := m.Data.(type) {
	case *mpb.Metric_Gauge:
		if len(d.Gauge.DataPoints) > 0 {
			return d.Gauge.DataPoints[0]
		}
	case *mpb.Metric_Sum:
		if len(d.Sum.DataPoints) > 0 {
			return d.Sum.DataPoints[0]
		}
	}
	return nil
}

func TestBuildDatapointLabels(t *testing.T) {
	s := New("http://gw", "k", "host1", nil, nil)
	points := []collect.Point{
		{Name: "system.filesystem.utilization", Value: 42, Labels: map[string]string{"mount": "/var"}},
		{Name: "system.cpu.utilization", Value: 10}, // sem labels
	}
	req := s.build(points, collect.HostInfo{})
	metrics := req.ResourceMetrics[0].ScopeMetrics[0].Metrics
	if len(metrics) != 2 {
		t.Fatalf("esperado 2 métricas, veio %d", len(metrics))
	}

	byName := map[string]*mpb.Metric{}
	for _, m := range metrics {
		byName[m.Name] = m
	}

	// A métrica de disco carrega o atributo mount no datapoint.
	fsDP := firstDataPoint(byName["system.filesystem.utilization"])
	if fsDP == nil {
		t.Fatal("filesystem sem datapoint")
	}
	if len(fsDP.Attributes) != 1 || fsDP.Attributes[0].Key != "mount" ||
		fsDP.Attributes[0].Value.GetStringValue() != "/var" {
		t.Errorf("atributo mount ausente/errado: %+v", fsDP.Attributes)
	}

	// A métrica sem labels não deve carregar atributos de datapoint.
	cpuDP := firstDataPoint(byName["system.cpu.utilization"])
	if cpuDP == nil {
		t.Fatal("cpu sem datapoint")
	}
	if len(cpuDP.Attributes) != 0 {
		t.Errorf("cpu não deveria ter atributos: %+v", cpuDP.Attributes)
	}
}

// O ponto sai com a hora em que foi LIDO. Antes todos saíam com a hora do envio,
// que vem depois da coleta de containers (até o orçamento de 10 s): na validação
// contra /proc, com a máquina carregada, o contador de rede e o load average
// ficaram fora da janela do instante — o número era de 1,5 s+ antes do carimbo.
func TestBuildUsaAHoraDaLeitura(t *testing.T) {
	s := New("http://gw", "k", "host1", nil, nil)
	lido := int64(1_790_000_000_123_000_000)
	req := s.build([]collect.Point{
		{Name: "system.network.io.bytes_sent", Value: 1, Counter: true, At: lido},
		{Name: "agent.self.goroutines", Value: 2}, // sem hora de leitura: hora do envio
	}, collect.HostInfo{})
	ms := req.ResourceMetrics[0].ScopeMetrics[0].Metrics
	if got := firstDataPoint(ms[0]).TimeUnixNano; got != uint64(lido) {
		t.Fatalf("contador carimbado com %d, lido em %d", got, lido)
	}
	if got := firstDataPoint(ms[1]).TimeUnixNano; got <= uint64(lido) {
		t.Fatalf("ponto sem hora de leitura deveria levar a hora do envio, veio %d", got)
	}
}

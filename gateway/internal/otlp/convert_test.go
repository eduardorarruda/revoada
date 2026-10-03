package otlp

import (
	"reflect"
	"testing"
	"time"

	cmnpb "go.opentelemetry.io/proto/otlp/common/v1"
	mpb "go.opentelemetry.io/proto/otlp/metrics/v1"
	rpb "go.opentelemetry.io/proto/otlp/resource/v1"
)

func strAttr(k, v string) *cmnpb.KeyValue {
	return &cmnpb.KeyValue{Key: k, Value: &cmnpb.AnyValue{Value: &cmnpb.AnyValue_StringValue{StringValue: v}}}
}

func TestFromResourceMetrics_GaugeESum(t *testing.T) {
	rms := []*mpb.ResourceMetrics{{
		Resource: &rpb.Resource{Attributes: []*cmnpb.KeyValue{strAttr("host.name", "srv1")}},
		ScopeMetrics: []*mpb.ScopeMetrics{{
			Metrics: []*mpb.Metric{
				{
					Name: "system.cpu.utilization",
					Data: &mpb.Metric_Gauge{Gauge: &mpb.Gauge{DataPoints: []*mpb.NumberDataPoint{
						{
							TimeUnixNano: 1700000000000000000,
							Attributes:   []*cmnpb.KeyValue{strAttr("core", "0")},
							Value:        &mpb.NumberDataPoint_AsDouble{AsDouble: 42.5},
						},
					}}},
				},
				{
					Name: "http.requests",
					Data: &mpb.Metric_Sum{Sum: &mpb.Sum{DataPoints: []*mpb.NumberDataPoint{
						{
							TimeUnixNano: 1700000000000000000,
							Value:        &mpb.NumberDataPoint_AsInt{AsInt: 7},
						},
					}}},
				},
			},
		}},
	}}

	out := FromResourceMetrics("tenantA", rms)
	if len(out) != 2 {
		t.Fatalf("esperava 2 métricas, veio %d", len(out))
	}

	var gauge, sum bool
	for _, m := range out {
		if m.TenantID != "tenantA" {
			t.Errorf("tenant errado: %s", m.TenantID)
		}
		if m.Labels["host.name"] != "srv1" {
			t.Errorf("atributo de recurso não virou label: %v", m.Labels)
		}
		switch m.Metric {
		case "system.cpu.utilization":
			gauge = true
			if m.Value != 42.5 || m.Labels["core"] != "0" {
				t.Errorf("gauge errado: %+v", m)
			}
			if m.TS.IsZero() {
				t.Error("timestamp do gauge não convertido")
			}
		case "http.requests":
			sum = true
			if m.Value != 7 {
				t.Errorf("sum (int) errado: %v", m.Value)
			}
		}
	}
	if !gauge || !sum {
		t.Errorf("faltou gauge(%v) ou sum(%v)", gauge, sum)
	}
}

// prodResourceAttrs reproduz EXATAMENTE os atributos de recurso que o agente 0.7.x
// manda em produção (conferidos no Postgres, tabela de séries de alerta). `ver` é a
// versão do agente, que é o que muda num upgrade.
func prodResourceAttrs(host, ver, kernel, cores string) []*cmnpb.KeyValue {
	return []*cmnpb.KeyValue{
		strAttr("host", host),
		strAttr("host.name", host),
		strAttr("os", "linux"),
		strAttr("platform", "ubuntu"),
		strAttr("kernel", kernel),
		strAttr("arch", "x86_64"),
		strAttr("host.cpu.cores", cores),
		strAttr("host.ips", "10.0.0.7,172.17.0.1"),
		strAttr("agent.version", ver),
	}
}

// TestFromResourceMetrics_InventarioForaDasLabels prova que o inventário do host some
// das labels da métrica, sobrando só a identidade da série (host/host.name) mais os
// atributos do próprio data point.
//
// Cenário de produção protegido: as labels são a identidade da série no ClickHouse e o
// avaliador de alertas não passa group_by — o fingerprint sai do mapa INTEIRO. Com
// `agent.version`/`kernel`/`host.cpu.cores` dentro, qualquer upgrade de agente, upgrade
// de kernel ou redimensionamento de VM criava uma série NOVA e abandonava a antiga.
func TestFromResourceMetrics_InventarioForaDasLabels(t *testing.T) {
	rms := []*mpb.ResourceMetrics{{
		Resource: &rpb.Resource{Attributes: prodResourceAttrs("whm-demo.exemplo.com", "0.7.0", "6.17.0-1008-gcp", "4")},
		ScopeMetrics: []*mpb.ScopeMetrics{{Metrics: []*mpb.Metric{{
			Name: "system.disk.used_percent",
			Data: &mpb.Metric_Gauge{Gauge: &mpb.Gauge{DataPoints: []*mpb.NumberDataPoint{{
				TimeUnixNano: 1700000000000000000,
				Attributes:   []*cmnpb.KeyValue{strAttr("mount", "/var"), strAttr("container", "api")},
				Value:        &mpb.NumberDataPoint_AsDouble{AsDouble: 91.2},
			}}}},
		}}}},
	}}

	out := FromResourceMetrics("tenantA", rms)
	if len(out) != 1 {
		t.Fatalf("esperava 1 métrica, veio %d", len(out))
	}
	got := out[0].Labels

	want := map[string]string{
		"host":      "whm-demo.exemplo.com",
		"host.name": "whm-demo.exemplo.com",
		"mount":     "/var",
		"container": "api",
	}
	if len(got) != len(want) {
		t.Fatalf("labels inesperadas: %v (queria exatamente %v)", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("label %q = %q, queria %q (labels=%v)", k, got[k], v, got)
		}
	}
	// Explicitando o que NÃO pode voltar: se alguma destas reaparecer, o defeito da
	// notificação falsa de "resolvido" volta junto.
	for _, k := range []string{"agent.version", "kernel", "arch", "platform", "os", "host.cpu.cores", "host.ips"} {
		if _, ok := got[k]; ok {
			t.Errorf("atributo de inventário %q vazou para as labels da métrica: %v", k, got)
		}
	}
}

// TestFromResourceMetrics_AtributosDoDataPointPreservados garante que a limpeza acertou
// só o inventário de RECURSO. mount/container/state/health/exit_code são dimensão de
// medição de verdade: sem eles, os discos de um host viram uma série só e o alerta de
// container parado perde o nome do container na notificação.
func TestFromResourceMetrics_AtributosDoDataPointPreservados(t *testing.T) {
	dpAttrs := []*cmnpb.KeyValue{
		strAttr("mount", "/"),
		strAttr("container", "postgres"),
		strAttr("state", "exited"),
		strAttr("health", "unhealthy"),
		strAttr("exit_code", "137"),
	}
	rms := []*mpb.ResourceMetrics{{
		Resource: &rpb.Resource{Attributes: prodResourceAttrs("srv1", "0.7.0", "6.8.0-124-generic", "8")},
		ScopeMetrics: []*mpb.ScopeMetrics{{Metrics: []*mpb.Metric{{
			Name: "container.state",
			Data: &mpb.Metric_Sum{Sum: &mpb.Sum{DataPoints: []*mpb.NumberDataPoint{{
				TimeUnixNano: 1700000000000000000,
				Attributes:   dpAttrs,
				Value:        &mpb.NumberDataPoint_AsInt{AsInt: 1},
			}}}},
		}}}},
	}}

	out := FromResourceMetrics("tenantA", rms)
	if len(out) != 1 {
		t.Fatalf("esperava 1 métrica, veio %d", len(out))
	}
	want := map[string]string{
		"mount": "/", "container": "postgres", "state": "exited",
		"health": "unhealthy", "exit_code": "137",
	}
	for k, v := range want {
		if out[0].Labels[k] != v {
			t.Errorf("dimensão de medição %q sumiu ou mudou: %q (queria %q) — labels=%v",
				k, out[0].Labels[k], v, out[0].Labels)
		}
	}
	if out[0].Labels["host"] != "srv1" || out[0].Labels["host.name"] != "srv1" {
		t.Errorf("host/host.name têm de sobreviver (são a identidade da série): %v", out[0].Labels)
	}
}

// TestHostFromResource_InventarioContinuaPopulado é a contraprova da mudança: tirar o
// inventário das LABELS não pode esvaziar a tabela `hosts`. hostFromResource lê os
// atributos OTLP crus do recurso, então SO/kernel/arch/versão/núcleos continuam
// chegando à tela de Infraestrutura e ao detalhe do host.
func TestHostFromResource_InventarioContinuaPopulado(t *testing.T) {
	rm := &mpb.ResourceMetrics{
		Resource: &rpb.Resource{Attributes: prodResourceAttrs("whm-demo.exemplo.com", "0.7.0", "6.17.0-1008-gcp", "4")},
	}
	now := time.Unix(1700000000, 0).UTC()

	h, ok := hostFromResource("tenantA", rm, now)
	if !ok {
		t.Fatal("hostFromResource devia reconhecer o host")
	}
	if h.Hostname != "whm-demo.exemplo.com" {
		t.Errorf("hostname errado: %q", h.Hostname)
	}
	if h.OS != "linux" {
		t.Errorf("OS errado: %q", h.OS)
	}
	if h.Kernel != "6.17.0-1008-gcp" {
		t.Errorf("kernel errado: %q", h.Kernel)
	}
	if h.Arch != "x86_64" {
		t.Errorf("arch errado: %q", h.Arch)
	}
	if h.AgentVersion != "0.7.0" {
		t.Errorf("agent version errada: %q", h.AgentVersion)
	}
	if h.CPUCores != 4 {
		t.Errorf("núcleos errados: %d", h.CPUCores)
	}
	if h.IPs != "10.0.0.7,172.17.0.1" {
		t.Errorf("IPs errados: %q", h.IPs)
	}
	if !h.LastSeen.Equal(now) {
		t.Errorf("last seen errado: %v", h.LastSeen)
	}
}

// TestFromResourceMetrics_UpgradeDeAgenteNaoTrocaASerie é O teste que prova que o
// defeito morreu. O mesmo host, a mesma métrica, com o agente 0.7.0 e depois 0.7.1
// (e kernel novo, e VM redimensionada de 4 para 8 núcleos) têm de produzir o MESMO
// conjunto de labels — ou seja, a MESMA série e o MESMO fingerprint de alerta.
//
// Antes: a série antiga sumia, o reconcile auto-resolvia o alerta aberto com motivo
// "sem dados" e o resolveStale mandava "resolvido" no WhatsApp para um problema que
// continuava acontecendo (e o gráfico do host partia em dois).
func TestFromResourceMetrics_UpgradeDeAgenteNaoTrocaASerie(t *testing.T) {
	metrica := func(ver, kernel, cores string) map[string]string {
		rms := []*mpb.ResourceMetrics{{
			Resource: &rpb.Resource{Attributes: prodResourceAttrs("srv-prod", ver, kernel, cores)},
			ScopeMetrics: []*mpb.ScopeMetrics{{Metrics: []*mpb.Metric{{
				Name: "system.memory.used_percent",
				Data: &mpb.Metric_Gauge{Gauge: &mpb.Gauge{DataPoints: []*mpb.NumberDataPoint{{
					TimeUnixNano: 1700000000000000000,
					Value:        &mpb.NumberDataPoint_AsDouble{AsDouble: 93.4},
				}}}},
			}}}},
		}}
		out := FromResourceMetrics("tenantA", rms)
		if len(out) != 1 {
			t.Fatalf("esperava 1 métrica, veio %d", len(out))
		}
		return out[0].Labels
	}

	antes := metrica("0.7.0", "6.17.0-1008-gcp", "4")
	depois := metrica("0.7.1", "6.17.0-1009-gcp", "8")

	if !reflect.DeepEqual(antes, depois) {
		t.Fatalf("upgrade de agente/kernel/VM trocou a identidade da série:\nantes = %v\ndepois = %v", antes, depois)
	}
	if antes["host"] != "srv-prod" {
		t.Errorf("a série tem de continuar identificada pelo host: %v", antes)
	}
}

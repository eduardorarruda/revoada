// Package otlp converte requisições OTLP de métricas para o modelo interno do pipeline.
package otlp

import (
	"strconv"
	"time"

	"github.com/eduardorarruda/revoada/gateway/internal/model"
	cmnpb "go.opentelemetry.io/proto/otlp/common/v1"
	mpb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// hostInventoryAttrs são atributos de RECURSO que descrevem o que a máquina É
// (inventário), e não uma dimensão do que está sendo MEDIDO. Eles são REMOVIDOS das
// labels de métrica — mas continuam sendo lidos dos atributos OTLP crus por
// hostFromResource, que os grava na tabela `hosts` (é lá que o inventário mora).
//
// POR QUE cada chave é inventário e não dimensão de medição:
//   - agent.version: versão do binário do agente que coletou. Não muda o que o número
//     mede; muda a cada upgrade do agente.
//   - kernel: versão do kernel do host. Muda a cada `apt upgrade`/reboot.
//   - os, platform, arch: sistema operacional, distribuição e arquitetura da máquina.
//     Propriedades fixas do host, iguais para TODAS as métricas dele.
//   - host.cpu.cores: nº de núcleos. É capacidade do host (fica na coluna CPUCores),
//     não uma dimensão da série; muda ao redimensionar a VM.
//   - host.ips: lista de IPs do host. Muda sozinha (DHCP, nova interface, IP flutuante).
//
// O QUE ACONTECEU por elas estarem aqui (produção, agente 0.7.0 → 0.7.1): as labels
// formam a IDENTIDADE da série no ClickHouse, e o avaliador de alertas não informa
// group_by — o fingerprint do alerta sai do mapa de labels INTEIRO. Ao subir o agente,
// a série antiga simplesmente parou de receber pontos: o reconcile auto-resolveu os
// alertas abertos com motivo "sem dados" e o resolveStale DISPAROU NOTIFICAÇÃO DE
// "RESOLVIDO" NO WHATSAPP para um problema que continuava acontecendo. O gráfico de
// cada host também partia em dois no upgrade. O mesmo valia para upgrade de kernel e
// redimensionamento de VM.
//
// `host` e `host.name` NÃO entram nesta lista: são a identidade da série e o sistema
// inteiro (filtros da UI, agrupamento de alertas, amarração anti-spoofing) depende deles.
var hostInventoryAttrs = map[string]bool{
	"agent.version":  true,
	"kernel":         true,
	"arch":           true,
	"platform":       true,
	"os":             true,
	"host.cpu.cores": true,
	"host.ips":       true,
}

// dropHostInventory remove, in-place, os atributos de inventário do mapa de labels.
// Aplicado APENAS aos atributos de RECURSO do caminho de MÉTRICAS (é onde o agente
// declara o inventário); atributos do data point não são tocados, porque lá `os`/`state`
// e afins são dimensão de medição de verdade.
func dropHostInventory(labels map[string]string) {
	for k := range hostInventoryAttrs {
		delete(labels, k)
	}
}

// FromResourceMetrics converte ResourceMetrics OTLP em métricas internas.
// Suporta os tipos numéricos mais comuns (Gauge e Sum); histogramas/summaries
// são ignorados nesta fase (registrar como TODO na doc de OTLP).
func FromResourceMetrics(tenant string, rms []*mpb.ResourceMetrics) []model.Metric {
	var out []model.Metric
	for _, rm := range rms {
		resAttrs := attrsToLabels(nil, rm.GetResource().GetAttributes())
		// Inventário do host fora da identidade da série (ver hostInventoryAttrs):
		// sem isto, um upgrade de agente/kernel troca a série e gera "resolvido" falso.
		dropHostInventory(resAttrs)
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				name := m.GetName()
				switch data := m.GetData().(type) {
				case *mpb.Metric_Gauge:
					out = appendPoints(out, tenant, name, resAttrs, data.Gauge.GetDataPoints())
				case *mpb.Metric_Sum:
					out = appendPoints(out, tenant, name, resAttrs, data.Sum.GetDataPoints())
				}
			}
		}
	}
	return out
}

func appendPoints(out []model.Metric, tenant, name string, resAttrs map[string]string, pts []*mpb.NumberDataPoint) []model.Metric {
	for _, dp := range pts {
		labels := attrsToLabels(resAttrs, dp.GetAttributes())
		ts := time.Unix(0, int64(dp.GetTimeUnixNano())).UTC()
		if dp.GetTimeUnixNano() == 0 {
			ts = time.Time{} // deixa o chamador aplicar "agora" se vier zerado
		}
		var v float64
		switch x := dp.GetValue().(type) {
		case *mpb.NumberDataPoint_AsDouble:
			v = x.AsDouble
		case *mpb.NumberDataPoint_AsInt:
			v = float64(x.AsInt)
		default:
			continue
		}
		out = append(out, model.Metric{TenantID: tenant, Metric: name, Labels: labels, TS: ts, Value: v})
	}
	return out
}

// attrsToLabels funde atributos de recurso (base) com os do data point.
// hostFromResource extrai o inventário de host dos atributos de recurso OTLP, para
// popular a tabela `hosts` a partir do agente (que reporta por OTLP — sem isto o
// host coleta métricas mas não aparece no inventário).
// Lê apenas atributos ESTÁTICOS (nunca uptime, que é dinâmico e viraria label de
// cardinalidade alta nas métricas). Devolve (host, true) se houver hostname.
// NÃO é afetado por dropHostInventory: lê os atributos OTLP crus do recurso, e não o
// mapa de labels já filtrado — é justamente por isso que dá para tirar o inventário
// das métricas sem perder OS/Kernel/Arch/AgentVersion/CPUCores no inventário.
func hostFromResource(tenant string, rm *mpb.ResourceMetrics, now time.Time) (model.Host, bool) {
	a := attrsToLabels(nil, rm.GetResource().GetAttributes())
	name := a["host.name"]
	if name == "" {
		name = a["host"]
	}
	if name == "" {
		return model.Host{}, false
	}
	h := model.Host{
		TenantID:     tenant,
		Hostname:     name,
		OS:           a["os"],
		Kernel:       a["kernel"],
		Arch:         a["arch"],
		AgentVersion: a["agent.version"],
		IPs:          a["host.ips"],
		LastSeen:     now,
	}
	if h.OS == "" {
		h.OS = a["platform"]
	}
	if n, err := strconv.Atoi(a["host.cpu.cores"]); err == nil {
		h.CPUCores = n
	}
	return h, true
}

// ensureHostLabel normaliza o rótulo de host: SDKs OTel oficiais emitem `host.name`
// (semconv), mas a UI filtra por `labels["host"]`. Sem isto, o filtro por servidor
// nunca casa em spans/logs vindos de SDKs oficiais. Só é aplicado a spans e logs
// (não a métricas nem ao attrsToLabels genérico, para evitar efeito colateral).
func ensureHostLabel(labels map[string]string) {
	if labels["host"] == "" && labels["host.name"] != "" {
		labels["host"] = labels["host.name"]
	}
}

func attrsToLabels(base map[string]string, attrs []*cmnpb.KeyValue) map[string]string {
	l := map[string]string{}
	for k, v := range base {
		l[k] = v
	}
	for _, kv := range attrs {
		l[kv.GetKey()] = anyValueToString(kv.GetValue())
	}
	return l
}

func anyValueToString(v *cmnpb.AnyValue) string {
	switch x := v.GetValue().(type) {
	case *cmnpb.AnyValue_StringValue:
		return x.StringValue
	case *cmnpb.AnyValue_BoolValue:
		return strconv.FormatBool(x.BoolValue)
	case *cmnpb.AnyValue_IntValue:
		return strconv.FormatInt(x.IntValue, 10)
	case *cmnpb.AnyValue_DoubleValue:
		return strconv.FormatFloat(x.DoubleValue, 'g', -1, 64)
	default:
		return ""
	}
}

package otlp

import (
	"strings"

	"github.com/eduardorarruda/revoada/gateway/internal/metrics"
	cpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	cmnpb "go.opentelemetry.io/proto/otlp/common/v1"
)

var (
	// hostFilled conta linhas que chegaram SEM host e ganharam o hostname da chave.
	// Um número alto aqui é o sintoma que gerou a correção (37,8% dos logs de dev).
	hostFilled = metrics.NewCounter(
		"revoada_ingest_host_filled_total",
		"Linhas sem rótulo host preenchidas com o hostname vinculado à serverkey.")
	// hostRebound conta linhas cujo host divergia da chave e foi normalizado.
	hostRebound = metrics.NewCounter(
		"revoada_ingest_host_rebound_total",
		"Linhas cujo rótulo host divergia da serverkey e foi corrigido (anti-spoofing).")
	// hostDropped conta linhas descartadas por divergência em modo strict.
	hostDropped = metrics.NewCounter(
		"revoada_ingest_host_dropped_total",
		"Linhas descartadas por host divergente da serverkey (modo strict).")
)

// Amarração de host (anti-spoofing): sem isto, um agente com uma serverkey válida
// pode reportar métricas rotuladas com o `host` de OUTRA máquina do mesmo tenant
// (falsos alertas, poluição de dados). A chave carrega um hostname vinculado
// (agents.hostname); quando a amarração está ligada, o host do payload é forçado a
// esse valor (normalize) ou o ponto é descartado se divergir (strict).
//
// DESLIGADA por padrão: em instalações existentes agents.hostname pode ser um rótulo
// cosmético (nome amigável) diferente do hostname real reportado — ligar cegamente
// relabelaria hosts legítimos. O operador habilita via REVOADA_HOST_BINDING depois de
// garantir que agents.hostname reflete o hostname real de cada chave.

// HostBindMode controla a política de amarração.
type HostBindMode int

const (
	HostBindOff       HostBindMode = iota // passthrough (default)
	HostBindNormalize                     // força o host = hostname da chave
	HostBindStrict                        // descarta o que divergir da chave
)

// ParseHostBindMode lê o modo de uma string (env). Desconhecido/"" => off.
func ParseHostBindMode(s string) HostBindMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "normalize", "on", "1", "true":
		return HostBindNormalize
	case "strict":
		return HostBindStrict
	default:
		return HostBindOff
	}
}

// bindHost decide o host efetivo (função pura, testável). Retorna (host, drop):
//   - off ou sem hostname vinculado => usa o payload, nunca descarta.
//   - payload == vinculado => ok.
//   - normalize => força o vinculado (impede o spoofing sem perder o ponto).
//   - strict => descarta (drop=true) quando diverge.
func bindHost(mode HostBindMode, bound, payload string) (string, bool) {
	if mode == HostBindOff || bound == "" || payload == bound {
		return payload, false
	}
	switch mode {
	case HostBindNormalize:
		return bound, false
	case HostBindStrict:
		return "", true
	}
	return payload, false
}

// resourceHost lê o host declarado nos atributos de recurso ("host" ou "host.name").
func resourceHost(attrs []*cmnpb.KeyValue) string {
	var host, hostName string
	for _, kv := range attrs {
		switch kv.GetKey() {
		case "host":
			host = kv.GetValue().GetStringValue()
		case "host.name":
			hostName = kv.GetValue().GetStringValue()
		}
	}
	if host != "" {
		return host
	}
	return hostName
}

// setResourceHost sobrescreve (ou acrescenta) "host" e "host.name" para val.
func setResourceHost(attrs []*cmnpb.KeyValue, val string) []*cmnpb.KeyValue {
	strVal := func() *cmnpb.AnyValue {
		return &cmnpb.AnyValue{Value: &cmnpb.AnyValue_StringValue{StringValue: val}}
	}
	set := func(a []*cmnpb.KeyValue, key string) []*cmnpb.KeyValue {
		for _, kv := range a {
			if kv.GetKey() == key {
				kv.Value = strVal()
				return a
			}
		}
		return append(a, &cmnpb.KeyValue{Key: key, Value: strVal()})
	}
	return set(set(attrs, "host"), "host.name")
}

// applyHostBindLabels aplica a política de host a um mapa de LABELS já convertido —
// é assim que logs e spans são cobertos.
//
// POR QUE aqui e não no recurso OTLP: em logs e spans o rótulo `host` pode chegar
// tanto pelo resource quanto pelos atributos do próprio registro, e o segundo vence na
// fusão (attrsToLabels). Amarrar só no recurso deixaria a porta aberta. Medido antes
// da correção: com uma chave vinculada a `otel-test` foi possível gravar uma linha de
// log com host=notebook-dev, e ela ficou pendurada naquele outro host.
//
// A segunda metade é o buraco maior: SEM host no payload, a linha entrava com
// labels['host']=” — 37,8% das linhas de log de dev estavam assim. Linha sem host é
// INVISÍVEL para usuário com escopo por servidor (o filtro nunca casa) e IMPURGÁVEL
// por qualquer rota do produto (todas recortam por host). Por isso o preenchimento
// com o hostname da chave vale SEMPRE, inclusive com a amarração desligada: não é
// política anti-spoofing, é tapar um buraco de dado.
//
// Devolve true quando a linha deve ser DESCARTADA (só ocorre em strict).
func applyHostBindLabels(mode HostBindMode, bound string, labels map[string]string) bool {
	if labels == nil {
		return false
	}
	ensureHostLabel(labels) // host.name (semconv) -> host, quando só veio o primeiro
	if bound == "" {
		return false
	}
	if labels["host"] == "" {
		labels["host"] = bound
		if labels["host.name"] == "" {
			labels["host.name"] = bound
		}
		hostFilled.Inc()
		return false
	}
	eff, drop := bindHost(mode, bound, labels["host"])
	if drop {
		return true
	}
	if eff != labels["host"] {
		hostRebound.Inc()
	}
	labels["host"] = eff
	if labels["host.name"] != "" {
		labels["host.name"] = eff // mantém os dois rótulos coerentes
	}
	return false
}

// applyHostBindMetrics aplica a amarração a uma requisição de métricas, no nível de
// recurso (é onde o agente declara o host). No-op quando off/sem vínculo. Em strict,
// filtra fora os ResourceMetrics de host divergente.
func applyHostBindMetrics(mode HostBindMode, bound string, req *cpb.ExportMetricsServiceRequest) {
	if mode == HostBindOff || bound == "" {
		return
	}
	rms := req.GetResourceMetrics()
	kept := rms[:0]
	for _, rm := range rms {
		res := rm.GetResource()
		if res == nil {
			kept = append(kept, rm)
			continue
		}
		eff, drop := bindHost(mode, bound, resourceHost(res.GetAttributes()))
		if drop {
			continue
		}
		res.Attributes = setResourceHost(res.GetAttributes(), eff)
		kept = append(kept, rm)
	}
	req.ResourceMetrics = kept
}

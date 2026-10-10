package alerting

import (
	"regexp"
	"strings"
)

// Deploy blue/green: o serviço roda em dois containers que se revezam
// (`talk-blue` / `talk-green`). A cada deploy a cor nova sobe e a antiga é PARADA DE
// PROPÓSITO — e fica lá, parada, até o próximo deploy.
//
// Sem isto, a regra "Container caído" via a cor parada como queda: um servidor com
// deploy blue/green rendeu ~300 mensagens num dia (20/h, o teto por regra) com o
// serviço no ar.
//
// A regra: containers do MESMO servidor cujo nome é <base>-blue e <base>-green
// (também `_`, e réplicas `-1`, `_2`…) são UM serviço. Uma cor parada não alerta
// enquanto outra cor do mesmo serviço estiver no ar; com todas paradas, alerta como
// sempre — aí o serviço caiu de verdade. Reconhecido pelo nome: vale sozinho para
// qualquer servidor que adote o padrão, sem configuração.

var reBlueGreen = regexp.MustCompile(`^(.+?)[-_](blue|green)(?:[-_]\d+)?$`)

// baseBlueGreen devolve o nome-base do serviço ("talk" para "talk-blue") e se o
// container segue o padrão. Sem diferença de maiúsculas.
func baseBlueGreen(container string) (string, bool) {
	m := reBlueGreen.FindStringSubmatch(strings.ToLower(container))
	if m == nil {
		return "", false
	}
	return m[1], true
}

// cobertosBlueGreen devolve os fingerprints das séries de cor PARADA (viola) que têm
// outra cor do mesmo serviço, no mesmo servidor, NO AR (não viola) neste ciclo.
func cobertosBlueGreen(series map[string]serieColapsada) map[string]bool {
	chave := func(labels map[string]string) (string, bool) {
		base, ok := baseBlueGreen(labels["container"])
		if !ok || labels["host"] == "" {
			return "", false
		}
		return labels["host"] + "\x00" + base, true
	}
	noAr := map[string]bool{}
	for _, s := range series {
		if k, ok := chave(s.labels); ok && !s.viola {
			noAr[k] = true
		}
	}
	cobertos := map[string]bool{}
	for fp, s := range series {
		if k, ok := chave(s.labels); ok && s.viola && noAr[k] {
			cobertos[fp] = true
		}
	}
	return cobertos
}

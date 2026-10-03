// Package health traduz utilização de recursos (CPU/RAM/disco) em estado de
// semáforo — ok | warn | crit — a partir de limiares configuráveis (Fase A).
//
// Os limiares vêm da tabela host_thresholds (global + override por host); quando
// nada está configurado, os defaults embutidos aqui garantem que todo host ganhe
// semáforo sem nenhuma configuração prévia. Disco é deliberadamente mais rígido
// que CPU/RAM: disco cheio quebra serviços de forma abrupta e é lento de remediar.
package health

import "math"

// Threshold é o par de limiares de um recurso (percentuais 0..100).
type Threshold struct {
	Warn float64
	Crit float64
}

// Defaults são os limiares embutidos por métrica, usados quando não há linha
// configurada em host_thresholds (nem por host, nem global).
var Defaults = map[string]Threshold{
	"cpu":  {Warn: 70, Crit: 90},
	"mem":  {Warn: 70, Crit: 90},
	"swap": {Warn: 70, Crit: 90}, // mesmo critério de mem (Fase B)
	"disk": {Warn: 75, Crit: 90},
}

// Metrics é a lista canônica de métricas de recurso com semáforo.
var Metrics = []string{"cpu", "mem", "disk"}

// Estados possíveis de uma métrica.
const (
	StateOK   = "ok"
	StateWarn = "warn"
	StateCrit = "crit"
	// StateNoData: a métrica NÃO foi medida nesta janela. É diferente de "medida e
	// deu zero". Existe porque o contrário — mostrar 0% verde para uma métrica que
	// não chegou — faz o operador ler "servidor ocioso" quando a verdade é
	// "ninguém está medindo". Ficou mais provável desde que o coletor de CPU passou
	// a NÃO emitir quando não consegue medir, em vez de emitir um número inventado.
	StateNoData = "nodata"
)

// Default devolve o limiar embutido de uma métrica (zero-value se desconhecida).
func Default(metric string) Threshold { return Defaults[metric] }

// Classify traduz uma utilização (pct 0..100) em estado de semáforo:
//
//	NaN ou ±Inf          => "nodata"
//	pct <  warn          => "ok"
//	warn <= pct <  crit  => "warn"
//	pct >= crit          => "crit"
//
// O caso NaN/Inf é a primeira coisa avaliada de propósito. Toda comparação com NaN
// é falsa — inclusive `NaN >= crit` —, então antes desta guarda um NaN escorregava
// por todos os `case` e caía no `default`, virando "ok" VERDE. Um valor impossível
// aparecia no mural como o mais tranquilizador que existe.
func Classify(pct, warn, crit float64) string {
	switch {
	case math.IsNaN(pct) || math.IsInf(pct, 0):
		return StateNoData
	case pct >= crit:
		return StateCrit
	case pct >= warn:
		return StateWarn
	default:
		return StateOK
	}
}

// worstRank ordena a severidade (maior = pior).
func worstRank(state string) int {
	switch state {
	case StateCrit:
		return 3
	case StateWarn:
		return 2
	case StateOK:
		return 1
	case StateNoData:
		// Deliberadamente abaixo de "ok": "não medido" não é uma severidade, e um
		// host com CPU faltando mas RAM e disco saudáveis não deve pintar o card
		// inteiro de amarelo. A ausência aparece no ladrilho da própria métrica
		// (State = "nodata"), que é onde ela é acionável.
		return 0
	}
	return 0 // desconhecido/"" fica abaixo de ok
}

// Worst devolve o pior estado entre os informados (crit > warn > ok). Estados
// vazios/desconhecidos/"nodata" são ignorados; sem nenhum estado válido devolve "ok".
func Worst(states ...string) string {
	best := StateOK
	bestRank := worstRank(StateOK)
	for _, s := range states {
		if r := worstRank(s); r > bestRank {
			best, bestRank = s, r
		}
	}
	return best
}

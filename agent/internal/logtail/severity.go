package logtail

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Classificação de severidade em log ESTRUTURADO.
//
// POR QUE existe: a classificação era só regex de palavra sobre o texto, e a regex
// não entende o formato em que a aplicação moderna escreve. Medido num serviço Node
// com pino: 49 de 51 linhas caíram em UNKNOWN — `{"level":50,"msg":"…"}` não tem a
// palavra "error" em lugar nenhum, e `{"level":"critical"}` usa um rótulo que não
// está na lista da regex. O efeito prático é o pior possível para quem opera: o
// painel mostra "0 erros" para um serviço que só imprimia erro, porque a linha
// existe mas não entra no filtro nem no histograma (severity_num >= 17).
//
// A ordem é deliberada: o nível DECLARADO pela aplicação vence a adivinhação por
// palavra. Se o emissor diz `"level":"info"` numa linha que contém a palavra
// "error" (mensagem tipo "0 errors found"), quem sabe é ele. UNKNOWN continua sendo
// o último recurso — nunca INFO —, pela mesma razão de sempre: chamar de rotina o
// que não se conseguiu classificar transforma ignorância em afirmação.

// nivelChaves são os nomes de campo de nível, em ordem de precedência. A lista é
// minúscula e a comparação é case-insensitive porque `levelName` (Java/log4j),
// `LEVEL` e `level` são o mesmo campo escrito por bibliotecas diferentes.
var nivelChaves = []string{
	"level", "severity", "lvl", "levelname", "level_name",
	"loglevel", "log_level", "log.level", "severitytext", "severity_text",
}

// severityFromJSON lê o nível declarado numa linha estruturada. Devolve ok=false
// quando a linha não é JSON ou não traz campo de nível reconhecível — e aí a
// classificação segue para a regex, como antes.
func severityFromJSON(line string) (string, bool) {
	// Só a PRIMEIRA linha: uma entrada costurada (stack trace) tem o registro
	// estruturado no cabeçalho e texto livre embaixo; procurar o `}` no fim do bloco
	// pegaria uma chave de outra linha e o parse falharia à toa.
	if k := strings.IndexByte(line, '\n'); k >= 0 {
		line = line[:k]
	}
	// Prefixo antes do JSON é rotina (timestamp do runtime, tag do supervisor).
	i := strings.IndexByte(line, '{')
	j := strings.LastIndexByte(line, '}')
	if i < 0 || j <= i {
		return "", false
	}
	var campos map[string]json.RawMessage
	if json.Unmarshal([]byte(line[i:j+1]), &campos) != nil {
		return "", false
	}
	for _, alvo := range nivelChaves {
		for k, v := range campos {
			if !strings.EqualFold(k, alvo) {
				continue
			}
			if s, ok := nivelDeRaw(v); ok {
				return s, true
			}
		}
	}
	return "", false
}

// nivelDeRaw interpreta o valor do campo de nível: texto ("error", "CRITICAL") ou
// número (pino/bunyan e, na faixa baixa, a prioridade do syslog).
func nivelDeRaw(v json.RawMessage) (string, bool) {
	s := strings.TrimSpace(string(v))
	if s == "" {
		return "", false
	}
	if s[0] == '"' {
		var txt string
		if json.Unmarshal(v, &txt) != nil {
			return "", false
		}
		if lbl, ok := nivelCanonico(txt); ok {
			return lbl, true
		}
		// `"level":"50"` — número entregue como string, comum em log convertido.
		if n, err := strconv.ParseFloat(strings.TrimSpace(txt), 64); err == nil {
			return nivelNumerico(n)
		}
		return "", false
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", false
	}
	return nivelNumerico(n)
}

// nivelCanonico normaliza o rótulo textual para o vocabulário que o gateway
// reconhece (TRACE/DEBUG/INFO/WARN/ERROR/FATAL). Rótulo fora do vocabulário vira
// UNKNOWN/0 no gateway e some do filtro de nível — daí a lista incluir as grafias
// que de fato aparecem (crit, alert, emerg, panic, severe, e as em português).
func nivelCanonico(s string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "TRACE", "VERBOSE":
		return "TRACE", true
	case "DEBUG", "DBG", "FINE":
		return "DEBUG", true
	case "INFO", "INFORMATION", "INFORMATIONAL", "NOTICE", "LOG":
		return "INFO", true
	case "WARN", "WARNING", "AVISO":
		return "WARN", true
	case "ERROR", "ERR", "ERRO", "SEVERE":
		return "ERROR", true
	case "FATAL", "CRIT", "CRITICAL", "CRITICO", "CRÍTICO", "ALERT", "ALERTA", "EMERG", "EMERGENCY", "PANIC":
		return "FATAL", true
	}
	return "", false
}

// nivelNumerico traduz o nível numérico. Duas escalas convivem no mundo real e não
// se sobrepõem, o que permite decidir pelo próprio valor:
//   - pino/bunyan/zap: 10=trace, 20=debug, 30=info, 40=warn, 50=error, 60=fatal
//     (valores intermediários são níveis customizados e caem na faixa de baixo);
//   - syslog/journald: 0..7, já mapeado por severityFromPriority.
//
// 8 e 9 não pertencem a nenhuma das duas: devolver um palpite ali seria inventar.
func nivelNumerico(n float64) (string, bool) {
	switch {
	case n >= 60:
		return "FATAL", true
	case n >= 50:
		return "ERROR", true
	case n >= 40:
		return "WARN", true
	case n >= 30:
		return "INFO", true
	case n >= 20:
		return "DEBUG", true
	case n >= 10:
		return "TRACE", true
	case n >= 0 && n <= 7:
		return severityFromPriority(int(n)), true
	}
	return "", false
}

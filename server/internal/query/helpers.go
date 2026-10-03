package query

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// quote escapa uma string para literal SQL do ClickHouse.
func quote(s string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, "\\", "\\\\"), "'", "\\'") + "'"
}

// safeLabel valida um nome de label (evita injeção em labels[...]).
func safeLabel(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	case int64:
		return x
	}
	return 0
}

// toFloat converte o valor de uma linha do ClickHouse e diz se ele é MEDIDA.
//
// Devolve ok=false para tudo que não é um número finito: JSON null (é assim que o
// ClickHouse serializa NaN/±Inf por padrão), a string "nan"/"inf" (quando
// output_format_json_quote_denormals está ligado) e qualquer tipo inesperado.
// Antes, esses casos viravam 0 silenciosamente — e 0 numa série de utilização é
// uma afirmação forte ("o servidor estava ocioso") que o dado não sustenta. Um
// valor que não é medida tem de virar LACUNA, não zero.
func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, isFinite(x)
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return 0, false
		}
		return f, isFinite(f) // ParseFloat aceita "NaN"/"Inf"; aqui eles são barrados
	}
	return 0, false
}

// isFinite barra NaN e ±Inf — os dois valores de ponto flutuante que passam por
// número mas não descrevem nenhuma medição.
func isFinite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

func toLabels(v any) map[string]string {
	out := map[string]string{}
	if m, ok := v.(map[string]any); ok {
		for k, vv := range m {
			out[k] = fmt.Sprintf("%v", vv)
		}
	}
	return out
}

// labelKey gera uma chave determinística a partir dos labels ordenados.
func labelKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
		b.WriteByte(';')
	}
	return b.String()
}

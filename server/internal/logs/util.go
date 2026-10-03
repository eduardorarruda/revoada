package logs

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func decode(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }

// decodeStrict decodifica o corpo RECUSANDO campo desconhecido, com mensagem em
// pt-BR. Usado onde ignorar um campo é perigoso: num expurgo, um filtro digitado
// errado que o servidor ignora em silêncio devolve 200 e apaga muito mais do que o
// operador pediu.
func decodeStrict(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		msg := err.Error()
		if i := strings.Index(msg, "unknown field "); i >= 0 {
			return fmt.Errorf("campo não reconhecido: %s", strings.TrimPrefix(msg[i:], "unknown field "))
		}
		return fmt.Errorf("corpo inválido")
	}
	return nil
}

// quote escapa uma string para uso seguro em SQL do ClickHouse.
func quote(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "'", "\\'")
	return "'" + s + "'"
}

// sevNum converte um rótulo de severidade em número mínimo OTLP (0 = sem filtro).
func sevNum(text string) int {
	switch strings.ToUpper(strings.TrimSpace(text)) {
	case "FATAL":
		return 21
	case "ERROR", "ERR":
		return 17
	case "WARN", "WARNING":
		return 13
	case "INFO":
		return 9
	case "DEBUG":
		return 5
	case "TRACE":
		return 1
	default:
		return 0
	}
}

func parseTime(s string, def time.Time) time.Time {
	if s == "" {
		return def
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		// aceita epoch em segundos ou milissegundos
		if n > 1e12 {
			return time.UnixMilli(n)
		}
		return time.Unix(n, 0)
	}
	return def
}

func clampInt(s string, def, max int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// bucketFor escolhe um passo de histograma (~60 buckets na janela).
func bucketFor(from, to time.Time) int {
	span := int(to.Sub(from).Seconds())
	if span <= 0 {
		return 60
	}
	step := span / 60
	if step < 1 {
		step = 1
	}
	return step
}

func enc(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }

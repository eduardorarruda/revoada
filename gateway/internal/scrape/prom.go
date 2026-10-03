// Package scrape implementa o scraper Prometheus: alvos vindos do PostgreSQL,
// loop com timeout e paralelismo limitado, parse do formato de exposição.
package scrape

import (
	"bufio"
	"strconv"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/gateway/internal/model"
)

// parseExposition faz o parse mínimo do formato de exposição Prometheus (text/plain).
// Trata linhas `metric{label="v",...} valor [timestamp_ms]`; ignora # HELP/# TYPE.
// extra são labels adicionais do alvo (mesclados; os do alvo têm prioridade).
func parseExposition(tenant, body string, extra map[string]string, now time.Time) []model.Metric {
	var out []model.Metric
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, labels, value, ts, ok := parseLine(line, now)
		if !ok {
			continue
		}
		for k, v := range extra {
			labels[k] = v
		}
		out = append(out, model.Metric{TenantID: tenant, Metric: name, Labels: labels, TS: ts, Value: value})
	}
	return out
}

func parseLine(line string, now time.Time) (name string, labels map[string]string, value float64, ts time.Time, ok bool) {
	labels = map[string]string{}
	ts = now

	// nome (e labels opcionais entre {})
	var rest string
	if i := strings.IndexByte(line, '{'); i >= 0 {
		name = strings.TrimSpace(line[:i])
		j := strings.IndexByte(line, '}')
		if j < 0 || j < i {
			return "", nil, 0, ts, false
		}
		parseLabels(line[i+1:j], labels)
		rest = strings.TrimSpace(line[j+1:])
	} else {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return "", nil, 0, ts, false
		}
		name = fields[0]
		rest = strings.Join(fields[1:], " ")
	}

	parts := strings.Fields(rest)
	if len(parts) == 0 {
		return "", nil, 0, ts, false
	}
	v, err := parseValue(parts[0])
	if err != nil {
		return "", nil, 0, ts, false
	}
	value = v
	if len(parts) >= 2 {
		if ms, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
			ts = time.UnixMilli(ms).UTC()
		}
	}
	return name, labels, value, ts, true
}

// parseLabels lê `a="1",b="2"` para o map (aspas obrigatórias no formato Prometheus).
func parseLabels(s string, into map[string]string) {
	for _, pair := range splitTopLevelCommas(s) {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		eq := strings.IndexByte(pair, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(pair[:eq])
		val := strings.TrimSpace(pair[eq+1:])
		val = strings.Trim(val, `"`)
		if key != "" {
			into[key] = val
		}
	}
}

// splitTopLevelCommas divide por vírgulas que não estão dentro de aspas.
func splitTopLevelCommas(s string) []string {
	var out []string
	var b strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuote = !inQuote
			b.WriteByte(c)
		case c == ',' && !inQuote:
			out = append(out, b.String())
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

func parseValue(s string) (float64, error) {
	switch s {
	case "+Inf":
		return strconv.ParseFloat("+Inf", 64)
	case "-Inf":
		return strconv.ParseFloat("-Inf", 64)
	case "NaN":
		return strconv.ParseFloat("NaN", 64)
	}
	return strconv.ParseFloat(s, 64)
}

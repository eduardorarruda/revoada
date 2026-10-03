package traces

import (
	"fmt"
	"net/http"
	"time"
)

// Correlate junta, para um serviço numa janela de tempo, os logs (erros primeiro),
// os traces mais lentos e as anotações de deploy do período. É o back-end do painel
// de correlação (P5.4): de um ponto/série → traces e logs daquele instante/serviço.
func (h *Handler) Correlate(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	service := q.Get("service")
	host := q.Get("host")
	// janela padrão: ±2min ao redor de `ts`, ou [from,to] explícito.
	var from, to time.Time
	if tsStr := q.Get("ts"); tsStr != "" {
		center := parseTime(tsStr, time.Now())
		from, to = center.Add(-2*time.Minute), center.Add(2*time.Minute)
	} else {
		from = parseTime(q.Get("from"), time.Now().Add(-5*time.Minute))
		to = parseTime(q.Get("to"), time.Now())
	}

	// Escopo por serviço e/ou host. Ao correlacionar a partir de um gráfico de
	// métrica de um servidor, o front manda `host` → só os logs/spans daquela
	// máquina (labels['host']) entram; sem isto o painel misturava logs e traces
	// de TODOS os hosts do usuário no mesmo instante.
	svcFilter := scopeFilter(service, host)
	// Enforcement por usuário: só logs/spans/deploys dos hosts permitidos.
	hp := andPred(h.hostPred(r.Context()))

	// logs (erros primeiro)
	logsSQL := fmt.Sprintf(`SELECT toUnixTimestamp64Milli(ts) AS t, service, severity, severity_num, body, trace_id
		FROM logs WHERE tenant_id='default' %sAND ts >= toDateTime(%d) AND ts <= toDateTime(%d)%s
		ORDER BY severity_num DESC, ts DESC LIMIT 50`, svcFilter, from.Unix(), to.Unix(), hp)

	// traces mais lentos. `partial` viaja junto pelo mesmo motivo da lista de traces:
	// num trace incompleto o `root_name` pode ser um filho promovido a raiz e a duração
	// é um piso — e aqui isso pesa mais, porque a ordenação é POR DURAÇÃO. Sem a marca,
	// um trace cuja raiz foi descartada some do topo e a correlação aponta para o
	// vizinho errado.
	tracesSQL := fmt.Sprintf(`SELECT trace_id, argMin(service, ts) AS root_service, argMin(name, ts) AS root_name,
		max(duration_ms) AS duration_ms, maxIf(1, status_code='ERROR') AS has_error,
		max(labels['revoada.trace_partial'] = '1') AS partial,
		toUnixTimestamp64Milli(min(ts)) AS start_ms
		FROM spans WHERE tenant_id='default' %sAND ts >= toDateTime(%d) AND ts <= toDateTime(%d)%s
		GROUP BY trace_id ORDER BY duration_ms DESC LIMIT 20`, svcFilter, from.Unix(), to.Unix(), hp)

	// deploys (globais no período — cruzados automaticamente)
	deploySQL := fmt.Sprintf(`SELECT toUnixTimestamp64Milli(ts) AS t, title, body, labels
		FROM events WHERE tenant_id='default' AND kind='deploy'
		AND ts >= toDateTime(%d) AND ts <= toDateTime(%d)%s ORDER BY ts DESC LIMIT 10`, from.Unix(), to.Unix(), hp)

	logs, err := h.ch.QueryJSON(r.Context(), logsSQL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tr, err := h.ch.QueryJSON(r.Context(), tracesSQL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	deploys, _ := h.ch.QueryJSON(r.Context(), deploySQL) // deploys são best-effort

	writeJSON(w, http.StatusOK, map[string]any{
		"service": service,
		"from":    from.UnixMilli(),
		"to":      to.UnixMilli(),
		"logs":    orEmpty(logs),
		"traces":  orEmpty(tr),
		"deploys": orEmpty(deploys),
	})
}

// scopeFilter monta o trecho SQL de escopo (serviço e/ou host) para as queries de
// logs e spans da correlação. Cada cláusula começa com "AND " e termina com espaço,
// para encaixar após "WHERE tenant_id='default'". Valores são escapados via quote().
func scopeFilter(service, host string) string {
	f := ""
	if service != "" {
		f += "AND service = " + quote(service) + " "
	}
	if host != "" {
		f += "AND labels['host'] = " + quote(host) + " "
	}
	return f
}

func orEmpty(rows []map[string]any) []map[string]any {
	if rows == nil {
		return []map[string]any{}
	}
	return rows
}

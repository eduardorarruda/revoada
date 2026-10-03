package query

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/authz"
)

// hostFilterClause devolve " AND <pred>" quando há escopo restrito no contexto, ou ""
// (admin/sem escopo). Usado nos metadados para não vazar nomes de host/métrica de
// servidores que o usuário não pode ver.
func hostFilterClause(ctx context.Context) string {
	scope, ok := authz.ScopeFrom(ctx)
	if !ok {
		return ""
	}
	pred := scope.HostPredicate("labels['host']")
	if pred == "" {
		return ""
	}
	return " AND " + pred
}

// Metrics lista os nomes de métricas disponíveis (últimos 7 dias).
func (h *Handler) Metrics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	rows, err := h.ch.QueryJSON(ctx,
		"SELECT DISTINCT metric FROM metrics WHERE ts > now() - INTERVAL 7 DAY"+hostFilterClause(ctx)+" ORDER BY metric")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := []string{}
	for _, row := range rows {
		if s, ok := row["metric"].(string); ok {
			out = append(out, s)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"metrics": out})
}

// LabelValues lista os valores de um label para uma métrica (autocomplete).
func (h *Handler) LabelValues(w http.ResponseWriter, r *http.Request) {
	metric := r.URL.Query().Get("metric")
	label := r.URL.Query().Get("label")
	if metric == "" || !safeLabel(label) {
		http.Error(w, "metric e label (válido) obrigatórios", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	sql := fmt.Sprintf(
		"SELECT DISTINCT labels[%s] AS v FROM metrics WHERE metric = %s AND ts > now() - INTERVAL 7 DAY AND v != ''%s ORDER BY v LIMIT 1000",
		quote(label), quote(metric), hostFilterClause(ctx))
	rows, err := h.ch.QueryJSON(ctx, sql)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := []string{}
	for _, row := range rows {
		if s, ok := row["v"].(string); ok {
			out = append(out, s)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"label": label, "values": out})
}

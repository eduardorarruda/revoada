package logs

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/chquery"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// MetricsRunner conta periodicamente os logs que casam com cada log_metric e
// grava o resultado como série em `metrics` (assim um alerta comum pode observá-la).
type MetricsRunner struct {
	st  *store.Store
	ch  *chquery.Client
	log *slog.Logger
}

func NewMetricsRunner(st *store.Store, ch *chquery.Client, log *slog.Logger) *MetricsRunner {
	return &MetricsRunner{st: st, ch: ch, log: log}
}

// Run avalia todas as log_metrics a cada 60s (janela de contagem = 60s).
func (m *MetricsRunner) Run(ctx context.Context) {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	m.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.tick(ctx)
		}
	}
}

func (m *MetricsRunner) tick(ctx context.Context) {
	lms, err := m.st.ListLogMetrics(ctx)
	if err != nil {
		m.log.Warn("logmetrics: listando", "err", err)
		return
	}
	var points []chquery.MetricPoint
	for _, lm := range lms {
		if !lm.Enabled {
			continue
		}
		f := filters{tenant: "default", q: lm.Query, service: lm.Service, sevMin: lm.SeverityMin,
			from: time.Now().Add(-60 * time.Second), to: time.Now()}
		// A regra roda a cada minuto: sem noLog(), o padrão salvo pelo usuário viraria
		// ~1440 cópias por dia dentro de system.query_log.
		sql := fmt.Sprintf("SELECT count() AS c FROM logs WHERE %s%s", f.where(), f.noLog())
		rows, err := m.ch.QueryJSON(ctx, sql)
		if err != nil || len(rows) == 0 {
			continue
		}
		c, _ := toInt64(rows[0]["c"])
		points = append(points, chquery.MetricPoint{
			Metric: lm.MetricName,
			Labels: map[string]string{"source": "logs", "log_metric": lm.Name},
			Value:  float64(c),
		})
	}
	if err := m.ch.InsertMetrics(ctx, "default", points); err != nil {
		m.log.Warn("logmetrics: gravando série", "err", err)
	}
}

// --- Handlers ---

func (h *Handler) ListMetrics(w http.ResponseWriter, r *http.Request) {
	lms, err := h.st.ListLogMetrics(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if lms == nil {
		lms = []store.LogMetric{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"log_metrics": lms})
}

func (h *Handler) CreateMetric(w http.ResponseWriter, r *http.Request) {
	var req store.LogMetric
	if err := decode(r, &req); err != nil || req.Name == "" || req.MetricName == "" {
		http.Error(w, "name e metric_name obrigatórios", http.StatusBadRequest)
		return
	}
	id, err := h.st.CreateLogMetric(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (h *Handler) DeleteMetric(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	if err := h.st.DeleteLogMetric(r.Context(), id); err != nil {
		http.Error(w, "não encontrado", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

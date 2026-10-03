// Package writer consome lotes de métricas do NATS e os insere no ClickHouse,
// com ack só após a inserção confirmada (durabilidade — ver ADR 003).
package writer

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/eduardorarruda/revoada/gateway/internal/chhttp"
	"github.com/eduardorarruda/revoada/gateway/internal/metrics"
	"github.com/eduardorarruda/revoada/gateway/internal/model"
)

// Writer insere métricas no ClickHouse e mantém contadores internos (dogfooding /metrics).
type Writer struct {
	ch          *chhttp.Client
	rowsWritten atomic.Int64
	batches     atomic.Int64
	errors      atomic.Int64
}

// New cria o writer.
func New(ch *chhttp.Client) *Writer { return &Writer{ch: ch} }

// Handle é o MetricsHandler do consumer: insere o lote; erro → NATS reentrega (sem perda).
func (w *Writer) Handle(ctx context.Context, batch []model.Metric) error {
	if len(batch) == 0 {
		return nil
	}
	if err := w.ch.InsertMetrics(ctx, batch); err != nil {
		w.errors.Add(1)
		return err
	}
	w.rowsWritten.Add(int64(len(batch)))
	w.batches.Add(1)
	return nil
}

// Stats devolve os contadores acumulados.
func (w *Writer) Stats() (rows, batches, errs int64) {
	return w.rowsWritten.Load(), w.batches.Load(), w.errors.Load()
}

// PrometheusHandler expõe os contadores internos no formato de exposição Prometheus
// (dogfooding — o próprio pipeline é observável em /metrics).
//
// Além dos três contadores do writer, ele renderiza o REGISTRO GLOBAL do pacote
// metrics. Sem isso, todo contador de recusa criado no gateway (relógio fora de
// faixa, teto de cardinalidade, rate limit por IP, lote perdido pelo batcher) ficava
// invisível: `/metrics` é servido por este handler e nada mais chamava
// metrics.Write/metrics.Handler — o descarte continuava sem número, que é
// exatamente a falha que aqueles contadores existem para fechar.
func (w *Writer) PrometheusHandler() http.HandlerFunc {
	return func(rw http.ResponseWriter, _ *http.Request) {
		rows, batches, errs := w.Stats()
		rw.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprintf(rw, "# HELP revoada_writer_rows_written_total Linhas de métrica inseridas no ClickHouse.\n")
		fmt.Fprintf(rw, "# TYPE revoada_writer_rows_written_total counter\n")
		fmt.Fprintf(rw, "revoada_writer_rows_written_total %d\n", rows)
		fmt.Fprintf(rw, "# HELP revoada_writer_batches_total Lotes inseridos.\n")
		fmt.Fprintf(rw, "# TYPE revoada_writer_batches_total counter\n")
		fmt.Fprintf(rw, "revoada_writer_batches_total %d\n", batches)
		fmt.Fprintf(rw, "# HELP revoada_writer_errors_total Falhas de inserção (reentregues pelo NATS).\n")
		fmt.Fprintf(rw, "# TYPE revoada_writer_errors_total counter\n")
		fmt.Fprintf(rw, "revoada_writer_errors_total %d\n", errs)
		metrics.Write(rw)
	}
}

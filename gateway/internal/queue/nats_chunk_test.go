package queue

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/gateway/internal/model"
)

func makeMetrics(n int) []model.Metric {
	out := make([]model.Metric, n)
	ts := time.Unix(0, 0).UTC()
	for i := range out {
		out[i] = model.Metric{
			TenantID: "tenant-abc",
			Metric:   "cpu.usage.percent.core",
			Labels:   map[string]string{"host": "server-01", "core": "0", "mode": "user"},
			TS:       ts,
			Value:    float64(i),
		}
	}
	return out
}

// TestChunkPublishSplits: um lote grande vira várias mensagens, cada uma <= limit,
// e a soma dos registros é preservada.
func TestChunkPublishSplits(t *testing.T) {
	metrics := makeMetrics(5000)
	full, _ := json.Marshal(metrics)
	limit := len(full) / 8 // força fragmentação

	var msgs [][]byte
	publish := func(b []byte) error {
		cp := append([]byte(nil), b...)
		msgs = append(msgs, cp)
		return nil
	}
	var oversize int
	onOversize := func([]model.Metric) { oversize++ }

	if err := chunkPublish(metrics, limit, publish, onOversize); err != nil {
		t.Fatalf("chunkPublish erro: %v", err)
	}
	if len(msgs) < 2 {
		t.Fatalf("esperava fragmentação em >=2 mensagens, veio %d", len(msgs))
	}
	total := 0
	for i, m := range msgs {
		if len(m) > limit {
			t.Fatalf("mensagem %d tem %d bytes > limite %d", i, len(m), limit)
		}
		var part []model.Metric
		if err := json.Unmarshal(m, &part); err != nil {
			t.Fatalf("mensagem %d inválida: %v", i, err)
		}
		total += len(part)
	}
	if total != len(metrics) {
		t.Fatalf("registros publicados=%d, quer %d", total, len(metrics))
	}
	if oversize != 0 {
		t.Fatalf("oversize=%d, quer 0", oversize)
	}
}

// TestChunkPublishSingleMessage: lote pequeno cabe numa única mensagem.
func TestChunkPublishSingleMessage(t *testing.T) {
	metrics := makeMetrics(3)
	var msgs [][]byte
	publish := func(b []byte) error { msgs = append(msgs, b); return nil }

	if err := chunkPublish(metrics, 1<<20, publish, nil); err != nil {
		t.Fatalf("erro: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("esperava 1 mensagem, veio %d", len(msgs))
	}
}

// TestChunkPublishOversizeSingle: um único registro acima do limite é entregue a
// onOversize (log/skip) sem derrubar o restante do lote.
func TestChunkPublishOversizeSingle(t *testing.T) {
	metrics := makeMetrics(4)
	// limite minúsculo: cada registro isolado ainda o excede.
	var msgs [][]byte
	publish := func(b []byte) error { msgs = append(msgs, b); return nil }
	var oversize [][]model.Metric
	onOversize := func(m []model.Metric) {
		oversize = append(oversize, append([]model.Metric(nil), m...))
	}

	if err := chunkPublish(metrics, 5, publish, onOversize); err != nil {
		t.Fatalf("erro: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("nada deveria ser publicado, veio %d mensagens", len(msgs))
	}
	if len(oversize) != 4 {
		t.Fatalf("esperava 4 registros oversize, veio %d", len(oversize))
	}
	for _, m := range oversize {
		if len(m) != 1 {
			t.Fatalf("oversize deve ser registro isolado, veio %d", len(m))
		}
	}
}

// TestChunkPublishEmpty: lote vazio não publica nada.
func TestChunkPublishEmpty(t *testing.T) {
	called := false
	publish := func([]byte) error { called = true; return nil }
	if err := chunkPublish(nil, 1000, publish, nil); err != nil {
		t.Fatalf("erro: %v", err)
	}
	if called {
		t.Fatal("não deveria publicar lote vazio")
	}
}

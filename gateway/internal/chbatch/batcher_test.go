package chbatch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func quietLog() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

// fakeInsert registra os lotes recebidos e pode ser configurado para falhar.
type fakeInsert struct {
	mu      sync.Mutex
	batches [][]int
	failN   int // falha nas primeiras failN chamadas
	calls   int
}

func (f *fakeInsert) insert(_ context.Context, rows []int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.failN {
		return errors.New("boom")
	}
	cp := append([]int(nil), rows...)
	f.batches = append(f.batches, cp)
	return nil
}

func (f *fakeInsert) totalRows() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, b := range f.batches {
		n += len(b)
	}
	return n
}

func (f *fakeInsert) numBatches() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.batches)
}

// TestFlushBySize: acumula até MaxRows e faz flush sem esperar o intervalo.
func TestFlushBySize(t *testing.T) {
	fi := &fakeInsert{}
	b := New(Config[int]{
		Name: "t", Insert: fi.insert,
		MaxRows: 10, Interval: time.Hour, QueueLen: 64, Log: quietLog(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	b.Start(ctx)

	// 12 linhas em enqueues de 4 -> ultrapassa 10 e deve dar flush por tamanho.
	for i := 0; i < 3; i++ {
		if !b.Enqueue([]int{1, 2, 3, 4}) {
			t.Fatalf("enqueue %d recusado", i)
		}
	}
	waitFor(t, func() bool { return fi.totalRows() >= 10 }, time.Second)

	cancel()
	b.Stop()
	if got := fi.totalRows(); got != 12 {
		t.Fatalf("totalRows=%d, quer 12", got)
	}
}

// TestFlushByTime: menos de MaxRows, o flush ocorre pelo intervalo.
func TestFlushByTime(t *testing.T) {
	fi := &fakeInsert{}
	b := New(Config[int]{
		Name: "t", Insert: fi.insert,
		MaxRows: 1000, Interval: 30 * time.Millisecond, QueueLen: 64, Log: quietLog(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.Start(ctx)

	b.Enqueue([]int{1, 2, 3})
	waitFor(t, func() bool { return fi.totalRows() == 3 }, time.Second)
	if fi.numBatches() == 0 {
		t.Fatal("esperava flush por tempo")
	}
}

// TestBackpressure: fila cheia -> Enqueue devolve false e conta rejeição.
func TestBackpressure(t *testing.T) {
	block := make(chan struct{})
	insert := func(_ context.Context, _ []int) error {
		<-block // trava o worker no primeiro flush
		return nil
	}
	b := New(Config[int]{
		Name: "t", Insert: insert,
		MaxRows: 1, Interval: time.Hour, QueueLen: 1, Log: quietLog(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	b.Start(ctx)

	// Enche: 1 lote pega o worker (trava no insert), 1 ocupa o buffer do canal.
	// A partir daí, Enqueue deve recusar.
	rejected := false
	for i := 0; i < 200; i++ {
		if !b.Enqueue([]int{i}) {
			rejected = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !rejected {
		close(block)
		cancel()
		b.Stop()
		t.Fatal("esperava backpressure (Enqueue=false), mas nunca recusou")
	}
	if b.Rejected() == 0 {
		t.Fatal("contador de rejeição não incrementou")
	}
	close(block)
	cancel()
	b.Stop()
}

// TestDrainOnShutdown: ao cancelar o ctx, o worker drena e faz flush do que restou.
func TestDrainOnShutdown(t *testing.T) {
	fi := &fakeInsert{}
	b := New(Config[int]{
		Name: "t", Insert: fi.insert,
		MaxRows: 100000, Interval: time.Hour, QueueLen: 256, Log: quietLog(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	b.Start(ctx)

	for i := 0; i < 50; i++ {
		if !b.Enqueue([]int{i}) {
			t.Fatalf("enqueue %d recusado", i)
		}
	}
	// Nada deve ter sido gravado ainda (sem flush por tamanho/tempo).
	cancel()
	b.Stop()
	if got := fi.totalRows(); got != 50 {
		t.Fatalf("após drain totalRows=%d, quer 50", got)
	}
}

// TestRetryThenSuccess: insert falha algumas vezes e depois grava; sem perdas.
func TestRetryThenSuccess(t *testing.T) {
	fi := &fakeInsert{failN: 2}
	b := New(Config[int]{
		Name: "t", Insert: fi.insert,
		MaxRows: 3, Interval: time.Hour, QueueLen: 8, Log: quietLog(),
		MaxRetries: 5, BackoffBase: time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	b.Start(ctx)

	b.Enqueue([]int{1, 2, 3})
	waitFor(t, func() bool { return fi.totalRows() == 3 }, 2*time.Second)

	cancel()
	b.Stop()
	if b.Dropped() != 0 {
		t.Fatalf("dropped=%d, quer 0", b.Dropped())
	}
}

// TestDropAfterRetries: insert sempre falha -> lote é descartado e contado.
func TestDropAfterRetries(t *testing.T) {
	insert := func(_ context.Context, _ []int) error { return errors.New("always") }
	b := New(Config[int]{
		Name: "t", Insert: insert,
		MaxRows: 3, Interval: time.Hour, QueueLen: 8, Log: quietLog(),
		MaxRetries: 2, BackoffBase: time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	b.Start(ctx)

	b.Enqueue([]int{1, 2, 3})
	waitFor(t, func() bool { return b.Dropped() == 3 }, 2*time.Second)

	cancel()
	b.Stop()
	if b.Dropped() != 3 {
		t.Fatalf("dropped=%d, quer 3", b.Dropped())
	}
}

func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condição não satisfeita no tempo limite")
}

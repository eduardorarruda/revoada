// Package chbatch fornece um batcher genérico e assíncrono para escrever no
// ClickHouse. Os handlers de logs/traces ENFILEIRAM (não-bloqueante) e retornam
// rápido; um worker em background acumula e faz FLUSH ao atingir N linhas OU um
// intervalo T (o que vier primeiro). Isso troca muitos inserts pequenos ("too many
// parts") por poucos inserts grandes e evita que um ClickHouse lento bloqueie o
// handler até o timeout (descartando o lote sem WAL).
package chbatch

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/eduardorarruda/revoada/gateway/internal/metrics"
)

// InsertFunc grava um lote no destino (ex.: chhttp.Client.InsertLogs). Deve respeitar
// o ctx recebido (que carrega o timeout do flush).
type InsertFunc[T any] func(ctx context.Context, rows []T) error

// Config parametriza um Batcher.
type Config[T any] struct {
	Name         string        // rótulo p/ logs/métricas (ex.: "logs", "spans")
	Insert       InsertFunc[T] // função de escrita em lote (injetável — fake em testes)
	MaxRows      int           // flush ao acumular esse nº de linhas
	Interval     time.Duration // flush no máximo a cada esse intervalo
	QueueLen     int           // capacidade da fila (lotes pendentes) — backpressure
	Log          *slog.Logger  // opcional (default slog.Default())
	MaxRetries   int           // tentativas extras em erro de insert (default 3)
	BackoffBase  time.Duration // atraso inicial do backoff (default 200ms)
	FlushTimeout time.Duration // timeout de cada insert (default 30s)
}

// Batcher acumula lotes de T e os escreve periodicamente.
type Batcher[T any] struct {
	name         string
	insert       InsertFunc[T]
	maxRows      int
	interval     time.Duration
	log          *slog.Logger
	maxRetries   int
	backoffBase  time.Duration
	flushTimeout time.Duration

	ch       chan []T
	done     chan struct{}
	queueLen int

	dropped  atomic.Int64 // linhas perdidas após esgotar retries
	enqueued atomic.Int64
	rejected atomic.Int64 // enfileiramentos recusados por backpressure (viram 429)
}

// New cria um Batcher (ainda não iniciado — chame Start).
func New[T any](cfg Config[T]) *Batcher[T] {
	if cfg.MaxRows < 1 {
		cfg.MaxRows = 5000
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Second
	}
	if cfg.QueueLen < 1 {
		cfg.QueueLen = 256
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = 200 * time.Millisecond
	}
	if cfg.FlushTimeout <= 0 {
		cfg.FlushTimeout = 30 * time.Second
	}
	b := &Batcher[T]{
		name:         cfg.Name,
		insert:       cfg.Insert,
		maxRows:      cfg.MaxRows,
		interval:     cfg.Interval,
		log:          cfg.Log,
		maxRetries:   cfg.MaxRetries,
		backoffBase:  cfg.BackoffBase,
		flushTimeout: cfg.FlushTimeout,
		ch:           make(chan []T, cfg.QueueLen),
		done:         make(chan struct{}),
		queueLen:     cfg.QueueLen,
	}
	b.publishMetrics()
	return b
}

// publishMetrics expõe os contadores do batcher no /metrics.
//
// POR QUE: dropped/rejected existiam desde o início mas NUNCA eram lidos fora dos
// testes. Um lote de logs ou traces descartado após as 4 tentativas de insert sumia
// deixando só uma linha de log — a série continuava "contínua" no painel, sem buraco
// visível, porque ninguém sabia que faltava. Contador que ninguém lê é o mesmo que não
// existir.
func (b *Batcher[T]) publishMetrics() {
	p := "revoada_chbatch_" + b.name
	metrics.NewCounterFunc(p+"_dropped_rows_total",
		"Linhas descartadas pelo batcher após esgotar as tentativas de insert.",
		func() float64 { return float64(b.dropped.Load()) })
	metrics.NewCounterFunc(p+"_rejected_batches_total",
		"Lotes recusados por fila cheia (contrapressão; viram 429 para o emissor).",
		func() float64 { return float64(b.rejected.Load()) })
	metrics.NewCounterFunc(p+"_enqueued_rows_total",
		"Linhas enfileiradas no batcher.",
		func() float64 { return float64(b.enqueued.Load()) })
	metrics.NewGaugeFunc(p+"_queue_depth",
		"Lotes pendentes na fila do batcher.",
		func() float64 { return float64(len(b.ch)) })
	metrics.NewGaugeFunc(p+"_queue_capacity",
		"Capacidade da fila do batcher.",
		func() float64 { return float64(b.queueLen) })
}

// Enqueue tenta enfileirar rows SEM bloquear. Devolve false se a fila estiver cheia
// (ClickHouse não acompanha) — o chamador deve sinalizar 429/503 para o agente
// re-bufferar. Nunca descarta em silêncio.
func (b *Batcher[T]) Enqueue(rows []T) bool {
	if len(rows) == 0 {
		return true
	}
	select {
	case b.ch <- rows:
		b.enqueued.Add(int64(len(rows)))
		return true
	default:
		b.rejected.Add(1)
		return false
	}
}

// Start lança o worker, ligado ao ctx. Ao cancelar o ctx, o worker DRENA a fila e faz
// flush do que restou antes de sair (shutdown gracioso). Chame Stop para aguardar.
func (b *Batcher[T]) Start(ctx context.Context) {
	go b.run(ctx)
}

// Stop aguarda o worker drenar e sair. Deve ser chamado após cancelar o ctx passado
// a Start (ex.: no defer do main, depois do signal.NotifyContext).
func (b *Batcher[T]) Stop() { <-b.done }

// Dropped/Rejected expõem contadores para observabilidade/testes.
func (b *Batcher[T]) Dropped() int64  { return b.dropped.Load() }
func (b *Batcher[T]) Rejected() int64 { return b.rejected.Load() }

func (b *Batcher[T]) run(ctx context.Context) {
	defer close(b.done)

	timer := time.NewTimer(b.interval)
	defer timer.Stop()

	var buf []T
	resetTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(b.interval)
	}
	flush := func() {
		if len(buf) == 0 {
			return
		}
		b.flush(buf)
		buf = buf[:0]
	}

	for {
		select {
		case <-ctx.Done():
			// Drena o que já foi enfileirado e faz o flush final.
			b.drain(&buf)
			flush()
			return
		case rows := <-b.ch:
			buf = append(buf, rows...)
			if len(buf) >= b.maxRows {
				flush()
				resetTimer()
			}
		case <-timer.C:
			flush()
			timer.Reset(b.interval)
		}
	}
}

// drain consome, sem bloquear, tudo que já está na fila para o buffer, fazendo flush
// intermediário se ultrapassar maxRows (evita um insert gigante no shutdown).
func (b *Batcher[T]) drain(buf *[]T) {
	for {
		select {
		case rows := <-b.ch:
			*buf = append(*buf, rows...)
			if len(*buf) >= b.maxRows {
				b.flush(*buf)
				*buf = (*buf)[:0]
			}
		default:
			return
		}
	}
}

// flush escreve rows com retry/backoff limitado. Persistindo o erro, LOGA e conta as
// perdas (degrade visível, nunca silencioso). Usa context.Background com timeout
// próprio para não ser abortado pelo cancelamento do ctx de shutdown no meio da escrita.
func (b *Batcher[T]) flush(rows []T) {
	for attempt := 0; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), b.flushTimeout)
		err := b.insert(ctx, rows)
		cancel()
		if err == nil {
			return
		}
		if attempt >= b.maxRetries {
			b.dropped.Add(int64(len(rows)))
			b.log.Error("chbatch: flush falhou após retries, descartando lote",
				"batcher", b.name, "rows", len(rows), "attempts", attempt+1, "err", err)
			return
		}
		backoff := b.backoffBase << attempt
		b.log.Warn("chbatch: flush falhou, retry",
			"batcher", b.name, "rows", len(rows), "attempt", attempt+1, "err", err)
		time.Sleep(backoff)
	}
}

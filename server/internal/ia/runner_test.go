package ia

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/chquery"
)

var reMinutoMarcador = regexp.MustCompile(`toDateTime64\((\d+), 3\)`)

// chMinutos simula o ClickHouse do runner: toda consulta do marcador responde "já
// gravado" (FecharMinuto sai cedo, sem calcular nada) e registra o minuto pedido;
// falharEm faz a consulta daquele minuto devolver erro.
type chMinutos struct {
	mu       sync.Mutex
	minutos  []time.Time
	falharEm time.Time
}

func (c *chMinutos) QueryJSON(_ context.Context, q string) ([]map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := reMinutoMarcador.FindStringSubmatch(q)
	if m == nil {
		return nil, errors.New("consulta inesperada no teste do runner: " + q)
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	quando := time.Unix(n, 0).UTC()
	c.minutos = append(c.minutos, quando)
	if quando.Equal(c.falharEm) {
		return nil, errors.New("clickhouse fora")
	}
	return []map[string]any{{"n": "1"}}, nil
}

func (c *chMinutos) Exec(context.Context, string) error { return nil }

func (c *chMinutos) InsertMetricsAt(context.Context, string, time.Time, []chquery.MetricPoint) error {
	return nil
}

func TestRunnerTique(t *testing.T) {
	agora := time.Date(2026, 10, 7, 12, 30, 42, 0, time.UTC)
	// alvo = agora - atraso, truncado no minuto, menos um (o minuto inteiro anterior)
	alvo := time.Date(2026, 10, 7, 12, 24, 0, 0, time.UTC)
	minuto := func(n int) time.Time { return alvo.Add(time.Duration(n) * time.Minute) }
	casos := []struct {
		nome       string
		ultimo     time.Time // zero = primeiro tique depois de subir
		falharEm   time.Time
		querFechar []time.Time // minutos tentados, em ordem
		querUltimo time.Time
	}{
		{"primeiro tique fecha só o alvo", time.Time{}, time.Time{}, []time.Time{alvo}, alvo},
		{"em dia não refaz nada", alvo, time.Time{}, nil, alvo},
		{"lacuna curta recupera tudo", minuto(-3), time.Time{}, []time.Time{minuto(-2), minuto(-1), alvo}, alvo},
		{"parada longa recupera só os últimos 10", minuto(-180), time.Time{},
			[]time.Time{minuto(-9), minuto(-8), minuto(-7), minuto(-6), minuto(-5), minuto(-4), minuto(-3), minuto(-2), minuto(-1), alvo}, alvo},
		{"erro para o laço e não avança", minuto(-5), minuto(-3), []time.Time{minuto(-4), minuto(-3)}, minuto(-4)},
		{"erro no primeiro minuto não avança nada", minuto(-2), minuto(-1), []time.Time{minuto(-1)}, minuto(-2)},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			ch := &chMinutos{falharEm: c.falharEm}
			h := New(ch, &lojaFalsa{}, nil)
			h.agora = func() time.Time { return agora }
			r := NewRunner(h, slog.New(slog.NewTextHandler(io.Discard, nil)))
			r.ultimo = c.ultimo
			r.tique(context.Background())
			if len(ch.minutos) != len(c.querFechar) {
				t.Fatalf("fechou %v, quer %v", ch.minutos, c.querFechar)
			}
			for i := range c.querFechar {
				if !ch.minutos[i].Equal(c.querFechar[i]) {
					t.Fatalf("minuto %d = %v, quer %v", i, ch.minutos[i], c.querFechar[i])
				}
			}
			if !r.ultimo.Equal(c.querUltimo) {
				t.Fatalf("ultimo = %v, quer %v", r.ultimo, c.querUltimo)
			}
		})
	}
}

// TestRunnerRetomaDoMinutoQueFalhou: depois do erro, o tique seguinte começa no minuto
// que falhou — nenhum minuto fica para trás sem as séries llm.*.
func TestRunnerRetomaDoMinutoQueFalhou(t *testing.T) {
	agora := time.Date(2026, 10, 7, 12, 30, 42, 0, time.UTC)
	alvo := time.Date(2026, 10, 7, 12, 24, 0, 0, time.UTC)
	ch := &chMinutos{falharEm: alvo.Add(-time.Minute)}
	h := New(ch, &lojaFalsa{}, nil)
	h.agora = func() time.Time { return agora }
	r := NewRunner(h, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.ultimo = alvo.Add(-3 * time.Minute)
	r.tique(context.Background())
	ch.falharEm = time.Time{}
	ch.minutos = nil
	r.tique(context.Background())
	if len(ch.minutos) != 2 || !ch.minutos[0].Equal(alvo.Add(-time.Minute)) || !r.ultimo.Equal(alvo) {
		t.Fatalf("retomada: %v, ultimo %v", ch.minutos, r.ultimo)
	}
}

// TestRunnerRunParaComOContexto: Run faz um tique e sai quando o contexto acaba.
func TestRunnerRunParaComOContexto(t *testing.T) {
	ch := &chMinutos{}
	h := New(ch, &lojaFalsa{}, nil)
	r := NewRunner(h, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	feito := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(feito)
	}()
	select {
	case <-feito:
	case <-time.After(5 * time.Second):
		t.Fatal("Run não parou com o contexto cancelado")
	}
	if len(ch.minutos) != 1 {
		t.Fatalf("Run deveria fazer um tique antes de sair: %v", ch.minutos)
	}
}

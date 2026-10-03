package otlp

import (
	"strings"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/gateway/internal/metrics"
	"github.com/eduardorarruda/revoada/gateway/internal/model"
)

func lerMetrics() string {
	var b strings.Builder
	metrics.Write(&b)
	return b.String()
}

// TestClockSkewExpostoPorHost: a idade do carimbo de cada host tinha de virar NÚMERO.
// Ela existia em memória no meio do process() e era jogada fora — e é justamente o
// desvio DENTRO da tolerância que faz o Mural dizer "sem métricas" para um host que
// reporta normalmente: o ponto no futuro passa na guarda e é gravado, mas as consultas
// de último valor filtram `ts <= now()` e não o enxergam. Nada na tela explicava.
//
// O SINAL segue a palavra "idade": positivo é carimbo velho, negativo é carimbo no
// futuro (host adiantado).
func TestClockSkewExpostoPorHost(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	pts := []model.Metric{
		// host adiantado 47 s (passa na tolerância de 2 min e some do Mural).
		{Metric: "cpu", Labels: map[string]string{"host": "vm-adiantada"}, TS: now.Add(47 * time.Second)},
		{Metric: "mem", Labels: map[string]string{"host": "vm-adiantada"}, TS: now.Add(30 * time.Second)},
		// host em dia (o lote tem a idade natural de coleta).
		{Metric: "cpu", Labels: map[string]string{"host": "vm-ok"}, TS: now.Add(-2 * time.Second)},
	}
	observeClockSkew(pts, now, "")

	out := lerMetrics()
	if !strings.Contains(out, `revoada_agent_ts_age_seconds{host="vm-adiantada"} -47`) {
		t.Fatalf("idade do carimbo do host adiantado ausente/errada em /metrics:\n%s", trechoSkew(out))
	}
	if !strings.Contains(out, `revoada_agent_ts_age_seconds{host="vm-ok"} 2`) {
		t.Fatalf("idade do carimbo do host em dia ausente/errada:\n%s", trechoSkew(out))
	}
}

// TestClockSkewUsaTSMaisRecente: o lote cobre um intervalo; a medida é a do ponto mais
// novo, senão a idade natural do lote inflaria a leitura.
func TestClockSkewUsaTSMaisRecente(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	observeClockSkew([]model.Metric{
		{Labels: map[string]string{"host": "vm-lote"}, TS: now.Add(-60 * time.Second)},
		{Labels: map[string]string{"host": "vm-lote"}, TS: now.Add(-1 * time.Second)},
	}, now, "")
	if !strings.Contains(lerMetrics(), `revoada_agent_ts_age_seconds{host="vm-lote"} 1`) {
		t.Fatalf("a medida deveria vir do ts mais recente:\n%s", trechoSkew(lerMetrics()))
	}
}

// TestClockSkewCaiNoHostnameDaChave: ponto sem rótulo host usa o hostname vinculado à
// serverkey — o host mais difícil de diagnosticar não pode ficar de fora do medidor.
func TestClockSkewCaiNoHostnameDaChave(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	observeClockSkew([]model.Metric{
		{Labels: map[string]string{}, TS: now.Add(10 * time.Second)},
	}, now, "host-da-chave")
	if !strings.Contains(lerMetrics(), `revoada_agent_ts_age_seconds{host="host-da-chave"} -10`) {
		t.Fatalf("medida sem rótulo host não caiu no hostname da chave:\n%s", trechoSkew(lerMetrics()))
	}
}

// TestClockSkewTemTeto: o rótulo vem do PAYLOAD, então o medidor precisa de teto —
// senão um emissor inventando um host por requisição vira vazamento de memória.
func TestClockSkewTemTeto(t *testing.T) {
	v := metrics.NewGaugeVec("teste_skew_teto", "teto", 8, time.Hour, "host")
	for i := 0; i < 200; i++ {
		v.Set(1, string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	if v.Len() > 8 {
		t.Fatalf("séries=%d acima do teto 8", v.Len())
	}
}

func trechoSkew(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, "ts_age") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

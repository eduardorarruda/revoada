package logtail

import (
	"strings"
	"testing"
	"time"
)

// clock controlável para testar o token bucket sem depender do relógio real.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }

func TestRateLimiterUnlimited(t *testing.T) {
	if rl := newRateLimiter(0, time.Now); rl != nil {
		t.Fatalf("perSec<=0 deveria ser ilimitado (nil), veio %v", rl)
	}
	var rl *rateLimiter // nil = ilimitado
	if got := rl.admit(1000); got != 1000 {
		t.Fatalf("limitador nil deveria admitir tudo; admit=%d", got)
	}
	if rl.notice() != nil {
		t.Fatal("limitador nil não emite aviso")
	}
}

func TestRateLimiterAdmitAndRefill(t *testing.T) {
	c := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	rl := newRateLimiter(10, c.now) // 10 linhas/s, 1 s de cota inicial

	// Primeira janela: 10 tokens disponíveis.
	if got := rl.admit(5); got != 5 {
		t.Fatalf("admit(5) esperado 5, veio %d", got)
	}
	if got := rl.admit(10); got != 5 { // só restam 5
		t.Fatalf("admit(10) esperado 5, veio %d", got)
	}
	if rl.dropped != 5 {
		t.Fatalf("esperado 5 descartes, veio %d", rl.dropped)
	}

	// Passa 1s: +10 tokens (agora 10).
	c.add(time.Second)
	if got := rl.admit(20); got != 10 {
		t.Fatalf("após refill de 1s, admit(20) esperado 10, veio %d", got)
	}
	if rl.dropped != 15 { // 5 + 10
		t.Fatalf("esperado 15 descartes acumulados, veio %d", rl.dropped)
	}
}

func TestRateLimiterBurstCap(t *testing.T) {
	c := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	rl := newRateLimiter(10, c.now) // teto = 10 × janelaDeRajada (10 s) = 100
	rl.admit(1)                     // estabelece o baseline de tempo (last=t0)
	c.add(time.Hour)                // muito tempo parado não acumula além do teto
	teto := int(10 * janelaDeRajada.Seconds())
	if got := rl.admit(1000); got != teto {
		t.Fatalf("burst deveria ser limitado a %d, veio %d", teto, got)
	}
}

func TestRateLimiterNotice(t *testing.T) {
	c := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	rl := newRateLimiter(10, c.now)
	rl.admit(10) // consome tudo
	rl.admit(7)  // descarta 7
	n := rl.notice()
	if n == nil {
		t.Fatal("com descarte, notice() deveria devolver um aviso")
	}
	if n.severity != "WARN" || !strings.Contains(n.body, "7") {
		t.Fatalf("aviso inesperado: sev=%s body=%q", n.severity, n.body)
	}
	if rl.dropped != 0 {
		t.Fatalf("notice() deveria zerar o contador; veio %d", rl.dropped)
	}
	// Dentro de 30s não repete o aviso.
	rl.admit(100)
	c.add(29 * time.Second)
	if rl.notice() != nil {
		t.Fatal("notice() não deveria repetir dentro de 30s")
	}
	// Passados 30s, volta a avisar.
	c.add(2 * time.Second)
	if rl.notice() == nil {
		t.Fatal("após 30s com novos descartes, notice() deveria avisar")
	}
}

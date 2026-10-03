package agenda

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// PROVA: uma sondagem de 3 s marcada "+60 s" a partir do fim volta a ser +60 s a
// partir do início — antes ficava em 63 s e, no degrau de 5 s do agendador, 65 s.
func TestReancorarCadenciaDeInicioAInicio(t *testing.T) {
	inicio := time.Unix(1_000_000, 0)
	fim := inicio.Add(3 * time.Second)
	prox := fim.Add(60 * time.Second)
	got := Reancorar(&prox, inicio, fim)
	if want := inicio.Add(60 * time.Second); !got.Equal(want) {
		t.Fatalf("próxima = %v, quer %v (início + 60 s)", got, want)
	}
}

// PROVA: execução mais longa que o intervalo não é marcada no passado.
func TestReancorarNuncaAntesDoFim(t *testing.T) {
	inicio := time.Unix(1_000_000, 0)
	fim := inicio.Add(40 * time.Second)
	prox := fim.Add(30 * time.Second)
	got := Reancorar(&prox, inicio, fim)
	if !got.Equal(fim) {
		t.Fatalf("próxima = %v, quer o próprio fim %v", got, fim)
	}
	if Reancorar(nil, inicio, fim) != nil {
		t.Fatal("nil deveria continuar nil")
	}
}

// PROVA: com a âncora no início e a Folga, a execução cai no tick certo mesmo com
// atraso entre o tick e o início da sondagem (antes ia para o tick seguinte).
func TestComFolgaExecucaoCaiNoTickCerto(t *testing.T) {
	tick := time.Unix(1_000_000, 0)
	inicio := tick.Add(80 * time.Millisecond) // consulta ao banco + goroutine
	fim := inicio.Add(15 * time.Millisecond)
	prox := Reancorar(ptr(fim.Add(60*time.Second)), inicio, fim)
	proximoTick := tick.Add(60 * time.Second).Add(2 * time.Millisecond)
	if prox.After(proximoTick.Add(Folga)) {
		t.Fatalf("a execução marcada para %v não é devida no tick de %v nem com a folga", prox, proximoTick)
	}
}

// PROVA: o despachante não bloqueia quem dispara, não duplica um id em voo e
// respeita o teto de simultâneas.
func TestEmVooNaoBloqueiaNemDuplica(t *testing.T) {
	e := NovoEmVoo(2)
	libera := make(chan struct{})
	var rodando, pico, total atomic.Int32
	var mu sync.Mutex
	fn := func() {
		n := rodando.Add(1)
		mu.Lock()
		if n > pico.Load() {
			pico.Store(n)
		}
		mu.Unlock()
		total.Add(1)
		<-libera
		rodando.Add(-1)
	}
	ctx := context.Background()
	t0 := time.Now()
	for id := int64(1); id <= 5; id++ {
		if !e.Disparar(ctx, id, fn) {
			t.Fatalf("id %d recusado sem estar em voo", id)
		}
	}
	if time.Since(t0) > 200*time.Millisecond {
		t.Fatal("Disparar bloqueou quem dispara")
	}
	if e.Disparar(ctx, 3, fn) {
		t.Fatal("id 3 disparado de novo enquanto estava em voo")
	}
	time.Sleep(50 * time.Millisecond)
	if p := pico.Load(); p > 2 {
		t.Fatalf("%d simultâneas, teto 2", p)
	}
	close(libera)
	e.Esperar()
	if total.Load() != 5 {
		t.Fatalf("rodaram %d de 5", total.Load())
	}
	if !e.Disparar(ctx, 3, func() {}) {
		t.Fatal("id 3 deveria poder rodar de novo depois de terminar")
	}
	e.Esperar()
}

func ptr(t time.Time) *time.Time { return &t }

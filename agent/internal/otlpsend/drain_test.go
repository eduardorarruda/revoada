package otlpsend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/agent/internal/buffer"
	"github.com/eduardorarruda/revoada/agent/internal/collect"
)

// senderDeTeste monta um Sender apontado para `srv`, com WAL num diretório
// descartável e SEM pausa de relógio real (a pausa é contada, não dormida).
func senderDeTeste(t *testing.T, srv *httptest.Server) (*Sender, *buffer.Buffer, *[]time.Duration) {
	t.Helper()
	buf, err := buffer.New(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	s := New(srv.URL, "k", "host1", nil, buf)
	var mu sync.Mutex
	pausas := []time.Duration{}
	s.dormir = func(_ context.Context, d time.Duration) {
		mu.Lock()
		pausas = append(pausas, d)
		mu.Unlock()
	}
	return s, buf, &pausas
}

func encher(t *testing.T, buf *buffer.Buffer, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := buf.Put(int64(i+1), []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestDrainRespeitaTetoPorCiclo é a prova do "estouro da boiada": o drain antigo
// reenviava o WAL inteiro de uma vez (medido: 32 POSTs em 18 ms ao liberar o
// gateway). Com 100 agentes voltando juntos, isso é um pico que derruba o gateway
// no segundo em que ele volta. Agora sai no máximo maxLotesPorDrain por ciclo.
func TestDrainRespeitaTetoPorCiclo(t *testing.T) {
	var mu sync.Mutex
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		posts++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, buf, pausas := senderDeTeste(t, srv)
	encher(t, buf, 40)

	// Send = drain + o ponto do ciclo. Teto do drain + 1 do ciclo corrente.
	if err := s.Send(t.Context(), []collect.Point{{Name: "x", Value: 1}}, collect.HostInfo{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	mu.Lock()
	got := posts
	mu.Unlock()
	if want := maxLotesPorDrain + 1; got != want {
		t.Fatalf("%d POSTs num ciclo, quero %d (teto %d + o ponto do ciclo)", got, want, maxLotesPorDrain)
	}
	// Uma pausa entre cada par de lotes drenados (não antes do primeiro).
	if n := len(*pausas); n != maxLotesPorDrain-1 {
		t.Fatalf("%d pausas entre lotes, quero %d", n, maxLotesPorDrain-1)
	}
	// E as pausas precisam ter jitter: valores todos idênticos fariam a frota
	// continuar batendo em uníssono, só que mais devagar.
	distintas := map[time.Duration]bool{}
	for _, d := range *pausas {
		if d <= 0 || d > 2*pausaEntreLotes {
			t.Fatalf("pausa fora da faixa esperada: %s", d)
		}
		distintas[d] = true
	}
	if len(distintas) < 2 {
		t.Fatalf("pausas sem jitter (%v): a frota reagruparia", *pausas)
	}

	// O resto da fila continua no disco — nada foi perdido, só adiado.
	restantes, _ := buf.List()
	if len(restantes) != 40-maxLotesPorDrain {
		t.Fatalf("sobraram %d lotes no WAL, quero %d", len(restantes), 40-maxLotesPorDrain)
	}
}

// TestDrainVariosCiclosEsvaziaAFila garante que o teto ATRASA e não PERDE: em
// ciclos sucessivos a fila inteira chega.
func TestDrainVariosCiclosEsvaziaAFila(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, buf, _ := senderDeTeste(t, srv)
	encher(t, buf, 25)
	for i := 0; i < 5; i++ {
		if err := s.Send(t.Context(), nil, collect.HostInfo{}); err != nil {
			t.Fatalf("ciclo %d: %v", i, err)
		}
	}
	if restantes, _ := buf.List(); len(restantes) != 0 {
		t.Fatalf("WAL ainda com %d lotes depois de 5 ciclos", len(restantes))
	}
}

// TestRecuoNo429RespeitaRetryAfter: o gateway diz "pare, volte em 120s". Antes,
// qualquer não-200 virava erro genérico e o agente reenviava o buffer INTEIRO no
// tick seguinte — o agente virava o ataque contra quem acabou de pedir socorro.
func TestRecuoNo429RespeitaRetryAfter(t *testing.T) {
	var mu sync.Mutex
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		posts++
		mu.Unlock()
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	s, buf, _ := senderDeTeste(t, srv)
	relogio := time.Now()
	s.agora = func() time.Time { return relogio }
	encher(t, buf, 30)

	// Ciclo 1: o primeiro POST leva 429 e o drain para na hora.
	if err := s.Send(t.Context(), nil, collect.HostInfo{}); err == nil {
		t.Fatal("Send deveria falhar com o gateway em 429")
	}
	mu.Lock()
	apos1 := posts
	mu.Unlock()
	if apos1 != 1 {
		t.Fatalf("%d POSTs no ciclo do 429, quero 1 (parar no primeiro)", apos1)
	}

	// Ciclos seguintes, dentro da janela: NENHUM POST sai, e o ponto do ciclo vai
	// para o WAL (não se perde nada, só não se insiste).
	for i := 0; i < 3; i++ {
		relogio = relogio.Add(15 * time.Second)
		err := s.Send(t.Context(), []collect.Point{{Name: "x", Value: 1}}, collect.HostInfo{})
		if err == nil || !strings.Contains(err.Error(), "recuo") {
			t.Fatalf("ciclo %d dentro do recuo: erro %v, queria falar de recuo", i, err)
		}
	}
	mu.Lock()
	apos2 := posts
	mu.Unlock()
	if apos2 != apos1 {
		t.Fatalf("saíram %d POSTs durante o recuo, quero 0", apos2-apos1)
	}

	// Passada a janela de 120s, o agente volta a tentar.
	relogio = relogio.Add(121 * time.Second)
	_ = s.Send(t.Context(), nil, collect.HostInfo{})
	mu.Lock()
	apos3 := posts
	mu.Unlock()
	if apos3 == apos2 {
		t.Fatal("o agente não voltou a tentar depois do Retry-After vencer")
	}
	if n, _ := buf.List(); len(n) < 30 {
		t.Fatalf("o WAL perdeu lotes durante o recuo: %d", len(n))
	}
}

// TestRecuoSem429NaoAcontece: um 500 comum (bug do gateway, não sobrecarga) não
// pode calar o agente por 30s — só 429/503 são pedido explícito de recuo.
func TestRecuoSem429NaoAcontece(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	s, _, _ := senderDeTeste(t, srv)
	if err := s.Send(t.Context(), nil, collect.HostInfo{}); err == nil {
		t.Fatal("500 deveria falhar")
	}
	if _, recuando := s.recuando(); recuando {
		t.Fatal("500 não é 429: não pode agendar recuo")
	}
}

func TestRetryAfter(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	casos := []struct {
		nome string
		v    string
		want time.Duration
	}{
		{"segundos", "120", 120 * time.Second},
		{"zero é ignorado (usa o padrão)", "0", 0},
		{"negativo é ignorado", "-5", 0},
		{"ausente", "", 0},
		{"lixo", "logo ali", 0},
		{"data HTTP no futuro", base.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{"data HTTP no passado", base.Add(-time.Hour).Format(http.TimeFormat), 0},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			if got := retryAfter(c.v, base); got != c.want {
				t.Fatalf("retryAfter(%q) = %s, quero %s", c.v, got, c.want)
			}
		})
	}
}

// TestRecuoTemTeto: um Retry-After absurdo (ou proxy confuso) não pode deixar o
// agente mudo por horas — passado recuoMaximo ele volta a tentar, bufferizando.
func TestRecuoTemTeto(t *testing.T) {
	s := New("http://gw", "k", "h", nil, nil)
	agora := time.Now()
	s.agora = func() time.Time { return agora }
	s.marcarRecuo(48 * time.Hour)
	d, recuando := s.recuando()
	if !recuando || d > recuoMaximo {
		t.Fatalf("recuo de %s passou do teto %s", d, recuoMaximo)
	}
}

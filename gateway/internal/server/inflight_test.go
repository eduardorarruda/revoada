package server

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestInFlightRecusaComRetryAfter: estourado o teto, a resposta é 503 COM Retry-After —
// e é rápida. Sem o teto, o gateway respondia 200 e ficava lento até o POST estourar o
// timeout de 10 s do agente, que re-bufferava e mandava de novo: aceitar tudo, não
// entregar nada e ainda multiplicar o tráfego.
func TestInFlightRecusaComRetryAfter(t *testing.T) {
	f := NewInFlight(2)

	solta := make(chan struct{})
	entrou := make(chan struct{}, 2)
	h := f.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entrou <- struct{}{}
		<-solta
		w.WriteHeader(http.StatusOK)
	}))

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/metrics", nil))
			if rec.Code != http.StatusOK {
				t.Errorf("requisição dentro do teto = %d, quer 200", rec.Code)
			}
		}()
	}
	<-entrou
	<-entrou

	// A terceira não cabe: tem de ser recusada AGORA, não enfileirada.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/metrics", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("acima do teto = %d, quer 503", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" {
		t.Fatal("503 sem Retry-After: o agente não tem como saber que deve recuar")
	}

	close(solta)
	wg.Wait()

	// Vaga devolvida: depois do flush, cabe de novo.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("depois de liberar = %d, quer 200 (vaga não foi devolvida)", rec.Code)
	}
}

// TestInFlightTetoZeroDesliga: teto <= 0 é passthrough (não pode virar recusa de tudo).
func TestInFlightTetoZeroDesliga(t *testing.T) {
	f := NewInFlight(0)
	h := f.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for i := 0; i < 50; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/metrics", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("teto desligado recusou: %d", rec.Code)
		}
	}
}

// TestInFlightPico: o pico é o número que diz se o teto está bem dimensionado.
func TestInFlightPico(t *testing.T) {
	f := NewInFlight(4)
	for i := 0; i < 2; i++ {
		if !f.Enter() {
			t.Fatalf("Enter %d recusou dentro do teto", i)
		}
	}
	if got := f.peak.Load(); got != 2 {
		t.Fatalf("pico=%d, quer 2", got)
	}
	f.Leave()
	f.Leave()
	if got := f.cur.Load(); got != 0 {
		t.Fatalf("em voo=%d depois de liberar tudo, quer 0", got)
	}
	if got := f.peak.Load(); got != 2 {
		t.Fatalf("pico não pode diminuir: %d", got)
	}
}

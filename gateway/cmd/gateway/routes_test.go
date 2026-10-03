package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eduardorarruda/revoada/gateway/internal/server"
)

// TestTodaRotaDeIngestaoTemFreio: nenhuma rota pública de ingestão pode ficar sem rate
// limit.
//
// POR QUE este teste existe: GET /probe/assignments estava registrada FORA do rl.Wrap —
// não por decisão, mas por ter sido escrita numa linha diferente das outras. Medido
// antes da correção: 3.000 GETs com X-Revoada-Key rotativa a 5.170 req/s, 401 nas 3.000,
// ZERO 429, e as 3.000 batendo no PostgreSQL (o cache de autenticação cresceu 3.000
// entradas) — o banco que também guarda as sessões do painel e o cofre de credenciais
// SSH. O teste percorre a MESMA tabela que o main monta: uma rota nova nasce coberta,
// ou este teste falha.
func TestTodaRotaDeIngestaoTemFreio(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	hs := ingestHandlers{
		Metrics: ok, Logs: ok, Traces: ok, JSONLogs: ok,
		Discovery: ok, ProbeResult: ok, ProbeAssignments: ok,
	}
	if len(hs.routes()) != 7 {
		t.Fatalf("rotas=%d: a tabela mudou, confira se a nova rota é de ingestão", len(hs.routes()))
	}

	for _, rt := range hs.routes() {
		metodo, caminho, _ := strings.Cut(rt.Pattern, " ")

		mux := http.NewServeMux()
		rl := server.NewRateLimiter(1, 1) // 1 req/s, burst 1
		mountIngest(mux, hs, rl, server.NewInFlight(64))

		chamar := func() int {
			req := httptest.NewRequest(metodo, caminho, nil)
			req.RemoteAddr = "198.51.100.10:5555"
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			return rec.Code
		}
		if code := chamar(); code != http.StatusOK {
			t.Fatalf("%s: primeira requisição = %d, quer 200", rt.Pattern, code)
		}
		if code := chamar(); code != http.StatusTooManyRequests {
			t.Fatalf("%s: segunda requisição = %d, quer 429 — rota SEM rate limit", rt.Pattern, code)
		}
	}
}

// TestRotasDeIngestaoTemTetoGlobal: além do 429 por IP, toda rota de ingestão tem de
// responder 503 + Retry-After quando o teto GLOBAL de requisições em voo estoura.
func TestRotasDeIngestaoTemTetoGlobal(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	hs := ingestHandlers{
		Metrics: ok, Logs: ok, Traces: ok, JSONLogs: ok,
		Discovery: ok, ProbeResult: ok, ProbeAssignments: ok,
	}
	mux := http.NewServeMux()
	gate := server.NewInFlight(1)
	mountIngest(mux, hs, server.NewRateLimiter(1000, 1000), gate)

	// Ocupa a única vaga por fora e confirma que a rota recusa em vez de enfileirar.
	if !gate.Enter() {
		t.Fatal("não consegui ocupar a vaga")
	}
	defer gate.Leave()

	for _, rt := range hs.routes() {
		metodo, caminho, _ := strings.Cut(rt.Pattern, " ")
		req := httptest.NewRequest(metodo, caminho, nil)
		req.RemoteAddr = "198.51.100.11:5555"
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s com teto cheio = %d, quer 503", rt.Pattern, rec.Code)
		}
		if rec.Header().Get("Retry-After") == "" {
			t.Fatalf("%s: 503 sem Retry-After", rt.Pattern)
		}
	}
}

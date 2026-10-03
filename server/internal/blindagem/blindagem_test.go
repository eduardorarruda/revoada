package blindagem

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func servidor(c Config) http.Handler {
	c.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	return Envolver(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/panico" {
			panic("boom")
		}
		if r.URL.Path == "/grande" {
			if _, err := io.ReadAll(r.Body); err != nil {
				http.Error(w, "grande demais", http.StatusRequestEntityTooLarge)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}), c)
}

func TestCabecalhosEIDDeRequisicao(t *testing.T) {
	h := servidor(Config{HTTPSAtras: true})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil))
	for _, k := range []string{"X-Content-Type-Options", "X-Frame-Options", "Content-Security-Policy", "Strict-Transport-Security", "Referrer-Policy"} {
		if rec.Header().Get(k) == "" {
			t.Errorf("falta %s", k)
		}
	}
	if len(rec.Header().Get("X-Request-ID")) < 8 {
		t.Fatal("sem X-Request-ID")
	}
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Header.Set("X-Request-ID", "meu-id-de-correlacao")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Request-ID") != "meu-id-de-correlacao" {
		t.Fatal("deveria propagar o X-Request-ID válido")
	}
	req.Header.Set("X-Request-ID", "<script>")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Request-ID") == "<script>" {
		t.Fatal("ID inválido não pode ser ecoado")
	}
}

func TestCORSEDefesaCSRF(t *testing.T) {
	h := servidor(Config{OrigemPainel: "https://revoada.exemplo.com", OrigensCORS: []string{"https://ferramenta.exemplo.com"}})

	pre := httptest.NewRequest(http.MethodOptions, "/api/x", nil)
	pre.Header.Set("Origin", "https://ferramenta.exemplo.com")
	pre.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, pre)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "https://ferramenta.exemplo.com" {
		t.Fatalf("preflight permitido: %d %v", rec.Code, rec.Header())
	}

	pre.Header.Set("Origin", "https://malicioso.exemplo")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, pre)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("preflight de origem estranha: %d", rec.Code)
	}

	post := httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader("{}"))
	post.Header.Set("Origin", "https://malicioso.exemplo")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, post)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST de origem estranha (CSRF) deveria ser recusado: %d", rec.Code)
	}

	post.Header.Set("Origin", "https://revoada.exemplo.com")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, post)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST da própria interface: %d", rec.Code)
	}

	// mesma origem pelo Host (dev server com proxy)
	mesma := httptest.NewRequest(http.MethodPost, "http://localhost:5180/api/x", nil)
	mesma.Header.Set("Origin", "http://localhost:5180")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, mesma)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("mesma origem: %d", rec.Code)
	}
}

func TestLimiteDeCorpoEPanico(t *testing.T) {
	h := servidor(Config{LimiteCorpo: 10})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/grande", strings.NewReader(strings.Repeat("x", 100))))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("corpo grande: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panico", nil))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("pânico: %d %q (não pode vazar detalhe)", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	Metricas(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "revoada_http_requisicoes_total") {
		t.Fatal("métricas")
	}
}

func TestLocalhostSoEmDesenvolvimento(t *testing.T) {
	req := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8091/api/x", nil)
		r.Header.Set("Origin", "http://localhost:5180")
		return r
	}
	rec := httptest.NewRecorder()
	servidor(Config{}).ServeHTTP(rec, req())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("produção: localhost de outra porta deveria ser recusado: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	servidor(Config{PermitirLocalhost: true}).ServeHTTP(rec, req())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("desenvolvimento: %d", rec.Code)
	}
}

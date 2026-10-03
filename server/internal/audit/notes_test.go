package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAnnotateChegaNaTrilha: o número de linhas apagadas só existe na RESPOSTA e era
// descartado. Sem ele, meses depois ninguém distingue uma limpeza de 3 linhas de uma
// que levou 400 mil. Agora o handler anota e o middleware funde no payload.
func TestAnnotateChegaNaTrilha(t *testing.T) {
	r := newTestRecorder()
	h := r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		Annotate(req.Context(), "deleted_rows", int64(400000))
		Annotate(req.Context(), "escopo", "servidor web01, até 2026-01-01T00:00:00Z")
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("POST", "/api/logs/purge",
		strings.NewReader(`{"host":"web01","before":"2026-01-01T00:00:00Z"}`))
	h.ServeHTTP(httptest.NewRecorder(), req)

	e := <-r.ch
	res, ok := e.Payload["_resultado"].(map[string]any)
	if !ok {
		t.Fatalf("payload sem _resultado: %v", e.Payload)
	}
	if got, _ := res["deleted_rows"].(int64); got != 400000 {
		t.Errorf("deleted_rows = %v (%T); quer 400000", res["deleted_rows"], res["deleted_rows"])
	}
	if !strings.Contains(res["escopo"].(string), "web01") {
		t.Errorf("escopo perdido: %v", res["escopo"])
	}
	// O corpo do pedido continua na trilha, ao lado do resultado.
	if e.Payload["host"] != "web01" {
		t.Errorf("corpo do pedido sumiu: %v", e.Payload)
	}
	if _, err := json.Marshal(e.Payload); err != nil {
		t.Fatalf("payload não serializa para o JSONB: %v", err)
	}
}

// TestAnnotateSemMiddlewareNaoQuebra: chamada fora do middleware (job interno,
// teste) é no-op — anotar nunca pode virar caminho de erro do handler.
func TestAnnotateSemMiddlewareNaoQuebra(t *testing.T) {
	Annotate(context.Background(), "deleted_rows", 1)
	Annotate(nil, "x", 1) //nolint:staticcheck // SA1012: contexto nil é exatamente o caso defensivo testado
	Annotate(context.Background(), "", 1)
}

// TestSemAnotacaoPayloadIntacto: rota que não anota nada não ganha chave extra.
func TestSemAnotacaoPayloadIntacto(t *testing.T) {
	r := newTestRecorder()
	h := r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/hosts", strings.NewReader(`{"a":1}`)))
	e := <-r.ch
	if _, ok := e.Payload["_resultado"]; ok {
		t.Errorf("payload ganhou _resultado sem anotação: %v", e.Payload)
	}
}

// TestAnotacaoTambemEhRedigida: se um handler anotar um campo sensível por engano,
// a regra "a trilha nunca guarda segredo" continua valendo.
func TestAnotacaoTambemEhRedigida(t *testing.T) {
	r := newTestRecorder()
	h := r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		Annotate(req.Context(), "serverkey", "SK-123")
		Annotate(req.Context(), "detalhe", map[string]any{"token": "abc"})
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/x", strings.NewReader(`{}`)))
	e := <-r.ch
	raw, _ := json.Marshal(e.Payload)
	for _, leak := range []string{"SK-123", `"abc"`} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("segredo vazou pela anotação: %s em %s", leak, raw)
		}
	}
}

// TestLimiteDeAnotacoes: a trilha guarda um resumo, não um dump.
func TestLimiteDeAnotacoes(t *testing.T) {
	ctx := withNotes(context.Background())
	for i := 0; i < maxNotes*2; i++ {
		Annotate(ctx, string(rune('a'+i%26))+string(rune('0'+i/26)), i)
	}
	got := mergeNotes(ctx, map[string]any{})
	res := got["_resultado"].(map[string]any)
	if len(res) > maxNotes {
		t.Errorf("anotações sem teto: %d", len(res))
	}
}

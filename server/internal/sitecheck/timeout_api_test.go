package sitecheck

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func lerDe(corpo string) (*httptest.ResponseRecorder, int, *int, bool) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/site-checks", strings.NewReader(corpo))
	req, t, ok := lerCheck(w, r)
	return w, req.TimeoutMS, t, ok
}

// PROVA (validação de 02/10/2026): timeout_ms enviado pela API chega ao handler —
// antes era descartado e o check seguia com 20 s.
func TestLerCheckRepassaTimeout(t *testing.T) {
	_, _, tm, ok := lerDe(`{"name":"a","url":"http://x","timeout_ms":5000}`)
	if !ok || tm == nil || *tm != 5000 {
		t.Fatalf("timeout_ms=5000 não chegou: ok=%v t=%v", ok, tm)
	}
}

// PROVA: ausente ≠ zero — a edição pela tela (sem o campo) não pode zerar o limite.
func TestLerCheckAusenteEhNil(t *testing.T) {
	_, _, tm, ok := lerDe(`{"name":"a","url":"http://x"}`)
	if !ok || tm != nil {
		t.Fatalf("campo ausente deveria vir nil, veio %v", tm)
	}
	_, _, tm, ok = lerDe(`{"name":"a","url":"http://x","timeout_ms":0}`)
	if !ok || tm == nil || *tm != 0 {
		t.Fatalf("0 explícito (= padrão) deveria passar como 0, veio %v", tm)
	}
}

// PROVA: valores fora da faixa são recusados com 400, não gravados.
func TestLerCheckRecusaTimeoutForaDaFaixa(t *testing.T) {
	for _, v := range []string{"-1", "500", "600000"} {
		w, _, _, ok := lerDe(`{"name":"a","url":"http://x","timeout_ms":` + v + `}`)
		if ok || w.Code != http.StatusBadRequest {
			t.Fatalf("timeout_ms=%s deveria dar 400, veio ok=%v code=%d", v, ok, w.Code)
		}
	}
	if w, _, _, ok := lerDe(`{"url":"http://x"}`); ok || w.Code != http.StatusBadRequest {
		t.Fatal("sem name deveria dar 400")
	}
}

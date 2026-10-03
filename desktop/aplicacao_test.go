package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidarPainel(t *testing.T) {
	for entrada, quer := range map[string]string{
		"https://painel.empresa.com/qualquer": "https://painel.empresa.com/",
		"http://localhost:8091":               "http://localhost:8091/",
	} {
		if got, err := validarPainel(entrada); err != nil || got != quer {
			t.Errorf("%s: %q %v", entrada, got, err)
		}
	}
	for _, ruim := range []string{"http://painel.empresa.com", "javascript:alert(1)", "https://user:senha@x.com", "x"} {
		if _, err := validarPainel(ruim); err == nil {
			t.Errorf("%s deveria ser recusado", ruim)
		}
	}
}

func TestConectarExigeNonce(t *testing.T) {
	a := &aplicacao{nonce: "abc", arq: filepath.Join(t.TempDir(), "desktop.json")}
	envia := func(nonce, painel string) *httptest.ResponseRecorder {
		f := url.Values{"nonce": {nonce}, "painel": {painel}}
		r := httptest.NewRequest(http.MethodPost, "/conectar", strings.NewReader(f.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	if w := envia("errado", "https://mal.com"); w.Code != http.StatusForbidden {
		t.Fatalf("sem nonce: %d", w.Code)
	}
	if w := envia("abc", "https://painel.empresa.com"); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "https://painel.empresa.com/" {
		t.Fatalf("conectar: %d %s", w.Code, w.Header().Get("Location"))
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusFound {
		t.Fatalf("com painel salvo, a abertura segue direto: %d", w.Code)
	}
	a.trocar()
	w = httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `value="abc"`) {
		t.Fatalf("trocar de painel mostra o formulário com o nonce: %d", w.Code)
	}
}

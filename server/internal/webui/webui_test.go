package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServeSPA(t *testing.T) {
	h := Handler()
	for _, c := range []struct {
		caminho string
		codigo  int
	}{{"/", 200}, {"/migracao/projetos/x", 200}, {"/../../etc/passwd", 200}, {"/nao-existe.js", 404}} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.caminho, nil))
		if rec.Code != c.codigo {
			t.Errorf("%s: %d (queria %d)", c.caminho, rec.Code, c.codigo)
		}
		if c.codigo == 200 && rec.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: index não pode ficar em cache", c.caminho)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "script-src 'self' 'unsafe-inline'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("CSP da interface: %q", csp)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatal("só GET/HEAD")
	}
	if !ÉDaAPI("/api/x") || !ÉDaAPI("/mcp") || ÉDaAPI("/migracao") {
		t.Fatal("separação API × interface")
	}
}

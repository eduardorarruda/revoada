package webui

import (
	"bytes"
	"compress/gzip"
	"io"
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

// O binário único (sem nginx na frente) mandava tudo cru: 582 kB de JS onde o build
// mede 181 kB comprimido. Com Accept-Encoding: gzip a resposta sai comprimida e
// continua a mesma depois de descomprimida; sem o header, sai crua.
func TestComprimeQuemAceitaGzip(t *testing.T) {
	h := Handler()
	cru := httptest.NewRecorder()
	h.ServeHTTP(cru, httptest.NewRequest(http.MethodGet, "/", nil))
	if cru.Header().Get("Content-Encoding") != "" {
		t.Fatal("sem Accept-Encoding não pode comprimir")
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q; quer gzip", rec.Header().Get("Content-Encoding"))
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatal("resposta comprimida precisa de Vary: Accept-Encoding (senão um cache entrega gzip a quem não pediu)")
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	descomprimido, _ := io.ReadAll(zr)
	if !bytes.Equal(descomprimido, cru.Body.Bytes()) {
		t.Fatal("o gzip não devolve o mesmo conteúdo")
	}
}

// Sem validador, o "no-cache" do index e dos arquivos sem hash (favicons, logo)
// virava reenvio completo a cada navegação. Com ETag, a revalidação é um 304 vazio.
func TestETagDevolve304(t *testing.T) {
	h := Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("sem ETag")
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("revalidação: %d com %d bytes; quer 304 vazio", rec.Code, rec.Body.Len())
	}
	// A versão comprimida tem o próprio ETag, e o 304 vale para ela também.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	etagGz := rec.Header().Get("ETag")
	if etagGz == "" || etagGz == etag {
		t.Fatalf("o ETag do gzip (%q) tem de diferir do cru (%q)", etagGz, etag)
	}
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("If-None-Match", etagGz)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("revalidação do gzip: %d", rec.Code)
	}
}

func TestNaoComprimeQuemJaEComprimido(t *testing.T) {
	for _, p := range []string{"logo.webp", "fonte.woff2", "icone.png"} {
		if comprimivel(p) {
			t.Errorf("%s já vem comprimido; gzip de novo só gasta CPU", p)
		}
	}
	for _, p := range []string{"index.html", "assets/index-abc.js", "assets/index-abc.css", "robots.txt", "favicon.ico"} {
		if !comprimivel(p) {
			t.Errorf("%s deveria sair comprimido", p)
		}
	}
}

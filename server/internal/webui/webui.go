// Package webui embute a interface web (web/dist) no binário do painel: um
// executável só serve API, MCP e tela (Etapa 9). Sem a interface compilada, fica um
// index.html que explica como gerar — a API funciona igual.
package webui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"
)

//go:embed all:dist
var arquivos embed.FS

var raiz, _ = fs.Sub(arquivos, "dist")

// scriptsInline acha os <script> sem src do index.html (o tema é aplicado antes do
// primeiro desenho, num script inline).
var scriptsInline = regexp.MustCompile(`(?s)<script(?:\s+type="module")?>(.*?)</script>`)

// politica é a CSP da interface: script só do próprio painel e os inline do
// index.html pelo HASH (sem 'unsafe-inline' para script); estilo inline é permitido
// (as animações escrevem style); conexões só de volta ao painel (API, SSE, WebSocket).
var politica = func() string {
	hashes := ""
	if b, err := fs.ReadFile(raiz, "index.html"); err == nil {
		for _, m := range scriptsInline.FindAllSubmatch(b, -1) {
			h := sha256.Sum256(m[1])
			hashes += " 'sha256-" + base64.StdEncoding.EncodeToString(h[:]) + "'"
		}
	}
	return "default-src 'self'; script-src 'self'" + hashes + "; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
		"font-src 'self' data:; connect-src 'self' ws: wss:; frame-ancestors 'none'; base-uri 'self'; object-src 'none'; form-action 'self'"
}()

// Embutida diz se a interface de verdade foi compilada junto (e não o aviso).
func Embutida() bool {
	b, err := fs.ReadFile(raiz, "index.html")
	return err == nil && !bytes.Contains(b, []byte("revoada-sem-interface"))
}

// Handler serve a SPA: arquivo que existe sai como está (assets com hash ficam em cache
// para sempre); qualquer outra rota sem extensão devolve o index.html (o roteamento é
// por hash, mas links diretos também funcionam).
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		b, err := fs.ReadFile(raiz, p)
		if err != nil {
			if path.Ext(p) != "" {
				http.NotFound(w, r)
				return
			}
			p = "index.html"
			if b, err = fs.ReadFile(raiz, p); err != nil {
				http.NotFound(w, r)
				return
			}
		}
		if t := mime.TypeByExtension(path.Ext(p)); t != "" {
			w.Header().Set("Content-Type", t)
		}
		w.Header().Set("Content-Security-Policy", politica)
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		_, _ = w.Write(b)
	})
}

// ÉDaAPI diz se o caminho pertence à API (não à interface).
func ÉDaAPI(p string) bool {
	return strings.HasPrefix(p, "/api/") || p == "/mcp" || p == "/healthz" || p == "/readyz" || p == "/metrics"
}

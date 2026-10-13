// Package webui embute a interface web (web/dist) no binário do painel: um
// executável só serve API, MCP e tela (Etapa 9). Sem a interface compilada, fica um
// index.html que explica como gerar — a API funciona igual.
package webui

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"
	"sync"
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
//
// No binário único não há nginx na frente para comprimir: sem o gzip daqui, o
// navegador recebia o JS cru (~3x maior). E sem ETag, o "no-cache" do index e dos
// arquivos sem hash no nome virava reenvio completo a cada navegação.
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
		arq, err := carregar(p)
		if err != nil {
			if path.Ext(p) != "" {
				http.NotFound(w, r)
				return
			}
			p = "index.html"
			if arq, err = carregar(p); err != nil {
				http.NotFound(w, r)
				return
			}
		}
		servir(w, r, p, arq)
	})
}

func servir(w http.ResponseWriter, r *http.Request, p string, arq *arquivo) {
	if t := mime.TypeByExtension(path.Ext(p)); t != "" {
		w.Header().Set("Content-Type", t)
	}
	w.Header().Set("Content-Security-Policy", politica)
	if strings.HasPrefix(p, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	corpo, etag := arq.corpo, arq.etag
	if arq.gz != nil {
		w.Header().Set("Vary", "Accept-Encoding")
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			corpo, etag = arq.gz, arq.etagGz
			w.Header().Set("Content-Encoding", "gzip")
		}
	}
	w.Header().Set("ETag", etag)
	if strings.Contains(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(corpo)
}

// arquivo é um arquivo da interface pronto para servir: o conteúdo, a versão
// comprimida (quando vale a pena) e o ETag de cada um.
type arquivo struct {
	corpo, gz    []byte
	etag, etagGz string
}

// prontos guarda cada arquivo já preparado: o que está embutido não muda com o
// painel rodando, então comprimir e calcular o hash uma vez basta.
var prontos sync.Map // caminho → *arquivo

func carregar(p string) (*arquivo, error) {
	if v, ok := prontos.Load(p); ok {
		return v.(*arquivo), nil
	}
	b, err := fs.ReadFile(raiz, p)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	id := base64.RawURLEncoding.EncodeToString(sum[:12])
	arq := &arquivo{corpo: b, etag: `"` + id + `"`, etagGz: `"` + id + `-gz"`}
	if comprimivel(p) && len(b) >= minGzip {
		if gz := gzipar(b); len(gz) < len(b) {
			arq.gz = gz
		}
	}
	v, _ := prontos.LoadOrStore(p, arq)
	return v.(*arquivo), nil
}

// minGzip: abaixo disso o cabeçalho do gzip come o ganho.
const minGzip = 1024

// comprimivel diz se o tipo é texto. Imagem, fonte e afins já vêm comprimidos: gzip
// de novo só gasta CPU.
func comprimivel(p string) bool {
	switch path.Ext(p) {
	case ".html", ".js", ".mjs", ".css", ".json", ".svg", ".txt", ".map", ".ico", ".xml", ".webmanifest":
		return true
	}
	return false
}

func gzipar(b []byte) []byte {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return buf.Bytes()
}

// ÉDaAPI diz se o caminho pertence à API (não à interface).
func ÉDaAPI(p string) bool {
	return strings.HasPrefix(p, "/api/") || p == "/mcp" || p == "/healthz" || p == "/readyz" || p == "/metrics"
}

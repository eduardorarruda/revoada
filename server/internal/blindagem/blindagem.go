// Package blindagem é a camada HTTP que envolve TODAS as rotas do painel (lista de
// segurança e operação da ARQUITETURA §22): ID de requisição e log de acesso, cabeçalhos de
// segurança, HSTS, CORS por lista permitida, defesa contra CSRF por Origin, limite de
// corpo, recuperação de pânico e métricas no formato Prometheus.
package blindagem

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// LimiteCorpoPadrao é o maior corpo aceito (o mesmo do nginx de produção).
const LimiteCorpoPadrao = 16 << 20

// Config da blindagem.
type Config struct {
	// OrigemPainel é a origem da própria interface (ex.: https://revoada.empresa.com).
	OrigemPainel string
	// OrigensCORS são outras origens autorizadas a chamar a API pelo navegador.
	OrigensCORS []string
	// HTTPSAtras diz se há HTTPS na borda (TLS próprio ou proxy): liga o HSTS.
	HTTPSAtras bool
	// PermitirLocalhost aceita origens http(s)://localhost e 127.0.0.1 em qualquer
	// porta — SÓ em desenvolvimento (o proxy do Vite troca o Host da requisição).
	PermitirLocalhost bool
	LimiteCorpo       int64
	Log               *slog.Logger
}

var idValido = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

// Envolver aplica a blindagem na ordem certa (de fora para dentro).
func Envolver(h http.Handler, c Config) http.Handler {
	if c.LimiteCorpo <= 0 {
		c.LimiteCorpo = LimiteCorpoPadrao
	}
	permitidas := map[string]bool{}
	if o := origemDe(c.OrigemPainel); o != "" {
		permitidas[o] = true
	}
	for _, o := range c.OrigensCORS {
		if o = origemDe(strings.TrimSpace(o)); o != "" {
			permitidas[o] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inicio := time.Now()
		id := r.Header.Get("X-Request-ID")
		if !idValido.MatchString(id) {
			id = novoID()
		}
		w.Header().Set("X-Request-ID", id)
		cabecalhos(w, c.HTTPSAtras || r.TLS != nil)

		rw := &respostaGravada{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if p := recover(); p != nil {
				c.Log.Error("pânico na requisição", "request_id", id, "metodo", r.Method, "caminho", r.URL.Path, "panico", fmt.Sprint(p))
				if !rw.escreveu {
					http.Error(rw, "erro interno (ref "+id+")", http.StatusInternalServerError)
				}
			}
			dur := time.Since(inicio)
			metricas.registrar(r.Method, rw.status, dur)
			if r.URL.Path != "/healthz" && r.URL.Path != "/readyz" {
				// A query fica de fora do log: o WebSocket leva o token em ?access=.
				c.Log.Info("http", "request_id", id, "metodo", r.Method, "caminho", r.URL.Path, "status", rw.status,
					"duracao_ms", dur.Milliseconds(), "bytes", rw.bytes)
			}
		}()

		if !aplicarCORS(rw, r, permitidas, c.PermitirLocalhost) {
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(rw, r.Body, c.LimiteCorpo)
		}
		h.ServeHTTP(rw, r)
	})
}

// cabecalhos de segurança da API. O HTML da interface tem CSP própria (servida pelo
// nginx ou pelo binário único); aqui as respostas são JSON/SSE.
func cabecalhos(w http.ResponseWriter, https bool) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	if h.Get("Content-Security-Policy") == "" {
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	}
	if https {
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	}
}

// aplicarCORS devolve false se a requisição já foi respondida (preflight) ou recusada.
//
// Regras: só origens da lista recebem os cabeçalhos CORS; requisição que ALTERA
// algo (POST/PUT/PATCH/DELETE) vinda de origem fora da lista é recusada — é a
// defesa contra CSRF que não depende do navegador respeitar SameSite.
func aplicarCORS(w http.ResponseWriter, r *http.Request, permitidas map[string]bool, localhost bool) bool {
	origem := r.Header.Get("Origin")
	if origem == "" {
		return true // mesma origem (navegador) ou cliente de linha de comando
	}
	ok := permitidas[origemDe(origem)]
	if !ok && (mesmaOrigem(r, origem) || (localhost && ehLocalhost(origem))) {
		ok = true
	}
	if ok {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origem)
		h.Set("Access-Control-Allow-Credentials", "true")
		h.Add("Vary", "Origin")
	}
	if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
		if !ok {
			http.Error(w, "origem não autorizada", http.StatusForbidden)
			return false
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
		h.Set("Access-Control-Expose-Headers", "X-Access-Token, X-Request-ID")
		h.Set("Access-Control-Max-Age", "600")
		w.WriteHeader(http.StatusNoContent)
		return false
	}
	if !ok && slices.Contains([]string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}, r.Method) {
		http.Error(w, "origem não autorizada", http.StatusForbidden)
		return false
	}
	return true
}

// mesmaOrigem compara o Origin com o Host da própria requisição (o painel servido no
// mesmo endereço — inclusive o dev server do Vite, que faz proxy).
func mesmaOrigem(r *http.Request, origem string) bool {
	u, err := url.Parse(origem)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

func ehLocalhost(origem string) bool {
	u, err := url.Parse(origem)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

func origemDe(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

func novoID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// respostaGravada guarda o status e mantém Flush (SSE) e Hijack (WebSocket).
type respostaGravada struct {
	http.ResponseWriter
	status   int
	bytes    int
	escreveu bool
}

func (r *respostaGravada) WriteHeader(s int) {
	if !r.escreveu {
		r.status, r.escreveu = s, true
	}
	r.ResponseWriter.WriteHeader(s)
}

func (r *respostaGravada) Write(b []byte) (int, error) {
	r.escreveu = true
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func (r *respostaGravada) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *respostaGravada) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Hijack mantém o WebSocket (/api/live) funcionando através da blindagem.
func (r *respostaGravada) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("a resposta não suporta hijack")
	}
	r.status, r.escreveu = http.StatusSwitchingProtocols, true
	return hj.Hijack()
}

// ---------------------------------------------------------------- métricas

var limitesDuracao = []float64{0.005, 0.025, 0.1, 0.25, 1, 2.5, 10}

type contadores struct {
	mu          sync.Mutex
	porClasse   map[string]*atomic.Int64 // "GET 2xx"
	histograma  []atomic.Int64           // por limite
	totalSeg    atomic.Int64             // microssegundos
	totalPedido atomic.Int64
}

var metricas = &contadores{porClasse: map[string]*atomic.Int64{}, histograma: make([]atomic.Int64, len(limitesDuracao)+1)}

func (c *contadores) registrar(metodo string, status int, d time.Duration) {
	chave := fmt.Sprintf("%s|%dxx", metodo, status/100)
	c.mu.Lock()
	n, ok := c.porClasse[chave]
	if !ok {
		n = &atomic.Int64{}
		c.porClasse[chave] = n
	}
	c.mu.Unlock()
	n.Add(1)
	seg := d.Seconds()
	i := len(limitesDuracao)
	for j, l := range limitesDuracao {
		if seg <= l {
			i = j
			break
		}
	}
	c.histograma[i].Add(1)
	c.totalSeg.Add(d.Microseconds())
	c.totalPedido.Add(1)
}

// Metricas expõe os contadores no formato texto do Prometheus (GET /metrics).
func Metricas(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	var b strings.Builder
	b.WriteString("# HELP revoada_http_requisicoes_total Requisições HTTP atendidas pelo painel.\n# TYPE revoada_http_requisicoes_total counter\n")
	metricas.mu.Lock()
	chaves := make([]string, 0, len(metricas.porClasse))
	for k := range metricas.porClasse {
		chaves = append(chaves, k)
	}
	slices.Sort(chaves)
	for _, k := range chaves {
		m, cl, _ := strings.Cut(k, "|")
		fmt.Fprintf(&b, "revoada_http_requisicoes_total{metodo=%q,classe=%q} %d\n", m, cl, metricas.porClasse[k].Load())
	}
	metricas.mu.Unlock()
	b.WriteString("# HELP revoada_http_duracao_segundos Duração das requisições.\n# TYPE revoada_http_duracao_segundos histogram\n")
	var acumulado int64
	for i, l := range limitesDuracao {
		acumulado += metricas.histograma[i].Load()
		fmt.Fprintf(&b, "revoada_http_duracao_segundos_bucket{le=\"%g\"} %d\n", l, acumulado)
	}
	acumulado += metricas.histograma[len(limitesDuracao)].Load()
	fmt.Fprintf(&b, "revoada_http_duracao_segundos_bucket{le=\"+Inf\"} %d\n", acumulado)
	fmt.Fprintf(&b, "revoada_http_duracao_segundos_sum %g\n", float64(metricas.totalSeg.Load())/1e6)
	fmt.Fprintf(&b, "revoada_http_duracao_segundos_count %d\n", metricas.totalPedido.Load())
	_, _ = w.Write([]byte(b.String()))
}

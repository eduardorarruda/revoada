package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// configuracao fica em <config do usuário>/revoada/desktop.json.
type configuracao struct {
	Painel string `json:"painel"`
}

type aplicacao struct {
	ctx    context.Context
	nonce  string
	arq    string
	mu     sync.Mutex
	cfg    configuracao
	manual bool // "Trocar de painel": não redireciona sozinho na próxima abertura
}

func novaAplicacao() (*aplicacao, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	a := &aplicacao{nonce: hex.EncodeToString(b), arq: filepath.Join(dir, "revoada", "desktop.json")}
	if raw, err := os.ReadFile(a.arq); err == nil {
		_ = json.Unmarshal(raw, &a.cfg)
	}
	return a, nil
}

func (a *aplicacao) inicio(ctx context.Context) { a.ctx = ctx }

func (a *aplicacao) trocar() {
	a.mu.Lock()
	a.manual = true
	a.mu.Unlock()
}

// validarPainel aceita https:// (ou http:// só para localhost, em teste).
func validarPainel(bruto string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(bruto))
	if err != nil || u.Host == "" || u.User != nil {
		return "", errors.New("endereço inválido (ex.: https://painel.suaempresa.com.br)")
	}
	host := u.Hostname()
	local := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return "", errors.New("use https:// (http só para localhost)")
	}
	return u.Scheme + "://" + u.Host + "/", nil
}

func (a *aplicacao) salvar(painel string) error {
	if err := os.MkdirAll(filepath.Dir(a.arq), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(configuracao{Painel: painel})
	return os.WriteFile(a.arq, b, 0o600)
}

var pagina = template.Must(template.ParseFS(assets, "frontend/index.html"))

// ServeHTTP é o servidor de páginas da janela: "/" mostra a tela de conexão (ou
// segue para o painel salvo) e "/conectar" grava o endereço — só com o nonce.
func (a *aplicacao) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; form-action 'self'; base-uri 'none'")
	switch r.URL.Path {
	case "/", "/index.html":
		a.mu.Lock()
		painel, manual := a.cfg.Painel, a.manual
		a.manual = false
		a.mu.Unlock()
		if painel != "" && !manual && r.URL.Query().Get("erro") == "" {
			http.Redirect(w, r, painel, http.StatusFound)
			return
		}
		_ = pagina.Execute(w, map[string]string{"Nonce": a.nonce, "Painel": painel, "Erro": r.URL.Query().Get("erro")})
	case "/conectar":
		if r.Method != http.MethodPost || subtle.ConstantTimeCompare([]byte(r.FormValue("nonce")), []byte(a.nonce)) != 1 {
			http.Error(w, "pedido recusado", http.StatusForbidden)
			return
		}
		painel, err := validarPainel(r.FormValue("painel"))
		if err == nil {
			err = a.salvar(painel)
		}
		if err != nil {
			http.Redirect(w, r, "/?erro="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		a.mu.Lock()
		a.cfg.Painel = painel
		a.mu.Unlock()
		http.Redirect(w, r, painel, http.StatusSeeOther)
	default:
		http.NotFound(w, r)
	}
}

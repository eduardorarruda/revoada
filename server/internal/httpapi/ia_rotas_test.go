package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/chquery"
	"github.com/eduardorarruda/revoada/server/internal/ia"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// chIAFalso responde qualquer consulta com uma linha de conteúdo/replay e registra
// tudo o que chegou: rota negada não pode ter tocado no ClickHouse.
type chIAFalso struct {
	mu   sync.Mutex
	sqls []string
}

func (c *chIAFalso) QueryJSON(_ context.Context, q string) ([]map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sqls = append(c.sqls, q)
	return []map[string]any{{"span_id": "s1", "lado": "entrada", "papel": "user", "texto": "oi",
		"operacao": "chat", "ts_ms": "1700000000000", "trace_id": "abc"}}, nil
}

func (c *chIAFalso) Exec(_ context.Context, s string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sqls = append(c.sqls, s)
	return nil
}

func (c *chIAFalso) InsertMetricsAt(context.Context, string, time.Time, []chquery.MetricPoint) error {
	return nil
}

func (c *chIAFalso) chamadas() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sqls)
}

// precosFalsos guarda a tabela em memória; só o necessário para as rotas responderem.
type precosFalsos struct {
	mu     sync.Mutex
	precos []store.PrecoLLM
}

func (p *precosFalsos) ListarPrecosLLM(context.Context) ([]store.PrecoLLM, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]store.PrecoLLM(nil), p.precos...), nil
}

func (p *precosFalsos) CriarPrecoLLM(_ context.Context, n store.PrecoLLM) (store.PrecoLLM, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n.ID = int64(len(p.precos) + 1)
	p.precos = append(p.precos, n)
	return n, nil
}

func (p *precosFalsos) ApagarPrecoLLM(context.Context, int64) error { return nil }

func (p *precosFalsos) SemearPrecosLLM(context.Context, string, []store.PrecoLLM) (int, error) {
	return 0, nil
}

const segredoTeste = "segredo-de-teste-das-rotas-de-ia"

// muxIA monta o roteador DE VERDADE (httpapi.New) só com auth e IA: é a composição
// das rotas que está sob teste, não os wrappers isolados.
func muxIA(t *testing.T) (http.Handler, *chIAFalso) {
	t.Helper()
	ch := &chIAFalso{}
	return New(Deps{Auth: auth.NewHandler(nil, segredoTeste), IA: ia.New(ch, &precosFalsos{}, nil)}), ch
}

func tokenDe(t *testing.T, c auth.Claims) string {
	t.Helper()
	tok, err := auth.SignClaims(segredoTeste, c, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// TestRotasIAPermissoes: a matriz de quem pode o quê em /api/ia/*, atravessando o
// RequireAuth, o rate-limit, o RequireRole/Exige e o handler — do jeito que a tela chama.
func TestRotasIAPermissoes(t *testing.T) {
	daqui := time.Now().Add(5 * time.Minute).Unix()
	leitor := auth.Claims{Sub: 1, Name: "lia", Role: auth.PapelLeitor, MFA: true, Reauth: daqui}
	opSem2FA := auth.Claims{Sub: 2, Name: "ops", Role: auth.PapelOperador}
	opSemReauth := auth.Claims{Sub: 3, Name: "ops", Role: auth.PapelOperador, MFA: true}
	operador := auth.Claims{Sub: 4, Name: "ops", Role: auth.PapelOperador, MFA: true, Reauth: daqui}
	adminSemReauth := auth.Claims{Sub: 5, Name: "adm", Role: auth.PapelAdmin, MFA: true}
	admin := auth.Claims{Sub: 6, Name: "adm", Role: auth.PapelAdmin, MFA: true, Reauth: daqui}

	const (
		conteudo = "/api/ia/execucoes/abc123/conteudo"
		purge    = `{"alvo":"trace","valor":"abc123"}`
		preco    = `{"provedor":"openai","modelo":"gpt-x*","entrada_por_1m":1,"saida_por_1m":2}`
	)
	casos := []struct {
		nome         string
		claims       *auth.Claims // nil = sem token
		metodo, rota string
		corpo        string
		quer         int
		codigo       string // codigo do JSON 403 (auth.Exige), quando há
	}{
		{"sem token", nil, http.MethodGet, "/api/ia/resumo", "", http.StatusUnauthorized, ""},
		{"leitor lê o resumo", &leitor, http.MethodGet, "/api/ia/resumo", "", http.StatusOK, ""},
		{"leitor lê a lista", &leitor, http.MethodGet, "/api/ia/execucoes", "", http.StatusOK, ""},
		{"leitor lê os preços", &leitor, http.MethodGet, "/api/ia/precos", "", http.StatusOK, ""},
		{"leitor não cria preço", &leitor, http.MethodPost, "/api/ia/precos", preco, http.StatusForbidden, ""},
		{"leitor não apaga preço", &leitor, http.MethodDelete, "/api/ia/precos/1", "", http.StatusForbidden, ""},
		{"leitor não purga", &leitor, http.MethodPost, "/api/ia/purge", purge, http.StatusForbidden, auth.CodigoSemPermissao},
		{"leitor não lê conteúdo", &leitor, http.MethodGet, conteudo, "", http.StatusForbidden, auth.CodigoSemPermissao},
		{"operador sem 2FA não lê conteúdo", &opSem2FA, http.MethodGet, conteudo, "", http.StatusForbidden, auth.CodigoMFANecessario},
		{"operador sem reauth não lê conteúdo", &opSemReauth, http.MethodGet, conteudo, "", http.StatusForbidden, auth.CodigoReautenticacao},
		{"operador completo lê conteúdo", &operador, http.MethodGet, conteudo, "", http.StatusOK, ""},
		{"operador não cria preço", &operador, http.MethodPost, "/api/ia/precos", preco, http.StatusForbidden, ""},
		{"operador não apaga preço", &operador, http.MethodDelete, "/api/ia/precos/1", "", http.StatusForbidden, ""},
		{"operador não purga", &operador, http.MethodPost, "/api/ia/purge", purge, http.StatusForbidden, auth.CodigoSemPermissao},
		{"admin sem reauth não lê conteúdo", &adminSemReauth, http.MethodGet, conteudo, "", http.StatusForbidden, auth.CodigoReautenticacao},
		{"admin lê conteúdo", &admin, http.MethodGet, conteudo, "", http.StatusOK, ""},
		{"admin cria preço", &admin, http.MethodPost, "/api/ia/precos", preco, http.StatusCreated, ""},
		{"admin apaga preço", &admin, http.MethodDelete, "/api/ia/precos/1", "", http.StatusNoContent, ""},
		{"admin sem reauth não purga", &adminSemReauth, http.MethodPost, "/api/ia/purge", purge, http.StatusForbidden, auth.CodigoReautenticacao},
		{"admin purga", &admin, http.MethodPost, "/api/ia/purge", purge, http.StatusAccepted, ""},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			mux, ch := muxIA(t)
			req := httptest.NewRequest(c.metodo, c.rota, strings.NewReader(c.corpo))
			if c.claims != nil {
				req.Header.Set("Authorization", "Bearer "+tokenDe(t, *c.claims))
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != c.quer {
				t.Fatalf("%s %s: código %d, quer %d (%s)", c.metodo, c.rota, rec.Code, c.quer, rec.Body)
			}
			if c.codigo != "" {
				var e auth.ErroAcesso
				if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Codigo != c.codigo {
					t.Fatalf("corpo do 403 = %s, quer codigo %q", rec.Body, c.codigo)
				}
			}
			// negado = o handler nem rodou: nada pode ter chegado ao ClickHouse
			if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
				if n := ch.chamadas(); n != 0 {
					t.Fatalf("rota negada consultou o ClickHouse %d vez(es)", n)
				}
			}
		})
	}
}

// TestReplayDizSeOUsuarioPodeVerOConteudo: o replay é de todos, mas o botão "ver
// conteúdo" só aparece para quem tem a permissão (a rota do conteúdo confere de novo).
func TestReplayDizSeOUsuarioPodeVerOConteudo(t *testing.T) {
	casos := []struct {
		papel   string
		podeVer bool
	}{
		{auth.PapelLeitor, false},
		{auth.PapelOperador, true},
		{auth.PapelAdmin, true},
	}
	for _, c := range casos {
		t.Run(c.papel, func(t *testing.T) {
			mux, _ := muxIA(t)
			req := httptest.NewRequest(http.MethodGet, "/api/ia/execucoes/abc123", nil)
			req.Header.Set("Authorization", "Bearer "+tokenDe(t, auth.Claims{Sub: 9, Role: c.papel}))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("código %d: %s", rec.Code, rec.Body)
			}
			var rep ia.Replay
			if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
				t.Fatal(err)
			}
			if rep.Conteudo.PodeVer != c.podeVer {
				t.Fatalf("pode_ver = %v, quer %v", rep.Conteudo.PodeVer, c.podeVer)
			}
		})
	}
}

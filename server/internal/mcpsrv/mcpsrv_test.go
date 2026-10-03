package mcpsrv

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/core/migracao/plano"
	"github.com/eduardorarruda/revoada/server/internal/store"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type comToken struct {
	token string
	base  http.RoundTripper
}

func (c comToken) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+c.token)
	return c.base.RoundTrip(r)
}

// servidor de teste: sem banco; o verificador aceita "rvm_leitura" (só leitura).
func conectar(t *testing.T, token string) *mcp.ClientSession {
	t.Helper()
	s := Novo(nil, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.verificar = func(_ context.Context, tok string, _ *http.Request) (*mcpauth.TokenInfo, error) {
		if tok != "rvm_leitura" {
			return nil, mcpauth.ErrInvalidToken
		}
		return &mcpauth.TokenInfo{Scopes: []string{EscopoLeitura}, Expiration: time.Now().Add(time.Hour), UserID: "mcp_1",
			Extra: map[string]any{"nome": "teste"}}, nil
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	cli := mcp.NewClient(&mcp.Implementation{Name: "teste", Version: "1"}, nil)
	cs, err := cli.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL,
		HTTPClient: &http.Client{Transport: comToken{token: token, base: http.DefaultTransport}}}, nil)
	if err != nil {
		t.Fatalf("conectar: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestNenhumaFerramentaExecutaOuVeCredencial(t *testing.T) {
	cs := conectar(t, "rvm_leitura")
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) < 10 {
		t.Fatalf("ferramentas: %d", len(res.Tools))
	}
	for _, tl := range res.Tools {
		for _, proibido := range []string{"executar", "reverter", "aprovar", "senha", "credencial", "descartar"} {
			if strings.Contains(tl.Name, proibido) {
				t.Errorf("o MCP não pode ter a ferramenta %q", tl.Name)
			}
		}
		if tl.Description == "" || tl.InputSchema == nil {
			t.Errorf("%s sem descrição ou schema de entrada", tl.Name)
		}
	}
}

func TestEscopoBarraEscrita(t *testing.T) {
	cs := conectar(t, "rvm_leitura")
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "gravar_rascunho",
		Arguments: map[string]any{"projeto_id": "pj_x", "mapeamento": map[string]any{"tabelas": []any{}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsError {
		t.Fatal("token só de leitura não pode gravar rascunho")
	}
	b, _ := json.Marshal(r.Content)
	if !strings.Contains(string(b), "escopo") {
		t.Fatalf("a mensagem deve explicar o escopo: %s", b)
	}
}

func TestTokenInvalidoNaoEntra(t *testing.T) {
	s := Novo(nil, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.verificar = func(context.Context, string, *http.Request) (*mcpauth.TokenInfo, error) {
		return nil, mcpauth.ErrInvalidToken
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer rvm_falso")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestSemDadosTiraChaves(t *testing.T) {
	rel := plano.RelatorioSimulacao{Tabelas: []plano.RelatorioTabela{{Amostras: []plano.Amostra{{Chave: "123.456.789-00", Tipo: "nulo", Mensagem: "x"}}}}}
	b, _ := json.Marshal(rel)
	e := semDados(store.ExecucaoMigracao{Tipo: "simular", Tarefa: store.Tarefa{Resumo: b,
		Erro: "CLIENTES, linha de chave 123.456.789-00: texto grande", Especificacao: json.RawMessage(`{"x":1}`)}})
	tudo, _ := json.Marshal(e)
	if bytes.Contains(tudo, []byte("123.456.789-00")) || e.Tarefa.Especificacao != nil {
		t.Fatalf("vazou valor de linha: %s", tudo)
	}
	if !strings.Contains(e.Tarefa.Erro, "linha de chave (oculta)") {
		t.Fatalf("erro: %q", e.Tarefa.Erro)
	}
	// chave com espaço e dois-pontos (nome, código composto): nada dela pode sobrar
	e = semDados(store.ExecucaoMigracao{Tipo: "executar", Tarefa: store.Tarefa{
		Erro: "CLIENTES, linha de chave Maria da Silva: Ltda: texto grande (rode a simulação de novo)"}})
	for _, pedaco := range []string{"Maria", "Silva", "Ltda"} {
		if strings.Contains(e.Tarefa.Erro, pedaco) {
			t.Fatalf("sobrou %q da chave: %q", pedaco, e.Tarefa.Erro)
		}
	}
	if !strings.HasPrefix(e.Tarefa.Erro, "CLIENTES, ") {
		t.Fatalf("a tabela pode ficar: %q", e.Tarefa.Erro)
	}
	// divergências de conteúdo (execução e verificação): a chave sai, a coluna fica
	for _, tipo := range []string{"executar", "verificar"} {
		var b []byte
		div := []plano.Divergencia{{Chave: "123.456.789-00", Colunas: []string{"nome"}}}
		if tipo == "executar" {
			b, _ = json.Marshal(plano.ResumoExecucao{Tabelas: []plano.ResumoTabela{{Destino: "clientes", Divergencias: div}}})
		} else {
			b, _ = json.Marshal(plano.ResumoVerificacao{Tabelas: []plano.ResumoVerificacaoTabela{{Destino: "clientes", Divergencias: div}}})
		}
		e = semDados(store.ExecucaoMigracao{Tipo: tipo, Tarefa: store.Tarefa{Resumo: b,
			Erro: "clientes: o conteúdo não confere nas colunas nome. Divergências na linha de chave 123.456.789-00"}})
		tudo, _ := json.Marshal(e)
		if bytes.Contains(tudo, []byte("123.456.789-00")) || !bytes.Contains(tudo, []byte("nome")) {
			t.Fatalf("%s: %s", tipo, tudo)
		}
	}
	// resumo ilegível: não devolve como está
	e = semDados(store.ExecucaoMigracao{Tipo: "simular", Tarefa: store.Tarefa{Resumo: json.RawMessage(`{"tabelas":"formato-novo","chave":"123.456.789-00"}`)}})
	if e.Tarefa.Resumo != nil {
		t.Fatalf("resumo em formato inesperado tem de sair vazio: %s", e.Tarefa.Resumo)
	}
}

func TestFreio(t *testing.T) {
	s := &Servidor{janela: map[string]*contador{}}
	agora := time.Now()
	for i := 0; i < chamadasPorMinuto; i++ {
		if !s.frear("t", agora) {
			t.Fatalf("chamada %d barrada cedo", i+1)
		}
	}
	if s.frear("t", agora) {
		t.Fatal("deveria barrar acima do limite")
	}
	if !s.frear("outro", agora) || !s.frear("t", agora.Add(time.Minute)) {
		t.Fatal("o limite é por token e por minuto")
	}
}

func TestPonteStdio(t *testing.T) {
	var sessoes []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer rvm_ok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		sessoes = append(sessoes, r.Method+":"+r.Header.Get("Mcp-Session-Id"))
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case strings.Contains(string(body), `"initialize"`):
			w.Header().Set("Mcp-Session-Id", "s1")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		case !strings.Contains(string(body), `"id"`): // notificação
			w.WriteHeader(http.StatusAccepted)
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"tools\":[]}}\n\n"))
		}
	}))
	defer ts.Close()
	entrada := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n")
	var saida bytes.Buffer
	if err := Ponte(context.Background(), ts.URL, "rvm_ok", entrada, &saida, nil); err != nil {
		t.Fatal(err)
	}
	linhas := strings.Split(strings.TrimSpace(saida.String()), "\n")
	if len(linhas) != 2 || !strings.Contains(linhas[1], `"tools"`) {
		t.Fatalf("saída: %q", saida.String())
	}
	if strings.Join(sessoes, ",") != "POST:,POST:s1,POST:s1,DELETE:s1" {
		t.Fatalf("a sessão precisa seguir nas chamadas e ser encerrada: %v", sessoes)
	}
	if err := Ponte(context.Background(), ts.URL, "rvm_revogado", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`+"\n"), io.Discard, nil); err == nil ||
		!strings.Contains(err.Error(), "recusou o token") {
		t.Fatalf("token recusado precisa de erro claro: %v", err)
	}
	if err := Ponte(context.Background(), "", "", strings.NewReader(""), io.Discard, nil); err == nil {
		t.Fatal("sem URL/token")
	}
}

package mcpsrv

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/chquery"
	"github.com/eduardorarruda/revoada/server/internal/ia"
	"github.com/eduardorarruda/revoada/server/internal/store"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// chIA responde toda consulta com uma linha (serve ao replay e ao conteúdo) e guarda o
// SQL recebido: é assim que o teste prova que uma chamada recusada não consultou nada.
type chIA struct {
	mu   sync.Mutex
	sqls []string
}

func (c *chIA) QueryJSON(_ context.Context, q string) ([]map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sqls = append(c.sqls, q)
	return []map[string]any{{"span_id": "s1", "lado": "entrada", "papel": "user", "texto": "olá",
		"operacao": "chat", "ts_ms": "1700000000000", "trace_id": "abc123"}}, nil
}

func (c *chIA) Exec(context.Context, string) error { return nil }

func (c *chIA) InsertMetricsAt(context.Context, string, time.Time, []chquery.MetricPoint) error {
	return nil
}

func (c *chIA) consultas() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sqls...)
}

func (c *chIA) zerar() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sqls = nil
}

type precosIA struct{}

func (precosIA) ListarPrecosLLM(context.Context) ([]store.PrecoLLM, error) { return nil, nil }
func (precosIA) CriarPrecoLLM(_ context.Context, p store.PrecoLLM) (store.PrecoLLM, error) {
	return p, nil
}
func (precosIA) ApagarPrecoLLM(context.Context, int64) error { return nil }
func (precosIA) SemearPrecosLLM(context.Context, string, []store.PrecoLLM) (int, error) {
	return 0, nil
}

// conectarIA sobe o /mcp com um ia.Handler de verdade (sobre fakes) e um token com os
// escopos dados.
func conectarIA(t *testing.T, ch *chIA, escopos ...string) *mcp.ClientSession {
	t.Helper()
	s := Novo(nil, nil, nil, ia.New(ch, precosIA{}, nil), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.verificar = func(context.Context, string, *http.Request) (*mcpauth.TokenInfo, error) {
		return &mcpauth.TokenInfo{Scopes: escopos, Expiration: time.Now().Add(time.Hour), UserID: "mcp_ia",
			Extra: map[string]any{"nome": "teste-ia"}}, nil
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	cli := mcp.NewClient(&mcp.Implementation{Name: "teste", Version: "1"}, nil)
	cs, err := cli.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL,
		HTTPClient: &http.Client{Transport: comToken{token: "rvm_qualquer", base: http.DefaultTransport}}}, nil)
	if err != nil {
		t.Fatalf("conectar: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func chamar(t *testing.T, cs *mcp.ClientSession, nome string, args map[string]any) (*mcp.CallToolResult, string) {
	t.Helper()
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: nome, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", nome, err)
	}
	b, _ := json.Marshal(r.Content)
	return r, string(b)
}

func TestFerramentasDeIAListadas(t *testing.T) {
	cs := conectarIA(t, &chIA{}, EscopoLeitura)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var nomes []string
	for _, tl := range res.Tools {
		nomes = append(nomes, tl.Name)
	}
	for _, quer := range []string{"ia_resumo", "listar_execucoes", "ver_execucao", "ler_conteudo_execucao"} {
		if !slices.Contains(nomes, quer) {
			t.Errorf("ferramenta %q não foi registrada (tem %v)", quer, nomes)
		}
	}
	// sem ia.Handler, as ferramentas de IA não aparecem (o painel sem ClickHouse de IA)
	sem := conectar(t, "rvm_leitura")
	res, err = sem.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if tl.Name == "ia_resumo" {
			t.Fatal("sem ia.Handler não deveria registrar ia_resumo")
		}
	}
}

// TestEscoposDasFerramentasDeIA: custo e passo a passo qualquer token de leitura vê; o
// texto das conversas só com ia_conteudo — e a recusa acontece antes de consultar.
func TestEscoposDasFerramentasDeIA(t *testing.T) {
	casos := []struct {
		nome    string
		escopos []string
		tool    string
		args    map[string]any
		negado  bool
	}{
		{"leitura vê o resumo", []string{EscopoLeitura}, "ia_resumo", map[string]any{"horas": 1}, false},
		{"leitura lista execuções", []string{EscopoLeitura}, "listar_execucoes", map[string]any{"agente": "x", "limite": 900}, false},
		{"leitura vê o replay", []string{EscopoLeitura}, "ver_execucao", map[string]any{"trace_id": "abc123"}, false},
		{"leitura não lê conteúdo", []string{EscopoLeitura}, "ler_conteudo_execucao", map[string]any{"trace_id": "abc123"}, true},
		{"ia_conteudo lê conteúdo", []string{EscopoLeitura, EscopoIAConteudo}, "ler_conteudo_execucao", map[string]any{"trace_id": "abc123"}, false},
		{"ia_conteudo sozinho não vê o resumo", []string{EscopoIAConteudo}, "ia_resumo", map[string]any{}, true},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			ch := &chIA{}
			cs := conectarIA(t, ch, c.escopos...)
			r, corpo := chamar(t, cs, c.tool, c.args)
			if r.IsError != c.negado {
				t.Fatalf("IsError = %v, quer %v: %s", r.IsError, c.negado, corpo)
			}
			if c.negado {
				if !strings.Contains(corpo, "escopo") {
					t.Fatalf("a recusa deve explicar o escopo: %s", corpo)
				}
				if q := ch.consultas(); len(q) != 0 {
					t.Fatalf("chamada recusada consultou o ClickHouse: %v", q)
				}
				return
			}
			if len(ch.consultas()) == 0 {
				t.Fatal("chamada permitida não consultou nada")
			}
		})
	}
}

func TestLerConteudoDevolveAsMensagens(t *testing.T) {
	ch := &chIA{}
	cs := conectarIA(t, ch, EscopoLeitura, EscopoIAConteudo)
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "ler_conteudo_execucao",
		Arguments: map[string]any{"trace_id": "abc123"}})
	if err != nil || r.IsError {
		t.Fatalf("ler conteúdo: %v %+v", err, r)
	}
	b, _ := json.Marshal(r.StructuredContent)
	if !strings.Contains(string(b), `"texto":"olá"`) || !strings.Contains(string(b), `"total":1`) {
		t.Fatalf("saída: %s", b)
	}
	// o MCP lê sem escopo de servidor (nil): nenhum predicado de host é aplicado
	if q := ch.consultas(); len(q) != 1 || strings.Contains(q[0], "host IN") || strings.Contains(q[0], "1=0") {
		t.Fatalf("consulta do conteúdo: %v", q)
	}
}

// TestTraceIDInvalidoNaoChegaAoSQL: o MCP chama o domínio direto (sem a borda HTTP que
// valida o path): ver_execucao e ler_conteudo_execucao com id não hex respondem "não
// encontrada"/vazio sem consultar o ClickHouse.
func TestTraceIDInvalidoNaoChegaAoSQL(t *testing.T) {
	ch := &chIA{}
	cs := conectarIA(t, ch, EscopoLeitura, EscopoIAConteudo)
	for _, id := range []string{"x' OR 1=1 --", "não-hex", ""} {
		ch.zerar()
		r, corpo := chamar(t, cs, "ver_execucao", map[string]any{"trace_id": id})
		if !r.IsError || !strings.Contains(corpo, ia.ErrExecucaoNaoEncontrada.Error()) {
			t.Fatalf("ver_execucao(%q): IsError=%v %s", id, r.IsError, corpo)
		}
		r, corpo = chamar(t, cs, "ler_conteudo_execucao", map[string]any{"trace_id": id})
		b, _ := json.Marshal(r.StructuredContent)
		if r.IsError || !strings.Contains(string(b), `"total":0`) {
			t.Fatalf("ler_conteudo_execucao(%q): %s %s", id, corpo, b)
		}
		if q := ch.consultas(); len(q) != 0 {
			t.Fatalf("id inválido %q chegou ao ClickHouse: %v", id, q)
		}
	}
}

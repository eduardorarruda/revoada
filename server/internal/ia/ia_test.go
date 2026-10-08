package ia

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/core/genai"
	"github.com/eduardorarruda/revoada/server/internal/authz"
	"github.com/eduardorarruda/revoada/server/internal/chquery"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

func f64(v float64) *float64 { return &v }

// lojaFalsa guarda a tabela de preços em memória.
type lojaFalsa struct {
	mu     sync.Mutex
	precos []store.PrecoLLM
	origem map[string]bool
}

func (l *lojaFalsa) ListarPrecosLLM(context.Context) ([]store.PrecoLLM, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]store.PrecoLLM(nil), l.precos...), nil
}

func (l *lojaFalsa) CriarPrecoLLM(_ context.Context, p store.PrecoLLM) (store.PrecoLLM, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p.Provedor, p.Modelo = strings.ToLower(strings.TrimSpace(p.Provedor)), strings.ToLower(strings.TrimSpace(p.Modelo)) // como o store
	for _, x := range l.precos {
		if x.Provedor == p.Provedor && x.Modelo == p.Modelo && x.VigenteDesde.Equal(p.VigenteDesde) {
			return p, store.ErrPrecoDuplicado
		}
	}
	p.ID = int64(len(l.precos) + 1)
	l.precos = append(l.precos, p)
	return p, nil
}

func (l *lojaFalsa) ApagarPrecoLLM(_ context.Context, id int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, x := range l.precos {
		if x.ID == id {
			l.precos = append(l.precos[:i], l.precos[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

func (l *lojaFalsa) SemearPrecosLLM(_ context.Context, origem string, ps []store.PrecoLLM) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.origem == nil {
		l.origem = map[string]bool{}
	}
	if l.origem[origem] {
		return 0, nil
	}
	l.origem[origem] = true
	for _, p := range ps {
		p.Origem = origem
		l.precos = append(l.precos, p)
	}
	return len(ps), nil
}

// chFalso devolve linhas prontas e registra o SQL recebido.
type chFalso struct {
	linhas []map[string]any
	sqls   []string
	err    error
}

func (c *chFalso) QueryJSON(_ context.Context, q string) ([]map[string]any, error) {
	c.sqls = append(c.sqls, q)
	return c.linhas, c.err
}

func (c *chFalso) Exec(_ context.Context, s string) error {
	c.sqls = append(c.sqls, s)
	return c.err
}

func (c *chFalso) InsertMetricsAt(context.Context, string, time.Time, []chquery.MetricPoint) error {
	return c.err
}

type auditorFalso struct{ entradas []store.AuditEntry }

func (a *auditorFalso) Registrar(e store.AuditEntry) { a.entradas = append(a.entradas, e) }

var jan2025 = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

func tabelaTeste() *genai.Tabela {
	return tabelaDePrecos([]store.PrecoLLM{
		{Provedor: "openai", Modelo: "gpt-4o-mini*", EntradaPor1M: 0.15, SaidaPor1M: 0.60, CacheLeituraPor1M: f64(0.075), VigenteDesde: jan2025},
	})
}

func TestCalcular(t *testing.T) {
	tab := tabelaTeste()
	quando := jan2025.AddDate(1, 0, 0)
	casos := []struct {
		nome      string
		l         linhaUso
		usd       *float64
		semPreco  int64
		semTokens int64
		calculado bool
	}{
		{"estimado", linhaUso{Quando: quando, Provedor: "openai", Modelo: "gpt-4o-mini-2024-07-18", Chamadas: 1, ComTokens: 1,
			Estimaveis: 1, Uso: genai.Uso{Entrada: 1_000_000, Saida: 1_000_000}, Estimar: genai.Uso{Entrada: 1_000_000, Saida: 1_000_000}},
			f64(0.75), 0, 0, true},
		{"sem preço não vira zero", linhaUso{Quando: quando, Provedor: "openai", Modelo: "desconhecido", Chamadas: 2, ComTokens: 2,
			Estimaveis: 2, Estimar: genai.Uso{Entrada: 10}}, nil, 2, 0, false},
		{"sem tokens", linhaUso{Quando: quando, Modelo: "gpt-4o-mini", Chamadas: 3, SemTokens: 3}, nil, 0, 3, false},
		{"informado vence", linhaUso{Quando: quando, Modelo: "desconhecido", Chamadas: 1, ComTokens: 1, ComCustoInformado: 1,
			CustoInformado: 0.5, Uso: genai.Uso{Entrada: 99}}, f64(0.5), 0, 0, true},
		// Proxy que informa custo sem tokens numa chamada e tokens sem custo noutra: as
		// contagens vêm do SQL linha a linha, sem supor que andam juntas.
		{"custo e tokens em chamadas diferentes", linhaUso{Quando: quando, Provedor: "openai", Modelo: "gpt-4o-mini",
			Chamadas: 2, ComTokens: 1, Estimaveis: 1, ComCustoInformado: 1, CustoInformado: 0.25,
			Estimar: genai.Uso{Entrada: 1_000_000}}, f64(0.40), 0, 0, true},
		{"antes da vigência não tem preço", linhaUso{Quando: jan2025.Add(-time.Hour), Provedor: "openai", Modelo: "gpt-4o-mini",
			Chamadas: 1, ComTokens: 1, Estimaveis: 1, Estimar: genai.Uso{Entrada: 1}}, nil, 1, 0, false},
		{"grupo vazio", linhaUso{Quando: quando, Modelo: "gpt-4o-mini"}, nil, 0, 0, false},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got := calcular(tab, c.l)
			u := got.usdOuNulo()
			if (u == nil) != (c.usd == nil) || (u != nil && abs(*u-*c.usd) > 1e-12) {
				t.Fatalf("usd = %v, quer %v", u, c.usd)
			}
			if got.SemPreco != c.semPreco || got.SemTokens != c.semTokens || got.Calculado != c.calculado {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func TestEscolherPassoESerie(t *testing.T) {
	if escolherPasso(time.Hour) != 60 || escolherPasso(24*time.Hour) != 900 || escolherPasso(400*24*time.Hour) != 86400 {
		t.Fatal("passo errado")
	}
	f := Filtros{De: time.Unix(1000, 0), Ate: time.Unix(1000+600, 0)}
	s := completarSerie(map[int64]*PontoSerie{960 + 120: {TsMs: 1080_000, Chamadas: 7}}, f, 60)
	if len(s) != 11 || s[2].Chamadas != 7 || s[0].TsMs != 960_000 {
		t.Fatalf("série: %d pontos, %+v", len(s), s[:3])
	}
}

func TestRepeticoesEProfundidade(t *testing.T) {
	ps := []Passo{
		{SpanID: "a", Operacao: "invoke_agent"},
		{SpanID: "b", ParentSpanID: "a", Operacao: "chat"},
		{SpanID: "c", ParentSpanID: "a", Operacao: "execute_tool", Ferramenta: "clima"},
		{SpanID: "d", ParentSpanID: "a", Operacao: "chat"},
		{SpanID: "e", ParentSpanID: "a", Operacao: "execute_tool", Ferramenta: "clima"},
		{SpanID: "f", ParentSpanID: "e", Operacao: "execute_tool", Ferramenta: "clima"},
		{SpanID: "g", ParentSpanID: "http-fora", Operacao: "execute_tool", Ferramenta: "outra"},
	}
	r := repeticoes(ps)
	if len(r) != 1 || r[0].Ferramenta != "clima" || r[0].Vezes != 3 || r[0].PrimeiroSpanID != "c" {
		t.Fatalf("repetições: %+v", r)
	}
	profundidades(ps)
	quer := []int{0, 1, 1, 1, 1, 2, 0}
	for i, p := range ps {
		if p.Profundidade != quer[i] {
			t.Fatalf("profundidade de %s = %d, quer %d", p.SpanID, p.Profundidade, quer[i])
		}
	}
}

func TestComandosPurge(t *testing.T) {
	if _, msg := comandosPurge(pedidoPurge{Alvo: "todo_conteudo", Frase: "apagar"}, nil); msg == "" {
		t.Fatal("frase errada deveria recusar")
	}
	s, msg := comandosPurge(pedidoPurge{Alvo: "todo_conteudo", Frase: FraseApagarConteudo}, nil)
	if msg != "" || s[0] != "TRUNCATE TABLE genai_conteudo" {
		t.Fatalf("todo_conteudo: %v %q", s, msg)
	}
	if _, msg := comandosPurge(pedidoPurge{Alvo: "trace", Valor: "x'; DROP"}, nil); msg == "" {
		t.Fatal("trace_id inválido deveria recusar")
	}
	s, _ = comandosPurge(pedidoPurge{Alvo: "trace", Valor: "abc123"}, nil)
	if len(s) != 2 || !strings.HasPrefix(s[0], "ALTER TABLE genai_conteudo") {
		t.Fatalf("trace: %v", s)
	}
	if _, msg := comandosPurge(pedidoPurge{Alvo: "conversa", Valor: "c1"}, nil); msg == "" {
		t.Fatal("conversa sem traces deveria explicar")
	}
	s, _ = comandosPurge(pedidoPurge{Alvo: "conversa", Valor: "c1"}, []string{"aa", "bb"})
	if !strings.Contains(s[0], "IN ('aa','bb')") || !strings.HasPrefix(s[1], "ALTER TABLE genai_spans") {
		t.Fatalf("conversa: %v", s)
	}
	if _, msg := comandosPurge(pedidoPurge{Alvo: "tudo"}, nil); msg == "" {
		t.Fatal("alvo desconhecido")
	}
}

func TestReferenciaEmbutida(t *testing.T) {
	r, err := lerReferencia()
	if err != nil || len(r.Precos) < 10 || !strings.HasPrefix(r.Origem, "referencia-") {
		t.Fatalf("referência: %v %d %q", err, len(r.Precos), r.Origem)
	}
	for _, p := range r.Precos {
		if p.Modelo != strings.ToLower(p.Modelo) || p.Entrada < 0 || p.Saida < 0 {
			t.Fatalf("linha inválida: %+v", p)
		}
	}
	loja := &lojaFalsa{}
	h := New(&chFalso{}, loja, nil)
	if n, _ := h.SemearReferencia(context.Background()); n != len(r.Precos) {
		t.Fatalf("semeou %d", n)
	}
	if n, _ := h.SemearReferencia(context.Background()); n != 0 {
		t.Fatal("semear de novo duplicou")
	}
	// A tabela embutida acha o modelo certo para os nomes com data que os provedores devolvem.
	tab := tabelaDePrecos(loja.precos)
	for modelo, entrada := range map[string]float64{"gpt-4o-mini-2024-07-18": 0.15, "gpt-4o-2024-08-06": 2.5,
		"claude-sonnet-4-5": 3, "claude-opus-4-5-20251101": 5, "gemini-2.5-flash-lite": 0.10} {
		p, ok := tab.Achar("", modelo, time.Now())
		if !ok || p.Entrada != entrada {
			t.Errorf("%s: ok=%v entrada=%v, quer %v", modelo, ok, p.Entrada, entrada)
		}
	}
	info := infoReferencia(loja.precos, time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC))
	if info == nil || !info.Desatualizada || info.Referencia != "2025-10" {
		t.Fatalf("info: %+v", info)
	}
	if infoReferencia(nil, time.Now()) != nil {
		t.Fatal("sem referência deveria ser nil")
	}
}

func TestValidarPreco(t *testing.T) {
	ok := novoPreco{Modelo: "gpt-x*", EntradaPor1M: f64(1), SaidaPor1M: f64(2)}
	if msg := ok.validar(); msg != "" {
		t.Fatal(msg)
	}
	ruins := []novoPreco{
		{EntradaPor1M: f64(1), SaidaPor1M: f64(2)},
		{Modelo: "gp*t", EntradaPor1M: f64(1), SaidaPor1M: f64(2)},
		{Modelo: "m"},
		{Modelo: "m", EntradaPor1M: f64(-1), SaidaPor1M: f64(2)},
		{Modelo: "m", EntradaPor1M: f64(1), SaidaPor1M: f64(2), CacheLeituraPor1M: f64(20_000)},
	}
	for _, r := range ruins {
		if r.validar() == "" {
			t.Errorf("deveria recusar %+v", r)
		}
	}
}

func TestPrecosHTTP(t *testing.T) {
	loja := &lojaFalsa{}
	h := New(&chFalso{linhas: []map[string]any{{"provedor": "openai", "modelo": "gpt-9", "chamadas": "4"}}}, loja, nil)
	rec := httptest.NewRecorder()
	h.CriarPrecoHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/ia/precos",
		strings.NewReader(`{"provedor":"OpenAI","modelo":"GPT-4o*","entrada_por_1m":2.5,"saida_por_1m":10}`)))
	if rec.Code != http.StatusCreated || loja.precos[0].Modelo != "gpt-4o*" || loja.precos[0].Provedor != "openai" {
		t.Fatalf("criar: %d %s %+v", rec.Code, rec.Body, loja.precos)
	}
	rec = httptest.NewRecorder()
	h.CriarPrecoHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/ia/precos", strings.NewReader(`{"modelo":""}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("validação: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.PrecosHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ia/precos", nil))
	if !strings.Contains(rec.Body.String(), `"modelos_sem_preco":[{"provedor":"openai","modelo":"gpt-9","chamadas":4}]`) {
		t.Fatalf("listar: %s", rec.Body)
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/ia/precos/1", nil)
	req.SetPathValue("id", "1")
	rec = httptest.NewRecorder()
	h.ApagarPrecoHTTP(rec, req)
	if rec.Code != http.StatusNoContent || len(loja.precos) != 0 {
		t.Fatalf("apagar: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ApagarPrecoHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("apagar de novo: %d", rec.Code)
	}
}

func TestConteudoAuditaEFiltraEscopo(t *testing.T) {
	ch := &chFalso{linhas: []map[string]any{{"span_id": "s1", "lado": "entrada", "papel": "user", "ordem": 0,
		"texto": "oi", "truncado": 0.0, "redigido": 1.0}}}
	aud := &auditorFalso{}
	h := New(ch, &lojaFalsa{}, aud)
	req := httptest.NewRequest(http.MethodGet, "/api/ia/execucoes/abc/conteudo", nil)
	req.SetPathValue("trace_id", "abc")
	escopo := &authz.Scope{}
	req = req.WithContext(authz.WithScope(req.Context(), escopo))
	rec := httptest.NewRecorder()
	h.ConteudoHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"redigido":true`) {
		t.Fatalf("conteúdo: %d %s", rec.Code, rec.Body)
	}
	if len(aud.entradas) != 1 || aud.entradas[0].Resource != "ia_conteudo" || aud.entradas[0].Target != "abc" {
		t.Fatalf("auditoria: %+v", aud.entradas)
	}
	// usuário sem servidores: o predicado bloqueia tudo (1=0) dentro da subconsulta
	if !strings.Contains(ch.sqls[0], "1=0") {
		t.Fatalf("escopo não aplicado: %s", ch.sqls[0])
	}
	bad := httptest.NewRequest(http.MethodGet, "/x", nil)
	bad.SetPathValue("trace_id", "não-hex")
	rec = httptest.NewRecorder()
	h.ConteudoHTTP(rec, bad)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("trace_id inválido: %d", rec.Code)
	}
}

func TestFiltrosNaoInjetam(t *testing.T) {
	f := Filtros{De: time.Unix(0, 0), Ate: time.Unix(60, 0), Service: "a' OR 1=1 --", Agente: `x\'`}
	w := f.onde("s.")
	if !strings.Contains(w, `s.service = 'a\' OR 1=1 --'`) || !strings.Contains(w, `agente = 'x\\\''`) {
		t.Fatalf("filtro mal escapado: %s", w)
	}
	if (Filtros{}).usaBruto() || !(Filtros{Conversa: "c"}).usaBruto() {
		t.Fatal("usaBruto")
	}
}

func TestErroDoClickHouseViraErro500(t *testing.T) {
	h := New(&chFalso{err: errors.New("caiu")}, &lojaFalsa{}, nil)
	rec := httptest.NewRecorder()
	h.ResumoHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ia/resumo", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("código %d", rec.Code)
	}
}

func TestEhTraceID(t *testing.T) {
	casos := map[string]bool{"": false, "abc123": true, "ABCDEF0123": true, strings.Repeat("a", 64): true,
		strings.Repeat("a", 65): false, "xyz": false, "ab cd": false, "ab'--": false}
	for id, quer := range casos {
		if ehTraceID(id) != quer {
			t.Errorf("ehTraceID(%q) = %v, quer %v", id, !quer, quer)
		}
	}
}

func TestDominioRecusaTraceIDInvalido(t *testing.T) {
	ch := &chFalso{}
	h := New(ch, &lojaFalsa{}, nil)
	if _, err := h.Execucao(context.Background(), "x' OR 1=1 --", nil); !errors.Is(err, ErrExecucaoNaoEncontrada) {
		t.Fatalf("replay com id inválido: %v", err)
	}
	if ms, err := h.Conteudo(context.Background(), "não-hex", nil); err != nil || ms != nil {
		t.Fatalf("conteúdo com id inválido: %v %v", ms, err)
	}
	if len(ch.sqls) != 0 {
		t.Fatalf("id inválido chegou ao ClickHouse: %v", ch.sqls)
	}
}

func TestLeituraToleraTiposInesperados(t *testing.T) {
	if inteiro(true) != 0 || numero(nil) != 0 || inteiroOuNulo(nil) != nil || len(textos([]any{"a", nil, 3})) != 1 {
		t.Fatal("leitura de tipo inesperado deveria cair em zero/nil")
	}
}

// Package ia serve a tela Agentes de IA: custo, tokens, latência e erro das chamadas de
// IA das aplicações monitoradas, o replay passo a passo de cada execução, a tabela de
// preços e as séries llm.* que os alertas comuns observam (ADR 008).
//
// Os dados vêm do gateway (core/genai normaliza os spans OTLP): genai_spans tem uma
// linha por chamada (90 dias), genai_1m o agregado exato por minuto (400 dias) e
// genai_conteudo o prompt/resposta quando o operador liga a gravação (7 dias).
//
// Regra que atravessa o pacote: null é "não informado" ou "sem preço" — nunca zero.
package ia

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/core/genai"
	"github.com/eduardorarruda/revoada/server/internal/authz"
	"github.com/eduardorarruda/revoada/server/internal/chquery"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// Janelas máximas por fonte: o agregado guarda 400 dias; as linhas, 90.
const (
	maxJanelaBruta    = 90 * 24 * time.Hour
	maxJanelaAgregada = 400 * 24 * time.Hour
	janelaPadrao      = time.Hour
	limitePadrao      = 100
	limiteMaximo      = 500
)

// opsDeModelo é a lista SQL das operações que consomem tokens (genai.EhChamadaDeModelo).
const opsDeModelo = "('chat','text_completion','generate_content','embeddings')"

// Consultor é o pedaço do cliente ClickHouse que o pacote usa (fake nos testes).
type Consultor interface {
	QueryJSON(ctx context.Context, query string) ([]map[string]any, error)
	Exec(ctx context.Context, statement string) error
	InsertMetricsAt(ctx context.Context, tenant string, quando time.Time, points []chquery.MetricPoint) error
}

// Precos é o pedaço do store que guarda a tabela de preços.
type Precos interface {
	ListarPrecosLLM(ctx context.Context) ([]store.PrecoLLM, error)
	CriarPrecoLLM(ctx context.Context, p store.PrecoLLM) (store.PrecoLLM, error)
	ApagarPrecoLLM(ctx context.Context, id int64) error
	SemearPrecosLLM(ctx context.Context, origem string, precos []store.PrecoLLM) (int, error)
}

// Auditor grava a leitura de conteúdo na trilha (GET não passa pelo middleware).
type Auditor interface {
	Registrar(e store.AuditEntry)
}

// Handler atende /api/ia/* e é a fonte das ferramentas de IA do MCP.
type Handler struct {
	ch    Consultor
	st    Precos
	audit Auditor
	agora func() time.Time
}

// New monta o handler. audit pode ser nil (testes).
func New(ch Consultor, st Precos, audit Auditor) *Handler {
	return &Handler{ch: ch, st: st, audit: audit, agora: time.Now}
}

// Filtros são os parâmetros comuns das consultas.
type Filtros struct {
	De, Ate  time.Time
	Service  string
	Agente   string
	Modelo   string
	Host     string
	Conversa string
	escopo   *authz.Scope // servidores que o usuário pode ver (nil = sem restrição)
}

// lerFiltros lê a query string. Sem from/to: última hora. Janela maior que o máximo
// da fonte é cortada no início (o fim é o que o usuário quer ver).
func (h *Handler) lerFiltros(r *http.Request) Filtros {
	q := r.URL.Query()
	agora := h.agora()
	f := Filtros{
		Ate:     lerTempo(q.Get("to"), agora),
		Service: q.Get("service"), Agente: q.Get("agente"), Modelo: q.Get("modelo"),
		Host: q.Get("host"), Conversa: q.Get("conversa_id"),
	}
	f.De = lerTempo(q.Get("from"), f.Ate.Add(-janelaPadrao))
	if !f.De.Before(f.Ate) {
		f.De = f.Ate.Add(-janelaPadrao)
	}
	if escopo, ok := authz.ScopeFrom(r.Context()); ok {
		f.escopo = escopo
	}
	return f
}

func lerTempo(s string, padrao time.Time) time.Time {
	if s == "" {
		return padrao
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1e12 {
			return time.UnixMilli(n)
		}
		return time.Unix(n, 0)
	}
	return padrao
}

// usaBruto diz se a consulta precisa das linhas (genai_spans): filtro por agente ou
// conversa só existe lá — no agregado, a chamada ao modelo não sabe de que agente é.
func (f Filtros) usaBruto() bool { return f.Agente != "" || f.Conversa != "" }

// cortar limita a janela ao máximo da fonte.
func (f Filtros) cortar(max time.Duration) Filtros {
	if f.Ate.Sub(f.De) > max {
		f.De = f.Ate.Add(-max)
	}
	return f
}

// onde monta o WHERE das linhas (genai_spans). p é o prefixo da tabela ("" ou "s.").
func (f Filtros) onde(p string) string {
	c := []string{
		p + "tenant_id = 'default'",
		fmt.Sprintf("%sts >= fromUnixTimestamp64Milli(%d)", p, f.De.UnixMilli()),
		fmt.Sprintf("%sts < fromUnixTimestamp64Milli(%d)", p, f.Ate.UnixMilli()),
	}
	c = append(c, f.comuns(p)...)
	if f.Agente != "" {
		c = append(c, fmt.Sprintf(`%strace_id IN (SELECT trace_id FROM genai_spans WHERE tenant_id = 'default'
			AND ts >= fromUnixTimestamp64Milli(%d) AND ts < fromUnixTimestamp64Milli(%d) AND agente = %s)`,
			p, f.De.Add(-time.Hour).UnixMilli(), f.Ate.UnixMilli(), quote(f.Agente)))
	}
	if f.Conversa != "" {
		c = append(c, p+"conversa_id = "+quote(f.Conversa))
	}
	return strings.Join(c, " AND ")
}

// ondeAgregado monta o WHERE de genai_1m (colunas DateTime, sem agente/conversa).
func (f Filtros) ondeAgregado() string {
	c := []string{
		"tenant_id = 'default'",
		fmt.Sprintf("ts >= toDateTime(%d)", f.De.Unix()),
		fmt.Sprintf("ts < toDateTime(%d)", f.Ate.Unix()),
	}
	return strings.Join(append(c, f.comuns("")...), " AND ")
}

func (f Filtros) comuns(p string) []string {
	var c []string
	if f.Service != "" {
		c = append(c, p+"service = "+quote(f.Service))
	}
	if f.Modelo != "" {
		c = append(c, p+"modelo = "+quote(f.Modelo))
	}
	if f.Host != "" {
		c = append(c, p+"host = "+quote(f.Host))
	}
	if f.escopo != nil {
		if pred := f.escopo.HostPredicate(p + "host"); pred != "" {
			c = append(c, pred)
		}
	}
	return c
}

func quote(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "'", "\\'")
	return "'" + s + "'"
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func erroInterno(w http.ResponseWriter, err error) {
	http.Error(w, "erro consultando as chamadas de IA: "+err.Error(), http.StatusInternalServerError)
}

// inteiro e numero leem valores do JSONEachRow: o ClickHouse manda UInt64/Int64 entre
// aspas (output_format_json_quote_64bit_integers) e Float64 como número.
func inteiro(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	case json.Number:
		n, _ := x.Int64()
		return n
	}
	return 0
}

func numero(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	case json.Number:
		f, _ := x.Float64()
		return f
	}
	return 0
}

// inteiroOuNulo devolve nil para JSON null (token não informado).
func inteiroOuNulo(v any) *int64 {
	if v == nil {
		return nil
	}
	n := inteiro(v)
	return &n
}

func texto(v any) string {
	s, _ := v.(string)
	return s
}

func textos(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, x := range l {
		if s, ok := x.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// quantis lê o array [p50, p95, p99] (ou [p50, p95]) de uma linha.
func quantis(v any) []float64 {
	l, _ := v.([]any)
	out := make([]float64, len(l))
	for i, x := range l {
		out[i] = numero(x)
	}
	return out
}

// tabelaDePrecos converte as linhas do store para o core/genai.
func tabelaDePrecos(ps []store.PrecoLLM) *genai.Tabela {
	out := make([]genai.Preco, 0, len(ps))
	for _, p := range ps {
		out = append(out, genai.Preco{
			Provedor: p.Provedor, Modelo: p.Modelo, Entrada: p.EntradaPor1M, Saida: p.SaidaPor1M,
			CacheLeitura: p.CacheLeituraPor1M, CacheEscrita: p.CacheEscritaPor1M,
			VigenteDesde: p.VigenteDesde, Origem: p.Origem,
		})
	}
	return genai.NovaTabela(out)
}

func (h *Handler) tabela(ctx context.Context) (*genai.Tabela, []store.PrecoLLM, error) {
	ps, err := h.st.ListarPrecosLLM(ctx)
	if err != nil {
		return nil, nil, err
	}
	return tabelaDePrecos(ps), ps, nil
}

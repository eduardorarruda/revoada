package ia

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/eduardorarruda/revoada/core/genai"
	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/authz"
)

// ErrExecucaoNaoEncontrada: o trace não tem spans de IA visíveis para o usuário.
var ErrExecucaoNaoEncontrada = errors.New("execução não encontrada")

// minimoRepeticao é a partir de quantas chamadas seguidas da mesma ferramenta o replay
// aponta um possível loop.
const minimoRepeticao = 3

// Passo é um span de IA no replay.
type Passo struct {
	SpanID             string   `json:"span_id"`
	ParentSpanID       string   `json:"parent_span_id"`
	TsMs               int64    `json:"ts_ms"`
	DuracaoMs          float64  `json:"duracao_ms"`
	Profundidade       int      `json:"profundidade"`
	Operacao           string   `json:"operacao"`
	Nome               string   `json:"nome"`
	Provedor           string   `json:"provedor"`
	Modelo             string   `json:"modelo"`
	Agente             string   `json:"agente"`
	Ferramenta         string   `json:"ferramenta"`
	ChamadaID          string   `json:"chamada_id"`
	TokensEntrada      *int64   `json:"tokens_entrada"`
	TokensSaida        *int64   `json:"tokens_saida"`
	TokensCacheLeitura *int64   `json:"tokens_cache_leitura"`
	CustoUSD           *float64 `json:"custo_usd"`
	CustoOrigem        string   `json:"custo_origem"` // informado | estimado | sem_preco | sem_tokens | "" (não é chamada de modelo)
	PrecoData          string   `json:"preco_data"`
	Erro               string   `json:"erro"`
	MotivosFim         []string `json:"motivos_fim"`
	ComConteudo        bool     `json:"com_conteudo"`
}

// Repeticao aponta a mesma ferramenta chamada várias vezes seguidas.
type Repeticao struct {
	Ferramenta     string `json:"ferramenta"`
	Vezes          int    `json:"vezes"`
	PrimeiroSpanID string `json:"primeiro_span_id"`
}

// Replay é a resposta de GET /api/ia/execucoes/{trace_id}.
type Replay struct {
	TraceID    string         `json:"trace_id"`
	Agente     string         `json:"agente"`
	Service    string         `json:"service"`
	Host       string         `json:"host"`
	ConversaID string         `json:"conversa_id"`
	InicioMs   int64          `json:"inicio_ms"`
	DuracaoMs  float64        `json:"duracao_ms"`
	Totais     TotaisExecucao `json:"totais"`
	Passos     []Passo        `json:"passos"`
	Repeticoes []Repeticao    `json:"repeticoes"`
	Conteudo   EstadoConteudo `json:"conteudo"`
}

type TotaisExecucao struct {
	ChamadasModelo     int64    `json:"chamadas_modelo"`
	ChamadasFerramenta int64    `json:"chamadas_ferramenta"`
	Erros              int64    `json:"erros"`
	TokensEntrada      int64    `json:"tokens_entrada"`
	TokensSaida        int64    `json:"tokens_saida"`
	CustoUSD           *float64 `json:"custo_usd"`
	CustoParcial       bool     `json:"custo_parcial"`
}

type EstadoConteudo struct {
	Disponivel bool `json:"disponivel"`
	PodeVer    bool `json:"pode_ver"`
}

// ExecucaoHTTP atende GET /api/ia/execucoes/{trace_id}.
func (h *Handler) ExecucaoHTTP(w http.ResponseWriter, r *http.Request) {
	tid := r.PathValue("trace_id")
	if !ehTraceID(tid) {
		http.Error(w, "trace_id inválido", http.StatusBadRequest)
		return
	}
	escopo, _ := authz.ScopeFrom(r.Context())
	rep, err := h.Execucao(r.Context(), tid, escopo)
	if errors.Is(err, ErrExecucaoNaoEncontrada) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		erroInterno(w, err)
		return
	}
	if c, ok := auth.ClaimsFrom(r.Context()); ok {
		rep.Conteudo.PodeVer = auth.Pode(c.Role, auth.PermVerConteudoIA)
	}
	writeJSON(w, http.StatusOK, rep)
}

// Execucao monta o replay de um trace. escopo nil = sem restrição de servidor (MCP).
func (h *Handler) Execucao(ctx context.Context, traceID string, escopo *authz.Scope) (Replay, error) {
	if !ehTraceID(traceID) { // a borda HTTP já valida; o MCP chega aqui direto
		return Replay{}, ErrExecucaoNaoEncontrada
	}
	pred := eTambem(predicadoHost(escopo, "host"))
	rows, err := h.ch.QueryJSON(ctx, fmt.Sprintf(`SELECT span_id, parent_span_id, toUnixTimestamp64Milli(ts) AS ts_ms,
		duracao_ms, operacao, nome, provedor, modelo, agente, ferramenta, chamada_id, service, host, conversa_id,
		tokens_entrada, tokens_saida, tokens_cache_leitura, tokens_cache_escrita, custo_informado_usd,
		erro, motivos_fim, com_conteudo
		FROM genai_spans WHERE tenant_id = 'default' AND trace_id = %s%s ORDER BY ts, span_id`, quote(traceID), pred))
	if err != nil {
		return Replay{}, err
	}
	if len(rows) == 0 {
		return Replay{}, ErrExecucaoNaoEncontrada
	}
	tab, _, err := h.tabela(ctx)
	if err != nil {
		return Replay{}, err
	}
	rep := Replay{TraceID: traceID, Passos: make([]Passo, 0, len(rows))}
	var tot custo
	for _, r := range rows {
		p, c := passoDe(r, tab)
		rep.Passos = append(rep.Passos, p)
		somarPasso(&rep, r, p, c, &tot)
	}
	rep.Totais.CustoUSD, rep.Totais.CustoParcial = tot.usdOuNulo(), tot.parcial()
	profundidades(rep.Passos)
	rep.Repeticoes = repeticoes(rep.Passos)
	return rep, nil
}

// passoDe converte uma linha num passo, já com o custo da chamada.
func passoDe(r map[string]any, tab *genai.Tabela) (Passo, custo) {
	p := Passo{SpanID: texto(r["span_id"]), ParentSpanID: texto(r["parent_span_id"]), TsMs: inteiro(r["ts_ms"]),
		DuracaoMs: numero(r["duracao_ms"]), Operacao: texto(r["operacao"]), Nome: texto(r["nome"]),
		Provedor: texto(r["provedor"]), Modelo: texto(r["modelo"]), Agente: texto(r["agente"]),
		Ferramenta: texto(r["ferramenta"]), ChamadaID: texto(r["chamada_id"]),
		TokensEntrada: inteiroOuNulo(r["tokens_entrada"]), TokensSaida: inteiroOuNulo(r["tokens_saida"]),
		TokensCacheLeitura: inteiroOuNulo(r["tokens_cache_leitura"]), Erro: texto(r["erro"]),
		MotivosFim: textos(r["motivos_fim"]), ComConteudo: inteiro(r["com_conteudo"]) == 1}
	if !genai.EhChamadaDeModelo(p.Operacao) {
		return p, custo{}
	}
	l := linhaDoPasso(r, p)
	c := calcular(tab, l)
	p.CustoUSD = c.usdOuNulo()
	switch {
	case l.ComCustoInformado > 0:
		p.CustoOrigem = "informado"
	case c.SemTokens > 0:
		p.CustoOrigem = "sem_tokens"
	case c.SemPreco > 0:
		p.CustoOrigem = "sem_preco"
	default:
		p.CustoOrigem = "estimado"
	}
	if c.Preco != nil && p.CustoOrigem == "estimado" {
		p.PrecoData = c.Preco.VigenteDesde.Format("2006-01-02")
	}
	return p, c
}

// linhaDoPasso monta o uso de uma chamada só, no formato das linhas agregadas.
func linhaDoPasso(r map[string]any, p Passo) linhaUso {
	l := linhaUso{Quando: time.UnixMilli(p.TsMs), Provedor: p.Provedor, Modelo: p.Modelo, Chamadas: 1}
	if p.Erro != "" {
		l.Erros = 1
	}
	temTokens := p.TokensEntrada != nil || p.TokensSaida != nil
	informado := r["custo_informado_usd"] != nil
	switch {
	case temTokens && !informado:
		l.ComTokens, l.Estimaveis = 1, 1
	case temTokens:
		l.ComTokens = 1
	case !informado:
		l.SemTokens = 1
	}
	l.Uso = genai.Uso{Entrada: inteiro(r["tokens_entrada"]), Saida: inteiro(r["tokens_saida"]),
		CacheLeitura: inteiro(r["tokens_cache_leitura"]), CacheEscrita: inteiro(r["tokens_cache_escrita"])}
	if informado {
		l.CustoInformado, l.ComCustoInformado = numero(r["custo_informado_usd"]), 1
	} else {
		l.Estimar = l.Uso
	}
	return l
}

func somarPasso(rep *Replay, r map[string]any, p Passo, c custo, tot *custo) {
	// Os passos chegam em ordem de início: o primeiro marca o começo da execução.
	if rep.InicioMs == 0 {
		rep.InicioMs = p.TsMs
	}
	rep.DuracaoMs = max(rep.DuracaoMs, float64(p.TsMs-rep.InicioMs)+p.DuracaoMs)
	rep.Service, rep.Host = primeiroNaoVazio(rep.Service, texto(r["service"])), primeiroNaoVazio(rep.Host, texto(r["host"]))
	rep.ConversaID = primeiroNaoVazio(rep.ConversaID, texto(r["conversa_id"]))
	if p.Operacao == genai.OpAgente && rep.Agente == "" {
		rep.Agente = p.Agente
	}
	if p.Erro != "" {
		rep.Totais.Erros++
	}
	rep.Conteudo.Disponivel = rep.Conteudo.Disponivel || p.ComConteudo
	switch {
	case p.Operacao == genai.OpFerramenta:
		rep.Totais.ChamadasFerramenta++
	case genai.EhChamadaDeModelo(p.Operacao):
		rep.Totais.ChamadasModelo++
		rep.Totais.TokensEntrada += valorOuZero(p.TokensEntrada)
		rep.Totais.TokensSaida += valorOuZero(p.TokensSaida)
		tot.somar(c)
	}
}

func primeiroNaoVazio(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func valorOuZero(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// profundidades dá o nível de cada passo na árvore dos spans de IA. O pai pode ser um
// span comum (a requisição HTTP que disparou o agente): nesse caso o passo é raiz.
func profundidades(ps []Passo) {
	pai := make(map[string]string, len(ps))
	for _, p := range ps {
		pai[p.SpanID] = p.ParentSpanID
	}
	for i := range ps {
		d, atual := 0, ps[i].ParentSpanID
		for atual != "" && d < len(ps) {
			proximo, ok := pai[atual]
			if !ok {
				break
			}
			d++
			atual = proximo
		}
		ps[i].Profundidade = d
	}
}

// repeticoes acha a mesma ferramenta chamada minimoRepeticao vezes ou mais em
// sequência (olhando só as chamadas de ferramenta, na ordem): o agente que pede de novo
// e de novo a mesma ferramenta costuma estar preso num loop.
func repeticoes(ps []Passo) []Repeticao {
	out := []Repeticao{}
	var atual *Repeticao
	for _, p := range ps {
		if p.Operacao != genai.OpFerramenta {
			continue
		}
		if atual != nil && atual.Ferramenta == p.Ferramenta {
			atual.Vezes++
			continue
		}
		if atual != nil && atual.Vezes >= minimoRepeticao {
			out = append(out, *atual)
		}
		atual = &Repeticao{Ferramenta: p.Ferramenta, Vezes: 1, PrimeiroSpanID: p.SpanID}
	}
	if atual != nil && atual.Vezes >= minimoRepeticao {
		out = append(out, *atual)
	}
	return out
}

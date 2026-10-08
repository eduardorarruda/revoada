package ia

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Execucao é uma linha da lista de execuções (um trace com spans de IA).
type Execucao struct {
	TraceID            string   `json:"trace_id"`
	InicioMs           int64    `json:"inicio_ms"`
	DuracaoMs          float64  `json:"duracao_ms"`
	Service            string   `json:"service"`
	Host               string   `json:"host"`
	Agente             string   `json:"agente"`
	ConversaID         string   `json:"conversa_id"`
	ChamadasModelo     int64    `json:"chamadas_modelo"`
	ChamadasFerramenta int64    `json:"chamadas_ferramenta"`
	Erros              int64    `json:"erros"`
	TokensEntrada      int64    `json:"tokens_entrada"`
	TokensSaida        int64    `json:"tokens_saida"`
	CustoUSD           *float64 `json:"custo_usd"`
	CustoParcial       bool     `json:"custo_parcial"`
	Modelos            []string `json:"modelos"`
	Status             string   `json:"status"`
}

// FiltroExecucoes acrescenta aos Filtros o que só a lista tem.
type FiltroExecucoes struct {
	Filtros
	SoErros  bool
	CustoMin float64
	Limite   int
}

func (h *Handler) lerFiltroExecucoes(r *http.Request) FiltroExecucoes {
	q := r.URL.Query()
	fe := FiltroExecucoes{Filtros: h.lerFiltros(r), SoErros: q.Get("status") == "erro", Limite: limitePadrao}
	fe.CustoMin, _ = strconv.ParseFloat(q.Get("custo_min"), 64)
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		fe.Limite = min(n, limiteMaximo)
	}
	return fe
}

// ExecucoesHTTP atende GET /api/ia/execucoes.
func (h *Handler) ExecucoesHTTP(w http.ResponseWriter, r *http.Request) {
	ex, err := h.Execucoes(r.Context(), h.lerFiltroExecucoes(r))
	if err != nil {
		erroInterno(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"execucoes": ex})
}

// Execucoes lista as execuções mais recentes da janela. O filtro de custo mínimo é
// aplicado depois da conta (o custo depende da tabela de preços, que mora no Postgres),
// então ele filtra dentro das `Limite` execuções mais recentes.
func (h *Handler) Execucoes(ctx context.Context, fe FiltroExecucoes) ([]Execucao, error) {
	f := fe.cortar(maxJanelaBruta)
	having := ""
	if fe.SoErros {
		having = "HAVING erros > 0"
	}
	// Os aliases não repetem nomes de coluna (svc, hst, ag, conv): no ClickHouse o
	// alias vale também dentro do WHERE, e `any(service) AS service` transformava o
	// filtro `service = 'x'` num agregado dentro do WHERE.
	rows, err := h.ch.QueryJSON(ctx, fmt.Sprintf(`SELECT trace_id,
		min(toUnixTimestamp64Milli(ts)) AS inicio, max(toUnixTimestamp64Milli(ts) + duracao_ms) AS fim,
		any(service) AS svc, any(host) AS hst,
		%[5]s AS ag,
		anyIf(conversa_id, conversa_id != '') AS conv,
		countIf(operacao IN %[1]s) AS chamadas_modelo, countIf(operacao = 'execute_tool') AS ferramentas,
		countIf(erro != '') AS erros,
		sumIf(ifNull(tokens_entrada, 0), operacao IN %[1]s) AS te, sumIf(ifNull(tokens_saida, 0), operacao IN %[1]s) AS tsa,
		groupUniqArrayIf(modelo, operacao IN %[1]s AND modelo != '') AS modelos
		FROM genai_spans WHERE %[2]s GROUP BY trace_id %[3]s ORDER BY inicio DESC LIMIT %[4]d`,
		opsDeModelo, f.onde(""), having, fe.Limite, sqlAgenteDoTrace))
	if err != nil {
		return nil, err
	}
	out := make([]Execucao, 0, len(rows))
	for _, r := range rows {
		out = append(out, lerExecucao(r))
	}
	if err := h.custearExecucoes(ctx, f, out); err != nil {
		return nil, err
	}
	return filtrarCusto(out, fe.CustoMin), nil
}

func lerExecucao(r map[string]any) Execucao {
	ini := inteiro(r["inicio"])
	e := Execucao{TraceID: texto(r["trace_id"]), InicioMs: ini, DuracaoMs: numero(r["fim"]) - float64(ini),
		Service: texto(r["svc"]), Host: texto(r["hst"]), Agente: texto(r["ag"]),
		ConversaID: texto(r["conv"]), ChamadasModelo: inteiro(r["chamadas_modelo"]),
		ChamadasFerramenta: inteiro(r["ferramentas"]), Erros: inteiro(r["erros"]),
		TokensEntrada: inteiro(r["te"]), TokensSaida: inteiro(r["tsa"]), Modelos: textos(r["modelos"]), Status: "ok"}
	if e.Erros > 0 {
		e.Status = "erro"
	}
	return e
}

// custearExecucoes calcula o custo de cada execução com o preço vigente no início dela
// (ou no começo do trecho, se o preço mudou durante a execução).
func (h *Handler) custearExecucoes(ctx context.Context, f Filtros, ex []Execucao) error {
	if len(ex) == 0 {
		return nil
	}
	ids := make([]string, len(ex))
	idx := map[string]int{}
	for i, e := range ex {
		ids[i] = quote(e.TraceID)
		idx[e.TraceID] = i
	}
	tab, ps, err := h.tabela(ctx)
	if err != nil {
		return err
	}
	fs := fronteiras(ps, f.De, f.Ate)
	rows, err := h.ch.QueryJSON(ctx, fmt.Sprintf(`SELECT trace_id, %s AS trecho, provedor, modelo, %s FROM genai_spans
		WHERE %s AND operacao IN %s AND trace_id IN (%s) GROUP BY trace_id, trecho, provedor, modelo`,
		exprTrecho("ts", fs, false), colunasUso(""), f.onde(""), opsDeModelo, strings.Join(ids, ",")))
	if err != nil {
		return err
	}
	custos := make([]custo, len(ex))
	for _, r := range rows {
		i := idx[texto(r["trace_id"])]
		quando := inicioDoTrecho(time.UnixMilli(ex[i].InicioMs), inteiro(r["trecho"]), fs)
		custos[i].somar(calcular(tab, lerUso(r, quando)))
	}
	for i := range ex {
		ex[i].CustoUSD, ex[i].CustoParcial = custos[i].usdOuNulo(), custos[i].parcial()
	}
	return nil
}

func filtrarCusto(ex []Execucao, minimo float64) []Execucao {
	if minimo <= 0 {
		return ex
	}
	out := ex[:0]
	for _, e := range ex {
		if e.CustoUSD != nil && *e.CustoUSD >= minimo {
			out = append(out, e)
		}
	}
	return out
}

// ehTraceID aceita só hex (o formato que o gateway grava): o id vai para dentro do SQL.
func ehTraceID(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

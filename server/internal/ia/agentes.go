package ia

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/eduardorarruda/revoada/core/genai"
)

// agentePorTrace é a subconsulta que dá o agente de cada execução (sqlAgenteDoTrace).
// Agrupar direto por genai_spans.agente jogaria todo o custo num agente vazio. A janela
// começa uma hora antes para alcançar o span do agente de uma execução longa que
// começou antes do intervalo consultado.
func agentePorTrace(f Filtros) string {
	g := f
	g.De = f.De.Add(-time.Hour)
	g.Modelo = "" // o span do agente não tem modelo; filtrar por ele aqui o esconderia
	return fmt.Sprintf(`SELECT trace_id, %s AS ag FROM genai_spans WHERE %s GROUP BY trace_id`,
		sqlAgenteDoTrace, g.onde(""))
}

// porAgente soma uso e custo por agente (execuções sem agente aparecem pelo service).
func (h *Handler) porAgente(ctx context.Context, f Filtros, tab *genai.Tabela, fs []time.Time) ([]PorAgente, error) {
	passo := escolherPasso(f.Ate.Sub(f.De))
	rows, err := h.ch.QueryJSON(ctx, fmt.Sprintf(`SELECT ex.ag AS agente, s.service AS service,
		s.provedor AS provedor, s.modelo AS modelo,
		toUnixTimestamp(toStartOfInterval(s.ts, INTERVAL %d SECOND)) AS b, %s AS trecho, %s
		FROM genai_spans AS s INNER JOIN (%s) AS ex ON s.trace_id = ex.trace_id
		WHERE %s AND s.operacao IN %s
		GROUP BY agente, service, provedor, modelo, b, trecho`,
		passo, exprTrecho("s.ts", fs, false), colunasUso("s."), agentePorTrace(f), f.onde("s."), opsDeModelo))
	if err != nil {
		return nil, err
	}
	grupos := map[[2]string]*totaisUso{}
	for _, r := range rows {
		k := [2]string{texto(r["agente"]), texto(r["service"])}
		if grupos[k] == nil {
			grupos[k] = &totaisUso{}
		}
		l := lerUso(r, inicioDoTrecho(time.Unix(inteiro(r["b"]), 0), inteiro(r["trecho"]), fs))
		grupos[k].somar(l, calcular(tab, l))
	}
	out := make([]PorAgente, 0, len(grupos))
	for k, t := range grupos {
		out = append(out, PorAgente{Agente: k[0], Service: k[1], Chamadas: t.Chamadas, Erros: t.Erros,
			CustoUSD: t.Custo.usdOuNulo(), SemPreco: t.Custo.SemPreco > 0, TokensEntrada: t.Uso.Entrada, TokensSaida: t.Uso.Saida})
	}
	sort.Slice(out, func(i, j int) bool {
		ci, cj := valorOuMenos(out[i].CustoUSD), valorOuMenos(out[j].CustoUSD)
		if ci != cj {
			return ci > cj
		}
		return out[i].Chamadas > out[j].Chamadas
	})
	return out, nil
}

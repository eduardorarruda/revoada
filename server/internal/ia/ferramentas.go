package ia

import (
	"context"
	"fmt"
	"net/http"
)

// Ferramenta é uma linha de GET /api/ia/ferramentas.
type Ferramenta struct {
	Ferramenta      string          `json:"ferramenta"`
	Chamadas        int64           `json:"chamadas"`
	Erros           int64           `json:"erros"`
	TaxaErro        float64         `json:"taxa_erro"`
	LatenciaP50Ms   *float64        `json:"latencia_p50_ms"`
	LatenciaP95Ms   *float64        `json:"latencia_p95_ms"`
	ErrosFrequentes []ErroFrequente `json:"erros_frequentes"`
}

type ErroFrequente struct {
	Erro  string `json:"erro"`
	Vezes int64  `json:"vezes"`
}

const errosPorFerramenta = 3

// FerramentasHTTP atende GET /api/ia/ferramentas.
func (h *Handler) FerramentasHTTP(w http.ResponseWriter, r *http.Request) {
	fs, err := h.ferramentas(r.Context(), h.lerFiltros(r).cortar(maxJanelaBruta), limiteMaximo)
	if err != nil {
		erroInterno(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ferramentas": fs})
}

// ferramentas lista as ferramentas da janela, as que mais falham primeiro.
func (h *Handler) ferramentas(ctx context.Context, f Filtros, limite int) ([]Ferramenta, error) {
	rows, err := h.ch.QueryJSON(ctx, fmt.Sprintf(`SELECT ferramenta, count() AS chamadas, countIf(erro != '') AS erros,
		quantiles(0.5, 0.95)(duracao_ms) AS q
		FROM genai_spans WHERE %s AND operacao = 'execute_tool' AND ferramenta != ''
		GROUP BY ferramenta ORDER BY erros DESC, chamadas DESC LIMIT %d`, f.onde(""), limite))
	if err != nil {
		return nil, err
	}
	out := make([]Ferramenta, 0, len(rows))
	idx := map[string]int{}
	for _, r := range rows {
		x := Ferramenta{Ferramenta: texto(r["ferramenta"]), Chamadas: inteiro(r["chamadas"]), Erros: inteiro(r["erros"]),
			ErrosFrequentes: []ErroFrequente{}}
		if x.Chamadas > 0 {
			x.TaxaErro = float64(x.Erros) / float64(x.Chamadas)
		}
		if q := quantis(r["q"]); len(q) == 2 {
			x.LatenciaP50Ms, x.LatenciaP95Ms = &q[0], &q[1]
		}
		idx[x.Ferramenta] = len(out)
		out = append(out, x)
	}
	return out, h.errosFrequentes(ctx, f, out, idx)
}

func (h *Handler) errosFrequentes(ctx context.Context, f Filtros, fs []Ferramenta, idx map[string]int) error {
	if len(fs) == 0 {
		return nil
	}
	rows, err := h.ch.QueryJSON(ctx, fmt.Sprintf(`SELECT ferramenta, erro, count() AS vezes
		FROM genai_spans WHERE %s AND operacao = 'execute_tool' AND erro != ''
		GROUP BY ferramenta, erro ORDER BY vezes DESC LIMIT %d BY ferramenta`, f.onde(""), errosPorFerramenta))
	if err != nil {
		return err
	}
	for _, r := range rows {
		if i, ok := idx[texto(r["ferramenta"])]; ok {
			fs[i].ErrosFrequentes = append(fs[i].ErrosFrequentes, ErroFrequente{texto(r["erro"]), inteiro(r["vezes"])})
		}
	}
	return nil
}

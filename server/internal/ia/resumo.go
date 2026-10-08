package ia

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/eduardorarruda/revoada/core/genai"
)

// Resumo é a resposta de GET /api/ia/resumo (contrato em docs/api-ia.md).
type Resumo struct {
	Janela      Janela             `json:"janela"`
	Totais      TotaisResumo       `json:"totais"`
	Serie       []PontoSerie       `json:"serie"`
	PorModelo   []PorModelo        `json:"por_modelo"`
	PorAgente   []PorAgente        `json:"por_agente"`
	Ferramentas []FerramentaResumo `json:"ferramentas"`
	Precos      *InfoReferencia    `json:"precos"`
}

type Janela struct {
	DeMs   int64 `json:"from_ms"`
	AteMs  int64 `json:"to_ms"`
	PassoS int64 `json:"passo_s"`
}

type TotaisResumo struct {
	Chamadas            int64    `json:"chamadas"`
	Erros               int64    `json:"erros"`
	TaxaErro            float64  `json:"taxa_erro"`
	Execucoes           int64    `json:"execucoes"`
	FerramentasChamadas int64    `json:"ferramentas_chamadas"`
	FerramentasErros    int64    `json:"ferramentas_erros"`
	TokensEntrada       int64    `json:"tokens_entrada"`
	TokensSaida         int64    `json:"tokens_saida"`
	TokensCacheLeitura  int64    `json:"tokens_cache_leitura"`
	SemTokens           int64    `json:"sem_tokens"`
	CustoUSD            *float64 `json:"custo_usd"`
	CustoInformadoUSD   float64  `json:"custo_informado_usd"`
	CustoParcial        bool     `json:"custo_parcial"`
	ChamadasSemPreco    int64    `json:"chamadas_sem_preco"`
	LatenciaP50Ms       *float64 `json:"latencia_p50_ms"`
	LatenciaP95Ms       *float64 `json:"latencia_p95_ms"`
	LatenciaP99Ms       *float64 `json:"latencia_p99_ms"`
}

type PontoSerie struct {
	TsMs     int64   `json:"ts_ms"`
	CustoUSD float64 `json:"custo_usd"`
	Chamadas int64   `json:"chamadas"`
	Erros    int64   `json:"erros"`
}

type PrecoAplicado struct {
	EntradaPor1M float64   `json:"entrada_por_1m"`
	SaidaPor1M   float64   `json:"saida_por_1m"`
	VigenteDesde time.Time `json:"vigente_desde"`
	Origem       string    `json:"origem"`
}

type PorModelo struct {
	Provedor      string         `json:"provedor"`
	Modelo        string         `json:"modelo"`
	Chamadas      int64          `json:"chamadas"`
	Erros         int64          `json:"erros"`
	TokensEntrada int64          `json:"tokens_entrada"`
	TokensSaida   int64          `json:"tokens_saida"`
	CustoUSD      *float64       `json:"custo_usd"`
	SemPreco      bool           `json:"sem_preco"`
	LatenciaP95Ms *float64       `json:"latencia_p95_ms"`
	Preco         *PrecoAplicado `json:"preco"`
}

type PorAgente struct {
	Agente        string   `json:"agente"`
	Service       string   `json:"service"`
	Chamadas      int64    `json:"chamadas"`
	Erros         int64    `json:"erros"`
	CustoUSD      *float64 `json:"custo_usd"`
	TokensEntrada int64    `json:"tokens_entrada"`
	TokensSaida   int64    `json:"tokens_saida"`
}

type FerramentaResumo struct {
	Ferramenta    string   `json:"ferramenta"`
	Chamadas      int64    `json:"chamadas"`
	Erros         int64    `json:"erros"`
	LatenciaP95Ms *float64 `json:"latencia_p95_ms"`
}

// passos possíveis da série; o escolhido dá no máximo maxPontosSerie pontos.
var passos = []int64{60, 300, 900, 1800, 3600, 10800, 21600, 43200, 86400}

const maxPontosSerie = 240

func escolherPasso(d time.Duration) int64 {
	for _, p := range passos {
		if int64(d.Seconds())/p <= maxPontosSerie {
			return p
		}
	}
	return passos[len(passos)-1]
}

// ResumoHTTP atende GET /api/ia/resumo.
func (h *Handler) ResumoHTTP(w http.ResponseWriter, r *http.Request) {
	res, err := h.Resumo(r.Context(), h.lerFiltros(r))
	if err != nil {
		erroInterno(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// Resumo monta a visão geral. Totais, série e modelos vêm do agregado por minuto
// (até 400 dias); com filtro de agente ou conversa, das linhas (até 90 dias).
func (h *Handler) Resumo(ctx context.Context, f Filtros) (Resumo, error) {
	if f.usaBruto() {
		f = f.cortar(maxJanelaBruta)
	} else {
		f = f.cortar(maxJanelaAgregada)
	}
	tab, ps, err := h.tabela(ctx)
	if err != nil {
		return Resumo{}, err
	}
	passo := escolherPasso(f.Ate.Sub(f.De))
	res := Resumo{Janela: Janela{DeMs: f.De.UnixMilli(), AteMs: f.Ate.UnixMilli(), PassoS: passo},
		Precos: infoReferencia(ps, h.agora())}
	if err := h.preencherUso(ctx, f, passo, tab, fronteiras(ps, f.De, f.Ate), &res); err != nil {
		return res, err
	}
	if err := h.preencherLatencias(ctx, f, &res); err != nil {
		return res, err
	}
	fb := f.cortar(maxJanelaBruta)
	if res.Totais.Execucoes, err = h.contarExecucoes(ctx, fb); err != nil {
		return res, err
	}
	if res.PorAgente, err = h.porAgente(ctx, fb, tab, fronteiras(ps, fb.De, fb.Ate)); err != nil {
		return res, err
	}
	fs, err := h.ferramentas(ctx, fb, 5)
	if err != nil {
		return res, err
	}
	for _, x := range fs {
		res.Ferramentas = append(res.Ferramentas, FerramentaResumo{x.Ferramenta, x.Chamadas, x.Erros, x.LatenciaP95Ms})
	}
	return res, nil
}

// linhasPorOperacao devolve o uso por (intervalo, trecho de preço, provedor, modelo, operação).
func (h *Handler) linhasPorOperacao(ctx context.Context, f Filtros, passo int64, fs []time.Time) ([]map[string]any, error) {
	if f.usaBruto() {
		return h.ch.QueryJSON(ctx, fmt.Sprintf(`SELECT toUnixTimestamp(toStartOfInterval(ts, INTERVAL %d SECOND)) AS b,
			%s AS trecho, provedor, modelo, operacao, %s FROM genai_spans WHERE %s
			GROUP BY b, trecho, provedor, modelo, operacao`, passo, exprTrecho("ts", fs, false), colunasUso(""), f.onde("")))
	}
	return h.ch.QueryJSON(ctx, fmt.Sprintf(`SELECT toUnixTimestamp(toStartOfInterval(ts, INTERVAL %d SECOND)) AS b,
		%s AS trecho, provedor, modelo, operacao, %s FROM genai_1m WHERE %s
		GROUP BY b, trecho, provedor, modelo, operacao`, passo, exprTrecho("ts", fs, true), colunasUsoAgregado, f.ondeAgregado()))
}

// preencherUso calcula totais, série e o quadro por modelo a partir das mesmas linhas.
func (h *Handler) preencherUso(ctx context.Context, f Filtros, passo int64, tab *genai.Tabela, fs []time.Time, res *Resumo) error {
	rows, err := h.linhasPorOperacao(ctx, f, passo, fs)
	if err != nil {
		return err
	}
	serie := map[int64]*PontoSerie{}
	modelos := map[[2]string]*totaisUso{}
	var tot totaisUso
	for _, r := range rows {
		b := inteiro(r["b"])
		if texto(r["operacao"]) == genai.OpFerramenta {
			res.Totais.FerramentasChamadas += inteiro(r["chamadas"])
			res.Totais.FerramentasErros += inteiro(r["erros"])
			continue
		}
		if !genai.EhChamadaDeModelo(texto(r["operacao"])) {
			continue
		}
		l := lerUso(r, inicioDoTrecho(time.Unix(b, 0), inteiro(r["trecho"]), fs))
		c := calcular(tab, l)
		tot.somar(l, c)
		chave := [2]string{l.Provedor, l.Modelo}
		if modelos[chave] == nil {
			modelos[chave] = &totaisUso{}
		}
		modelos[chave].somar(l, c)
		p := pontoDe(serie, b)
		p.CustoUSD += c.USD
		p.Chamadas += l.Chamadas
		p.Erros += l.Erros
	}
	res.Totais.preencher(tot)
	res.Serie = completarSerie(serie, f, passo)
	res.PorModelo = quadroPorModelo(modelos, tab, f.Ate)
	return nil
}

func pontoDe(serie map[int64]*PontoSerie, b int64) *PontoSerie {
	if serie[b] == nil {
		serie[b] = &PontoSerie{TsMs: b * 1000}
	}
	return serie[b]
}

func (t *TotaisResumo) preencher(tot totaisUso) {
	t.Chamadas, t.Erros = tot.Chamadas, tot.Erros
	if tot.Chamadas > 0 {
		t.TaxaErro = float64(tot.Erros) / float64(tot.Chamadas)
	}
	t.TokensEntrada, t.TokensSaida, t.TokensCacheLeitura = tot.Uso.Entrada, tot.Uso.Saida, tot.Uso.CacheLeitura
	t.SemTokens = tot.Custo.SemTokens
	t.CustoUSD = tot.Custo.usdOuNulo()
	t.CustoInformadoUSD = tot.CustoInformado
	t.CustoParcial = tot.Custo.parcial()
	t.ChamadasSemPreco = tot.Custo.SemPreco
}

// completarSerie devolve um ponto por intervalo da janela, inclusive os sem chamada
// (zero chamadas é medido: a tabela recebeu tudo o que chegou e não havia nada).
func completarSerie(serie map[int64]*PontoSerie, f Filtros, passo int64) []PontoSerie {
	ini := f.De.Unix() / passo * passo
	out := make([]PontoSerie, 0, (f.Ate.Unix()-ini)/passo+1)
	for b := ini; b < f.Ate.Unix(); b += passo {
		if p := serie[b]; p != nil {
			out = append(out, *p)
			continue
		}
		out = append(out, PontoSerie{TsMs: b * 1000})
	}
	return out
}

func quadroPorModelo(modelos map[[2]string]*totaisUso, tab *genai.Tabela, ate time.Time) []PorModelo {
	out := make([]PorModelo, 0, len(modelos))
	for k, t := range modelos {
		pm := PorModelo{Provedor: k[0], Modelo: k[1], Chamadas: t.Chamadas, Erros: t.Erros,
			TokensEntrada: t.Uso.Entrada, TokensSaida: t.Uso.Saida,
			CustoUSD: t.Custo.usdOuNulo(), SemPreco: t.Custo.SemPreco > 0}
		if p, ok := tab.Achar(k[0], k[1], ate); ok {
			pm.Preco = &PrecoAplicado{p.Entrada, p.Saida, p.VigenteDesde, p.Origem}
		}
		out = append(out, pm)
	}
	sort.Slice(out, func(i, j int) bool {
		ci, cj := valorOuMenos(out[i].CustoUSD), valorOuMenos(out[j].CustoUSD)
		if ci != cj {
			return ci > cj
		}
		return out[i].Chamadas > out[j].Chamadas
	})
	return out
}

func valorOuMenos(p *float64) float64 {
	if p == nil {
		return -1
	}
	return *p
}

// preencherLatencias: p50/p95/p99 das chamadas de modelo, no total e por modelo.
func (h *Handler) preencherLatencias(ctx context.Context, f Filtros, res *Resumo) error {
	var sql string
	if f.usaBruto() {
		sql = fmt.Sprintf(`SELECT provedor, modelo, quantilesTDigest(0.5, 0.95, 0.99)(duracao_ms) AS q
			FROM genai_spans WHERE %s AND operacao IN %s GROUP BY provedor, modelo`, f.onde(""), opsDeModelo)
	} else {
		sql = fmt.Sprintf(`SELECT provedor, modelo, quantilesTDigestMerge(0.5, 0.95, 0.99)(latencia) AS q
			FROM genai_1m WHERE %s AND operacao IN %s GROUP BY provedor, modelo`, f.ondeAgregado(), opsDeModelo)
	}
	rows, err := h.ch.QueryJSON(ctx, sql)
	if err != nil {
		return err
	}
	porModelo := map[[2]string][]float64{}
	for _, r := range rows {
		porModelo[[2]string{texto(r["provedor"]), texto(r["modelo"])}] = quantis(r["q"])
	}
	for i := range res.PorModelo {
		if q := porModelo[[2]string{res.PorModelo[i].Provedor, res.PorModelo[i].Modelo}]; len(q) == 3 {
			res.PorModelo[i].LatenciaP95Ms = &q[1]
		}
	}
	return h.latenciaTotal(ctx, f, res)
}

// latenciaTotal faz a conta das chamadas de modelo da janela inteira.
func (h *Handler) latenciaTotal(ctx context.Context, f Filtros, res *Resumo) error {
	var sql string
	if f.usaBruto() {
		sql = fmt.Sprintf(`SELECT count() AS n, quantilesTDigest(0.5, 0.95, 0.99)(duracao_ms) AS q
			FROM genai_spans WHERE %s AND operacao IN %s`, f.onde(""), opsDeModelo)
	} else {
		sql = fmt.Sprintf(`SELECT sum(chamadas) AS n, quantilesTDigestMerge(0.5, 0.95, 0.99)(latencia) AS q
			FROM genai_1m WHERE %s AND operacao IN %s`, f.ondeAgregado(), opsDeModelo)
	}
	rows, err := h.ch.QueryJSON(ctx, sql)
	if err != nil || len(rows) == 0 || inteiro(rows[0]["n"]) == 0 {
		return err
	}
	if q := quantis(rows[0]["q"]); len(q) == 3 {
		res.Totais.LatenciaP50Ms, res.Totais.LatenciaP95Ms, res.Totais.LatenciaP99Ms = &q[0], &q[1], &q[2]
	}
	return nil
}

func (h *Handler) contarExecucoes(ctx context.Context, f Filtros) (int64, error) {
	rows, err := h.ch.QueryJSON(ctx, fmt.Sprintf(`SELECT uniqExact(trace_id) AS n FROM genai_spans WHERE %s`, f.onde("")))
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	return inteiro(rows[0]["n"]), nil
}

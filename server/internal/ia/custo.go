package ia

import (
	"fmt"
	"time"

	"github.com/eduardorarruda/revoada/core/genai"
)

// linhaUso é o consumo de um grupo de chamadas de modelo (um modelo num intervalo,
// num trace, num agente…). Mesmo formato vindo das linhas ou do agregado por minuto.
type linhaUso struct {
	Quando            time.Time // início do intervalo: é a data do preço usado
	Provedor, Modelo  string
	Chamadas, Erros   int64
	ComTokens         int64
	SemTokens         int64     // sem tokens E sem custo informado: nada a calcular
	Estimaveis        int64     // com tokens e sem custo informado: dependem do preço
	Uso, Estimar      genai.Uso // Estimar = tokens só das chamadas Estimaveis
	CustoInformado    float64
	ComCustoInformado int64
}

// colunasUso são as somas, escritas sobre as linhas de genai_spans (prefixo p). As
// contagens sem_tok e estimaveis são feitas linha a linha, aqui no SQL: deduzi-las das
// outras contagens no Go exigiria supor que "tem tokens" e "tem custo informado"
// andam juntos — e um proxy que informa custo sem tokens quebraria a conta.
func colunasUso(p string) string {
	return fmt.Sprintf(`count() AS chamadas, countIf(%[1]serro != '') AS erros,
		countIf(%[1]stokens_entrada IS NOT NULL OR %[1]stokens_saida IS NOT NULL) AS com_tokens,
		countIf(%[1]stokens_entrada IS NULL AND %[1]stokens_saida IS NULL AND %[1]scusto_informado_usd IS NULL) AS sem_tok,
		countIf((%[1]stokens_entrada IS NOT NULL OR %[1]stokens_saida IS NOT NULL) AND %[1]scusto_informado_usd IS NULL) AS estimaveis,
		sum(ifNull(%[1]stokens_entrada, 0)) AS te, sum(ifNull(%[1]stokens_saida, 0)) AS tsa,
		sum(ifNull(%[1]stokens_cache_leitura, 0)) AS tcl, sum(ifNull(%[1]stokens_cache_escrita, 0)) AS tce,
		sumIf(ifNull(%[1]stokens_entrada, 0), %[1]scusto_informado_usd IS NULL) AS ee,
		sumIf(ifNull(%[1]stokens_saida, 0), %[1]scusto_informado_usd IS NULL) AS es,
		sumIf(ifNull(%[1]stokens_cache_leitura, 0), %[1]scusto_informado_usd IS NULL) AS ecl,
		sumIf(ifNull(%[1]stokens_cache_escrita, 0), %[1]scusto_informado_usd IS NULL) AS ece,
		sum(ifNull(%[1]scusto_informado_usd, 0)) AS ci, countIf(%[1]scusto_informado_usd IS NOT NULL) AS cci`, p)
}

// colunasUsoAgregado são as mesmas somas sobre genai_1m.
const colunasUsoAgregado = `sum(chamadas) AS chamadas, sum(erros) AS erros, sum(com_tokens) AS com_tokens,
	sum(sem_tokens) AS sem_tok, sum(estimaveis) AS estimaveis,
	sum(tokens_entrada) AS te, sum(tokens_saida) AS tsa,
	sum(tokens_cache_leitura) AS tcl, sum(tokens_cache_escrita) AS tce,
	sum(estimar_entrada) AS ee, sum(estimar_saida) AS es,
	sum(estimar_cache_leitura) AS ecl, sum(estimar_cache_escrita) AS ece,
	sum(custo_informado_usd) AS ci, sum(com_custo_informado) AS cci`

// lerUso converte uma linha de consulta (com as colunas acima) em linhaUso.
func lerUso(r map[string]any, quando time.Time) linhaUso {
	return linhaUso{
		Quando: quando, Provedor: texto(r["provedor"]), Modelo: texto(r["modelo"]),
		Chamadas: inteiro(r["chamadas"]), Erros: inteiro(r["erros"]), ComTokens: inteiro(r["com_tokens"]),
		SemTokens: inteiro(r["sem_tok"]), Estimaveis: inteiro(r["estimaveis"]),
		Uso: genai.Uso{Entrada: inteiro(r["te"]), Saida: inteiro(r["tsa"]),
			CacheLeitura: inteiro(r["tcl"]), CacheEscrita: inteiro(r["tce"])},
		Estimar: genai.Uso{Entrada: inteiro(r["ee"]), Saida: inteiro(r["es"]),
			CacheLeitura: inteiro(r["ecl"]), CacheEscrita: inteiro(r["ece"])},
		CustoInformado: numero(r["ci"]), ComCustoInformado: inteiro(r["cci"]),
	}
}

// custo é o resultado da conta para um grupo de chamadas.
type custo struct {
	USD       float64
	Calculado bool  // algum valor entrou (estimado ou informado)
	SemPreco  int64 // chamadas com tokens cujo modelo não tem preço
	SemTokens int64 // chamadas sem tokens informados e sem custo informado
	Preco     *genai.Preco
}

func (c custo) parcial() bool { return c.SemPreco > 0 || c.SemTokens > 0 }

// somar acumula outro resultado neste.
func (c *custo) somar(o custo) {
	c.USD += o.USD
	c.Calculado = c.Calculado || o.Calculado
	c.SemPreco += o.SemPreco
	c.SemTokens += o.SemTokens
}

// usdOuNulo devolve nil quando nada pôde ser calculado: zero seria afirmar que não
// custou nada.
func (c custo) usdOuNulo() *float64 {
	if !c.Calculado {
		return nil
	}
	v := c.USD
	return &v
}

// calcular faz a conta de uma linha: custo informado + estimativa pelo preço vigente
// em l.Quando. Chamada com tokens e sem preço é contada (SemPreco), nunca vira zero.
func calcular(t *genai.Tabela, l linhaUso) custo {
	c := custo{USD: l.CustoInformado, Calculado: l.ComCustoInformado > 0, SemTokens: l.SemTokens}
	if l.Estimaveis == 0 {
		return c
	}
	p, ok := t.Achar(l.Provedor, l.Modelo, l.Quando)
	if !ok {
		c.SemPreco = l.Estimaveis
		return c
	}
	c.USD += genai.Custo(l.Estimar, p)
	c.Calculado = true
	c.Preco = &p
	return c
}

// Totais de tokens e de chamadas de um conjunto de linhas.
type totaisUso struct {
	Chamadas, Erros, ComTokens int64
	Uso                        genai.Uso
	CustoInformado             float64
	Custo                      custo
}

func (t *totaisUso) somar(l linhaUso, c custo) {
	t.Chamadas += l.Chamadas
	t.Erros += l.Erros
	t.ComTokens += l.ComTokens
	t.Uso.Entrada += l.Uso.Entrada
	t.Uso.Saida += l.Uso.Saida
	t.Uso.CacheLeitura += l.Uso.CacheLeitura
	t.Uso.CacheEscrita += l.Uso.CacheEscrita
	t.CustoInformado += l.CustoInformado
	t.Custo.somar(c)
}

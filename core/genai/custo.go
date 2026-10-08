package genai

import (
	"sort"
	"strings"
	"time"
)

// Preco é o preço de um modelo, em dólar por 1 milhão de tokens, a partir de uma data.
// Modelo aceita prefixo com "*" no fim ("gpt-4o*" cobre "gpt-4o-2024-08-06"): os
// provedores devolvem o nome com a data da versão, e a tabela não deveria precisar de
// uma linha por versão.
type Preco struct {
	Provedor     string // "" = qualquer provedor
	Modelo       string
	Entrada      float64
	Saida        float64
	CacheLeitura *float64 // nil = cobra como entrada (estimativa por cima, nunca por baixo)
	CacheEscrita *float64 // nil = cobra como entrada
	VigenteDesde time.Time
	Origem       string // "referencia-AAAA-MM" ou "manual"
}

// Uso é o que foi consumido. Mesmo formato numa chamada só ou num agregado: o custo é
// linear nos tokens, então somar antes ou depois dá o mesmo resultado.
type Uso struct {
	Entrada      int64 // inclui cache (ver normalizarCache)
	Saida        int64
	CacheLeitura int64
	CacheEscrita int64
}

const porMilhao = 1_000_000.0

// Custo devolve o custo em dólar do uso pelo preço dado.
func Custo(u Uso, p Preco) float64 {
	cacheLeitura := p.Entrada
	if p.CacheLeitura != nil {
		cacheLeitura = *p.CacheLeitura
	}
	cacheEscrita := p.Entrada
	if p.CacheEscrita != nil {
		cacheEscrita = *p.CacheEscrita
	}
	novos := u.Entrada - u.CacheLeitura - u.CacheEscrita
	if novos < 0 {
		novos = 0
	}
	return (float64(novos)*p.Entrada +
		float64(u.CacheLeitura)*cacheLeitura +
		float64(u.CacheEscrita)*cacheEscrita +
		float64(u.Saida)*p.Saida) / porMilhao
}

// Tabela acha o preço vigente de um modelo numa data.
type Tabela struct {
	precos []Preco // ordenados por VigenteDesde decrescente
}

// NovaTabela monta a tabela a partir das linhas cadastradas.
func NovaTabela(precos []Preco) *Tabela {
	ps := make([]Preco, len(precos))
	copy(ps, precos)
	for i := range ps {
		ps[i].Modelo = strings.ToLower(strings.TrimSpace(ps[i].Modelo))
		ps[i].Provedor = strings.ToLower(strings.TrimSpace(ps[i].Provedor))
	}
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].VigenteDesde.After(ps[j].VigenteDesde) })
	return &Tabela{precos: ps}
}

// Achar devolve o preço vigente em `quando` para o modelo. Entre as linhas que casam,
// vence a do mesmo provedor; depois, o padrão mais específico (exato antes de
// prefixo, prefixo longo antes de curto). "gpt-4o-mini-2024-07-18" casa com
// "gpt-4o-mini*" e não com "gpt-4o*", embora os dois sejam prefixos dele.
func (t *Tabela) Achar(provedor, modelo string, quando time.Time) (Preco, bool) {
	if t == nil {
		return Preco{}, false
	}
	provedor = strings.ToLower(strings.TrimSpace(provedor))
	modelo = strings.ToLower(strings.TrimSpace(modelo))
	if modelo == "" {
		return Preco{}, false
	}
	melhor, melhorNota := Preco{}, -1
	for _, p := range t.precos {
		if p.VigenteDesde.After(quando) {
			continue
		}
		nota := especificidade(p.Modelo, modelo)
		if nota < 0 {
			continue
		}
		switch {
		case p.Provedor != "" && p.Provedor == provedor:
			nota += 10_000
		case p.Provedor != "" && provedor != "" && !provedorCompativel(p.Provedor, provedor):
			continue
		}
		// Ordenado por vigência decrescente: a primeira linha de cada nota é a mais
		// recente já vigente, então só uma nota MAIOR substitui.
		if nota > melhorNota {
			melhor, melhorNota = p, nota
		}
	}
	return melhor, melhorNota >= 0
}

// especificidade: -1 se o padrão não casa; senão, quanto maior, mais específico.
func especificidade(padrao, modelo string) int {
	if prefixo, ok := strings.CutSuffix(padrao, "*"); ok {
		if strings.HasPrefix(modelo, prefixo) {
			return len(prefixo)
		}
		return -1
	}
	if padrao == modelo {
		return 5_000 // exato vence qualquer prefixo
	}
	return -1
}

// provedorCompativel aceita os nomes de nuvem que revendem o mesmo modelo: o
// "gpt-4o" servido por "azure.ai.openai" usa a linha de "openai" quando não há uma
// própria. Sem isto, trocar de provedor fazia todo custo virar "sem preço".
func provedorCompativel(tabela, recebido string) bool {
	return strings.Contains(recebido, tabela) || strings.Contains(tabela, recebido)
}

// Package sugestao propõe um mapeamento sem IA (ARQUITETURA D2): compara nomes normalizados
// (maiúsculas, acentos, prefixos como TB_, plural), sinônimos pt/en, compatibilidade
// de tipos e chaves. O usuário revisa; o MCP pode refinar por cima.
package sugestao

import (
	"sort"
	"strings"
	"unicode"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
	"github.com/eduardorarruda/revoada/core/migracao/modelo"
	"github.com/eduardorarruda/revoada/core/migracao/plano"
	"golang.org/x/text/unicode/norm"
)

const (
	limiarTabela = 0.62 // abaixo disso, melhor criar a tabela no destino do que errar o par
	limiarColuna = 0.60
)

// sinonimos agrupa nomes que querem dizer a mesma coisa em ERPs brasileiros e em
// schemas em inglês. Cada grupo vira a mesma "raiz".
var sinonimos = [][]string{
	{"id", "codigo", "cod", "cd", "code"},
	{"nome", "name", "nm", "razao", "razaosocial"},
	{"descricao", "description", "desc", "ds", "descr"},
	{"cliente", "customer", "client", "cli"},
	{"fornecedor", "supplier", "vendor", "forn"},
	{"produto", "product", "item", "prod"},
	{"pedido", "order", "ped"},
	{"usuario", "user", "usr"},
	{"telefone", "phone", "fone", "tel"},
	{"email", "mail", "eemail"},
	{"endereco", "address", "end", "logradouro"},
	{"cidade", "city", "municipio"},
	{"estado", "state", "uf"},
	{"cep", "zipcode", "zip", "postalcode"},
	{"cpf", "cnpj", "cpfcnpj", "documento", "document", "taxid"},
	{"preco", "price", "valor", "value", "vlr", "vl"},
	{"quantidade", "quantity", "qtd", "qtde", "qty"},
	{"datacadastro", "dtcadastro", "createdat", "criadoem", "datainclusao", "dtinclusao"},
	{"dataalteracao", "dtalteracao", "updatedat", "atualizadoem"},
	{"ativo", "active", "status", "situacao"},
	{"observacao", "obs", "notes", "note", "observacoes"},
}

var raiz = func() map[string]string {
	m := map[string]string{}
	for _, g := range sinonimos {
		for _, s := range g {
			m[s] = g[0]
		}
	}
	return m
}()

// Normalizar tira acento, separadores, caixa e prefixos/sufixos comuns.
func Normalizar(nome string) string {
	s := strings.ToLower(norm.NFD.String(nome))
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if r < unicode.MaxASCII {
				b.WriteRune(r)
			}
		}
	}
	n := b.String()
	for _, p := range []string{"tb", "tab", "tbl", "t"} {
		if strings.HasPrefix(n, p) && len(n) > len(p)+3 && strings.HasPrefix(strings.ToLower(nome), p+"_") {
			n = n[len(p):]
			break
		}
	}
	if r, ok := raiz[n]; ok {
		return r
	}
	// colunas de chave estrangeira: customer_id, id_cliente, codcliente, clienteid → cliente
	for _, afixo := range []string{"id", "cod", "cd"} {
		if r, ok := raiz[strings.TrimSuffix(n, afixo)]; ok && strings.HasSuffix(n, afixo) && len(n) > len(afixo)+2 {
			return r
		}
		if r, ok := raiz[strings.TrimPrefix(n, afixo)]; ok && strings.HasPrefix(n, afixo) && len(n) > len(afixo)+2 {
			return r
		}
	}
	// plural simples (clientes → cliente, pedidos → pedido)
	if strings.HasSuffix(n, "s") && len(n) > 4 {
		if r, ok := raiz[n[:len(n)-1]]; ok {
			return r
		}
		return n[:len(n)-1]
	}
	return n
}

// Similaridade em [0,1] entre dois nomes (1 = mesma coisa).
func Similaridade(a, b string) float64 {
	na, nb := Normalizar(a), Normalizar(b)
	if na == "" || nb == "" {
		return 0
	}
	if na == nb {
		return 1
	}
	d := levenshtein(na, nb)
	maior := max(len(na), len(nb))
	s := 1 - float64(d)/float64(maior)
	// um contém o outro (dtcadastro / datacadastro) ajuda
	if strings.Contains(na, nb) || strings.Contains(nb, na) {
		s = max(s, 0.75)
	}
	return s
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			custo := 1
			if ra[i-1] == rb[j-1] {
				custo = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+custo)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// Sugerir monta um mapeamento inicial completo.
func Sugerir(origem, destino esquema.Esquema) modelo.Mapeamento {
	ordem, _ := plano.OrdenarPorFK(origem.Tabelas)
	posicao := map[string]int{}
	for i, n := range ordem {
		posicao[n] = i + 1
	}
	usadas := map[string]bool{}
	var m modelo.Mapeamento
	for _, to := range origem.Tabelas {
		mt := modelo.MapTabela{TabelaOrigem: to.Nome, Ordem: posicao[to.Nome], Origem: modelo.OrigemHeuristica}
		if len(to.ChavePrimaria) == 1 {
			mt.ColunaLote = to.ChavePrimaria[0]
		}
		td, conf := melhorTabela(to, destino, usadas)
		if td == nil {
			mt.Acao, mt.TabelaDestino, mt.Confianca = modelo.CriarNoDestino, nomeDestino(to.Nome, destino.Motor), 1
			for _, c := range to.Colunas {
				mt.Colunas = append(mt.Colunas, modelo.MapColuna{
					ColunaOrigem: c.Nome, ColunaDestino: nomeDestino(c.Nome, destino.Motor), Transformacao: modelo.Nenhuma,
					Confianca: 1, Origem: modelo.OrigemHeuristica,
				})
			}
		} else {
			usadas[td.Nome] = true
			mt.Acao, mt.TabelaDestino, mt.Confianca = modelo.Copiar, td.Nome, conf
			mt.Colunas = sugerirColunas(&to, td)
		}
		m.Tabelas = append(m.Tabelas, mt)
	}
	sort.SliceStable(m.Tabelas, func(i, j int) bool { return m.Tabelas[i].Ordem < m.Tabelas[j].Ordem })
	return m
}

// nomeDestino: no PostgreSQL, nome em minúsculas evita aspas para sempre.
func nomeDestino(nome, motor string) string {
	if motor == "postgres" {
		return strings.ToLower(nome)
	}
	return nome
}

func melhorTabela(to esquema.Tabela, destino esquema.Esquema, usadas map[string]bool) (*esquema.Tabela, float64) {
	var melhor *esquema.Tabela
	var nota float64
	for i := range destino.Tabelas {
		td := &destino.Tabelas[i]
		if usadas[td.Nome] {
			continue
		}
		// nome pesa mais; colunas em comum confirmam (CLIENTES × clientes com 80% das colunas)
		n := 0.7*Similaridade(to.Nome, td.Nome) + 0.3*colunasEmComum(&to, td)
		if n > nota {
			melhor, nota = td, n
		}
	}
	if nota < limiarTabela {
		return nil, 0
	}
	return melhor, nota
}

func colunasEmComum(a, b *esquema.Tabela) float64 {
	if len(a.Colunas) == 0 {
		return 0
	}
	n := 0
	for _, ca := range a.Colunas {
		for _, cb := range b.Colunas {
			if Similaridade(ca.Nome, cb.Nome) >= 0.9 {
				n++
				break
			}
		}
	}
	return float64(n) / float64(len(a.Colunas))
}

type candidato struct {
	origem, destino int
	nota            float64
}

// sugerirColunas faz o pareamento guloso pelas melhores notas (cada coluna de
// destino recebe no máximo uma de origem).
func sugerirColunas(to, td *esquema.Tabela) []modelo.MapColuna {
	var cs []candidato
	for i, co := range to.Colunas {
		for j, cd := range td.Colunas {
			n := Similaridade(co.Nome, cd.Nome)
			comp := esquema.Comparar(co, cd)
			switch {
			case comp.PrecisaTransformar:
				n *= 0.5
			case comp.Alerta != "":
				n *= 0.9
			}
			if pk(to, co.Nome) && pk(td, cd.Nome) {
				n = max(n, 0.8) // chave primária com chave primária
			}
			if n >= limiarColuna {
				cs = append(cs, candidato{i, j, n})
			}
		}
	}
	sort.SliceStable(cs, func(a, b int) bool { return cs[a].nota > cs[b].nota })
	usadaO, usadaD := map[int]bool{}, map[int]bool{}
	var out []modelo.MapColuna
	for _, c := range cs {
		if usadaO[c.origem] || usadaD[c.destino] {
			continue
		}
		usadaO[c.origem], usadaD[c.destino] = true, true
		co, cd := to.Colunas[c.origem], td.Colunas[c.destino]
		mc := modelo.MapColuna{ColunaOrigem: co.Nome, ColunaDestino: cd.Nome, Transformacao: modelo.Nenhuma,
			Confianca: round2(c.nota), Origem: modelo.OrigemHeuristica}
		comp := esquema.Comparar(co, cd)
		if comp.PrecisaTransformar {
			mc.Transformacao = modelo.ConverterTipo
		}
		if co.Charset != "" && cd.Charset != "" && !strings.EqualFold(co.Charset, cd.Charset) &&
			(co.Tipo == esquema.Texto || co.Tipo == esquema.TextoLongo) {
			mc.Transformacao = modelo.Charset
			mc.Parametros = map[string]string{"de": co.Charset, "para": cd.Charset}
		}
		mc.Alerta = comp.Alerta
		out = append(out, mc)
	}
	sort.SliceStable(out, func(a, b int) bool { return indice(to, out[a].ColunaOrigem) < indice(to, out[b].ColunaOrigem) })
	return out
}

func pk(t *esquema.Tabela, col string) bool {
	return len(t.ChavePrimaria) == 1 && t.ChavePrimaria[0] == col
}

func indice(t *esquema.Tabela, col string) int {
	for i, c := range t.Colunas {
		if c.Nome == col {
			return i
		}
	}
	return len(t.Colunas)
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

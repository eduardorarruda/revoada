// Package plano decide a ordem e os passos de uma migração (ARQUITETURA §9.4).
package plano

import (
	"sort"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
)

// OrdenarPorFK devolve a ordem de carga: toda tabela vem DEPOIS das que ela
// referencia (senão a chave estrangeira recusa a linha). Ciclos (A→B→A) não têm
// ordem possível; voltam separados para o plano carregar sem as FKs e religá-las
// no fim. Referência à própria tabela (hierarquias) não é ciclo para a ordem.
//
// Busca em profundidade recursiva; a saída é determinística (nomes em ordem
// alfabética como desempate) para o plano e o hash não mudarem à toa.
func OrdenarPorFK(tabelas []esquema.Tabela) (ordem []string, ciclos [][]string) {
	deps := map[string][]string{}
	nomes := make([]string, 0, len(tabelas))
	for _, t := range tabelas {
		nomes = append(nomes, t.Nome)
		for _, fk := range t.Estrangeiras {
			if fk.TabelaRef != t.Nome {
				deps[t.Nome] = append(deps[t.Nome], fk.TabelaRef)
			}
		}
	}
	sort.Strings(nomes)
	existe := map[string]bool{}
	for _, n := range nomes {
		existe[n] = true
		sort.Strings(deps[n])
	}

	const (
		branco = iota // não visitada
		cinza         // na pilha da busca atual
		preto         // resolvida
	)
	cor := map[string]int{}
	var pilha []string
	var visitar func(n string)
	visitar = func(n string) {
		cor[n] = cinza
		pilha = append(pilha, n)
		for _, d := range deps[n] {
			if !existe[d] {
				continue // referência a tabela fora do escopo da migração
			}
			switch cor[d] {
			case branco:
				visitar(d)
			case cinza:
				ciclos = append(ciclos, cicloAte(pilha, d))
			}
		}
		pilha = pilha[:len(pilha)-1]
		cor[n] = preto
		ordem = append(ordem, n)
	}
	for _, n := range nomes {
		if cor[n] == branco {
			visitar(n)
		}
	}
	return ordem, ciclos
}

func cicloAte(pilha []string, inicio string) []string {
	for i := len(pilha) - 1; i >= 0; i-- {
		if pilha[i] == inicio {
			return append([]string(nil), pilha[i:]...)
		}
	}
	return []string{inicio}
}

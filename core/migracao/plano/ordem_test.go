package plano

import (
	"reflect"
	"testing"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
)

func fk(ref string) esquema.ChaveEstrangeira { return esquema.ChaveEstrangeira{TabelaRef: ref} }

func TestOrdenarPorFKRespeitaDependencias(t *testing.T) {
	tabs := []esquema.Tabela{
		{Nome: "ITENS", Estrangeiras: []esquema.ChaveEstrangeira{fk("PEDIDOS"), fk("PRODUTOS")}},
		{Nome: "PEDIDOS", Estrangeiras: []esquema.ChaveEstrangeira{fk("CLIENTES")}},
		{Nome: "CLIENTES"},
		{Nome: "PRODUTOS", Estrangeiras: []esquema.ChaveEstrangeira{fk("PRODUTOS")}}, // auto-referência
		{Nome: "LOG", Estrangeiras: []esquema.ChaveEstrangeira{fk("FORA_DO_ESCOPO")}},
	}
	ordem, ciclos := OrdenarPorFK(tabs)
	if len(ciclos) != 0 {
		t.Fatalf("não há ciclo: %v", ciclos)
	}
	pos := map[string]int{}
	for i, n := range ordem {
		pos[n] = i
	}
	for _, par := range [][2]string{{"CLIENTES", "PEDIDOS"}, {"PEDIDOS", "ITENS"}, {"PRODUTOS", "ITENS"}} {
		if pos[par[0]] > pos[par[1]] {
			t.Fatalf("%s deveria vir antes de %s: %v", par[0], par[1], ordem)
		}
	}
	if len(ordem) != 5 {
		t.Fatalf("todas as tabelas: %v", ordem)
	}
	// determinística
	ordem2, _ := OrdenarPorFK(tabs)
	if !reflect.DeepEqual(ordem, ordem2) {
		t.Fatal("ordem mudou entre execuções")
	}
}

func TestOrdenarPorFKDetectaCiclo(t *testing.T) {
	tabs := []esquema.Tabela{
		{Nome: "A", Estrangeiras: []esquema.ChaveEstrangeira{fk("B")}},
		{Nome: "B", Estrangeiras: []esquema.ChaveEstrangeira{fk("A")}},
	}
	ordem, ciclos := OrdenarPorFK(tabs)
	if len(ciclos) != 1 || len(ciclos[0]) != 2 || len(ordem) != 2 {
		t.Fatalf("ordem %v ciclos %v", ordem, ciclos)
	}
}

func TestOrdemDestinoConsideraFKsReligadas(t *testing.T) {
	passos := []Passo{
		{Origem: esquema.Tabela{Nome: "ITENS", Estrangeiras: []esquema.ChaveEstrangeira{{Nome: "FK", TabelaRef: "PRODUTOS"}}},
			Destino: esquema.Tabela{Nome: "a_itens"}, Criar: true},
		{Origem: esquema.Tabela{Nome: "PRODUTOS"}, Destino: esquema.Tabela{Nome: "z_produtos"}, Criar: true},
	}
	o := OrdemDestino(passos)
	if o[0].Destino.Nome != "z_produtos" || o[1].Destino.Nome != "a_itens" {
		t.Fatalf("o pai vem antes da filha mesmo fora da ordem alfabética: %v, %v", o[0].Destino.Nome, o[1].Destino.Nome)
	}
}

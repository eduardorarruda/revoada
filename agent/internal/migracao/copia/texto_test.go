package copia

import (
	"strings"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
	"github.com/eduardorarruda/revoada/core/migracao/modelo"
	"github.com/eduardorarruda/revoada/core/migracao/plano"
	"github.com/eduardorarruda/revoada/core/migracao/transformar"
)

func txt(s string) *string { return &s }

// O texto que o Firebird imprime contra o que o PostgreSQL imprime: a normalização
// mínima aceita só o que é a mesma coisa escrita de outro jeito.
func TestCompararTexto(t *testing.T) {
	casos := []struct {
		nome    string
		ct      colunaTexto
		origem  *string
		destino *string
		igual   bool
	}{
		{"inteiro", colunaTexto{fam: famInteiro}, txt("42"), txt("42"), true},
		{"inteiro diferente", colunaTexto{fam: famInteiro}, txt("42"), txt("43"), false},
		{"numeric: escala diferente", colunaTexto{fam: famDecimal, escala: 2}, txt("12.5000"), txt("12.50"), true},
		{"numeric: arredonda como numeric(p,s)", colunaTexto{fam: famDecimal, escala: 2}, txt("12.3450"), txt("12.35"), true},
		{"numeric: um centavo", colunaTexto{fam: famDecimal, escala: 2}, txt("12.34"), txt("12.35"), false},
		{"double: 16 algarismos do Firebird", colunaTexto{fam: famFlutuante}, txt("0.3333333333333333"), txt("0.333333333333333315"), true},
		{"double: expoente do Firebird", colunaTexto{fam: famFlutuante}, txt("1.000000000000000e+300"), txt("1e+300"), true},
		{"double diferente", colunaTexto{fam: famFlutuante}, txt("0.3333333333333333"), txt("0.3333334"), false},
		{"real: float32", colunaTexto{fam: famFlutuante, real: true}, txt("0.1000000000000000"), txt("0.1"), true},
		{"real diferente", colunaTexto{fam: famFlutuante, real: true}, txt("0.1"), txt("0.2"), false},
		{"date", colunaTexto{fam: famData}, txt("2005-10-16"), txt("2005-10-16"), true},
		{"date um dia antes (o bug do horário de verão)", colunaTexto{fam: famData}, txt("2005-10-16"), txt("2005-10-15"), false},
		{"timestamp → date: só a data", colunaTexto{fam: famData}, txt("2024-01-02 10:00:00.0000"), txt("2024-01-02"), true},
		{"timestamp: 1/10000 s contra µs", colunaTexto{fam: famDataHora, casas: 6}, txt("2005-10-16 00:30:00.1234"), txt("2005-10-16 00:30:00.123400"), true},
		{"timestamp uma hora antes", colunaTexto{fam: famDataHora, casas: 6}, txt("2005-10-16 00:30:00.1234"), txt("2005-10-15 23:30:00.123400"), false},
		{"timestamp(0) arredonda a origem", colunaTexto{fam: famDataHora, casas: 0}, txt("2024-01-02 10:00:00.5678"), txt("2024-01-02 10:00:01.000000"), true},
		{"date → timestamp", colunaTexto{fam: famDataHora, casas: 6}, txt("2024-01-02"), txt("2024-01-02 00:00:00.000000"), true},
		{"time", colunaTexto{fam: famHora, casas: 6}, txt("23:59:59.9999"), txt("23:59:59.999900"), true},
		{"time diferente", colunaTexto{fam: famHora, casas: 6}, txt("08:00:00"), txt("09:00:00.000000"), false},
		{"texto: espaço à direita conta em varchar", colunaTexto{fam: famTexto}, txt("Ana "), txt("Ana"), false},
		{"char(n): preenchimento não conta", colunaTexto{fam: famTexto, fixo: true}, txt("AB  "), txt("AB"), true},
		{"texto com acento", colunaTexto{fam: famTexto}, txt("ação"), txt("aÃ§Ã£o"), false},
		{"texto longo: tamanho e começo", colunaTexto{fam: famTexto, longo: true}, txt("4:ação"), txt("4:ação"), true},
		{"texto longo: tamanho diferente", colunaTexto{fam: famTexto, longo: true}, txt("9000:abc"), txt("8999:abc"), false},
		{"json", colunaTexto{fam: famJSON}, txt(`{"b":1,"a":2}`), txt(`{"a": 2, "b": 1}`), true},
		{"uuid", colunaTexto{fam: famUUID}, txt("A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11"), txt("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"), true},
		{"booleano", colunaTexto{fam: famBooleano}, txt("t"), txt("true"), true},
		{"binário: tamanho", colunaTexto{fam: famBinario}, txt("2"), txt("2"), true},
		{"nulo == nulo", colunaTexto{fam: famTexto}, nil, nil, true},
		{"nulo != vazio", colunaTexto{fam: famTexto}, nil, txt(""), false},
		{"vazio != nulo", colunaTexto{fam: famInteiro}, txt("0"), nil, false},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			if igual, _ := c.ct.comparar(c.origem, c.destino); igual != c.igual {
				t.Fatalf("igual=%v, queria %v", igual, c.igual)
			}
		})
	}
	if _, parcial := (colunaTexto{fam: famTexto, longo: true}).comparar(txt("9000:abc"), txt("9000:abc")); !parcial {
		t.Error("texto maior que o começo comparado é conferência parcial")
	}
}

// Quem entra na dupla conferência, como cada banco renderiza, e quem fica de fora
// (sempre com motivo).
func TestColunasTexto(t *testing.T) {
	origem := esquema.Tabela{Nome: "T", ChavePrimaria: []string{"ID"}, Colunas: []esquema.Coluna{
		{Nome: "ID", Tipo: esquema.Inteiro, TipoNativo: "INTEGER"},
		{Nome: "DIA", Tipo: esquema.Data, TipoNativo: "DATE"},
		{Nome: "MOMENTO", Tipo: esquema.DataHora, TipoNativo: "TIMESTAMP"},
		{Nome: "NOME", Tipo: esquema.Texto, TipoNativo: "VARCHAR(20)"},
		{Nome: "ATIVO", Tipo: esquema.Inteiro, TipoNativo: "SMALLINT"},
		{Nome: "OBS", Tipo: esquema.TextoLongo, TipoNativo: "BLOB SUB_TYPE TEXT"},
	}}
	dest := []esquema.Coluna{
		{Nome: "id", Tipo: esquema.Inteiro, TipoNativo: "integer"},
		{Nome: "dia", Tipo: esquema.Data, TipoNativo: "date"},
		{Nome: "momento", Tipo: esquema.DataHora, TipoNativo: "timestamp with time zone"},
		{Nome: "nome", Tipo: esquema.Texto, TipoNativo: "character varying(20)"},
		{Nome: "ativo", Tipo: esquema.Booleano, TipoNativo: "boolean"},
		{Nome: "obs", Tipo: esquema.TextoLongo, TipoNativo: "text"},
	}
	p := plano.Passo{Origem: origem, Destino: esquema.Tabela{Nome: "t", Colunas: dest, ChavePrimaria: []string{"id"}},
		Map: modelo.MapTabela{ColunaLote: "ID", Colunas: []modelo.MapColuna{
			{ColunaOrigem: "ID", ColunaDestino: "id"}, {ColunaOrigem: "DIA", ColunaDestino: "dia"},
			{ColunaOrigem: "MOMENTO", ColunaDestino: "momento"},
			{ColunaOrigem: "NOME", ColunaDestino: "nome", Transformacao: modelo.Aparar},
			{ColunaOrigem: "ATIVO", ColunaDestino: "ativo"}, {ColunaOrigem: "OBS", ColunaDestino: "obs"},
		}}}
	conf := novaConferencia(p, dest, nil)
	fb := dialeto{motor: "firebird"}
	pg := dialeto{motor: "postgres", esquema: "public"}
	cs, fora := colunasTexto(p, fb, pg, conf, dest, nil, "America/Sao_Paulo")
	porNome := map[string]colunaTexto{}
	for _, c := range cs {
		porNome[c.nome] = c
	}
	if len(cs) != 4 || len(fora) != 2 {
		t.Fatalf("entram id, dia, momento, obs; ficam de fora nome (transformação) e ativo (tipos): %+v / %+v", cs, fora)
	}
	for _, f := range fora {
		if f.Motivo == "" {
			t.Fatalf("%s fora sem motivo", f.Coluna)
		}
	}
	if !strings.Contains(fora[0].Motivo, "aparar") || !strings.Contains(fora[1].Motivo, "tipos diferentes") {
		t.Fatalf("motivos: %+v", fora)
	}
	if porNome["dia"].exprOrig != `CAST("DIA" AS VARCHAR(64))` || porNome["dia"].exprDest != `to_char("dia", 'YYYY-MM-DD')` {
		t.Fatalf("dia: %+v", porNome["dia"])
	}
	if !strings.Contains(porNome["momento"].exprDest, `AT TIME ZONE 'America/Sao_Paulo'`) {
		t.Fatalf("timestamptz é renderizado como relógio no fuso de origem: %s", porNome["momento"].exprDest)
	}
	if !porNome["obs"].longo || !strings.Contains(porNome["obs"].exprOrig, "SUBSTRING") || !strings.Contains(porNome["obs"].exprDest, "substr") {
		t.Fatalf("BLOB de texto: tamanho e começo: %+v", porNome["obs"])
	}
	// sem nome de fuso, o timestamptz não entra (com motivo)
	_, fora = colunasTexto(p, fb, pg, conf, dest, nil, "")
	if !contemFora(fora, "momento") {
		t.Fatalf("sem fuso, momento fica de fora: %+v", fora)
	}
}

func TestNomeDoFuso(t *testing.T) {
	sp, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Skip("sem base de fusos")
	}
	transformar.FusoOrigem = sp
	t.Cleanup(func() { transformar.FusoOrigem = nil })
	if n := nomeDoFuso(); n != "America/Sao_Paulo" {
		t.Fatalf("fuso: %q", n)
	}
	transformar.FusoOrigem = time.FixedZone("'; DROP TABLE x; --", 0)
	if n := nomeDoFuso(); n != "" {
		t.Fatalf("nome de fuso estranho não pode ir para o SQL: %q", n)
	}
}

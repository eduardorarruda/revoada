package copia

import (
	"math"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
	"github.com/eduardorarruda/revoada/core/migracao/modelo"
	"github.com/eduardorarruda/revoada/core/migracao/plano"
	"github.com/eduardorarruda/revoada/core/migracao/transformar"
)

// Cada caso: o valor que o motor manda gravar (saída de Ajustar) e o que o pgx
// devolve ao reler a coluna. igual=true: o PostgreSQL guardou o esperado; false: algo
// mudou no caminho e a conferência TEM de acusar.
func TestCanonicoGravadoVersusRelido(t *testing.T) {
	sp := time.FixedZone("BRT", -3*3600)
	casos := []struct {
		nome    string
		col     esquema.Coluna
		casas   map[string]int
		gravado any
		relido  any
		igual   bool
	}{
		// inteiros
		{"int4", col(esquema.Inteiro, "integer"), nil, int64(42), int64(42), true},
		{"int diferente", col(esquema.Inteiro, "integer"), nil, int64(42), int64(43), false},
		{"int128 em numeric(39,0)", col(esquema.Inteiro, "numeric(39,0)"), nil, int64(-7), "-7", true},
		{"int não é float", col(esquema.Inteiro, "integer"), nil, int64(1), float64(1), false},

		// numeric
		{"12.5 == 12.50", dec(15, 2), nil, "12.5", "12.50", true},
		{"1.5 != 1.25", dec(15, 2), nil, "1.5", "1.25", false},
		{"arredonda como o PG (metade p/ longe do zero)", dec(15, 2), nil, "12.345", "12.35", true},
		{"negativo arredonda p/ longe do zero", dec(15, 2), nil, "-12.345", "-12.35", true},
		{"arredondamento errado acusa", dec(15, 2), nil, "12.345", "12.34", false},
		{"truncar centavos acusa", dec(15, 2), nil, "10.99", "10.00", false},
		{"numeric sem limite não arredonda", col(esquema.Decimal, "numeric"), nil, "0.123456789", "0.123456789", true},
		{"numeric sem limite: casas perdidas acusam", col(esquema.Decimal, "numeric"), nil, "0.123456789", "0.1234568", false},
		{"inteiro em numeric", dec(10, 2), nil, "7", "7.00", true},
		{"float que virou decimal", dec(15, 4), nil, float64(0.1), "0.1000", true},
		{"NaN só bate com NaN", col(esquema.Decimal, "numeric"), nil, "NaN", "NaN", true},

		// ponto flutuante
		{"double exato", col(esquema.Flutuante, "double precision"), nil, 0.1, 0.1, true},
		{"double perdeu precisão", col(esquema.Flutuante, "double precision"), nil, 0.1, float64(float32(0.1)), false},
		{"real guarda float32", col(esquema.Flutuante, "real"), nil, 0.1, float64(float32(0.1)), true},
		{"real com valor diferente", col(esquema.Flutuante, "real"), nil, 0.1, float64(float32(0.2)), false},
		{"NaN", col(esquema.Flutuante, "double precision"), nil, math.NaN(), math.NaN(), true},

		// texto
		{"texto igual", col(esquema.Texto, "character varying(40)"), nil, "abc", "abc", true},
		{"abc != abd", col(esquema.Texto, "character varying(40)"), nil, "abc", "abd", false},
		{"acento != mojibake", col(esquema.Texto, "character varying(40)"), nil, "ação", "aÃ§Ã£o", false},
		{"truncado acusa", col(esquema.Texto, "text"), nil, "Padaria Pão Quente", "Padaria Pão", false},
		{"varchar: espaço à direita conta", col(esquema.Texto, "character varying(10)"), nil, "ab ", "ab", false},
		{"char(n): o PG completa com espaços", col(esquema.Texto, "character(8)"), nil, "AB12", "AB12    ", true},
		{"char(n) com origem já completa", col(esquema.Texto, "character(8)"), nil, "AB12  ", "AB12    ", true},
		{"char(n): espaço à esquerda conta", col(esquema.Texto, "character(8)"), nil, "AB12", " AB12   ", false},
		{"texto longo de []byte", col(esquema.TextoLongo, "text"), nil, "linha\r\n2", []byte("linha\r\n2"), true},
		{"maiúscula conta", col(esquema.Texto, "character varying(10)"), nil, "Ana", "ana", false},

		// data
		{"date", col(esquema.Data, "date"), nil, time.Date(2024, 3, 9, 0, 0, 0, 0, time.UTC), time.Date(2024, 3, 9, 0, 0, 0, 0, time.UTC), true},
		{"date um dia antes (fuso)", col(esquema.Data, "date"), nil, time.Date(2024, 3, 9, 0, 0, 0, 0, time.UTC), time.Date(2024, 3, 8, 0, 0, 0, 0, time.UTC), false},

		// timestamp sem fuso: compara o relógio (o pgx descarta o fuso ao gravar)
		{"timestamp: relógio local == relido em UTC", col(esquema.DataHora, "timestamp without time zone"), nil,
			time.Date(2024, 3, 9, 10, 30, 0, 0, sp), time.Date(2024, 3, 9, 10, 30, 0, 0, time.UTC), true},
		{"timestamp deslocado 1h acusa", col(esquema.DataHora, "timestamp without time zone"), nil,
			time.Date(2024, 3, 9, 10, 30, 0, 0, time.UTC), time.Date(2024, 3, 9, 11, 30, 0, 0, time.UTC), false},
		{"timestamp: nanossegundo truncado (PG guarda µs)", col(esquema.DataHora, "timestamp without time zone"), nil,
			time.Date(2024, 3, 9, 10, 30, 0, 123456789, time.UTC), time.Date(2024, 3, 9, 10, 30, 0, 123456000, time.UTC), true},
		{"timestamp: microssegundo perdido acusa", col(esquema.DataHora, "timestamp without time zone"), nil,
			time.Date(2024, 3, 9, 10, 30, 0, 123456000, time.UTC), time.Date(2024, 3, 9, 10, 30, 0, 123000000, time.UTC), false},
		{"timestamp(0) arredonda para o segundo", col(esquema.DataHora, "timestamp without time zone"), map[string]int{"c": 0},
			time.Date(2024, 3, 9, 10, 30, 0, 500000000, time.UTC), time.Date(2024, 3, 9, 10, 30, 1, 0, time.UTC), true},
		{"timestamp(0): 0.4s fica no segundo", col(esquema.DataHora, "timestamp without time zone"), map[string]int{"c": 0},
			time.Date(2024, 3, 9, 10, 30, 0, 400000000, time.UTC), time.Date(2024, 3, 9, 10, 30, 0, 0, time.UTC), true},
		// o PG arredonda os µs desde 2000-01-01 para longe do ZERO: antes de 2000, a
		// metade vai para trás (AdjustTimestampForTypmod) — a regra imita isso
		{"antes de 2000 a metade vai para trás", col(esquema.DataHora, "timestamp without time zone"), map[string]int{"c": 0},
			time.Date(1999, 12, 31, 23, 59, 58, 500000000, time.UTC), time.Date(1999, 12, 31, 23, 59, 58, 0, time.UTC), true},
		{"antes de 2000: o segundo seguinte não bate", col(esquema.DataHora, "timestamp without time zone"), map[string]int{"c": 0},
			time.Date(1999, 12, 31, 23, 59, 58, 500000000, time.UTC), time.Date(1999, 12, 31, 23, 59, 59, 0, time.UTC), false},

		// timestamptz: compara o instante
		{"timestamptz: mesmo instante em fusos diferentes", col(esquema.DataHora, "timestamp with time zone"), nil,
			time.Date(2024, 3, 9, 10, 30, 0, 0, sp), time.Date(2024, 3, 9, 13, 30, 0, 0, time.UTC), true},
		{"timestamptz: relógio igual, instante diferente", col(esquema.DataHora, "timestamp with time zone"), nil,
			time.Date(2024, 3, 9, 10, 30, 0, 0, sp), time.Date(2024, 3, 9, 10, 30, 0, 0, time.UTC), false},

		// hora
		{"time: time.Time gravado, texto relido", col(esquema.Hora, "time without time zone"), nil,
			time.Date(0, 1, 1, 15, 4, 5, 0, time.UTC), "15:04:05", true},
		{"time com fração", col(esquema.Hora, "time without time zone"), nil,
			time.Date(0, 1, 1, 15, 4, 5, 250000000, time.UTC), "15:04:05.25", true},
		{"time diferente", col(esquema.Hora, "time without time zone"), nil,
			time.Date(0, 1, 1, 15, 4, 5, 0, time.UTC), "16:04:05", false},
		{"time(0) arredonda", col(esquema.Hora, "time without time zone"), map[string]int{"c": 0},
			time.Date(0, 1, 1, 15, 4, 5, 600000000, time.UTC), "15:04:06", true},

		// booleano
		{"bool", col(esquema.Booleano, "boolean"), nil, true, true, true},
		{"bool trocado", col(esquema.Booleano, "boolean"), nil, true, false, false},

		// binário
		{"bytea", col(esquema.Binario, "bytea"), nil, []byte{0, 1, 0xff}, []byte{0, 1, 0xff}, true},
		{"bytea com 1 byte diferente", col(esquema.Binario, "bytea"), nil, []byte{0, 1, 0xff}, []byte{0, 1, 0xfe}, false},
		{"bytea cortado", col(esquema.Binario, "bytea"), nil, []byte{0, 1, 0xff}, []byte{0, 1}, false},

		// json / jsonb
		{"jsonb reordena e tira espaços", col(esquema.JSON, "jsonb"), nil, `{"b": 1, "a": [1.50, "x"]}`, []byte(`{"a": [1.50, "x"], "b": 1}`), true},
		{"jsonb: número exato (1.5 == 1.50)", col(esquema.JSON, "jsonb"), nil, `{"v":1.5}`, []byte(`{"v": 1.50}`), true},
		{"jsonb: escape unicode == caractere", col(esquema.JSON, "jsonb"), nil, `{"n":"José"}`, []byte(`{"n": "José"}`), true},
		{"jsonb: valor diferente", col(esquema.JSON, "jsonb"), nil, `{"v":1}`, []byte(`{"v": 2}`), false},
		{"jsonb: ordem de array conta", col(esquema.JSON, "jsonb"), nil, `[1,2]`, []byte(`[2, 1]`), false},

		// uuid
		{"uuid em maiúsculas == minúsculas", col(esquema.UUID, "uuid"), nil, "A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11", "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11", true},
		{"uuid diferente", col(esquema.UUID, "uuid"), nil, "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11", "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a12", false},

		// nulo
		{"nulo == nulo", col(esquema.Texto, "text"), nil, nil, nil, true},
		{"nulo != texto vazio", col(esquema.Texto, "text"), nil, nil, "", false},
		{"nulo != zero", col(esquema.Inteiro, "integer"), nil, nil, int64(0), false},
		{"nulo != texto 'N'", col(esquema.Texto, "text"), nil, nil, "N", false},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			r := novaRegra(c.col, c.casas)
			if !r.comparavel {
				t.Fatalf("a coluna deveria ser comparável: %s", r.motivo)
			}
			a, b := r.canonico(c.gravado), r.canonico(c.relido)
			if (a == b) != c.igual {
				t.Fatalf("gravado %q, relido %q: igual=%v, queria %v", a, b, a == b, c.igual)
			}
		})
	}
}

func col(tipo esquema.TipoLogico, nativo string) esquema.Coluna {
	return esquema.Coluna{Nome: "c", Tipo: tipo, TipoNativo: nativo, Nulavel: true}
}

func dec(p, s int) esquema.Coluna {
	c := col(esquema.Decimal, "numeric")
	c.Precisao, c.Escala = p, s
	return c
}

// O que não tem forma canônica segura fica de fora — com o motivo, nunca em silêncio.
func TestRegraNaoComparavel(t *testing.T) {
	for _, c := range []esquema.Coluna{
		col(esquema.Desconhecido, "interval"),
		col(esquema.Desconhecido, "ARRAY"),
		col(esquema.Decimal, "money"),
		col(esquema.Hora, "time with time zone"),
	} {
		r := novaRegra(c, nil)
		if r.comparavel || r.motivo == "" {
			t.Errorf("%s: deveria ficar de fora com motivo (comparável=%v, motivo=%q)", c.TipoNativo, r.comparavel, r.motivo)
		}
	}
	if !novaRegra(col(esquema.Texto, "character varying(10)"), nil).comparavel || novaRegra(col(esquema.Texto, "character varying(10)"), nil).bpchar {
		t.Error("varchar não é char(n)")
	}
	if !novaRegra(col(esquema.Texto, "character(3)"), nil).bpchar {
		t.Error("character(3) é char(n)")
	}
}

// A conversão de verdade (Ajustar) seguida do que o PG devolveria tem de bater:
// a regra precisa aceitar os tipos que converter produz.
func TestCanonicoAceitaSaidaDeAjustar(t *testing.T) {
	casos := []struct {
		c      esquema.Coluna
		origem any
		relido any
	}{
		{dec(15, 2), "12.345", "12.35"},
		{col(esquema.Inteiro, "integer"), int32(7), int64(7)},
		{col(esquema.Booleano, "boolean"), int16(1), true},
		{col(esquema.Data, "date"), time.Date(2024, 1, 2, 13, 0, 0, 0, time.Local), time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)},
		{col(esquema.UUID, "uuid"), " A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11 ", "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"},
		{col(esquema.Flutuante, "real"), float32(1.1), float64(float32(1.1))},
	}
	for _, c := range casos {
		aj, _, vio := transformar.Ajustar(c.origem, c.c)
		if vio != nil {
			t.Fatalf("%s: %v", c.c.TipoNativo, vio)
		}
		r := novaRegra(c.c, nil)
		if a, b := r.canonico(aj), r.canonico(c.relido); a != b {
			t.Errorf("%s: gravado %q (de %T), relido %q", c.c.TipoNativo, a, aj, b)
		}
	}
}

func TestArredondarRat(t *testing.T) {
	for _, c := range []struct {
		in    string
		casas int
		out   string
	}{
		{"1.005", 2, "1.01"}, {"1.004", 2, "1.00"}, {"-1.005", 2, "-1.01"}, {"2.5", 0, "3"}, {"-2.5", 0, "-3"}, {"0", 2, "0.00"},
	} {
		r, _ := new(big.Rat).SetString(c.in)
		if got := arredondarRat(r, c.casas).FloatString(c.casas); got != c.out {
			t.Errorf("%s com %d casas: %s, queria %s", c.in, c.casas, got, c.out)
		}
	}
}

// ---------------------------------------------------------------- somas

func TestSoma256IndependeDaOrdemETemVaiUm(t *testing.T) {
	var a, b soma256
	ps := [][32]byte{parcela("1", "x"), parcela("2", "y"), parcela("3", "z")}
	for _, p := range ps {
		a.add(p)
	}
	for i := len(ps) - 1; i >= 0; i-- {
		b.add(ps[i])
	}
	if a != b {
		t.Fatal("a soma não pode depender da ordem")
	}
	// vai-um atravessando as palavras de 64 bits
	var c soma256
	var tudo1 [32]byte
	for i := range tudo1 {
		tudo1[i] = 0xff
	}
	um := [32]byte{31: 1}
	c.add(tudo1)
	c.add(um)
	if c != (soma256{}) {
		t.Fatalf("2^256-1 + 1 deveria dar 0 (mod 2^256): %s", c)
	}
	v, err := lerSoma256(a.String())
	if err != nil || v != a {
		t.Fatalf("ida e volta do texto: %v", err)
	}
	if _, err := lerSoma256("xyz"); err == nil {
		t.Fatal("texto inválido deveria dar erro")
	}
}

// A parcela liga valor e linha: trocar valores ENTRE linhas muda a soma.
func TestParcelaLigaValorALinha(t *testing.T) {
	var a, b soma256
	a.add(parcela("1", "Ana"))
	a.add(parcela("2", "Bia"))
	b.add(parcela("1", "Bia"))
	b.add(parcela("2", "Ana"))
	if a == b {
		t.Fatal("trocar os nomes de duas linhas tem de mudar a soma")
	}
}

func passoTeste() (plano.Passo, []esquema.Coluna) {
	dest := []esquema.Coluna{
		{Nome: "id", Tipo: esquema.Inteiro, TipoNativo: "integer"},
		{Nome: "nome", Tipo: esquema.Texto, TipoNativo: "character varying(40)", Nulavel: true},
		{Nome: "criado", Tipo: esquema.DataHora, TipoNativo: "timestamp without time zone", Padrao: "now()"},
		{Nome: "faixa", Tipo: esquema.Desconhecido, TipoNativo: "int4range", Nulavel: true},
	}
	p := plano.Passo{
		Map: modelo.MapTabela{ColunaLote: "ID", Colunas: []modelo.MapColuna{
			{ColunaOrigem: "ID", ColunaDestino: "id"}, {ColunaOrigem: "NOME", ColunaDestino: "nome"},
			{ColunaOrigem: "CRIADO", ColunaDestino: "criado"}, {ColunaOrigem: "FAIXA", ColunaDestino: "faixa"},
		}},
		Destino: esquema.Tabela{Nome: "t", Colunas: dest, ChavePrimaria: []string{"id"}},
	}
	return p, dest
}

func TestSomasConteudoDetectaColunaEVeredito(t *testing.T) {
	p, dest := passoTeste()
	conf := novaConferencia(p, dest, nil)
	if len(conf.chave) != 1 || conf.chave[0] != 0 {
		t.Fatalf("chave da linha: %v", conf.chave)
	}
	agora := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	gravado, relido := novasSomas(len(dest)), novasSomas(len(dest))
	gravado.add(conf, []any{int64(1), "Ana", transformar.UsarPadrao, "[1,2)"})
	gravado.add(conf, []any{int64(2), "Bia", agora, nil})
	relido.add(conf, []any{int64(1), "Ana", agora.Add(time.Hour), "[1,2)"})
	relido.add(conf, []any{int64(2), "Bea", agora, nil}) // corrompido
	if div := colunasDivergentes(conf, gravado, relido); strings.Join(div, ",") != "nome" {
		t.Fatalf("divergentes: %v (criado recebeu DEFAULT e faixa não é comparável: ficam fora)", div)
	}
	ok, fora := colunasDoVeredito(conf, gravado)
	if strings.Join(ok, ",") != "id,nome" || len(fora) != 2 || fora[0].Coluna != "criado" || fora[1].Coluna != "faixa" {
		t.Fatalf("veredito: %v %+v", ok, fora)
	}

	// ida e volta pelo checkpoint
	txt := gravado.codificar(conf, true)
	volta, conferido, err := decodificarSomas(txt, conf)
	if err != nil || !conferido {
		t.Fatalf("decodificar: %v %v", err, conferido)
	}
	if colunasDivergentes(conf, gravado, volta) != nil || !volta.padrao[2] {
		t.Fatal("o checkpoint tem de devolver exatamente as somas e as colunas com DEFAULT")
	}
	// checkpoint de agente antigo (vazio) ou de outro mapeamento: erro, nunca OK
	if _, _, err := decodificarSomas("", conf); err == nil {
		t.Fatal("checkpoint sem conteúdo deveria dar erro")
	}
	if _, _, err := decodificarSomas(`{"v":1,"somas":{"id":"`+strings.Repeat("0", 64)+`"}}`, conf); err == nil {
		t.Fatal("checkpoint sem a soma de uma coluna deveria dar erro")
	}
	var rt plano.ResumoTabela
	vereditoDoCheckpoint(&rt, checkpoint{concluida: true, linhas: 2}, conf)
	if rt.ConteudoOK || rt.ConteudoMotivo == "" {
		t.Fatalf("tabela concluída por agente antigo não pode sair com conteúdo OK: %+v", rt)
	}
}

func TestChaveInvalidaInvalidaTudo(t *testing.T) {
	p, dest := passoTeste()
	conf := novaConferencia(p, dest, nil)
	s := novasSomas(len(dest))
	s.add(conf, []any{transformar.UsarPadrao, "Ana", nil, nil})
	var rt plano.ResumoTabela
	veredito(&rt, conf, s, 1)
	if rt.ConteudoOK || len(rt.ColunasConferidas) != 0 || rt.ConteudoMotivo == "" {
		t.Fatalf("chave que nasce no destino: nada é conferido: %+v", rt)
	}
	if colunasDivergentes(conf, s, novasSomas(len(dest))) != nil {
		t.Fatal("sem chave válida não há o que comparar (nem falso alarme)")
	}
}

func TestChaveCanonicaComposta(t *testing.T) {
	p, dest := passoTeste()
	p.Destino.ChavePrimaria = []string{"id", "nome"}
	conf := novaConferencia(p, dest, nil)
	a, _ := conf.chaveCanonica([]any{int64(1), "2:x", nil, nil})
	b, _ := conf.chaveCanonica([]any{int64(12), ":x", nil, nil})
	if a == b {
		t.Fatal("partes da chave com tamanho na frente: não podem colidir")
	}
	// chave com parte não comparável: tabela vira "sem chave"
	p.Destino.ChavePrimaria = []string{"id", "faixa"}
	if c := novaConferencia(p, dest, nil); len(c.chave) != 0 {
		t.Fatalf("chave com tipo sem forma canônica não serve: %v", c.chave)
	}
}

// A coluna de lote só serve de chave da linha se for única (na origem, sem
// transformação, e no destino ou numa tabela que nasceu na migração): chave repetida
// faria duas linhas virarem uma na comparação.
func TestChaveDaLinhaPelaColunaDeLote(t *testing.T) {
	p, dest := passoTeste()
	p.Destino.ChavePrimaria = nil
	if c := novaConferencia(p, dest, nil); len(c.chave) != 0 {
		t.Fatalf("lote que não é único na origem não pode ser a chave: %v", c.chave)
	}
	p.Origem = esquema.Tabela{Nome: "T", ChavePrimaria: []string{"ID"}}
	if c := novaConferencia(p, dest, nil); len(c.chave) != 0 {
		t.Fatalf("único na origem, mas o destino (que já existia) não garante: %v", c.chave)
	}
	p.Criar = true
	if c := novaConferencia(p, dest, nil); len(c.chave) != 1 || c.chave[0] != 0 {
		t.Fatalf("único na origem e tabela criada pela migração: serve: %v", c.chave)
	}
	p.Map.Colunas[0].Transformacao = modelo.MapaValores
	if c := novaConferencia(p, dest, nil); len(c.chave) != 0 {
		t.Fatalf("uma transformação pode juntar valores: não serve: %v", c.chave)
	}
}

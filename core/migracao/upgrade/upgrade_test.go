package upgrade

import (
	"strings"
	"testing"
	"time"
)

func TestReservadaEUDF(t *testing.T) {
	if v, ok := Reservada("row"); !ok || v != "3" {
		t.Fatalf("ROW é reservada desde o FB3: %q %v", v, ok)
	}
	if _, ok := Reservada("CLIENTES"); ok {
		t.Fatal("CLIENTES não é reservada")
	}
	if s, ok := SubstitutoUDF("STRLEN", ""); !ok || !strings.Contains(s, "CHAR_LENGTH") {
		t.Fatalf("STRLEN: %q", s)
	}
	if s, ok := SubstitutoUDF("MINHA_FUNCAO", "IB_UDF_ltrim"); !ok || !strings.Contains(s, "LEADING") {
		t.Fatalf("pelo entrypoint: %q", s)
	}
	if _, ok := SubstitutoUDF("CALCULA_IMPOSTO", "calc"); ok {
		t.Fatal("UDF própria não tem substituto conhecido")
	}
	if !UsaDataHoraAmbigua("begin x = current_timestamp; end") || UsaDataHoraAmbigua("x = 'NOW'") {
		t.Fatal("data/hora")
	}
}

func TestPontuar(t *testing.T) {
	if n, nivel, b := Pontuar(nil); n != 0 || nivel != "baixo" || b != 0 {
		t.Fatal(n, nivel, b)
	}
	if _, nivel, _ := Pontuar([]Achado{{Nivel: Risco}, {Nivel: Risco}, {Nivel: Risco}}); nivel != "medio" {
		t.Fatal(nivel)
	}
	if n, nivel, b := Pontuar([]Achado{{Nivel: Bloqueio}, {Nivel: Bloqueio}, {Nivel: Bloqueio}, {Nivel: Info}}); n != 100 || nivel != "alto" || b != 3 {
		t.Fatal(n, nivel, b)
	}
}

func TestEstimarParada(t *testing.T) {
	um := EstimarParada(1<<30, 1)
	if um < 70*time.Second || um > 3*time.Minute {
		t.Fatalf("1 GiB: %v", um)
	}
	if EstimarParada(1<<30, 4) >= um {
		t.Fatal("restore em paralelo deveria encurtar")
	}
	if EstimarParada(0, 1) != time.Minute {
		t.Fatal("banco vazio")
	}
}

func TestFixSQLComentaOQueMudaDado(t *testing.T) {
	d := Diagnostico{Versao: "2.5.9", ODS: "11.2", Dialeto: 3, Charset: "WIN1252", CapturadoEm: "agora",
		Achados: []Achado{
			{Nivel: Info, Categoria: CatData, Objeto: "P1", Mensagem: "usa CURRENT_TIMESTAMP"},
			{Nivel: Bloqueio, Categoria: CatNulo, Objeto: "T.C", Mensagem: "3 nulos", Correcao: "-- UPDATE \"T\" SET \"C\" = ? WHERE \"C\" IS NULL;"},
			{Nivel: Risco, Categoria: CatPalavra, Objeto: "ROW", Mensagem: "reservada", Correcao: "ALTER TABLE \"T\" ALTER \"ROW\" TO \"ROW_\";"},
		}}
	s := GerarFixSQL(d)
	if strings.Index(s, "BLOQUEIO") > strings.Index(s, "RISCO") {
		t.Fatal("bloqueios primeiro")
	}
	if !strings.Contains(s, "-- UPDATE") {
		t.Fatal("o UPDATE tem de sair comentado")
	}
	if strings.Contains(s, "CURRENT_TIMESTAMP\n") {
		t.Fatal("achado sem correção não entra")
	}
}

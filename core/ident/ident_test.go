package ident

import (
	"errors"
	"strings"
	"testing"
)

func TestCitarPorMotor(t *testing.T) {
	casos := []struct {
		m          Motor
		nome, quer string
	}{
		{Postgres, "clientes", `"clientes"`},
		{Firebird, "CLIENTES", `"CLIENTES"`},
		{Postgres, `a"b`, `"a""b"`},
		{Postgres, `x"; DROP TABLE t; --`, `"x""; DROP TABLE t; --"`},
		{MySQL, "a`b", "`a``b`"},
		{SQLServer, "a]b", "[a]]b]"},
	}
	for _, c := range casos {
		got, err := Citar(c.m, c.nome)
		if err != nil || got != c.quer {
			t.Errorf("%s %q: got %s err %v, quer %s", c.m, c.nome, got, err, c.quer)
		}
	}
}

func TestCitarRecusaNomesInvalidos(t *testing.T) {
	if _, err := Citar(Postgres, ""); !errors.Is(err, ErrNomeVazio) {
		t.Errorf("vazio: %v", err)
	}
	if _, err := Citar(Postgres, "a\x00b"); !errors.Is(err, ErrNomeInvalido) {
		t.Errorf("NUL: %v", err)
	}
	if _, err := Citar(Postgres, strings.Repeat("a", 64)); !errors.Is(err, ErrNomeLongo) {
		t.Errorf("longo: %v", err)
	}
	if _, err := Citar("oracle", "a"); !errors.Is(err, ErrMotorDesconhecido) {
		t.Errorf("motor: %v", err)
	}
}

func TestListaPermitida(t *testing.T) {
	l := NovaLista("CLIENTES", "PEDIDOS")
	if got, err := l.CitarPermitido(Firebird, "CLIENTES"); err != nil || got != `"CLIENTES"` {
		t.Fatalf("got %s err %v", got, err)
	}
	if _, err := l.CitarPermitido(Firebird, "clientes"); !errors.Is(err, ErrNaoPermitido) {
		t.Fatalf("comparação deve ser exata: %v", err)
	}
	if _, err := l.CitarPermitido(Firebird, "USUARIOS"); !errors.Is(err, ErrNaoPermitido) {
		t.Fatalf("fora da lista: %v", err)
	}
}

func TestCitado(t *testing.T) {
	got, err := Citado(Postgres, "public", "clientes")
	if err != nil || got != `"public"."clientes"` {
		t.Fatalf("got %s err %v", got, err)
	}
	if _, err := Citado(Postgres, "public", ""); err == nil {
		t.Fatal("esperava erro")
	}
}

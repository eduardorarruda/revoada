// Package ident trata nomes de tabelas e colunas que entram em SQL montado
// dinamicamente (ARQUITETURA §13 — injeção).
//
// Regra: valor de usuário NUNCA vira texto de SQL — vai como parâmetro. Já nomes de
// tabela/coluna não podem ser parâmetro, então passam por duas barreiras:
//  1. Lista permitida: o nome tem de existir na foto do schema capturado.
//  2. Aspas do motor: o nome é citado com as regras daquele banco, com escape.
package ident

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Motor identifica o dialeto SQL.
type Motor string

const (
	Firebird  Motor = "firebird"
	Postgres  Motor = "postgres"
	MySQL     Motor = "mysql"
	SQLServer Motor = "mssql"
)

var (
	ErrNomeVazio         = errors.New("ident: nome vazio")
	ErrNomeInvalido      = errors.New("ident: nome com caractere proibido")
	ErrNomeLongo         = errors.New("ident: nome maior que o permitido pelo banco")
	ErrNaoPermitido      = errors.New("ident: nome não existe no schema capturado")
	ErrMotorDesconhecido = errors.New("ident: motor desconhecido")
)

// tamanhoMaximo segue o limite de cada banco (Firebird 4+ aceita 63; o 2.5 aceita 31,
// mas nomes do 2.5 nunca passam disso — vêm da foto do próprio banco).
var tamanhoMaximo = map[Motor]int{Firebird: 63, Postgres: 63, MySQL: 64, SQLServer: 128}

// Citar devolve o nome entre aspas, pronto para entrar no SQL do motor.
func Citar(m Motor, nome string) (string, error) {
	max, ok := tamanhoMaximo[m]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrMotorDesconhecido, m)
	}
	if nome == "" {
		return "", ErrNomeVazio
	}
	if !utf8.ValidString(nome) || strings.ContainsRune(nome, 0) {
		return "", ErrNomeInvalido
	}
	if utf8.RuneCountInString(nome) > max {
		return "", ErrNomeLongo
	}
	switch m {
	case MySQL:
		return "`" + strings.ReplaceAll(nome, "`", "``") + "`", nil
	case SQLServer:
		return "[" + strings.ReplaceAll(nome, "]", "]]") + "]", nil
	default: // padrão SQL: Firebird e PostgreSQL
		return `"` + strings.ReplaceAll(nome, `"`, `""`) + `"`, nil
	}
}

// Citado concatena partes citadas com ponto (ex.: schema.tabela).
func Citado(m Motor, partes ...string) (string, error) {
	out := make([]string, 0, len(partes))
	for _, p := range partes {
		c, err := Citar(m, p)
		if err != nil {
			return "", err
		}
		out = append(out, c)
	}
	return strings.Join(out, "."), nil
}

// Lista é o conjunto de nomes que existem no schema capturado.
type Lista struct{ nomes map[string]struct{} }

// NovaLista cria a lista permitida. A comparação é exata (sensível a maiúsculas):
// o nome usado tem de ser o mesmo que o banco devolveu.
func NovaLista(nomes ...string) Lista {
	l := Lista{nomes: make(map[string]struct{}, len(nomes))}
	for _, n := range nomes {
		l.nomes[n] = struct{}{}
	}
	return l
}

// Exigir falha se o nome não estiver na lista.
func (l Lista) Exigir(nome string) error {
	if _, ok := l.nomes[nome]; !ok {
		return fmt.Errorf("%w: %q", ErrNaoPermitido, nome)
	}
	return nil
}

// CitarPermitido junta as duas barreiras: precisa estar na lista e é citado.
func (l Lista) CitarPermitido(m Motor, nome string) (string, error) {
	if err := l.Exigir(nome); err != nil {
		return "", err
	}
	return Citar(m, nome)
}

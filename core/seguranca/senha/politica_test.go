package senha

import (
	"errors"
	"strings"
	"testing"
)

func TestValidar(t *testing.T) {
	casos := []struct {
		nome, senha, usuario string
		quer                 error
	}{
		{"curta", "abc123", "", ErrCurta},
		{"longa", strings.Repeat("ab", 129), "", ErrLonga},
		{"repetida", "aaaaaaaaaaaaaa", "", ErrRepetida},
		{"comum", "Senha12345678", "", ErrComum},
		{"comum com símbolo no fim", "senha12345678!!", "", ErrComum},
		{"contém o usuário", "ana-do-revoada-forte", "ana", ErrComUsuario},
		{"contém a parte local do e-mail", "maria.souza#forte!", "maria.souza@exemplo.com", ErrComUsuario},
		{"boa", "cavalo-bateria-grampo", "ana", nil},
		{"boa com acentos conta runas", "pássaro-voa-alto", "", nil},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			if err := Validar(c.senha, c.usuario); !errors.Is(err, c.quer) {
				t.Fatalf("got %v, quer %v", err, c.quer)
			}
		})
	}
}

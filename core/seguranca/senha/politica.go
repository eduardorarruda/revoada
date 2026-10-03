// Package senha aplica a política mínima de senha do painel (ARQUITETURA §13):
// comprimento (12+), nada de senha comum, nada de senha igual ao usuário.
// Segue a linha do NIST SP 800-63B: comprimento e lista de bloqueio valem mais do
// que regras de "1 maiúscula + 1 símbolo", que só geram "Senha@123".
package senha

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	Minimo = 12
	Maximo = 256 // teto para o Argon2 não virar vetor de negação de serviço
)

var (
	ErrCurta      = errors.New("a senha precisa ter pelo menos 12 caracteres")
	ErrLonga      = errors.New("a senha pode ter no máximo 256 caracteres")
	ErrComum      = errors.New("essa senha é muito comum e aparece em listas de vazamento; escolha outra")
	ErrRepetida   = errors.New("a senha não pode ser um único caractere repetido")
	ErrComUsuario = errors.New("a senha não pode conter o seu nome de usuário")
)

// Validar devolve nil se `s` atende à política. `usuario` pode ser vazio.
func Validar(s, usuario string) error {
	n := utf8.RuneCountInString(s)
	switch {
	case n < Minimo:
		return ErrCurta
	case n > Maximo:
		return ErrLonga
	}
	baixa := strings.ToLower(s)
	if repetida(baixa) {
		return ErrRepetida
	}
	if _, ok := comuns[baixa]; ok {
		return ErrComum
	}
	// variações óbvias de senha comum: "senha123456!" → núcleo "senha123456"
	if _, ok := comuns[strings.TrimRight(baixa, "!@#$%&*.?-_ ")]; ok {
		return ErrComum
	}
	if u := strings.ToLower(strings.TrimSpace(usuario)); len(u) >= 3 {
		if local, _, ok := strings.Cut(u, "@"); ok && len(local) >= 3 {
			u = local
		}
		if strings.Contains(baixa, u) {
			return ErrComUsuario
		}
	}
	return nil
}

func repetida(s string) bool {
	var primeiro rune
	for i, r := range s {
		if i == 0 {
			primeiro = r
			continue
		}
		if r != primeiro {
			return false
		}
	}
	return true
}

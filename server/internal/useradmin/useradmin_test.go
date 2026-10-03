package useradmin

import "testing"

// TestValidEmail cobre a validação de sintaxe de email do cadastro (login por email).
func TestValidEmail(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"pessoa@empresa.com.br", true},
		{"a@b.co", true},
		{"fulano.silva@exemplo.com.br", true},
		{"", false},
		{"semarroba", false},
		{"sem@dominio", false},     // domínio sem ponto
		{"@empresa.com", false},    // sem parte local
		{"a b@empresa.com", false}, // espaço
		{"nome <n@e.com>", false},  // não pode vir com display name
	}
	for _, c := range cases {
		if got := validEmail(c.in); got != c.want {
			t.Errorf("validEmail(%q) = %v; quer %v", c.in, got, c.want)
		}
	}
}

// TestOnlyDigits garante que o celular é normalizado só a dígitos (formato Evolution).
func TestOnlyDigits(t *testing.T) {
	cases := map[string]string{
		"(11) 99999-9999":   "11999999999",
		"+55 11 99999-9999": "5511999999999",
		"11999999999":       "11999999999",
		"abc":               "",
		"":                  "",
	}
	for in, want := range cases {
		if got := onlyDigits(in); got != want {
			t.Errorf("onlyDigits(%q) = %q; quer %q", in, got, want)
		}
	}
}

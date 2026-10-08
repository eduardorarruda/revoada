package redacao

import (
	"strings"
	"testing"
)

// Número de cartão (PAN) em log de aplicação de pagamento: a validação ponta a
// ponta mostrou "cobrança no cartão 4111 1111 1111 1111 aprovada" chegando inteiro
// ao painel. A regra exige bandeira conhecida E dígito verificador (Luhn), para não
// comer números longos legítimos — epoch em milissegundos, ids de pedido.
func TestRedactCartao(t *testing.T) {
	for _, c := range []struct{ entrada, cartao string }{
		{"cobrança no cartão 4111 1111 1111 1111 aprovada", "4111 1111 1111 1111"},
		{"pan=4111111111111111 ok", "4111111111111111"},
		{"mastercard 5500-0000-0000-0004 recusado", "5500-0000-0000-0004"},
		{"amex 378282246310005", "378282246310005"},
		{"elo 6362970000457013", "6362970000457013"},
		{`{"card":"4012888888881881"}`, "4012888888881881"},
	} {
		got := Texto(c.entrada)
		if strings.Contains(got, c.cartao) || !strings.Contains(got, Marca) {
			t.Errorf("%q → %q: o cartão deveria sair mascarado", c.entrada, got)
		}
	}
	for _, s := range []string{
		"ts=1727900000000 evento",                // epoch ms (13 dígitos, sem bandeira)
		"cartão 4111111111111112 inválido",       // falha no Luhn
		"pedido 9876543210123456 enviado",        // sem bandeira conhecida
		"telefone 11987654321 e cpf 12345678909", // curtos demais
	} {
		if got := Texto(s); got != s {
			t.Errorf("%q virou %q: número legítimo não é cartão", s, got)
		}
	}
}

// Os quatro últimos dígitos ficam: é o que o atendimento usa para achar a transação,
// e o padrão PCI permite exibi-los.
func TestRedactCartaoMantemFinal(t *testing.T) {
	if got := Texto("cartão 4111 1111 1111 1111"); !strings.HasSuffix(got, "1111") || strings.Contains(got, "4111 1111") {
		t.Fatalf("%q", got)
	}
}

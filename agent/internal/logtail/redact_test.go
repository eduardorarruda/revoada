package logtail

import (
	"strings"
	"testing"
)

// As regras têm a bateria completa em core/redacao; aqui só se prova que o tailer
// continua mascarando pelo caminho compartilhado.
func TestRedactSecretsUsaRegrasCompartilhadas(t *testing.T) {
	got := redactSecrets(`Authorization: Bearer abcdefghijklmnop`)
	if !strings.Contains(got, redactMark) || strings.Contains(got, "abcdefghijklmnop") {
		t.Fatalf("token não foi redigido: %q", got)
	}
	if got := redactSecrets("token expired"); got != "token expired" {
		t.Fatalf("texto sem segredo mudou: %q", got)
	}
}

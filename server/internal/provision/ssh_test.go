package provision

import (
	"strings"
	"testing"
)

func TestHostKeyPin(t *testing.T) {
	const fp = "SHA256:AAAA"
	// Primeira conexão (pin vazio): aceita qualquer fp (TOFU).
	if err := hostKeyMatch("h", "", fp); err != nil {
		t.Fatalf("TOFU deveria aceitar: %v", err)
	}
	// Mesmo fp pinado: aceita.
	if err := hostKeyMatch("h", fp, fp); err != nil {
		t.Fatalf("mesmo fp deveria aceitar: %v", err)
	}
	// fp diferente: rejeita.
	if err := hostKeyMatch("h", fp, "SHA256:BBBB"); err == nil {
		t.Fatal("fp divergente deveria rejeitar")
	}
}

func TestTargetRedaction(t *testing.T) {
	tgt := NewTarget("host", 22, "root", AuthPassword, []byte("senha-secreta"), "")
	if strings.Contains(tgt.String(), "senha-secreta") {
		t.Fatalf("String() vazou o secret: %s", tgt.String())
	}
	if !strings.Contains(tgt.String(), "REDACTED") {
		t.Fatalf("String() deveria redigir: %s", tgt.String())
	}
}

func TestAuthMethodsValidation(t *testing.T) {
	if _, err := authMethods(NewTarget("h", 22, "u", AuthPassword, nil, "")); err == nil {
		t.Fatal("senha vazia deveria falhar")
	}
	if _, err := authMethods(NewTarget("h", 22, "u", "bogus", []byte("x"), "")); err == nil {
		t.Fatal("auth_type inválido deveria falhar")
	}
	if _, err := authMethods(NewTarget("h", 22, "u", AuthKey, []byte("not-a-pem"), "")); err == nil {
		t.Fatal("chave inválida deveria falhar")
	} else if strings.Contains(err.Error(), "not-a-pem") {
		t.Fatalf("erro da chave inválida vazou o secret: %v", err)
	}
}

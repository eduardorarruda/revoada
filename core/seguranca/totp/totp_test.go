package totp

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

// Vetores da RFC 6238, apêndice B (SHA1, 8 dígitos, segredo ASCII "12345678901234567890").
func TestVetoresRFC6238(t *testing.T) {
	segredo := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	casos := []struct {
		unix int64
		quer string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}
	for _, c := range casos {
		got, err := Codigo(segredo, Passo(time.Unix(c.unix, 0)), 8)
		if err != nil || got != c.quer {
			t.Errorf("t=%d: got %s err %v, quer %s", c.unix, got, err, c.quer)
		}
	}
}

func TestVerificarAceitaJanelaERecusaReplay(t *testing.T) {
	seg, err := NovoSegredo()
	if err != nil {
		t.Fatal(err)
	}
	agora := time.Unix(1_800_000_000, 0)
	cod, _ := Codigo(seg, Passo(agora)-1, Digitos) // relógio do celular 30 s atrasado
	passo, ok := Verificar(seg, cod, agora, 0)
	if !ok {
		t.Fatal("código do passo anterior deveria valer")
	}
	if _, ok := Verificar(seg, cod, agora, passo); ok {
		t.Fatal("replay do mesmo código deveria ser recusado")
	}
}

func TestVerificarRecusaCodigoErradoOuForaDaJanela(t *testing.T) {
	seg, _ := NovoSegredo()
	agora := time.Unix(1_800_000_000, 0)
	velho, _ := Codigo(seg, Passo(agora)-5, Digitos)
	if _, ok := Verificar(seg, velho, agora, 0); ok {
		t.Fatal("código de 2,5 min atrás não deveria valer")
	}
	if _, ok := Verificar(seg, "12345", agora, 0); ok {
		t.Fatal("tamanho errado não deveria valer")
	}
	if _, ok := Verificar("@@@", "123456", agora, 0); ok {
		t.Fatal("segredo inválido não deveria valer")
	}
}

func TestURIContemSegredoEEmissor(t *testing.T) {
	u := URI("Revoada", "ana@exemplo.com", "ABC")
	if !strings.HasPrefix(u, "otpauth://totp/Revoada:ana@exemplo.com?") || !strings.Contains(u, "secret=ABC") || !strings.Contains(u, "issuer=Revoada") {
		t.Fatalf("URI inesperada: %s", u)
	}
}

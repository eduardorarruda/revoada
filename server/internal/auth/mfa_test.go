package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

func TestDesafioNaoServeComoTokenDeAcesso(t *testing.T) {
	h := &Handler{secret: "segredo-de-teste"}
	d, err := h.emitirDesafio(7, "ana")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAccessToken(h.secret, d); err == nil {
		t.Fatal("o desafio do 2FA foi aceito como token de acesso")
	}
	c, err := h.lerDesafio(d)
	if err != nil || c.Sub != 7 {
		t.Fatalf("desafio válido recusado: %v", err)
	}
	acesso, _ := SignAccessToken(h.secret, 7, "ana", PapelAdmin, time.Minute)
	if _, err := h.lerDesafio(acesso); err == nil {
		t.Fatal("token de acesso foi aceito como desafio")
	}
}

func TestCodigosDeRecuperacao(t *testing.T) {
	cods, hashes, err := novosCodigosRecuperacao()
	if err != nil || len(cods) != qtdCodigosRecuper || len(hashes) != qtdCodigosRecuper {
		t.Fatalf("err %v, %d códigos", err, len(cods))
	}
	vistos := map[string]bool{}
	for i, c := range cods {
		if len(c) != 11 || c[5] != '-' {
			t.Fatalf("formato inesperado: %s", c)
		}
		if vistos[c] {
			t.Fatal("código repetido")
		}
		vistos[c] = true
		// digitado em minúsculas e sem traço ainda bate
		if hashRecuperacao(strings.ToLower(strings.ReplaceAll(c, "-", ""))) != hashes[i] {
			t.Fatal("normalização do código de recuperação falhou")
		}
	}
}

func TestMensagemBloqueio(t *testing.T) {
	agora := time.Unix(1_800_000_000, 0)
	if m := mensagemBloqueio(store.User{}, agora); m != "" {
		t.Fatalf("sem bloqueio: %q", m)
	}
	ate := agora.Add(90 * time.Second)
	if m := mensagemBloqueio(store.User{BloqueadoAte: &ate}, agora); !strings.Contains(m, "2 min") {
		t.Fatalf("bloqueado: %q", m)
	}
	passado := agora.Add(-time.Second)
	if m := mensagemBloqueio(store.User{BloqueadoAte: &passado}, agora); m != "" {
		t.Fatalf("bloqueio vencido: %q", m)
	}
}

func TestQRPNG(t *testing.T) {
	b, err := qrPNG("otpauth://totp/Revoada:ana?secret=ABC")
	if err != nil || len(b) < 100 || string(b[1:4]) != "PNG" {
		t.Fatalf("err %v len %d", err, len(b))
	}
}

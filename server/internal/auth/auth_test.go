package auth

import (
	"testing"
	"time"
)

func TestArgon2Roundtrip(t *testing.T) {
	h, err := HashPassword("senha-super-secreta")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("senha-super-secreta", h) {
		t.Error("senha correta não verificou")
	}
	if VerifyPassword("senha-errada", h) {
		t.Error("senha errada verificou (!)")
	}
}

func TestJWTRoundtrip(t *testing.T) {
	secret := "s3cr3t"
	tok, err := SignAccessToken(secret, 42, "ana", "editor", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseAccessToken(secret, tok)
	if err != nil {
		t.Fatalf("parse falhou: %v", err)
	}
	if c.Sub != 42 || c.Role != "editor" {
		t.Errorf("claims erradas: %+v", c)
	}
	// assinatura com outro segredo deve falhar
	if _, err := ParseAccessToken("outro", tok); err == nil {
		t.Error("token aceito com segredo errado")
	}
}

func TestJWTExpired(t *testing.T) {
	tok, _ := SignAccessToken("s", 1, "x", "viewer", -time.Second)
	if _, err := ParseAccessToken("s", tok); err == nil {
		t.Error("token expirado foi aceito")
	}
}

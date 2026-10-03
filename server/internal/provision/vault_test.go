package provision

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestVaultRoundTrip(t *testing.T) {
	t.Setenv("REVOADA_VAULT_KEY", "")
	t.Setenv("REVOADA_JWT_SECRET", "um-segredo-de-teste-suficientemente-longo-1234")

	plain := []byte("senha-super-secreta-ssh")
	blob, err := Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Contains(blob, plain) {
		t.Fatal("o ciphertext contém o plaintext em claro")
	}
	if len(blob) <= nonceLen {
		t.Fatalf("blob curto demais: %d", len(blob))
	}
	got, err := Decrypt(blob)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round-trip divergente: %q != %q", got, plain)
	}
}

func TestVaultBase64RoundTrip(t *testing.T) {
	t.Setenv("REVOADA_VAULT_KEY", "")
	t.Setenv("REVOADA_JWT_SECRET", "segredo-de-teste")
	s, err := EncryptToBase64([]byte("chave privada PEM aqui"))
	if err != nil {
		t.Fatalf("EncryptToBase64: %v", err)
	}
	got, err := DecryptFromBase64(s)
	if err != nil {
		t.Fatalf("DecryptFromBase64: %v", err)
	}
	if string(got) != "chave privada PEM aqui" {
		t.Fatalf("round-trip base64 divergente: %q", got)
	}
}

func TestVaultWrongKeyFails(t *testing.T) {
	t.Setenv("REVOADA_VAULT_KEY", "")
	t.Setenv("REVOADA_JWT_SECRET", "segredo-A")
	blob, err := Encrypt([]byte("dados"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	// Troca a chave (outro JWT secret) → Decrypt deve falhar na autenticação.
	t.Setenv("REVOADA_JWT_SECRET", "segredo-B-completamente-diferente")
	if _, err := Decrypt(blob); err == nil {
		t.Fatal("Decrypt com chave errada deveria falhar")
	}
}

func TestVaultExplicitKeyHexAndBase64(t *testing.T) {
	// 32 bytes: 0x01..0x20.
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	hexKey := ""
	for _, b := range raw {
		const hexd = "0123456789abcdef"
		hexKey += string(hexd[b>>4]) + string(hexd[b&0xf])
	}

	// Cifra com a chave em hex.
	t.Setenv("REVOADA_VAULT_KEY", hexKey)
	blob, err := Encrypt([]byte("via-hex"))
	if err != nil {
		t.Fatalf("Encrypt hex: %v", err)
	}
	// Decifra com a MESMA chave expressa em base64 → deve funcionar (mesma chave).
	t.Setenv("REVOADA_VAULT_KEY", base64.StdEncoding.EncodeToString(raw))
	got, err := Decrypt(blob)
	if err != nil {
		t.Fatalf("Decrypt base64: %v", err)
	}
	if string(got) != "via-hex" {
		t.Fatalf("divergente: %q", got)
	}
}

func TestVaultInvalidKeyRejected(t *testing.T) {
	t.Setenv("REVOADA_VAULT_KEY", "curta-demais")
	if _, err := Encrypt([]byte("x")); err == nil {
		t.Fatal("REVOADA_VAULT_KEY inválida deveria falhar")
	}
}

// TestCofreLeSegredoAntigoDepoisDeDefinirChave é a migração que torna a recomendação
// segura. Sem REVOADA_VAULT_KEY o cofre deriva a chave de REVOADA_JWT_SECRET — e aí
// rotacionar o JWT (operação de rotina) tornaria TODA credencial SSH guardada
// indecifrável, em silêncio. A saída é definir a chave dedicada; mas se ao ligá-la os
// segredos já gravados parassem de abrir, a recomendação viraria uma armadilha.
func TestCofreLeSegredoAntigoDepoisDeDefinirChave(t *testing.T) {
	t.Setenv("REVOADA_JWT_SECRET", "segredo-de-sessao-antigo-com-32-bytes")
	t.Setenv("REVOADA_VAULT_KEY", "")

	blob, err := Encrypt([]byte("senha-ssh-do-cliente"))
	if err != nil {
		t.Fatalf("cifrar com a chave derivada: %v", err)
	}

	// Operador passa a definir a chave dedicada (openssl rand -hex 32).
	t.Setenv("REVOADA_VAULT_KEY", strings.Repeat("ab", 32))

	plain, err := Decrypt(blob)
	if err != nil {
		t.Fatalf("o segredo antigo precisa continuar legível: %v", err)
	}
	if string(plain) != "senha-ssh-do-cliente" {
		t.Fatalf("conteúdo errado: %q", plain)
	}

	// O que for cifrado AGORA usa a chave dedicada — e deixa de depender do JWT.
	novo, err := Encrypt([]byte("senha-nova"))
	if err != nil {
		t.Fatalf("cifrar com a chave dedicada: %v", err)
	}
	t.Setenv("REVOADA_JWT_SECRET", "jwt-rotacionado-em-uma-terca-feira")
	plain2, err := Decrypt(novo)
	if err != nil || string(plain2) != "senha-nova" {
		t.Fatalf("com chave dedicada, rotacionar o JWT não pode quebrar o cofre: %v %q", err, plain2)
	}
}

// TestAvisoChaveDeCofre: sem chave dedicada, o boot precisa AVISAR — o risco é
// silencioso e só aparece meses depois, no dia de reprovisionar um servidor.
func TestAvisoChaveDeCofre(t *testing.T) {
	t.Setenv("REVOADA_VAULT_KEY", "")
	if !ChaveDerivadaDoJWT() || AvisoChaveDeCofre() == "" {
		t.Error("sem REVOADA_VAULT_KEY o painel tem de avisar")
	}
	t.Setenv("REVOADA_VAULT_KEY", strings.Repeat("cd", 32))
	if ChaveDerivadaDoJWT() || AvisoChaveDeCofre() != "" {
		t.Error("com chave dedicada não há aviso a dar")
	}
}

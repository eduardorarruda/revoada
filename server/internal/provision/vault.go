// Package provision implementa o onboarding SSH do agente (Fase G) e o cofre
// cifrado de credenciais. REGRA INEGOCIÁVEL: a credencial SSH (senha ou chave
// privada) nunca é logada, serializada nem devolvida ao cliente em claro — só
// existe em memória durante o provisionamento e cifrada (secretbox) no banco.
package provision

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/eduardorarruda/revoada/server/internal/config"
	"golang.org/x/crypto/nacl/secretbox"
)

const nonceLen = 24 // secretbox usa nonce de 24 bytes

// ChaveDerivadaDoJWT diz se o cofre está SEM chave dedicada, derivando-a do
// REVOADA_JWT_SECRET. O main loga isso de forma proeminente no boot.
//
// Por que isso importa: derivando do JWT, rotacionar o segredo de sessão — uma
// operação de rotina, que ninguém associa a SSH — torna TODA credencial guardada no
// cofre indecifrável, em silêncio. O erro só aparece meses depois, no dia em que
// alguém tenta reprovisionar um servidor e o painel diz "falha ao decifrar".
//
// Não é fail-fast de propósito: a produção que está no ar HOJE não define
// REVOADA_VAULT_KEY, e abortar o boot por causa disso derrubaria o painel no próximo
// deploy — trocar um risco latente por uma queda certa. O caminho é: avisar alto,
// oferecer migração sem perda (ver Decrypt) e passar a definir a variável.
func ChaveDerivadaDoJWT() bool { return strings.TrimSpace(config.VaultKeyEnv()) == "" }

// AvisoChaveDeCofre devolve o aviso a logar no boot, ou "" quando está tudo certo.
func AvisoChaveDeCofre() string {
	if !ChaveDerivadaDoJWT() {
		return ""
	}
	return "REVOADA_VAULT_KEY ausente: o cofre de credenciais SSH está derivando a chave de " +
		"REVOADA_JWT_SECRET, se o JWT for rotacionado, TODA credencial SSH guardada fica " +
		"indecifrável. Defina REVOADA_VAULT_KEY (32 bytes em hex/base64); os segredos já " +
		"gravados continuam sendo lidos pela chave antiga."
}

// vaultKey devolve a chave de 32 bytes usada para CIFRAR. Ordem:
//  1. REVOADA_VAULT_KEY (hex de 64 chars OU base64 de 32 bytes) — o certo em produção;
//  2. fallback: sha256(REVOADA_JWT_SECRET) — funciona sem configurar nada.
func vaultKey() ([32]byte, error) {
	var key [32]byte
	if raw := strings.TrimSpace(config.VaultKeyEnv()); raw != "" {
		if b, err := hex.DecodeString(raw); err == nil && len(b) == 32 {
			copy(key[:], b)
			return key, nil
		}
		if b, err := base64.StdEncoding.DecodeString(raw); err == nil && len(b) == 32 {
			copy(key[:], b)
			return key, nil
		}
		if b, err := base64.RawStdEncoding.DecodeString(raw); err == nil && len(b) == 32 {
			copy(key[:], b)
			return key, nil
		}
		return key, errors.New("REVOADA_VAULT_KEY inválida: use hex (64 chars) ou base64 de exatamente 32 bytes")
	}
	return chaveLegada(), nil
}

// chaveLegada é a chave derivada do JWT — a que cifrou tudo o que já está no banco
// das instalações que nunca definiram REVOADA_VAULT_KEY.
func chaveLegada() [32]byte {
	return sha256.Sum256([]byte(config.JWTSecret()))
}

// Encrypt cifra o plaintext com NaCl secretbox e devolve o blob nonce||ciphertext
// (bytes crus; a borda do banco codifica em base64). Um nonce aleatório novo por
// segredo garante que o mesmo texto cifre diferente a cada vez.
func Encrypt(plaintext []byte) ([]byte, error) {
	key, err := vaultKey()
	if err != nil {
		return nil, err
	}
	var nonce [nonceLen]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	// out começa com o nonce; secretbox.Seal anexa o ciphertext em seguida.
	out := secretbox.Seal(nonce[:], plaintext, &nonce, &key)
	return out, nil
}

// Decrypt reverte Encrypt: separa o nonce, decifra e autentica. Chave errada ou
// blob adulterado falham na autenticação (secretbox.Open == false).
//
// Se a chave configurada não abrir o blob, tentamos a chave LEGADA (derivada do
// REVOADA_JWT_SECRET). É o que torna possível ligar REVOADA_VAULT_KEY numa instalação
// que já tem credenciais guardadas SEM perder nenhuma: o que foi cifrado antes
// continua sendo lido, o que for cifrado depois usa a chave dedicada. Sem esta
// tentativa, definir a variável recomendada seria uma operação destrutiva — e a
// recomendação viraria uma armadilha.
func Decrypt(blob []byte) ([]byte, error) {
	if len(blob) < nonceLen+secretbox.Overhead {
		return nil, errors.New("blob cifrado curto/corrompido")
	}
	key, err := vaultKey()
	if err != nil {
		return nil, err
	}
	var nonce [nonceLen]byte
	copy(nonce[:], blob[:nonceLen])
	if plain, ok := secretbox.Open(nil, blob[nonceLen:], &nonce, &key); ok {
		return plain, nil
	}
	if !ChaveDerivadaDoJWT() {
		legada := chaveLegada()
		if plain, ok := secretbox.Open(nil, blob[nonceLen:], &nonce, &legada); ok {
			return plain, nil
		}
	}
	return nil, errors.New("falha ao decifrar o segredo (chave errada ou dado corrompido)")
}

// EncryptToBase64/DecryptFromBase64 são conveniências para a borda do banco
// (secret_blob é TEXT). O blob (nonce||ciphertext) vai em base64 padrão.
func EncryptToBase64(plaintext []byte) (string, error) {
	blob, err := Encrypt(plaintext)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(blob), nil
}

func DecryptFromBase64(s string) ([]byte, error) {
	blob, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("secret_blob não é base64 válido")
	}
	return Decrypt(blob)
}

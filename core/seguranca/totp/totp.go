// Package totp implementa senhas de uso único baseadas em tempo (RFC 6238), as do
// Google Authenticator, Authy, 1Password etc. — o segundo fator do login (ARQUITETURA §13).
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 usa HMAC-SHA1 por padrão; é o que os apps esperam
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	Periodo = 30 * time.Second
	Digitos = 6
	// Janela aceita um passo antes e um depois (relógio do celular adiantado/atrasado).
	Janela = 1
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NovoSegredo gera 160 bits aleatórios em base32 (o formato que os apps aceitam).
func NovoSegredo() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("totp: %w", err)
	}
	return b32.EncodeToString(b), nil
}

// URI monta o otpauth:// que vira QR code no app autenticador.
func URI(emissor, conta, segredo string) string {
	rotulo := url.PathEscape(emissor + ":" + conta)
	q := url.Values{}
	q.Set("secret", segredo)
	q.Set("issuer", emissor)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(Digitos))
	q.Set("period", fmt.Sprint(int(Periodo.Seconds())))
	return "otpauth://totp/" + rotulo + "?" + q.Encode()
}

// Passo é o número do intervalo de 30 s em `t`.
func Passo(t time.Time) int64 { return t.Unix() / int64(Periodo.Seconds()) }

// Codigo calcula o código de `digitos` dígitos para um passo.
func Codigo(segredo string, passo int64, digitos int) (string, error) {
	chave, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(segredo)))
	if err != nil {
		return "", fmt.Errorf("totp: segredo inválido: %w", err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(passo))
	m := hmac.New(sha1.New, chave)
	m.Write(msg[:])
	h := m.Sum(nil)
	off := h[len(h)-1] & 0x0f
	bin := binary.BigEndian.Uint32(h[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for i := 0; i < digitos; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digitos, bin%mod), nil
}

// Verificar confere `codigo` em `agora` (±Janela) e devolve o passo que bateu.
// O chamador guarda esse passo e recusa passos ≤ ao último usado: o mesmo código não
// serve duas vezes (proteção contra replay de quem viu o código por cima do ombro).
func Verificar(segredo, codigo string, agora time.Time, ultimoPasso int64) (passo int64, ok bool) {
	codigo = strings.ReplaceAll(strings.TrimSpace(codigo), " ", "")
	if len(codigo) != Digitos {
		return 0, false
	}
	base := Passo(agora)
	for d := -Janela; d <= Janela; d++ {
		p := base + int64(d)
		if p <= ultimoPasso {
			continue
		}
		esperado, err := Codigo(segredo, p, Digitos)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(esperado), []byte(codigo)) == 1 {
			return p, true
		}
	}
	return 0, false
}

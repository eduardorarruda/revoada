package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Claims do token de acesso.
type Claims struct {
	Sub  int64  `json:"sub"`  // user id
	Name string `json:"name"` // username (auditoria)
	Role string `json:"role"` // admin | operador | leitor
	Exp  int64  `json:"exp"`
	Iat  int64  `json:"iat"`
	// MFA: a sessão foi aberta com o segundo fator (TOTP ou código de recuperação).
	MFA bool `json:"mfa,omitempty"`
	// Reauth: até quando (unix) vale a reautenticação para ações críticas.
	Reauth int64 `json:"reauth,omitempty"`
}

var ErrInvalidToken = errors.New("token inválido")

// SignAccessToken emite um JWT HS256 curto.
func SignAccessToken(secret string, userID int64, username, role string, ttl time.Duration) (string, error) {
	return SignClaims(secret, Claims{Sub: userID, Name: username, Role: role}, ttl)
}

// SignClaims emite um JWT HS256 com as claims dadas; Exp e Iat são preenchidos aqui.
func SignClaims(secret string, claims Claims, ttl time.Duration) (string, error) {
	now := time.Now()
	claims.Exp = now.Add(ttl).Unix()
	claims.Iat = now.Unix()
	header := b64(`{"alg":"HS256","typ":"JWT"}`)
	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := b64(string(payloadJSON))
	signingInput := header + "." + payload
	sig := sign(secret, signingInput)
	return signingInput + "." + sig, nil
}

// ParseAccessToken valida assinatura e expiração e devolve as claims.
func ParseAccessToken(secret, token string) (Claims, error) {
	var c Claims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return c, ErrInvalidToken
	}
	expected := sign(secret, parts[0]+"."+parts[1])
	if subtle.ConstantTimeCompare([]byte(expected), []byte(parts[2])) != 1 {
		return c, ErrInvalidToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return c, ErrInvalidToken
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, ErrInvalidToken
	}
	if time.Now().Unix() >= c.Exp {
		return c, ErrInvalidToken
	}
	return c, nil
}

func sign(secret, input string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(input))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// HashToken devolve o sha256 (hex) de um token opaco (para guardar sessões).
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 64)
	for i, b := range sum {
		out[i*2] = hexdigits[b>>4]
		out[i*2+1] = hexdigits[b&0x0f]
	}
	return string(out)
}

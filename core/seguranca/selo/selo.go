// Package selo sela uma credencial para UM agente específico (ARQUITETURA §13, D3).
//
// O agente gera um par X25519 na inscrição e só a chave pública vai para o painel.
// Para cada tarefa, o painel sela a senha do banco para essa chave pública com uma
// chave efêmera (ECDH X25519 → HKDF-SHA256 → XChaCha20-Poly1305). Só aquele agente
// abre; o selo vale por pouco tempo e é amarrado ao contexto (ex.: id da tarefa),
// então não serve para outra tarefa nem depois de vencido.
package selo

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

var (
	// ErrSeloInvalido: chave errada, contexto errado ou conteúdo adulterado.
	ErrSeloInvalido = errors.New("selo: inválido ou adulterado")
	// ErrSeloVencido: o selo passou da validade.
	ErrSeloVencido = errors.New("selo: vencido")
)

const rotulo = "revoada/selo/v1"

// Selo é a credencial cifrada que viaja na tarefa.
type Selo struct {
	Efemera  []byte `json:"efemera"`   // chave pública efêmera do remetente
	Nonce    []byte `json:"nonce"`     // 24 bytes (XChaCha20)
	Cifrado  []byte `json:"cifrado"`   // texto cifrado + tag
	ExpiraEm int64  `json:"expira_em"` // unix (segundos)
}

// GerarPar cria o par de chaves do agente.
func GerarPar() (*ecdh.PrivateKey, error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("selo: gerando par de chaves: %w", err)
	}
	return k, nil
}

// CarregarPrivada reconstrói a chave privada a partir dos 32 bytes guardados.
func CarregarPrivada(b []byte) (*ecdh.PrivateKey, error) {
	k, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("selo: chave privada inválida: %w", err)
	}
	return k, nil
}

// Selar cifra `texto` para a chave pública `destino`, válido por `validade` a partir de `agora`.
func Selar(destino []byte, texto []byte, contexto string, validade time.Duration, agora time.Time) (Selo, error) {
	pub, err := ecdh.X25519().NewPublicKey(destino)
	if err != nil {
		return Selo{}, fmt.Errorf("selo: chave pública de destino inválida: %w", err)
	}
	efemera, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return Selo{}, fmt.Errorf("selo: %w", err)
	}
	segredo, err := efemera.ECDH(pub)
	if err != nil {
		return Selo{}, fmt.Errorf("selo: %w", err)
	}
	expira := agora.Add(validade).Unix()
	aead, err := derivar(segredo, efemera.PublicKey().Bytes(), destino, contexto)
	if err != nil {
		return Selo{}, err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return Selo{}, fmt.Errorf("selo: %w", err)
	}
	return Selo{
		Efemera:  efemera.PublicKey().Bytes(),
		Nonce:    nonce,
		Cifrado:  aead.Seal(nil, nonce, texto, aad(contexto, expira)),
		ExpiraEm: expira,
	}, nil
}

// Abrir decifra o selo com a chave privada do agente.
func Abrir(privada *ecdh.PrivateKey, s Selo, contexto string, agora time.Time) ([]byte, error) {
	if privada == nil || len(s.Nonce) != chacha20poly1305.NonceSizeX {
		return nil, ErrSeloInvalido
	}
	if agora.Unix() > s.ExpiraEm {
		return nil, ErrSeloVencido
	}
	pubEfemera, err := ecdh.X25519().NewPublicKey(s.Efemera)
	if err != nil {
		return nil, ErrSeloInvalido
	}
	segredo, err := privada.ECDH(pubEfemera)
	if err != nil {
		return nil, ErrSeloInvalido
	}
	aead, err := derivar(segredo, s.Efemera, privada.PublicKey().Bytes(), contexto)
	if err != nil {
		return nil, err
	}
	texto, err := aead.Open(nil, s.Nonce, s.Cifrado, aad(contexto, s.ExpiraEm))
	if err != nil {
		return nil, ErrSeloInvalido
	}
	return texto, nil
}

// derivar transforma o segredo ECDH numa chave de cifra amarrada às duas chaves
// públicas e ao contexto (HKDF-SHA256).
func derivar(segredo, efemera, destino []byte, contexto string) (cipherAEAD, error) {
	salt := append(append([]byte{}, efemera...), destino...)
	r := hkdf.New(sha256.New, segredo, salt, []byte(rotulo+"|"+contexto))
	chave := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(r, chave); err != nil {
		return nil, fmt.Errorf("selo: derivando chave: %w", err)
	}
	return chacha20poly1305.NewX(chave)
}

// aad inclui o contexto e a validade: mudar a data de vencimento invalida o selo.
func aad(contexto string, expira int64) []byte {
	b := make([]byte, 8, 8+len(contexto))
	binary.BigEndian.PutUint64(b, uint64(expira))
	return append(b, contexto...)
}

type cipherAEAD = interface {
	Seal(dst, nonce, plaintext, additionalData []byte) []byte
	Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
}

// Package cofre implementa criptografia em envelope (ARQUITETURA §13).
//
// Cada segredo é cifrado com uma chave de dados própria (DEK, AES-256-GCM). A DEK,
// por sua vez, é cifrada pela chave mestra (KEK), que vem de um Provedor e NUNCA fica
// no banco da aplicação. Quem rouba só o banco leva textos cifrados e DEKs cifradas —
// inúteis sem a KEK.
//
// Rotação da chave mestra não exige decifrar os dados: Reembrulhar troca só a DEK
// cifrada para a versão nova da KEK.
package cofre

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

const tamanhoChave = 32 // AES-256

var (
	// ErrSegredoInvalido cobre texto adulterado, contexto errado ou chave errada. A
	// mensagem é a mesma de propósito: não dizer ao atacante qual das três falhou.
	ErrSegredoInvalido = errors.New("cofre: segredo inválido ou adulterado")
	// ErrVersaoDesconhecida indica uma DEK cifrada por uma versão da chave mestra que
	// o provedor não conhece (chave antiga removida antes de reembrulhar tudo).
	ErrVersaoDesconhecida = errors.New("cofre: versão da chave mestra desconhecida")
)

// Provedor guarda a chave mestra (KEK) e embrulha/desembrulha DEKs com ela.
type Provedor interface {
	// VersaoAtiva é a versão usada para embrulhar DEKs novas.
	VersaoAtiva() int
	Embrulhar(ctx context.Context, versao int, dek []byte) ([]byte, error)
	Desembrulhar(ctx context.Context, versao int, dekCifrada []byte) ([]byte, error)
}

// Segredo é o que vai para o banco: nada aqui é texto puro.
type Segredo struct {
	Cifrado    []byte `json:"cifrado"`     // nonce || texto cifrado (AES-256-GCM com a DEK)
	DEKCifrada []byte `json:"dek_cifrada"` // DEK cifrada pela KEK
	KEKVersao  int    `json:"kek_versao"`
}

// Vazio diz se não há segredo guardado.
func (s Segredo) Vazio() bool { return len(s.Cifrado) == 0 }

// Cofre cifra e decifra segredos usando um Provedor de chave mestra.
type Cofre struct{ p Provedor }

// Novo cria um cofre. O provedor é obrigatório.
func Novo(p Provedor) (*Cofre, error) {
	if p == nil {
		return nil, errors.New("cofre: provedor de chave mestra ausente")
	}
	return &Cofre{p: p}, nil
}

// Cifrar protege texto. `contexto` amarra o segredo ao seu dono (ex.: "conexao:42"):
// um texto cifrado copiado para outro registro não decifra.
func (c *Cofre) Cifrar(ctx context.Context, texto, contexto []byte) (Segredo, error) {
	dek := make([]byte, tamanhoChave)
	if _, err := rand.Read(dek); err != nil {
		return Segredo{}, fmt.Errorf("cofre: gerando chave de dados: %w", err)
	}
	defer Limpar(dek)

	cifrado, err := SelarAESGCM(dek, texto, contexto)
	if err != nil {
		return Segredo{}, err
	}
	versao := c.p.VersaoAtiva()
	dekCifrada, err := c.p.Embrulhar(ctx, versao, dek)
	if err != nil {
		return Segredo{}, fmt.Errorf("cofre: embrulhando chave de dados: %w", err)
	}
	return Segredo{Cifrado: cifrado, DEKCifrada: dekCifrada, KEKVersao: versao}, nil
}

// Decifrar devolve o texto. O chamador deve Limpar o resultado assim que não precisar mais.
func (c *Cofre) Decifrar(ctx context.Context, s Segredo, contexto []byte) ([]byte, error) {
	if s.Vazio() {
		return nil, ErrSegredoInvalido
	}
	dek, err := c.p.Desembrulhar(ctx, s.KEKVersao, s.DEKCifrada)
	if err != nil {
		return nil, err
	}
	defer Limpar(dek)
	return AbrirAESGCM(dek, s.Cifrado, contexto)
}

// Reembrulhar passa a DEK para a versão ativa da chave mestra, sem tocar no texto
// cifrado. Segredo já na versão ativa volta igual.
func (c *Cofre) Reembrulhar(ctx context.Context, s Segredo) (Segredo, error) {
	ativa := c.p.VersaoAtiva()
	if s.KEKVersao == ativa {
		return s, nil
	}
	dek, err := c.p.Desembrulhar(ctx, s.KEKVersao, s.DEKCifrada)
	if err != nil {
		return Segredo{}, err
	}
	defer Limpar(dek)
	nova, err := c.p.Embrulhar(ctx, ativa, dek)
	if err != nil {
		return Segredo{}, fmt.Errorf("cofre: reembrulhando: %w", err)
	}
	return Segredo{Cifrado: s.Cifrado, DEKCifrada: nova, KEKVersao: ativa}, nil
}

// SelarAESGCM cifra com AES-256-GCM e devolve nonce || texto cifrado.
func SelarAESGCM(chave, texto, aad []byte) ([]byte, error) {
	aead, err := novoGCM(chave)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("cofre: gerando nonce: %w", err)
	}
	return aead.Seal(nonce, nonce, texto, aad), nil
}

// AbrirAESGCM desfaz SelarAESGCM.
func AbrirAESGCM(chave, cifrado, aad []byte) ([]byte, error) {
	aead, err := novoGCM(chave)
	if err != nil {
		return nil, err
	}
	n := aead.NonceSize()
	if len(cifrado) < n+aead.Overhead() {
		return nil, ErrSegredoInvalido
	}
	texto, err := aead.Open(nil, cifrado[:n], cifrado[n:], aad)
	if err != nil {
		return nil, ErrSegredoInvalido
	}
	return texto, nil
}

func novoGCM(chave []byte) (cipher.AEAD, error) {
	if len(chave) != tamanhoChave {
		return nil, fmt.Errorf("cofre: chave deve ter %d bytes, tem %d", tamanhoChave, len(chave))
	}
	bloco, err := aes.NewCipher(chave)
	if err != nil {
		return nil, fmt.Errorf("cofre: %w", err)
	}
	return cipher.NewGCM(bloco)
}

// Limpar zera um buffer com material sensível. É "melhor esforço": o coletor de lixo
// do Go pode ter copiado o conteúdo antes, mas reduz o tempo que a chave fica na memória.
func Limpar(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

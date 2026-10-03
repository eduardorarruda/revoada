package agentev1

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
)

// ErrAssinaturaInvalida: a tarefa não foi assinada pelo painel desta inscrição (ou
// foi alterada no caminho).
var ErrAssinaturaInvalida = errors.New("assinatura do painel inválida")

// ErrTarefaVencida: a tarefa passou do prazo e não pode mais ser executada.
var ErrTarefaVencida = errors.New("tarefa vencida")

// bytesParaAssinar serializa a mensagem de forma determinística SEM a assinatura.
func bytesParaAssinar(m proto.Message, limpar func(proto.Message)) ([]byte, error) {
	c := proto.Clone(m)
	limpar(c)
	return proto.MarshalOptions{Deterministic: true}.Marshal(c)
}

// AssinarTarefa preenche t.Assinatura com Ed25519 do painel.
func AssinarTarefa(chave ed25519.PrivateKey, t *Tarefa) error {
	b, err := bytesParaAssinar(t, func(m proto.Message) { m.(*Tarefa).Assinatura = nil })
	if err != nil {
		return fmt.Errorf("assinar tarefa: %w", err)
	}
	t.Assinatura = ed25519.Sign(chave, b)
	return nil
}

// VerificarTarefa confere assinatura e prazo. O agente chama isto ANTES de qualquer
// outra coisa: tarefa sem assinatura válida nem chega ao executor.
func VerificarTarefa(pub ed25519.PublicKey, t *Tarefa, agora time.Time) error {
	if len(pub) != ed25519.PublicKeySize || len(t.GetAssinatura()) != ed25519.SignatureSize {
		return ErrAssinaturaInvalida
	}
	b, err := bytesParaAssinar(t, func(m proto.Message) { m.(*Tarefa).Assinatura = nil })
	if err != nil || !ed25519.Verify(pub, b, t.GetAssinatura()) {
		return ErrAssinaturaInvalida
	}
	if t.GetExpiraEm() > 0 && agora.Unix() > t.GetExpiraEm() {
		return ErrTarefaVencida
	}
	return nil
}

// AssinarPedidoEsquema e VerificarPedidoEsquema fazem o mesmo para o pedido de
// leitura de schema (ele carrega uma credencial selada).
func AssinarPedidoEsquema(chave ed25519.PrivateKey, p *PedidoEsquema) error {
	b, err := bytesParaAssinar(p, func(m proto.Message) { m.(*PedidoEsquema).Assinatura = nil })
	if err != nil {
		return fmt.Errorf("assinar pedido de esquema: %w", err)
	}
	p.Assinatura = ed25519.Sign(chave, b)
	return nil
}

func VerificarPedidoEsquema(pub ed25519.PublicKey, p *PedidoEsquema) error {
	if len(pub) != ed25519.PublicKeySize || len(p.GetAssinatura()) != ed25519.SignatureSize {
		return ErrAssinaturaInvalida
	}
	b, err := bytesParaAssinar(p, func(m proto.Message) { m.(*PedidoEsquema).Assinatura = nil })
	if err != nil || !ed25519.Verify(pub, b, p.GetAssinatura()) {
		return ErrAssinaturaInvalida
	}
	return nil
}

// ---------------------------------------------------------------- token de inscrição

// prefixoToken identifica a versão do formato.
const prefixoToken = "rvd1"

// ErrTokenMalformado: o texto não é um token de inscrição do Revoada.
var ErrTokenMalformado = errors.New("token de inscrição malformado")

// TokenInscricao é o que o administrador cola no instalador do agente: um segredo de
// uso único + a impressão digital (SHA-256) da CA do painel. Com a impressão digital,
// o agente reconhece o painel certo já no PRIMEIRO contato, sem confiar em ninguém.
type TokenInscricao struct {
	Segredo     string
	ImpressaoCA []byte // sha256 do certificado DER da CA
}

// String formata como rvd1.<segredo>.<sha256-hex-da-CA>.
func (t TokenInscricao) String() string {
	return prefixoToken + "." + t.Segredo + "." + hex.EncodeToString(t.ImpressaoCA)
}

// LerToken desfaz String.
func LerToken(s string) (TokenInscricao, error) {
	partes := strings.Split(strings.TrimSpace(s), ".")
	if len(partes) != 3 || partes[0] != prefixoToken || len(partes[1]) < 32 {
		return TokenInscricao{}, ErrTokenMalformado
	}
	if _, err := base64.RawURLEncoding.DecodeString(partes[1]); err != nil {
		return TokenInscricao{}, ErrTokenMalformado
	}
	fp, err := hex.DecodeString(partes[2])
	if err != nil || len(fp) != sha256.Size {
		return TokenInscricao{}, ErrTokenMalformado
	}
	return TokenInscricao{Segredo: partes[1], ImpressaoCA: fp}, nil
}

// ImpressaoDigital é o SHA-256 de um certificado DER.
func ImpressaoDigital(der []byte) []byte {
	s := sha256.Sum256(der)
	return s[:]
}

// MesmaImpressao compara em tempo constante.
func MesmaImpressao(a, b []byte) bool { return subtle.ConstantTimeCompare(a, b) == 1 }

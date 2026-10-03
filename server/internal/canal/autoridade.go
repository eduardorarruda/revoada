// Package canal é o lado do painel no canal com os agentes (ARQUITETURA §7): CA interna,
// inscrição com token de uso único, servidor gRPC sobre mTLS, presença dos agentes e
// a fila de tarefas com eventos ao vivo.
package canal

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"time"

	"github.com/eduardorarruda/revoada/core/seguranca/cofre"
	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

const (
	validadeCA       = 10 * 365 * 24 * time.Hour
	ValidadeAgente   = 30 * 24 * time.Hour // ARQUITETURA §13: certificados de agente de 30 dias
	validadeServidor = 90 * 24 * time.Hour

	nomeChaveCA         = "agentes-ca"
	nomeChaveAssinatura = "tarefas-ed25519"
)

// Autoridade guarda a CA interna dos agentes, a chave que assina as tarefas e o
// certificado TLS do servidor do canal.
type Autoridade struct {
	caCert     *x509.Certificate
	caDER      []byte
	caChave    *ecdsa.PrivateKey
	assinatura ed25519.PrivateKey
	servidor   tls.Certificate
}

// guardaChaves é o pedaço do repositório que a Autoridade usa.
type guardaChaves interface {
	ChavePainel(ctx context.Context, nome string) ([]byte, error)
	SalvarChavePainel(ctx context.Context, nome string, segredo []byte) error
}

type caGuardada struct {
	CertDER []byte `json:"cert_der"`
	Chave   []byte `json:"chave_pkcs8"`
}

// CarregarAutoridade lê a CA e a chave de assinatura (cifradas pelo cofre); cria na
// primeira vez. `hosts` são os nomes/IPs pelos quais os agentes chegam ao painel.
func CarregarAutoridade(ctx context.Context, g guardaChaves, cf *cofre.Cofre, hosts []string, agora time.Time) (*Autoridade, error) {
	a := &Autoridade{}

	var ca caGuardada
	if err := carregarOuCriar(ctx, g, cf, nomeChaveCA, &ca, func() (any, error) { return novaCA(agora) }); err != nil {
		return nil, fmt.Errorf("CA dos agentes: %w", err)
	}
	cert, err := x509.ParseCertificate(ca.CertDER)
	if err != nil {
		return nil, fmt.Errorf("CA dos agentes corrompida: %w", err)
	}
	k, err := x509.ParsePKCS8PrivateKey(ca.Chave)
	if err != nil {
		return nil, fmt.Errorf("chave da CA corrompida: %w", err)
	}
	chave, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("chave da CA não é ECDSA")
	}
	a.caCert, a.caDER, a.caChave = cert, ca.CertDER, chave

	var semente []byte
	if err := carregarOuCriar(ctx, g, cf, nomeChaveAssinatura, &semente, func() (any, error) {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		return priv.Seed(), err
	}); err != nil {
		return nil, fmt.Errorf("chave de assinatura das tarefas: %w", err)
	}
	if len(semente) != ed25519.SeedSize {
		return nil, errors.New("chave de assinatura das tarefas corrompida")
	}
	a.assinatura = ed25519.NewKeyFromSeed(semente)

	if a.servidor, err = a.emitirServidor(hosts, agora); err != nil {
		return nil, err
	}
	return a, nil
}

// carregarOuCriar lê `nome` do banco (decifrando) para `destino`; se não existe,
// gera com `criar`, cifra e grava. Corrida entre dois painéis: quem perde relê.
func carregarOuCriar(ctx context.Context, g guardaChaves, cf *cofre.Cofre, nome string, destino any, criar func() (any, error)) error {
	contexto := []byte("painel-chave:" + nome)
	for tentativa := 0; tentativa < 2; tentativa++ {
		b, err := g.ChavePainel(ctx, nome)
		if err == nil {
			var s cofre.Segredo
			if err := json.Unmarshal(b, &s); err != nil {
				return err
			}
			texto, err := cf.Decifrar(ctx, s, contexto)
			if err != nil {
				return err
			}
			defer cofre.Limpar(texto)
			return json.Unmarshal(texto, destino)
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		novo, err := criar()
		if err != nil {
			return err
		}
		texto, err := json.Marshal(novo)
		if err != nil {
			return err
		}
		s, err := cf.Cifrar(ctx, texto, contexto)
		cofre.Limpar(texto)
		if err != nil {
			return err
		}
		b, err = json.Marshal(s)
		if err != nil {
			return err
		}
		if err := g.SalvarChavePainel(ctx, nome, b); err != nil && !errors.Is(err, store.ErrConflito) {
			return err
		}
	}
	return errors.New("não foi possível criar nem ler a chave")
}

func novaCA(agora time.Time) (caGuardada, error) {
	chave, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return caGuardada{}, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          serialAleatorio(),
		Subject:               pkix.Name{CommonName: "Revoada — CA dos agentes", Organization: []string{"Revoada"}},
		NotBefore:             agora.Add(-time.Hour),
		NotAfter:              agora.Add(validadeCA),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &chave.PublicKey, chave)
	if err != nil {
		return caGuardada{}, err
	}
	pk, err := x509.MarshalPKCS8PrivateKey(chave)
	if err != nil {
		return caGuardada{}, err
	}
	return caGuardada{CertDER: der, Chave: pk}, nil
}

func (a *Autoridade) emitirServidor(hosts []string, agora time.Time) (tls.Certificate, error) {
	chave, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tpl := &x509.Certificate{
		SerialNumber: serialAleatorio(),
		Subject:      pkix.Name{CommonName: "Revoada — painel", Organization: []string{"Revoada"}},
		NotBefore:    agora.Add(-time.Hour),
		NotAfter:     agora.Add(validadeServidor),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
		} else if h != "" {
			tpl.DNSNames = append(tpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, a.caCert, &chave.PublicKey, a.caChave)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("certificado do servidor do canal: %w", err)
	}
	return tls.Certificate{Certificate: [][]byte{der, a.caDER}, PrivateKey: chave}, nil
}

// ErrCSRInvalido: pedido de certificado malformado ou com chave fora do padrão.
var ErrCSRInvalido = errors.New("pedido de certificado inválido")

// ValidarCSR confere o pedido ANTES de gastar o token de inscrição.
func ValidarCSR(csrDER []byte) (*x509.CertificateRequest, error) {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil || csr.CheckSignature() != nil {
		return nil, ErrCSRInvalido
	}
	pub, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, fmt.Errorf("%w: use ECDSA P-256", ErrCSRInvalido)
	}
	return csr, nil
}

// EmitirAgente emite o certificado de cliente do agente: CN = id do agente.
func (a *Autoridade) EmitirAgente(csrDER []byte, agenteID string, agora time.Time) (der []byte, serial string, validoAte time.Time, err error) {
	csr, err := ValidarCSR(csrDER)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	sn := serialAleatorio()
	validoAte = agora.Add(ValidadeAgente)
	tpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: agenteID, Organization: []string{"Revoada — agente"}},
		NotBefore:    agora.Add(-5 * time.Minute),
		NotAfter:     validoAte,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err = x509.CreateCertificate(rand.Reader, tpl, a.caCert, csr.PublicKey, a.caChave)
	if err != nil {
		return nil, "", time.Time{}, fmt.Errorf("emitindo certificado do agente: %w", err)
	}
	return der, SerialHex(sn), validoAte, nil
}

// TLS devolve a configuração do servidor do canal: TLS 1.3, certificado de cliente
// opcional no handshake (a inscrição ainda não tem um) e verificado contra a CA
// quando apresentado. O Conectar exige que ele exista.
func (a *Autoridade) TLS() *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(a.caCert)
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{a.servidor},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    pool,
	}
}

// CADER é o certificado da CA (vai para o agente na inscrição).
func (a *Autoridade) CADER() []byte { return a.caDER }

// ImpressaoCA é o SHA-256 da CA (vai dentro do token de inscrição).
func (a *Autoridade) ImpressaoCA() []byte { return agentev1.ImpressaoDigital(a.caDER) }

// ChavePublicaAssinatura é a chave Ed25519 que o agente usa para conferir as tarefas.
func (a *Autoridade) ChavePublicaAssinatura() ed25519.PublicKey {
	return a.assinatura.Public().(ed25519.PublicKey)
}

// AssinarTarefa assina a tarefa com a chave do painel.
func (a *Autoridade) AssinarTarefa(t *agentev1.Tarefa) error {
	return agentev1.AssinarTarefa(a.assinatura, t)
}

// AssinarPedidoEsquema assina o pedido de leitura de schema.
func (a *Autoridade) AssinarPedidoEsquema(p *agentev1.PedidoEsquema) error {
	return agentev1.AssinarPedidoEsquema(a.assinatura, p)
}

func serialAleatorio() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic("canal: sem aleatoriedade do sistema: " + err.Error())
	}
	return n
}

// SerialHex é a forma do número de série guardada no banco.
func SerialHex(n *big.Int) string { return hex.EncodeToString(n.Bytes()) }

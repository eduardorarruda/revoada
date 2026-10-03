// Package canal é o lado do agente no canal com o painel (ARQUITETURA §7): inscrição com
// token de uso único, conexão gRPC sobre mTLS (sempre aberta PELO agente), batimento,
// execução de tarefas assinadas e eventos guardados em disco até o painel confirmar.
package canal

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/eduardorarruda/revoada/core/seguranca/selo"
	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// arquivoIdentidade guarda tudo o que o agente precisa para provar quem é. Fica no
// diretório de dados do agente, com permissão 0600 (só o usuário do serviço lê).
const arquivoIdentidade = "identidade.json"

// Identidade é quem o agente é para o painel.
type Identidade struct {
	AgenteID       string            `json:"agente_id"`
	Painel         string            `json:"painel"` // host:porta do canal
	ChaveTLS       []byte            `json:"chave_tls_pkcs8"`
	CertificadoDER []byte            `json:"certificado_der"`
	CADER          []byte            `json:"ca_der"`
	ChaveSelo      []byte            `json:"chave_selo"`     // X25519 privada: abre credenciais seladas
	PubAssinatura  ed25519.PublicKey `json:"pub_assinatura"` // confere a assinatura das tarefas
	ValidoAte      int64             `json:"valido_ate"`
}

// ErrSemIdentidade: o agente ainda não foi inscrito neste painel.
var ErrSemIdentidade = errors.New("agente ainda não inscrito no canal (rode: revoada-agent inscrever)")

// Carregar lê a identidade de `dir`.
func Carregar(dir string) (*Identidade, error) {
	b, err := os.ReadFile(filepath.Join(dir, arquivoIdentidade))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrSemIdentidade
	}
	if err != nil {
		return nil, fmt.Errorf("lendo identidade: %w", err)
	}
	var id Identidade
	if err := json.Unmarshal(b, &id); err != nil {
		return nil, fmt.Errorf("identidade corrompida: %w", err)
	}
	if id.AgenteID == "" || len(id.CertificadoDER) == 0 || len(id.PubAssinatura) != ed25519.PublicKeySize {
		return nil, errors.New("identidade incompleta; inscreva o agente de novo")
	}
	return &id, nil
}

// Salvar grava de forma atômica (temporário + rename), com permissão 0600.
func (id *Identidade) Salvar(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("criando %s: %w", dir, err)
	}
	b, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".identidade-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_ = tmp.Chmod(0o600) // no Windows a proteção vem da ACL do diretório de dados
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, arquivoIdentidade))
}

// certificadoTLS monta o certificado de cliente do mTLS.
func (id *Identidade) certificadoTLS() (tls.Certificate, error) {
	k, err := x509.ParsePKCS8PrivateKey(id.ChaveTLS)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("chave TLS da identidade: %w", err)
	}
	return tls.Certificate{Certificate: [][]byte{id.CertificadoDER}, PrivateKey: k}, nil
}

// configTLS é o TLS da conexão de trabalho: confia SÓ na CA do painel (recebida na
// inscrição) e apresenta o certificado do agente.
func (id *Identidade) configTLS() (*tls.Config, error) {
	cert, err := id.certificadoTLS()
	if err != nil {
		return nil, err
	}
	ca, err := x509.ParseCertificate(id.CADER)
	if err != nil {
		return nil, fmt.Errorf("CA do painel na identidade: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	host, _, err := net.SplitHostPort(id.Painel)
	if err != nil {
		return nil, fmt.Errorf("endereço do painel %q: %w", id.Painel, err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool, ServerName: host, Certificates: []tls.Certificate{cert}}, nil
}

// novaChaveTLS gera o par ECDSA P-256 e o pedido de certificado.
func novaChaveTLS(cn string) (pkcs8, csrDER []byte, err error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	csrDER, err = x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, k)
	if err != nil {
		return nil, nil, err
	}
	pkcs8, err = x509.MarshalPKCS8PrivateKey(k)
	return pkcs8, csrDER, err
}

// Apresentacao é o que o agente conta de si na inscrição e no Olá.
type Apresentacao struct {
	Hostname, SO, Arch, Versao string
}

// Inscrever troca o token de uso único pela identidade. O painel é reconhecido pela
// impressão digital da CA que vem DENTRO do token — nada de confiar no primeiro
// certificado que aparecer.
func Inscrever(ctx context.Context, painel, token string, ap Apresentacao) (*Identidade, error) {
	tok, err := agentev1.LerToken(token)
	if err != nil {
		return nil, err
	}
	if _, _, err := net.SplitHostPort(painel); err != nil {
		return nil, fmt.Errorf("endereço do painel deve ser host:porta (ex.: painel.empresa.com:7443): %w", err)
	}
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		// A verificação padrão fica desligada só porque a CA ainda não é conhecida; a
		// verificação REAL é a de baixo: a cadeia tem de terminar na CA fixada pelo token
		// e o certificado do servidor tem de ser assinado por ela.
		InsecureSkipVerify: true, //nolint:gosec // verificado em VerifyPeerCertificate
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			return conferirCadeiaFixada(raw, tok.ImpressaoCA)
		},
	}
	conn, err := grpc.NewClient(painel, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	chaveTLS, csrDER, err := novaChaveTLS(ap.Hostname)
	if err != nil {
		return nil, err
	}
	par, err := selo.GerarPar()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := agentev1.NewCanalClient(conn).Inscrever(ctx, &agentev1.PedidoInscricao{
		Token: token, CsrDer: csrDER, ChaveSelo: par.PublicKey().Bytes(),
		Hostname: ap.Hostname, So: ap.SO, Arch: ap.Arch, Versao: ap.Versao,
	})
	if err != nil {
		return nil, fmt.Errorf("o painel recusou a inscrição: %w", err)
	}
	if !agentev1.MesmaImpressao(agentev1.ImpressaoDigital(resp.GetCaDer()), tok.ImpressaoCA) {
		return nil, errors.New("a CA devolvida pelo painel não é a do token")
	}
	return &Identidade{
		AgenteID: resp.GetAgenteId(), Painel: painel, ChaveTLS: chaveTLS, CertificadoDER: resp.GetCertificadoDer(),
		CADER: resp.GetCaDer(), ChaveSelo: par.Bytes(), PubAssinatura: resp.GetChaveAssinaturaPainel(),
		ValidoAte: resp.GetValidoAte(),
	}, nil
}

// conferirCadeiaFixada: a última da cadeia é a CA do token e o primeiro (o
// certificado do servidor) foi assinado por ela.
func conferirCadeiaFixada(raw [][]byte, impressao []byte) error {
	if len(raw) < 2 {
		return errors.New("o painel não apresentou a cadeia completa")
	}
	caDER := raw[len(raw)-1]
	if !agentev1.MesmaImpressao(agentev1.ImpressaoDigital(caDER), impressao) {
		return errors.New("este painel não é o do token (CA diferente) — inscrição recusada")
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return err
	}
	folha, err := x509.ParseCertificate(raw[0])
	if err != nil {
		return err
	}
	return folha.CheckSignatureFrom(ca)
}

// abrirSelo abre uma credencial selada para este agente (usado pelas tarefas).
func (id *Identidade) abrirSelo(c *agentev1.CredencialSelada, contexto string, agora time.Time) ([]byte, error) {
	priv, err := ecdh.X25519().NewPrivateKey(id.ChaveSelo)
	if err != nil {
		return nil, err
	}
	return selo.Abrir(priv, selo.Selo{Efemera: c.GetEfemera(), Nonce: c.GetNonce(), Cifrado: c.GetCifrado(), ExpiraEm: c.GetExpiraEm()}, contexto, agora)
}

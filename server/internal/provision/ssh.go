package provision

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
)

// AuthType distingue as duas formas de autenticação SSH suportadas.
type AuthType string

const (
	AuthPassword AuthType = "password"
	AuthKey      AuthType = "key" // chave privada PEM
)

// Target descreve o destino SSH. O campo secret (senha OU chave privada PEM) é
// NÃO EXPORTADO de propósito: só o pacote provision o preenche/lê. String() e
// LogValue() redigem tudo, então um Target nunca vaza a credencial em log/erro.
type Target struct {
	Host      string
	Port      int
	User      string
	AuthType  AuthType
	HostKeyFP string // fingerprint SHA256 pinado (TOFU); vazio = primeira conexão

	secret []byte // senha (bytes) ou chave privada PEM — REDIGIDO em qualquer log
}

// NewTarget monta um Target. secret é copiado para o campo interno.
func NewTarget(host string, port int, user string, at AuthType, secret []byte, pinnedFP string) Target {
	if port == 0 {
		port = 22
	}
	cp := make([]byte, len(secret))
	copy(cp, secret)
	return Target{Host: host, Port: port, User: user, AuthType: at, HostKeyFP: pinnedFP, secret: cp}
}

// String redige a credencial — usado em qualquer formatação acidental.
func (t Target) String() string {
	return fmt.Sprintf("Target{host:%s port:%d user:%s auth:%s secret:REDACTED}", t.Host, t.Port, t.User, t.AuthType)
}

// LogValue garante que slog nunca serialize o secret.
func (t Target) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("host", t.Host),
		slog.Int("port", t.Port),
		slog.String("user", t.User),
		slog.String("auth", string(t.AuthType)),
		slog.String("secret", "REDACTED"),
	)
}

// Runner abstrai a abertura de uma sessão SSH (mockável em teste).
type Runner interface {
	Connect(ctx context.Context, t Target) (Session, error)
}

// Session é uma conexão SSH viva: roda comandos, expõe o fingerprint do host key
// efetivamente visto e fecha.
type Session interface {
	Run(ctx context.Context, cmd string) (stdout string, err error)
	HostKey() string
	Close() error
}

// sshRunner é a implementação real com golang.org/x/crypto/ssh.
type sshRunner struct {
	timeout time.Duration
}

// NewSSHRunner cria o Runner real. timeout=0 usa 20s.
func NewSSHRunner() Runner {
	return &sshRunner{timeout: 20 * time.Second}
}

func (r *sshRunner) Connect(ctx context.Context, t Target) (Session, error) {
	authMethods, err := authMethods(t)
	if err != nil {
		return nil, err // já redigido (não inclui o secret)
	}

	var seenFP string
	// HostKeyCallback: TOFU + pin. Nunca InsecureIgnoreHostKey.
	//   - HostKeyFP vazio  => primeira conexão: aceita e registra (o caller fixa o fp).
	//   - HostKeyFP setado => exige igualdade; qualquer divergência rejeita (MITM/troca).
	cb := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		seenFP = ssh.FingerprintSHA256(key)
		return hostKeyMatch(t.Host, t.HostKeyFP, seenFP)
	}

	cfg := &ssh.ClientConfig{
		User:            t.User,
		Auth:            authMethods,
		HostKeyCallback: cb,
		Timeout:         r.timeout,
	}

	addr := net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
	d := net.Dialer{Timeout: r.timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("conexão TCP com %s falhou: %w", addr, err)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		// err de auth pode, em teoria, referenciar métodos; não inclui o secret.
		return nil, fmt.Errorf("handshake SSH com %s falhou: %w", addr, err)
	}
	client := ssh.NewClient(c, chans, reqs)
	return &sshSession{client: client, hostKeyFP: seenFP, timeout: r.timeout}, nil
}

// hostKeyMatch implementa a política TOFU/pin: pin vazio aceita (primeira conexão);
// pin setado exige igualdade exata com o fingerprint visto. Pura, para testar sem SSH.
func hostKeyMatch(host, pinnedFP, seenFP string) error {
	if pinnedFP != "" && pinnedFP != seenFP {
		return fmt.Errorf("host key divergente para %s: esperado %s, recebido %s (possível MITM/troca de host)", host, pinnedFP, seenFP)
	}
	return nil
}

// authMethods traduz o Target em métodos de auth do x/crypto/ssh, sem vazar o
// secret em mensagens de erro.
func authMethods(t Target) ([]ssh.AuthMethod, error) {
	switch t.AuthType {
	case AuthPassword:
		if len(t.secret) == 0 {
			return nil, errors.New("senha SSH vazia")
		}
		return []ssh.AuthMethod{ssh.Password(string(t.secret))}, nil
	case AuthKey:
		if len(t.secret) == 0 {
			return nil, errors.New("chave privada SSH vazia")
		}
		signer, err := ssh.ParsePrivateKey(t.secret)
		if err != nil {
			// NÃO envolve err original: ele pode conter bytes/estrutura da chave.
			return nil, errors.New("chave privada SSH inválida ou protegida por passphrase")
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	default:
		return nil, fmt.Errorf("auth_type inválido: %q (use %q ou %q)", t.AuthType, AuthPassword, AuthKey)
	}
}

type sshSession struct {
	client    *ssh.Client
	hostKeyFP string
	timeout   time.Duration
}

func (s *sshSession) HostKey() string { return s.hostKeyFP }

func (s *sshSession) Run(ctx context.Context, cmd string) (string, error) {
	sess, err := s.client.NewSession()
	if err != nil {
		return "", fmt.Errorf("abrir sessão SSH: %w", err)
	}
	defer func() { _ = sess.Close() }()

	var out bytes.Buffer
	sess.Stdout = &out
	sess.Stderr = &out

	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()

	select {
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		return out.String(), ctx.Err()
	case err := <-done:
		return out.String(), err
	}
}

func (s *sshSession) Close() error {
	if s.client == nil {
		return nil
	}
	return s.client.Close()
}

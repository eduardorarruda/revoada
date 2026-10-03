package probe

// GUARDA CONTRA SSRF NA SONDA DO AGENTE — CÓPIA DE server/internal/safehttp
//
// A sonda do agente usava `&http.Client{Timeout: 15s}` puro: sem dialer guardado,
// sem bloqueio de IP interno, sem CheckRedirect. Isso importa mais aqui do que na
// central, porque a URL é DIGITADA PELO USUÁRIO no painel e a sonda roda DENTRO
// DA REDE DO CLIENTE — quem cadastra um check escolhe, na prática, o que a sonda
// vai acessar lá dentro.
//
// Medido: `http://169.254.169.254/latest/meta-data/` (metadata da nuvem) ficou
// pendurado 15 s pelo agente, tentando de verdade; pela sonda central é recusado
// em 0 ms.
//
// Por que copiar em vez de importar: `server/internal/safehttp` é um pacote
// `internal` de OUTRO módulo Go, e a regra `internal` do Go proíbe o módulo agent
// de importá-lo. O caminho definitivo (promover safehttp a um módulo comum) está
// descrito no relatório da frente. Somente stdlib, como o original.

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
)

// ErrBlockedAddress é devolvido quando um destino resolve para faixa proibida.
var ErrBlockedAddress = errors.New("safehttp: destino bloqueado (endereço interno/privado)")

// strict fecha também loopback/privado. Desligado por padrão: sondar serviço
// interno é uso legítimo de uma ferramenta de monitoramento — e a sonda existe
// justamente para medir de dentro da rede do cliente.
var strict = os.Getenv("REVOADA_SSRF_STRICT") == "1"

var extraBlocked = func() []*net.IPNet {
	cidrs := []string{
		"100.64.0.0/10", // CGNAT (RFC 6598)
		"192.0.0.0/24",  // IETF protocol assignments (RFC 6890)
		"::1/128",       // loopback IPv6
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			nets = append(nets, n)
		}
	}
	return nets
}()

func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// SEMPRE bloqueado: link-local (inclui 169.254.169.254, o metadata da nuvem) e
	// endereço não especificado — nunca são alvos legítimos de monitoramento.
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	if strict {
		if ip.IsLoopback() || ip.IsPrivate() {
			return true
		}
		for _, n := range extraBlocked {
			if n.Contains(ip) {
				return true
			}
		}
	}
	return false
}

// controlGuard roda DEPOIS do DNS (address já é "ip:port"), então cobre
// DNS-rebinding e cada salto de redirect.
func controlGuard(network, address string, _ syscall.RawConn) error {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return fmt.Errorf("%w: rede não permitida %q", ErrBlockedAddress, network)
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: endereço inválido %q", ErrBlockedAddress, address)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("%w: IP inválido %q", ErrBlockedAddress, host)
	}
	if isBlockedIP(ip) {
		return fmt.Errorf("%w: %s", ErrBlockedAddress, ip)
	}
	return nil
}

// safeTransport devolve um transport com o dialer guardado.
func safeTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			Control:   controlGuard,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// checkRedirect barra esquemas não-http(s) e limita os saltos. O Control do
// dialer revalida o IP em cada salto, então aqui só cuidamos disso.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("%w: excesso de redirects", ErrBlockedAddress)
	}
	return validateURL(req.URL.String())
}

// validateURL exige esquema http ou https.
func validateURL(raw string) error {
	if i := strings.Index(raw, ":"); i > 0 {
		scheme := strings.ToLower(raw[:i])
		if scheme == "http" || scheme == "https" {
			return nil
		}
		return fmt.Errorf("%w: esquema não permitido %q", ErrBlockedAddress, scheme)
	}
	return fmt.Errorf("%w: URL sem esquema", ErrBlockedAddress)
}

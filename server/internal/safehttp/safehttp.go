// Package safehttp fornece transporte HTTP endurecido contra SSRF. Um dialer
// guardado (net.Dialer.Control) roda APÓS a resolução de DNS — o "address" já é
// "ip:port" — então bloqueia IPs privados/internos mesmo com DNS-rebinding e em
// cada salto de redirect. CheckRedirect ainda barra esquemas não-http(s) e limita
// o número de redirects. Somente stdlib.
package safehttp

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

// ErrBlockedAddress é retornado quando um destino resolve para uma faixa proibida.
var ErrBlockedAddress = errors.New("safehttp: destino bloqueado (endereço interno/privado)")

// ErrTooManyRedirects é o laço de redirecionamento: o alvo mandou o cliente de
// volta mais vezes do que o limite. NÃO é ErrBlockedAddress.
//
// Por que um erro próprio: o excesso de saltos vinha embrulhado em
// ErrBlockedAddress, então classifyErr devolvia `bloqueado_pelo_painel`, o
// checker marcava Result.Blocked e RETORNAVA antes de writeMetrics e da máquina
// de estados. Resultado medido com um servidor local em laço 302
// (`ok=false blocked=true diag="bloqueado_pelo_painel" status=0`): o site estava
// inutilizável para qualquer visitante (loop http↔https, .htaccess quebrado,
// WordPress com siteurl errado) e o painel o mantinha UP para sempre, com o
// uptime intacto e sem alerta nenhum. Laço de redirect é falha REAL do alvo; a
// recusa do guard SSRF é decisão NOSSA. Misturar as duas apagava a única que
// precisa acordar alguém.
var ErrTooManyRedirects = errors.New("safehttp: laço de redirecionamento (excesso de saltos)")

// MaxRedirects é o número máximo de saltos aceitos numa sondagem.
const MaxRedirects = 10

// strict fecha também loopback/privado (multi-tenant/hostil). Por padrão desligado:
// monitorar serviços internos/localhost é uso legítimo de uma ferramenta de monitoramento.
var strict = os.Getenv("REVOADA_SSRF_STRICT") == "1"

// extraBlocked lista faixas adicionais não cobertas pelos helpers de net.IP.
var extraBlocked = func() []*net.IPNet {
	cidrs := []string{
		"100.64.0.0/10", // CGNAT (RFC 6598)
		"192.0.0.0/24",  // IETF protocol assignments (RFC 6890)
		"::1/128",       // loopback IPv6 (redundante com IsLoopback, mantido por robustez)
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			nets = append(nets, n)
		}
	}
	return nets
}()

// isBlockedIP decide se um IP resolvido deve ser recusado.
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// SEMPRE bloqueado: link-local (inclui o metadata da nuvem, 169.254.169.254) e
	// endereço não especificado — nunca são alvos legítimos de monitoramento.
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	// Loopback/privado/CGNAT: só no modo estrito (opt-in via REVOADA_SSRF_STRICT=1).
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

// controlGuard roda depois do DNS: address é "ip:port". Recusa não-TCP e IPs
// em faixas internas/privadas — cobre DNS-rebinding e cada salto de redirect.
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

// guardedDialer é um net.Dialer com defaults sãos e o Control de segurança.
func guardedDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   controlGuard,
	}
}

// Transport devolve um *http.Transport com o dialer guardado, baseado em defaults
// semelhantes ao http.DefaultTransport. O chamador pode ajustar campos como
// DisableKeepAlives ou TLSClientConfig no *http.Transport retornado.
func Transport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           guardedDialer().DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// CheckRedirect barra esquemas não-http(s) e limita o número de saltos. O Control
// do dialer revalida o IP em cada salto, então aqui só cuidamos de esquema/limite.
//
// O estouro de saltos devolve ErrTooManyRedirects (falha do ALVO), nunca
// ErrBlockedAddress (recusa NOSSA) — ver o comentário de ErrTooManyRedirects.
func CheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= MaxRedirects {
		return fmt.Errorf("%w: %d saltos", ErrTooManyRedirects, len(via))
	}
	if err := ValidateURL(req.URL.String()); err != nil {
		return err
	}
	return nil
}

// ValidateURL exige esquema http ou https. Usável na criação do alvo.
func ValidateURL(raw string) error {
	if i := strings.Index(raw, ":"); i > 0 {
		scheme := strings.ToLower(raw[:i])
		if scheme == "http" || scheme == "https" {
			return nil
		}
		return fmt.Errorf("%w: esquema não permitido %q", ErrBlockedAddress, scheme)
	}
	return fmt.Errorf("%w: URL sem esquema", ErrBlockedAddress)
}

// Package ipcliente identifica o IP de quem fez a requisição, para o rate-limit por
// IP e para a trilha de auditoria — os dois precisam da MESMA resposta.
//
// O X-Forwarded-For é escrito por QUEM quiser: se ele for lido sem antes conferir de
// onde a conexão veio, o balde do rate-limit deixa de existir e a auditoria grava o
// IP que o cliente inventou. Medido em dev, numa versão anterior do rate-limit: 15
// logins errados do mesmo IP deram 401×10 + 429×5; as 6 tentativas seguintes, só
// trocando `X-Forwarded-For: 203.0.113.N`, deram 401×6 e NENHUM 429 — brute-force
// ilimitado com um header. A auditoria tinha a mesma falha por mais tempo: lia o
// primeiro item do header de qualquer peer.
//
// Regra (a mesma do gateway): o XFF só é olhado quando o peer direto pertence a
// REVOADA_TRUSTED_PROXIES. Peer não confiável ⇒ o header é ignorado por completo e
// vale o RemoteAddr, que o cliente não escolhe.
package ipcliente

import (
	"net"
	"net/http"
	"strings"

	"github.com/eduardorarruda/revoada/server/internal/config"
)

// redesConfiaveis são as redes cujo X-Forwarded-For pode ser acreditado. Lidas UMA
// vez (a lista vem de env e não muda em runtime); entrada inválida é ignorada.
var redesConfiaveis = parseCIDRs(config.TrustedProxies())

func parseCIDRs(list []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(list))
	for _, c := range list {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// ehProxyConfiavel diz se um IP pertence a alguma rede de proxy confiável.
func ehProxyConfiavel(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range redesConfiaveis {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// De devolve o IP do cliente da requisição.
//
// Com o peer confiável, lemos o XFF da DIREITA para a ESQUERDA descartando os saltos
// que também são proxies confiáveis (a cadeia real em produção é cliente → Traefik →
// nginx → server, e cada salto ANEXA o anterior, então os últimos elementos são IPs
// internos). O primeiro que NÃO é proxy confiável é o cliente que a borda observou; o
// que o cliente tenha inventado fica à esquerda disso e nunca é alcançado.
func De(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !ehProxyConfiavel(net.ParseIP(host)) {
		return host
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ip := net.ParseIP(strings.TrimSpace(parts[i]))
			if ip == nil || ehProxyConfiavel(ip) {
				continue // inválido, ou salto interno da própria cadeia de proxies
			}
			return ip.String()
		}
	}
	return host
}

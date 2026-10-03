package server

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/eduardorarruda/revoada/gateway/internal/metrics"
	"golang.org/x/time/rate"
)

// RateLimiter limita a taxa de ingestão POR ENDEREÇO DE ORIGEM (IP), com token bucket.
//
// POR QUE não pela serverkey: até esta versão o balde era o header `X-Revoada-Key` —
// um valor ESCOLHIDO PELO CLIENTE e lido ANTES de qualquer validação. Medido em dev:
// 4000 POSTs com chave fixa passaram sem 429; 4000 POSTs com chave ROTATIVA sustentaram
// 654 req/s (acima do teto configurado de 500) com ZERO 429 — cada chave inventada
// ganhava um balde novo. Pior, cada chave nova criava uma entrada negativa permanente
// no cache de auth: 120 mil chaves distintas levaram o gateway de 32 para 102 MiB.
// Quem chaveia o limite é a rede, não o atacante.
//
// O CUSTO ACEITO: vários agentes atrás do mesmo NAT dividem o mesmo balde. Ver
// defaultRPS em config para o dimensionamento.
//
// Limiters ociosos são varridos periodicamente e o número de baldes tem TETO, para
// que o próprio limitador não vire o vazamento de memória que ele deveria conter.
type RateLimiter struct {
	mu      sync.Mutex
	m       map[string]*rlEntry
	rate    rate.Limit
	burst   int
	ttl     time.Duration // remove limiter após esse tempo sem uso
	maxKeys int           // teto de baldes vivos (proteção de memória)
	now     func() time.Time

	// trusted são as redes de proxies confiáveis (Traefik/LB). Só para uma origem
	// nesta lista o X-Forwarded-For é levado a sério; de qualquer outra ele é um
	// header inventado pelo cliente e seria a MESMA falha que estamos corrigindo.
	trusted []*net.IPNet
}

type rlEntry struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

// maxRateLimitKeys é o teto de IPs distintos com balde vivo. Uma /24 inteira cabe
// folgada; acima disso é varredura, e a varredura não pode custar memória sem fim.
const maxRateLimitKeys = 50000

var (
	rateLimited = metrics.NewCounter(
		"revoada_ingest_rate_limited_total",
		"Requisições recusadas com 429 pelo rate limit por IP de origem.")
	rateLimitBuckets = metrics.NewCounter(
		"revoada_ingest_rate_limit_buckets_evicted_total",
		"Baldes de rate limit despejados por teto de memória.")
)

// NewRateLimiter cria o limiter com rps (tokens/segundo) e burst.
func NewRateLimiter(rps, burst int) *RateLimiter {
	if rps < 1 {
		rps = 1
	}
	if burst < 1 {
		burst = 1
	}
	return &RateLimiter{
		m:       make(map[string]*rlEntry),
		rate:    rate.Limit(rps),
		burst:   burst,
		ttl:     10 * time.Minute,
		maxKeys: maxRateLimitKeys,
		now:     time.Now,
	}
}

// SetTrustedProxies define as redes (CIDR) cujo X-Forwarded-For é confiável. Entradas
// inválidas são ignoradas. Sem isto, o gateway atrás de um proxy vê o IP do PROXY em
// todas as requisições e todo mundo compartilha um balde só.
func (rl *RateLimiter) SetTrustedProxies(cidrs []string) {
	var nets []*net.IPNet
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !strings.Contains(c, "/") { // IP solto vira /32 ou /128
			if ip := net.ParseIP(c); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			}
			continue
		}
		if _, n, err := net.ParseCIDR(c); err == nil {
			nets = append(nets, n)
		}
	}
	rl.mu.Lock()
	rl.trusted = nets
	rl.mu.Unlock()
}

// allow consome um token para key; false => estourou (deve virar 429).
func (rl *RateLimiter) allow(key string) bool {
	now := rl.now()
	rl.mu.Lock()
	e, ok := rl.m[key]
	if !ok {
		rl.evictLocked(now)
		e = &rlEntry{lim: rate.NewLimiter(rl.rate, rl.burst)}
		rl.m[key] = e
	}
	e.lastSeen = now
	lim := e.lim
	rl.mu.Unlock()
	return lim.Allow()
}

// evictLocked garante o teto de baldes removendo o mais antigo quando cheio.
func (rl *RateLimiter) evictLocked(now time.Time) {
	if len(rl.m) < rl.maxKeys {
		return
	}
	var oldestKey string
	var oldest time.Time
	for k, e := range rl.m {
		if oldestKey == "" || e.lastSeen.Before(oldest) {
			oldestKey, oldest = k, e.lastSeen
		}
	}
	delete(rl.m, oldestKey)
	rateLimitBuckets.Inc()
}

// isTrusted diz se ip pertence a alguma rede de proxy confiável.
func (rl *RateLimiter) isTrusted(ip net.IP) bool {
	rl.mu.Lock()
	nets := rl.trusted
	rl.mu.Unlock()
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientKey identifica o cliente pelo IP de ORIGEM (nunca por header escolhido pelo
// cliente), a partir do endereço do peer direto e do X-Forwarded-For BRUTO. O
// X-Forwarded-For só é considerado quando o peer direto é um proxy declarado como
// confiável — e, mesmo aí, usamos o ÚLTIMO salto (o que o proxy confiável observou),
// porque os anteriores podem ter sido forjados pelo cliente.
//
// Recebe strings (e não *http.Request) porque o MESMO balde precisa valer para o
// OTLP/gRPC: lá o peer vem de peer.FromContext e o X-Forwarded-For de metadata.
func (rl *RateLimiter) ClientKey(remoteAddr, xff string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	peer := net.ParseIP(host)
	if peer != nil && rl.isTrusted(peer) && xff != "" {
		parts := strings.Split(xff, ",")
		cand := strings.TrimSpace(parts[len(parts)-1])
		if ip := net.ParseIP(cand); ip != nil {
			return "ip:" + ip.String()
		}
	}
	return "ip:" + host
}

// Allow decide se a requisição passa E CONTA a recusa em revoada_ingest_rate_limited_total.
//
// POR QUE é exportada e independente de http.Request: o freio existia SÓ nas rotas
// HTTP (rl.Wrap), enquanto o listener OTLP/gRPC de :4317 — que recebe métricas, logs e
// traces — subia com grpc.NewServer() sem interceptor nenhum. Medido na mesma máquina,
// mesma carga e mesma chave inválida: HTTP → 6.000 requisições, 3.590 recusadas com 429
// (delta de +3590 no contador); gRPC → as 6.000 passaram, ZERO rejeições, delta ZERO no
// contador, a 10.374 req/s. Toda requisição autenticava contra o Postgres, que é o mesmo
// banco das sessões do painel e do cofre de credenciais SSH. Quem quisesse burlar o
// limite só precisava trocar de porta.
func (rl *RateLimiter) Allow(remoteAddr, xff string) bool {
	if rl.allow(rl.ClientKey(remoteAddr, xff)) {
		return true
	}
	rateLimited.Inc()
	return false
}

// Wrap embrulha um handler de ingestão com o rate limit por IP.
func (rl *RateLimiter) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.Allow(r.RemoteAddr, r.Header.Get("X-Forwarded-For")) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limit excedido", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Run varre limiters ociosos periodicamente até o ctx ser cancelado.
func (rl *RateLimiter) Run(ctx context.Context) {
	t := time.NewTicker(rl.ttl)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rl.sweep()
		}
	}
}

func (rl *RateLimiter) sweep() {
	cutoff := rl.now().Add(-rl.ttl)
	rl.mu.Lock()
	for k, e := range rl.m {
		if e.lastSeen.Before(cutoff) {
			delete(rl.m, k)
		}
	}
	rl.mu.Unlock()
}

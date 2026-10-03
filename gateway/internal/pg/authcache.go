package pg

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/eduardorarruda/revoada/gateway/internal/metrics"
)

// authCache guarda o resultado de Authenticate por serverkey com TTL curto, para
// desacoplar a ingestão do PostgreSQL a cada requisição. Cacheia tanto entradas
// POSITIVAS (chave válida, mesmo revogada) quanto NEGATIVAS (chave desconhecida),
// para não martelar o PG com chaves ruins. Revogações/mudanças refletem dentro do
// TTL (comportamento aceito). Erros transitórios de PG NÃO são cacheados.
//
// POR QUE tem teto e varredura: antes desta versão o mapa só crescia — não havia
// sweep nem limite, e a entrada expirada continuava ocupando memória para sempre.
// Como a entrada NEGATIVA é criada por qualquer chave inventada, bastava uma rajada
// com chave rotativa para inflar o processo. Medido em dev: 120 mil chaves distintas
// levaram o gateway de 32 para 102 MiB. As negativas ganham cota e TTL PRÓPRIOS,
// bem menores: elas existem só para amortecer uma rajada de lixo, não para lembrar
// dela.
type authCache struct {
	mu     sync.RWMutex
	m      map[string]authEntry
	ttl    time.Duration // TTL de entradas positivas
	negTTL time.Duration // TTL de entradas negativas (<= ttl)
	now    func() time.Time

	maxPos int // teto de entradas positivas (chaves reais cadastradas)
	maxNeg int // teto de entradas negativas (lixo/varredura)
	nNeg   int // negativas vivas (contabilizadas para a cota)
}

type authEntry struct {
	agent Agent
	err   error // nil (positiva) ou ErrUnknownAgent (negativa)
	exp   time.Time
}

func (e authEntry) negative() bool { return e.err != nil }

// Dimensionamento do cache.
//
//   - maxAuthPositive = 20 000 chaves. A instalação tem centenas de agentes; 20 mil é
//     ~2 ordens de grandeza de folga e ainda assim um teto (≈ poucos MiB).
//   - maxAuthNegative = 4 096 chaves inválidas. Amortece uma rajada de varredura sem
//     virar memória. Estourando, a entrada mais nova simplesmente não é cacheada — a
//     próxima requisição com aquela chave bate no PG, que é exatamente o que o rate
//     limit por IP existe para conter.
//   - negTTL = 5 s (em vez do TTL positivo de 30 s). Chave inválida não precisa ser
//     lembrada por meio minuto; 5 s já corta 99% da repetição de uma rajada.
const (
	maxAuthPositive = 20000
	maxAuthNegative = 4096
	authNegTTL      = 5 * time.Second
	authSweepEvery  = 30 * time.Second
)

var (
	authCacheEntries = metrics.NewCounter(
		"revoada_auth_cache_evicted_total",
		"Entradas do cache de autenticação removidas por varredura ou teto.")
	authNegRejected = metrics.NewCounter(
		"revoada_auth_cache_negative_refused_total",
		"Entradas negativas não cacheadas por estouro de cota (chave inválida em rajada).")
)

func newAuthCache(ttl time.Duration) *authCache {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	negTTL := authNegTTL
	if negTTL > ttl {
		negTTL = ttl
	}
	c := &authCache{
		m:      make(map[string]authEntry),
		ttl:    ttl,
		negTTL: negTTL,
		now:    time.Now,
		maxPos: maxAuthPositive,
		maxNeg: maxAuthNegative,
	}
	metrics.NewGaugeFunc("revoada_auth_cache_size",
		"Entradas vivas no cache de autenticação de serverkey.",
		func() float64 {
			c.mu.RLock()
			defer c.mu.RUnlock()
			return float64(len(c.m))
		})
	return c
}

// lookup devolve a entrada cacheada se ainda válida; caso contrário chama load,
// cacheando o resultado quando ele for conclusivo (sucesso ou ErrUnknownAgent).
func (c *authCache) lookup(key string, load func() (Agent, error)) (Agent, error) {
	now := c.now()

	c.mu.RLock()
	e, ok := c.m[key]
	c.mu.RUnlock()
	if ok && now.Before(e.exp) {
		return e.agent, e.err
	}

	a, err := load()
	switch {
	case err == nil:
		c.put(key, authEntry{agent: a, exp: now.Add(c.ttl)})
	case errors.Is(err, ErrUnknownAgent):
		c.put(key, authEntry{err: ErrUnknownAgent, exp: now.Add(c.negTTL)})
	default:
		// Erro transitório (PG fora, timeout): não cacheia — a próxima
		// requisição tenta de novo.
	}
	return a, err
}

// put grava respeitando as cotas separadas de positivas e negativas.
func (c *authCache) put(key string, e authEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	old, existed := c.m[key]
	if existed && old.negative() {
		c.nNeg--
	}
	if e.negative() {
		if !existed && c.nNeg >= c.maxNeg {
			c.sweepLocked(c.now()) // tenta abrir espaço expurgando o que já venceu
			if c.nNeg >= c.maxNeg {
				authNegRejected.Inc()
				return // rajada de lixo não derruba as entradas boas do cache
			}
		}
		c.nNeg++
	} else if !existed && len(c.m)-c.nNeg >= c.maxPos {
		c.sweepLocked(c.now())
		if len(c.m)-c.nNeg >= c.maxPos {
			return
		}
	}
	c.m[key] = e
}

// sweep remove as entradas vencidas. Chamado periodicamente por Run e sob pressão
// de cota — sem ele, a entrada expirada continua ocupando memória indefinidamente
// (o mapa do Go não encolhe sozinho, mas ao menos deixa de crescer).
func (c *authCache) sweep() {
	c.mu.Lock()
	c.sweepLocked(c.now())
	c.mu.Unlock()
}

func (c *authCache) sweepLocked(now time.Time) {
	var n int64
	for k, e := range c.m {
		if now.Before(e.exp) {
			continue
		}
		if e.negative() {
			c.nNeg--
		}
		delete(c.m, k)
		n++
	}
	if n > 0 {
		authCacheEntries.Add(n)
	}
}

// Run varre o cache periodicamente até o ctx ser cancelado.
func (c *authCache) Run(ctx context.Context) {
	t := time.NewTicker(authSweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.sweep()
		}
	}
}

package pg

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// TestAuthCacheHit: uma segunda leitura dentro do TTL não chama o loader.
func TestAuthCacheHit(t *testing.T) {
	c := newAuthCache(30 * time.Second)
	now := time.Unix(0, 0)
	c.now = func() time.Time { return now }

	var calls int
	load := func() (Agent, error) {
		calls++
		return Agent{TenantID: "t1"}, nil
	}
	if a, err := c.lookup("k", load); err != nil || a.TenantID != "t1" {
		t.Fatalf("lookup1 = %+v, %v", a, err)
	}
	if a, err := c.lookup("k", load); err != nil || a.TenantID != "t1" {
		t.Fatalf("lookup2 = %+v, %v", a, err)
	}
	if calls != 1 {
		t.Fatalf("loader chamado %d vezes, quer 1 (cache hit)", calls)
	}
}

// TestAuthCacheMissNegative: chave desconhecida é cacheada (negativa) e não rebate no loader.
func TestAuthCacheNegative(t *testing.T) {
	c := newAuthCache(30 * time.Second)
	now := time.Unix(0, 0)
	c.now = func() time.Time { return now }

	var calls int
	load := func() (Agent, error) {
		calls++
		return Agent{}, ErrUnknownAgent
	}
	for i := 0; i < 3; i++ {
		if _, err := c.lookup("bad", load); !errors.Is(err, ErrUnknownAgent) {
			t.Fatalf("esperava ErrUnknownAgent, veio %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("loader chamado %d vezes, quer 1 (negativa cacheada)", calls)
	}
}

// TestAuthCacheExpiry: passado o TTL, o loader é chamado de novo.
func TestAuthCacheExpiry(t *testing.T) {
	c := newAuthCache(30 * time.Second)
	now := time.Unix(0, 0)
	c.now = func() time.Time { return now }

	var calls int
	load := func() (Agent, error) {
		calls++
		return Agent{TenantID: "t1"}, nil
	}
	c.lookup("k", load) // calls=1
	now = now.Add(31 * time.Second)
	c.lookup("k", load) // expirou -> calls=2
	if calls != 2 {
		t.Fatalf("loader chamado %d vezes, quer 2 (expiração)", calls)
	}
}

// TestAuthCacheTransientNotCached: erro transitório (não-ErrUnknownAgent) não é cacheado.
func TestAuthCacheTransientNotCached(t *testing.T) {
	c := newAuthCache(30 * time.Second)
	now := time.Unix(0, 0)
	c.now = func() time.Time { return now }

	transient := errors.New("pg fora")
	var calls int
	load := func() (Agent, error) {
		calls++
		return Agent{}, transient
	}
	c.lookup("k", load)
	c.lookup("k", load)
	if calls != 2 {
		t.Fatalf("erro transitório não deve cachear: calls=%d, quer 2", calls)
	}
}

// TestAuthCacheConcurrent: uso concorrente não gera race (rodar com -race).
func TestAuthCacheConcurrent(t *testing.T) {
	c := newAuthCache(50 * time.Millisecond)
	load := func() (Agent, error) { return Agent{TenantID: "t"}, nil }
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				c.lookup("k", load)
			}
		}()
	}
	wg.Wait()
}

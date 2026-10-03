package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestRateLimitBurstThen429: dentro do burst passa; acima, 429. IPs distintos têm
// baldes independentes — e a chave enviada pelo cliente NÃO cria balde novo (era a
// falha: chave rotativa sustentava 654 req/s sem um único 429).
func TestRateLimitBurstThen429(t *testing.T) {
	rl := NewRateLimiter(1, 3) // 1 req/s, burst 3
	// congela o tempo para não repor tokens durante o teste.
	rl.now = func() time.Time { return time.Unix(0, 0) }

	h := rl.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	call := func(ip, key string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/logs", nil)
		req.RemoteAddr = ip + ":5555"
		req.Header.Set("X-Revoada-Key", key)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := 0; i < 3; i++ {
		if code := call("198.51.100.1", "a"); code != http.StatusOK {
			t.Fatalf("req %d = %d, quer 200", i, code)
		}
	}
	// 4ª do MESMO IP estoura, mesmo trocando a chave a cada requisição.
	if code := call("198.51.100.1", "chave-rotativa-1"); code != http.StatusTooManyRequests {
		t.Fatalf("4ª req do mesmo IP com chave nova = %d, quer 429", code)
	}
	if code := call("198.51.100.1", "chave-rotativa-2"); code != http.StatusTooManyRequests {
		t.Fatalf("5ª req do mesmo IP com chave nova = %d, quer 429", code)
	}
	// Outro IP tem balde próprio.
	if code := call("198.51.100.2", "a"); code != http.StatusOK {
		t.Fatalf("outro IP = %d, quer 200", code)
	}
}

// TestRateLimitSweep: limiters ociosos são removidos após o TTL.
func TestRateLimitSweep(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	now := time.Unix(0, 0)
	rl.now = func() time.Time { return now }
	rl.allow("ip:x")
	if len(rl.m) != 1 {
		t.Fatalf("esperava 1 limiter, veio %d", len(rl.m))
	}
	now = now.Add(11 * time.Minute) // > ttl (10m)
	rl.sweep()
	if len(rl.m) != 0 {
		t.Fatalf("esperava sweep remover limiter ocioso, restaram %d", len(rl.m))
	}
}

// TestRateLimitBucketCap: o número de baldes tem teto (o limitador não pode virar o
// vazamento de memória que ele existe para conter).
func TestRateLimitBucketCap(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	rl.maxKeys = 4
	now := time.Unix(0, 0)
	rl.now = func() time.Time { now = now.Add(time.Millisecond); return now }
	for i := 0; i < 100; i++ {
		rl.allow(string(rune('a' + i%90)))
	}
	if len(rl.m) > rl.maxKeys {
		t.Fatalf("baldes=%d, acima do teto %d", len(rl.m), rl.maxKeys)
	}
}

// TestClientKeyIgnoraHeaderDoCliente: a chave do balde é o IP, sempre. O header
// X-Revoada-Key (escolhido pelo cliente) não influencia.
func TestClientKeyIgnoraHeaderDoCliente(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	req := httptest.NewRequest(http.MethodPost, "/v1/logs", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	if got := rl.ClientKey(req.RemoteAddr, req.Header.Get("X-Forwarded-For")); got != "ip:203.0.113.7" {
		t.Fatalf("ClientKey=%q, quer ip:203.0.113.7", got)
	}
	req.Header.Set("X-Revoada-Key", "abc")
	if got := rl.ClientKey(req.RemoteAddr, req.Header.Get("X-Forwarded-For")); got != "ip:203.0.113.7" {
		t.Fatalf("ClientKey com header=%q, quer ip:203.0.113.7", got)
	}
}

// TestClientKeyXFFSoDeProxyConfiavel: X-Forwarded-For de origem NÃO confiável é
// ignorado (senão o atacante escolhe o próprio balde de novo, só que por outro header).
func TestClientKeyXFFSoDeProxyConfiavel(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	req := httptest.NewRequest(http.MethodPost, "/v1/logs", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	req.Header.Set("X-Forwarded-For", "9.9.9.9")
	if got := rl.ClientKey(req.RemoteAddr, req.Header.Get("X-Forwarded-For")); got != "ip:203.0.113.7" {
		t.Fatalf("XFF de origem não confiável foi aceito: %q", got)
	}

	rl.SetTrustedProxies([]string{"203.0.113.0/24"})
	if got := rl.ClientKey(req.RemoteAddr, req.Header.Get("X-Forwarded-For")); got != "ip:9.9.9.9" {
		t.Fatalf("XFF de proxy confiável ignorado: %q", got)
	}
	// Cadeia forjada pelo cliente: vale o ÚLTIMO salto (o que o proxy confiável viu).
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 9.9.9.9")
	if got := rl.ClientKey(req.RemoteAddr, req.Header.Get("X-Forwarded-For")); got != "ip:9.9.9.9" {
		t.Fatalf("cadeia XFF: %q, quer ip:9.9.9.9", got)
	}
}

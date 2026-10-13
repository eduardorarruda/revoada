package httpapi

import (
	"net/http/httptest"
	"testing"
	"time"
)

// TestTVBucketKeyPorToken: o balde das rotas de TV é por TOKEN, não por IP — várias
// TVs saem pelo mesmo NAT do escritório e não podem se estrangular umas às outras; e
// um token vazado gasta o próprio balde. O token em claro nunca vira a chave.
func TestTVBucketKeyPorToken(t *testing.T) {
	r1 := httptest.NewRequest("POST", "/api/tv/query?token=abc", nil)
	r1.RemoteAddr = "203.0.113.1:1111"
	r2 := httptest.NewRequest("POST", "/api/tv/query?token=abc", nil)
	r2.RemoteAddr = "198.51.100.2:2222" // outro IP, mesmo token
	r3 := httptest.NewRequest("POST", "/api/tv/query?token=xyz", nil)
	r3.RemoteAddr = "203.0.113.1:1111"

	k1, k2, k3 := tvBucketKey(r1), tvBucketKey(r2), tvBucketKey(r3)
	if k1 != k2 {
		t.Error("o mesmo token tem de cair no mesmo balde, venha de onde vier")
	}
	if k1 == k3 {
		t.Error("tokens diferentes não podem dividir o balde")
	}
	if k1 == "tv:abc" || k1 == "abc" {
		t.Errorf("o token em claro não pode virar chave de balde: %q", k1)
	}
	semToken := httptest.NewRequest("GET", "/api/tv/wall", nil)
	semToken.RemoteAddr = "203.0.113.1:1111"
	if got := tvBucketKey(semToken); got != "ip:203.0.113.1" {
		t.Errorf("sem token o balde é por IP; veio %q", got)
	}
}

// TestTVRateLimitCorta prova o freio que não existia: medido em dev, 400 POSTs
// seguidos em /api/tv/query passaram com ZERO 429.
func TestTVRateLimitCorta(t *testing.T) {
	rl := newIPRateLimiter(600, time.Minute)
	r := httptest.NewRequest("POST", "/api/tv/query?token=abc", nil)
	r.RemoteAddr = "203.0.113.1:1111"
	key := tvBucketKey(r)
	passou := 0
	for i := 0; i < 900; i++ {
		if rl.allow(key) {
			passou++
		}
	}
	if passou != 600 {
		t.Errorf("esperado 600 liberados de 900, veio %d", passou)
	}
	// Outra TV (outro token) continua livre: o estrangulamento é individual.
	outro := httptest.NewRequest("POST", "/api/tv/query?token=zzz", nil)
	outro.RemoteAddr = "203.0.113.1:1111"
	if !rl.allow(tvBucketKey(outro)) {
		t.Error("uma TV estourando o limite não pode derrubar as outras")
	}
}

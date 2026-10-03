package httpapi

import (
	"net"
	"net/http/httptest"
	"testing"
	"time"
)

// TestClientIPIgnoraXFFDePeerNaoConfiavel é a correção do bypass medido em dev: 15
// logins errados do mesmo IP davam 401×10 + 429×5, e as 6 tentativas seguintes, só
// trocando `X-Forwarded-For: 203.0.113.N`, davam 401×6 e NENHUM 429 — brute-force
// ilimitado com um header. Quem conecta direto (peer fora de REVOADA_TRUSTED_PROXIES)
// não escolhe mais a própria chave do balde.
func TestClientIPIgnoraXFFDePeerNaoConfiavel(t *testing.T) {
	nets := trustedProxyNets
	trustedProxyNets = parseCIDRs([]string{"172.16.0.0/12"})
	defer func() { trustedProxyNets = nets }()

	for _, xff := range []string{"203.0.113.7", "203.0.113.8", "1.2.3.4, 5.6.7.8"} {
		r := httptest.NewRequest("POST", "/api/auth/login", nil)
		r.RemoteAddr = "198.51.100.10:44444"
		r.Header.Set("X-Forwarded-For", xff)
		if got := clientIP(r); got != "198.51.100.10" {
			t.Errorf("XFF %q de peer não confiável mudou o balde: %q", xff, got)
		}
	}
}

// TestClientIPUsaXFFDeProxyConfiavel: atrás do Traefik/nginx (peer em rede privada) o
// XFF é a ÚNICA forma de saber quem é o cliente — ignorá-lo colocaria a internet
// inteira num balde só, transformando a proteção contra brute-force numa negação de
// serviço no login. Da direita para a esquerda, pulando os saltos internos.
func TestClientIPUsaXFFDeProxyConfiavel(t *testing.T) {
	nets := trustedProxyNets
	trustedProxyNets = parseCIDRs([]string{"172.16.0.0/12", "10.0.0.0/8"})
	defer func() { trustedProxyNets = nets }()

	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	r.RemoteAddr = "172.18.0.5:33333" // nginx do compose
	// cadeia real: cliente → Traefik → nginx (cada salto ANEXA o anterior)
	r.Header.Set("X-Forwarded-For", "203.0.113.9, 172.18.0.2")
	if got := clientIP(r); got != "203.0.113.9" {
		t.Errorf("cliente real esperado 203.0.113.9, veio %q", got)
	}
}

// TestClientIPXFFForjadoNaoVence: o cliente pode escrever o começo do XFF; o proxy só
// ANEXA. O valor forjado fica à esquerda do que a borda observou e nunca é escolhido.
func TestClientIPXFFForjadoNaoVence(t *testing.T) {
	nets := trustedProxyNets
	trustedProxyNets = parseCIDRs([]string{"172.16.0.0/12"})
	defer func() { trustedProxyNets = nets }()

	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	r.RemoteAddr = "172.18.0.5:33333"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9, 172.18.0.2")
	if got := clientIP(r); got != "203.0.113.9" {
		t.Errorf("o forjado 6.6.6.6 não podia vencer; veio %q", got)
	}
}

// TestDefaultConfiaEmRedePrivada garante que o default embarcado cobre a topologia
// real do compose (nginx e Traefik em rede privada do Docker) — se ele deixasse de
// cobrir, o login inteiro cairia num balde só e travaria usuários legítimos.
func TestDefaultConfiaEmRedePrivada(t *testing.T) {
	for _, ip := range []string{"172.18.0.5", "10.1.2.3", "192.168.0.9", "127.0.0.1"} {
		if !ipIsTrustedProxy(net.ParseIP(ip)) {
			t.Errorf("%s deveria ser proxy confiável no default", ip)
		}
	}
	for _, ip := range []string{"203.0.113.9", "8.8.8.8"} {
		if ipIsTrustedProxy(net.ParseIP(ip)) {
			t.Errorf("%s NÃO pode ser proxy confiável", ip)
		}
	}
}

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

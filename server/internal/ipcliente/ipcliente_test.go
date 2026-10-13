package ipcliente

import (
	"net"
	"net/http/httptest"
	"testing"
)

func comRedes(t *testing.T, cidrs ...string) {
	t.Helper()
	antes := redesConfiaveis
	redesConfiaveis = parseCIDRs(cidrs)
	t.Cleanup(func() { redesConfiaveis = antes })
}

// Correção do bypass medido em dev: quem conecta direto (peer fora de
// REVOADA_TRUSTED_PROXIES) não escolhe mais o próprio IP trocando o XFF.
func TestIgnoraXFFDePeerNaoConfiavel(t *testing.T) {
	comRedes(t, "172.16.0.0/12")
	for _, xff := range []string{"203.0.113.7", "203.0.113.8", "1.2.3.4, 5.6.7.8"} {
		r := httptest.NewRequest("POST", "/api/auth/login", nil)
		r.RemoteAddr = "198.51.100.10:44444"
		r.Header.Set("X-Forwarded-For", xff)
		if got := De(r); got != "198.51.100.10" {
			t.Errorf("XFF %q de peer não confiável mudou o IP: %q", xff, got)
		}
	}
}

// A trilha de auditoria lia o PRIMEIRO item do XFF de qualquer peer: quem falasse
// direto com o painel gravava na auditoria o IP que quisesse.
func TestAuditoriaNaoGravaIPForjadoPorPeerDireto(t *testing.T) {
	comRedes(t, "10.0.0.0/8")
	r := httptest.NewRequest("POST", "/api/users", nil)
	r.RemoteAddr = "203.0.113.50:5555"
	r.Header.Set("X-Forwarded-For", "10.9.9.9")
	if got := De(r); got != "203.0.113.50" {
		t.Errorf("IP forjado entrou na trilha: %q", got)
	}
}

// Atrás do Traefik/nginx (peer em rede privada) o XFF é a ÚNICA forma de saber quem é
// o cliente. Da direita para a esquerda, pulando os saltos internos.
func TestUsaXFFDeProxyConfiavel(t *testing.T) {
	comRedes(t, "172.16.0.0/12", "10.0.0.0/8")
	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	r.RemoteAddr = "172.18.0.5:33333"                          // nginx do compose
	r.Header.Set("X-Forwarded-For", "203.0.113.9, 172.18.0.2") // cliente → Traefik → nginx
	if got := De(r); got != "203.0.113.9" {
		t.Errorf("cliente real esperado 203.0.113.9, veio %q", got)
	}
}

// O cliente pode escrever o começo do XFF; o proxy só ANEXA. O valor forjado fica à
// esquerda do que a borda observou e nunca é escolhido.
func TestXFFForjadoNaoVence(t *testing.T) {
	comRedes(t, "172.16.0.0/12")
	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	r.RemoteAddr = "172.18.0.5:33333"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9, 172.18.0.2")
	if got := De(r); got != "203.0.113.9" {
		t.Errorf("o forjado 6.6.6.6 não podia vencer; veio %q", got)
	}
}

func TestSemXFFValeORemoteAddr(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/x", nil)
	r.RemoteAddr = "10.0.0.5:4444"
	if got := De(r); got != "10.0.0.5" {
		t.Errorf("sem proxy = %q; quer 10.0.0.5", got)
	}
}

// O default embarcado cobre a topologia real do compose (nginx e Traefik em rede
// privada do Docker) — se deixasse de cobrir, o login inteiro cairia num balde só.
func TestDefaultConfiaEmRedePrivada(t *testing.T) {
	for _, ip := range []string{"172.18.0.5", "10.1.2.3", "192.168.0.9", "127.0.0.1"} {
		if !ehProxyConfiavel(net.ParseIP(ip)) {
			t.Errorf("%s deveria ser proxy confiável no default", ip)
		}
	}
	for _, ip := range []string{"203.0.113.9", "8.8.8.8"} {
		if ehProxyConfiavel(net.ParseIP(ip)) {
			t.Errorf("%s NÃO pode ser proxy confiável", ip)
		}
	}
}

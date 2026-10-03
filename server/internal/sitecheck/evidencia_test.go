package sitecheck

import (
	"strings"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

// TestEvidenciaSeparaAsFasesDaFalha é o teste do caso real de 12/08/2026: o Portal
// da Eduq, atrás de CDN, com o TCP conectando em 1 ms e o TLS pendurado por 10 s. O
// diagnóstico gravado foi `connect_timeout` — o MESMO rótulo que sai quando o SYN
// nem é respondido. Se a mensagem repetisse só o rótulo, ela mentiria sobre a fase.
func TestEvidenciaSeparaAsFasesDaFalha(t *testing.T) {
	portal := &store.SiteCheck{Name: "Eduq — Portal", ConsecutiveFails: 2}

	tls := evidenciaDaSondagem(portal, Result{
		Diagnosis: DiagConnTimeout, ConnectMs: 1.1, TLSMs: 10002.8, TTFBMs: 0, TotalMs: 10007.3,
	}, "firing")
	if !strings.Contains(tls, "2 tentativas seguidas") {
		t.Errorf("a evidência tem de dizer quantas tentativas: %q", tls)
	}
	if !strings.Contains(tls, "TLS") || !strings.Contains(tls, "10 s") {
		t.Errorf("a fase que morreu foi o TLS, e o limite foi 10 s: %q", tls)
	}

	// Mesmo rótulo, fase diferente: aqui o pedido de conexão é que ficou sem resposta.
	syn := evidenciaDaSondagem(portal, Result{
		Diagnosis: DiagConnTimeout, ConnectMs: 0, TLSMs: 0, TotalMs: 15001,
	}, "firing")
	if strings.Contains(syn, "TLS") {
		t.Errorf("sem handshake não se pode culpar o TLS: %q", syn)
	}
	if !strings.Contains(syn, "conexão não foi respondido em 15 s") {
		t.Errorf("deveria acusar o pedido de conexão sem resposta: %q", syn)
	}
	if tls == syn {
		t.Error("duas falhas em fases diferentes não podem gerar a mesma frase")
	}
}

// TestEvidenciaDeclaraDeOndeMediu trava a confissão de alcance. Um alerta que afirma
// "está fora do ar" sem dizer que olhou de um lugar só é a razão de o aviso correto
// de 12/08 ter sido respondido com "errado".
func TestEvidenciaDeclaraDeOndeMediu(t *testing.T) {
	sozinha := evidenciaDaSondagem(
		&store.SiteCheck{ConsecutiveFails: 2},
		Result{Diagnosis: DiagConnTimeout, TotalMs: 15000}, "firing")
	if !strings.Contains(sozinha, "uma sonda só") {
		t.Errorf("com sonda única a mensagem tem de admitir o alcance: %q", sozinha)
	}

	comSondas := evidenciaDaSondagem(
		&store.SiteCheck{ConsecutiveFails: 2, ProbeLocations: []string{"gcloud"}},
		Result{Diagnosis: DiagConnTimeout, TotalMs: 15000}, "firing")
	if strings.Contains(comSondas, "uma sonda só") || !strings.Contains(comSondas, "gcloud") {
		t.Errorf("com sonda designada a frase muda e nomeia a sonda: %q", comSondas)
	}
}

func TestEvidenciaPorDiagnostico(t *testing.T) {
	chk := &store.SiteCheck{ConsecutiveFails: 1, ExpectStatus: 200}
	casos := []struct {
		res    Result
		estado string
		quer   string
	}{
		{Result{Diagnosis: DiagHTTPStatus, Status: 404}, "firing", "HTTP 404 quando esperávamos 200"},
		{Result{Diagnosis: DiagHTTP5xx, Status: 502}, "firing", "HTTP 502"},
		{Result{Diagnosis: DiagDNSError}, "firing", "DNS"},
		{Result{Diagnosis: DiagConnRefused}, "firing", "fechada"},
		{Result{Diagnosis: DiagKeywordAusent}, "firing", "palavra-chave"},
		{Result{Diagnosis: "down em 2 sonda(s): central, gcloud"}, "firing", "central, gcloud"},
		{Result{Status: 200, TotalMs: 35.1}, "resolved", "voltou a responder HTTP 200 em 35 ms"},
	}
	for _, c := range casos {
		got := evidenciaDaSondagem(chk, c.res, c.estado)
		if !strings.Contains(got, c.quer) {
			t.Errorf("diag %q (%s): esperava conter %q, veio %q", c.res.Diagnosis, c.estado, c.quer, got)
		}
	}

	// Consenso de sondas conta sondas, não tentativas de uma delas.
	consenso := evidenciaDaSondagem(chk, Result{Diagnosis: "down em 2 sonda(s): central, gcloud"}, "firing")
	if strings.Contains(consenso, "tentativa") {
		t.Errorf("no consenso multi-sonda a contagem é de sondas: %q", consenso)
	}
}

func TestEvidenciaSemRespostaExplicaAFase(t *testing.T) {
	chk := &store.SiteCheck{ConsecutiveFails: 3, Tier: "padrao"}
	res := Result{Diagnosis: DiagSemResposta, ConnectMs: 137, TLSMs: 140, TTFBMs: 0, TotalMs: 20002}
	ev := evidenciaDaSondagem(chk, res, "firing")
	for _, quer := range []string{"3 tentativas seguidas", "conexão e o TLS abriram", "20 s", "ocupado ou travado"} {
		if !strings.Contains(ev, quer) {
			t.Errorf("evidência sem %q: %s", quer, ev)
		}
	}
}

// A lista das tentativas é o que deixa o aviso conferível: hora e fase de cada
// falha, em Brasília. Sem ela, "3 tentativas falharam" é uma afirmação sem prova.
func TestEvidenciaListaAsTentativas(t *testing.T) {
	ts := func(h, m, s int) string { return time.Date(2026, 9, 25, h, m, s, 0, time.UTC).Format(time.RFC3339) }
	ultimas := []store.SiteCheckResultView{
		{TS: ts(20, 8, 40), OK: false, Diagnosis: DiagSemResposta, ConnectMs: 137, TLSMs: 140, TotalMs: 20002},
		{TS: ts(20, 7, 58), OK: false, Diagnosis: DiagConnTimeout, TotalMs: 20001},
		{TS: ts(20, 7, 3), OK: false, Diagnosis: DiagConnTimeout, TotalMs: 20001},
		{TS: ts(20, 2, 0), OK: true, Status: 200, TotalMs: 700},
	}
	texto := listaDeTentativas(ultimas, 3)
	quer := "Tentativas: 17:07:03 conexão sem resposta; 17:07:58 conexão sem resposta; 17:08:40 conectou, mas a página não veio."
	if texto != quer {
		t.Errorf("lista = %q\nquero  %q", texto, quer)
	}
	if listaDeTentativas(nil, 3) != "" {
		t.Error("sem histórico, sem lista")
	}
}

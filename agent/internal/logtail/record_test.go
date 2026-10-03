package logtail

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// srvColetor devolve um servidor que responde `status` nas primeiras `falhas`
// requisições e 204 depois, guardando os corpos recebidos.
func srvColetor(t *testing.T, status int, falhas int32) (*httptest.Server, *atomic.Int32, *[]string) {
	t.Helper()
	var n atomic.Int32
	corpos := &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) <= falhas {
			w.WriteHeader(status)
			return
		}
		sc := bufio.NewScanner(r.Body)
		for sc.Scan() {
			*corpos = append(*corpos, sc.Text())
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, &n, corpos
}

func sinkDeTeste(url string) *sink {
	s := newSink(url, "k", "h")
	s.limiter = nil // o rate limit não é o assunto destes testes
	return s
}

// zeraPerdas isola cada teste do contador de processo.
func zeraPerdas(t *testing.T) {
	t.Helper()
	sendLoss.mu.Lock()
	sendLoss.dropped, sendLoss.lastMsg = 0, time.Time{}
	sendLoss.mu.Unlock()
	t.Cleanup(func() {
		sendLoss.mu.Lock()
		sendLoss.dropped, sendLoss.lastMsg = 0, time.Time{}
		sendLoss.mu.Unlock()
	})
}

// TestPost429Retenta: 429 é backpressure, cujo contrato é "retente". Antes o
// StatusCode nem era lido e o lote inteiro sumia na primeira resposta.
func TestPost429Retenta(t *testing.T) {
	zeraPerdas(t)
	postBackoffOriginal := postBackoff
	postBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { postBackoff = postBackoffOriginal }()

	srv, n, corpos := srvColetor(t, http.StatusTooManyRequests, 1)
	s := sinkDeTeste(srv.URL)
	s.post(context.Background(), []record{{service: "app", severity: "ERROR", body: "disco cheio"}})

	if got := n.Load(); got != 2 {
		t.Fatalf("esperava 2 tentativas (429 depois sucesso), veio %d", got)
	}
	if len(*corpos) != 1 || !strings.Contains((*corpos)[0], "disco cheio") {
		t.Fatalf("o lote não chegou após o retry: %#v", *corpos)
	}
	if sendLoss.dropped != 0 {
		t.Errorf("nada se perdeu, mas o contador acusou %d", sendLoss.dropped)
	}
}

// TestPost503Retenta: 5xx (gateway reiniciando, ClickHouse ocupado) também é
// repetível.
func TestPost503Retenta(t *testing.T) {
	zeraPerdas(t)
	postBackoffOriginal := postBackoff
	postBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { postBackoff = postBackoffOriginal }()

	srv, n, corpos := srvColetor(t, http.StatusServiceUnavailable, 2)
	s := sinkDeTeste(srv.URL)
	s.post(context.Background(), []record{{service: "app", body: "x"}})

	if got := n.Load(); got != 3 {
		t.Fatalf("esperava 3 tentativas, veio %d", got)
	}
	if len(*corpos) != 1 {
		t.Fatalf("o lote não chegou: %#v", *corpos)
	}
}

// TestPost400NaoRetenta: corpo malformado ou chave errada não melhora com
// insistência — repetir só somaria carga a um gateway que já respondeu.
func TestPost400NaoRetenta(t *testing.T) {
	zeraPerdas(t)
	srv, n, _ := srvColetor(t, http.StatusBadRequest, 99)
	s := sinkDeTeste(srv.URL)
	s.post(context.Background(), []record{{service: "app", body: "x"}})

	if got := n.Load(); got != 1 {
		t.Fatalf("400 não deveria ser repetido, houve %d tentativas", got)
	}
	if sendLoss.dropped != 1 {
		t.Errorf("a perda deveria ter sido contada, veio %d", sendLoss.dropped)
	}
}

// TestPerdaViraAvisoNoProprioStream: o lote perdido tem de aparecer como linha de
// log no próximo envio bem-sucedido. Sem isso a falta de log é indistinguível de um
// servidor que ficou quieto.
func TestPerdaViraAvisoNoProprioStream(t *testing.T) {
	zeraPerdas(t)
	postBackoffOriginal := postBackoff
	postBackoff = nil // uma tentativa só, para o teste ser rápido
	defer func() { postBackoff = postBackoffOriginal }()

	// 1) servidor recusa permanentemente: 3 linhas se perdem.
	ruim, _, _ := srvColetor(t, http.StatusBadRequest, 99)
	sr := sinkDeTeste(ruim.URL)
	sr.post(context.Background(), []record{{body: "a"}, {body: "b"}, {body: "c"}})
	if sendLoss.dropped != 3 {
		t.Fatalf("esperava 3 perdas contadas, veio %d", sendLoss.dropped)
	}

	// 2) servidor volta: o aviso vai junto do próximo lote.
	bom, _, corpos := srvColetor(t, 0, 0)
	sb := sinkDeTeste(bom.URL)
	sb.post(context.Background(), []record{{service: "app", body: "voltou"}})

	var achou bool
	for _, linha := range *corpos {
		var m map[string]any
		if json.Unmarshal([]byte(linha), &m) != nil {
			continue
		}
		if body, _ := m["body"].(string); strings.Contains(body, "3 linha(s) não chegaram") {
			achou = true
			if m["severity"] != "WARN" {
				t.Errorf("o aviso de perda deveria ser WARN, veio %v", m["severity"])
			}
		}
	}
	if !achou {
		t.Fatalf("o aviso de perda não foi injetado no stream: %#v", *corpos)
	}
	if sendLoss.dropped != 0 {
		t.Errorf("após o aviso chegar, o contador deveria zerar, veio %d", sendLoss.dropped)
	}
}

// TestAvisoDePerdaSobreviveAoLoteQueFalha: se o lote que carregava o aviso também
// falhar, a contagem NÃO pode ser zerada — senão a perda some justamente no cenário
// em que ela é mais provável.
func TestAvisoDePerdaSobreviveAoLoteQueFalha(t *testing.T) {
	zeraPerdas(t)
	postBackoffOriginal := postBackoff
	postBackoff = nil
	defer func() { postBackoff = postBackoffOriginal }()

	ruim, _, _ := srvColetor(t, http.StatusBadRequest, 99)
	s := sinkDeTeste(ruim.URL)
	s.post(context.Background(), []record{{body: "a"}, {body: "b"}})
	primeiro := sendLoss.dropped
	s.post(context.Background(), []record{{body: "c"}})
	if sendLoss.dropped <= primeiro {
		t.Fatalf("a contagem de perda regrediu (%d -> %d) num envio que também falhou", primeiro, sendLoss.dropped)
	}
}

// TestPostRedigeAntesDeEnviar: guarda de que a redaction continua no choke point.
func TestPostRedigeAntesDeEnviar(t *testing.T) {
	zeraPerdas(t)
	srv, _, corpos := srvColetor(t, 0, 0)
	s := sinkDeTeste(srv.URL)
	s.post(context.Background(), []record{{
		service: "app",
		body:    "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG",
		labels:  map[string]string{"source": "file", "file": "/var/log/app.log"},
	}})
	if len(*corpos) != 1 {
		t.Fatalf("nada chegou: %#v", *corpos)
	}
	if strings.Contains((*corpos)[0], "wJalrXUtnFEMI") {
		t.Fatalf("o segredo saiu do host: %s", (*corpos)[0])
	}
}

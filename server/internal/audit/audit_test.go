package audit

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

// newTestRecorder devolve um Recorder SEM worker de banco: as entradas ficam na
// fila e o teste as inspeciona, sem precisar de Postgres.
func newTestRecorder() *Recorder {
	return &Recorder{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		ch:  make(chan store.AuditEntry, 8),
	}
}

// TestRedactBody: nenhum segredo pode chegar à trilha, em qualquer profundidade.
func TestRedactBody(t *testing.T) {
	body := []byte(`{
		"name": "canal x",
		"password": "hunter2",
		"config": {"apikey": "abc123", "base_url": "https://evo", "headers": {"Authorization": "Bearer z"}},
		"ssh": {"user": "root", "secret": "-----BEGIN PRIVATE KEY-----"},
		"alvos": [{"serverkey": "SK-1", "host": "srv1"}]
	}`)
	got := redactBody(body)

	// Valores sensíveis não podem aparecer em lugar nenhum do JSON final.
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, leak := range []string{"hunter2", "abc123", "BEGIN PRIVATE KEY", "SK-1", "Bearer z"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("segredo vazou na trilha: %q em %s", leak, raw)
		}
	}
	// Campos não-sensíveis devem sobreviver — a trilha precisa ser útil.
	for _, keep := range []string{"canal x", "https://evo", "root", "srv1"} {
		if !strings.Contains(string(raw), keep) {
			t.Errorf("campo útil sumiu da trilha: %q em %s", keep, raw)
		}
	}
	if got["password"] != redactedMark {
		t.Errorf("password = %v; quer %q", got["password"], redactedMark)
	}
	// headers inteiro é redigido (pode carregar Authorization).
	cfg, _ := got["config"].(map[string]any)
	if cfg["headers"] != redactedMark {
		t.Errorf("headers = %v; quer %q", cfg["headers"], redactedMark)
	}
	if cfg["base_url"] != "https://evo" {
		t.Errorf("base_url foi redigido indevidamente: %v", cfg["base_url"])
	}
}

// TestRedactBodyNaoJSON: corpo vazio ou não-JSON não pode quebrar a auditoria.
// Os corpos REAIS das rotas em português: criar conexão de banco e reautenticar.
func TestRedactCamposEmPortugues(t *testing.T) {
	for _, corpo := range []string{
		`{"nome":"ERP","motor":"firebird","usuario":"SYSDBA","senha":"masterkey-123","opcoes":{"charset":"WIN1252"}}`,
		`{"senha":"minha-senha-de-conta"}`,
		`{"codigo":"123456"}`,
		`{"desafio":"eyJ.desafio.assinado","codigo":"654321"}`,
		`{"nova_senha":"x-nova-1","senha_atual":"x-velha-2","refresh_token":"rt-3"}`,
	} {
		raw, _ := json.Marshal(redactBody([]byte(corpo)))
		for _, leak := range []string{"masterkey-123", "minha-senha-de-conta", "123456", "eyJ.desafio", "654321", "x-nova-1", "x-velha-2", "rt-3"} {
			if strings.Contains(string(raw), leak) {
				t.Errorf("vazou %q na trilha: %s", leak, raw)
			}
		}
	}
	raw, _ := json.Marshal(redactBody([]byte(`{"nome":"ERP","usuario":"SYSDBA"}`)))
	if !strings.Contains(string(raw), "SYSDBA") || !strings.Contains(string(raw), "ERP") {
		t.Fatalf("campos comuns têm de continuar na trilha: %s", raw)
	}
}

func TestRedactBodyNaoJSON(t *testing.T) {
	if got := redactBody(nil); len(got) != 0 {
		t.Errorf("corpo vazio = %v; quer mapa vazio", got)
	}
	if got := redactBody([]byte("   ")); len(got) != 0 {
		t.Errorf("corpo em branco = %v; quer mapa vazio", got)
	}
	got := redactBody([]byte("isto não é json"))
	if got["_nota"] == nil {
		t.Errorf("corpo inválido deveria virar nota, veio %v", got)
	}
}

func TestSplitResource(t *testing.T) {
	cases := []struct{ path, resource, target string }{
		{"/api/site-checks", "site-checks", ""},
		{"/api/site-checks/12", "site-checks", "12"},
		{"/api/hosts/srv1.local/delete", "hosts", "srv1.local/delete"},
		{"/api/users/7/perms", "users", "7/perms"},
		{"/api/", "", ""},
	}
	for _, c := range cases {
		r, tg := splitResource(c.path)
		if r != c.resource || tg != c.target {
			t.Errorf("splitResource(%q) = (%q,%q); quer (%q,%q)", c.path, r, tg, c.resource, c.target)
		}
	}
}

// TestIsWrite: GET não pode gerar entrada (senão a trilha vira log de acesso).
func TestIsWrite(t *testing.T) {
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if !isWrite(m) {
			t.Errorf("isWrite(%s) = false; quer true", m)
		}
	}
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if isWrite(m) {
			t.Errorf("isWrite(%s) = true; quer false", m)
		}
	}
}

// TestMiddlewarePreservaCorpo: o handler precisa receber o corpo intacto depois de
// a auditoria tê-lo lido — senão auditar quebraria toda escrita da API.
func TestMiddlewarePreservaCorpo(t *testing.T) {
	const payload = `{"name":"x","password":"segredo"}`
	var seen string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, len(payload))
		n, _ := r.Body.Read(b)
		seen = string(b[:n])
		w.WriteHeader(http.StatusCreated)
	})

	rec := newTestRecorder()
	h := rec.Middleware(next)

	req := httptest.NewRequest(http.MethodPost, "/api/notify/channels", strings.NewReader(payload))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if seen != payload {
		t.Errorf("handler recebeu %q; quer o corpo intacto %q", seen, payload)
	}
	if w.Code != http.StatusCreated {
		t.Errorf("status = %d; quer 201", w.Code)
	}
	e := <-rec.ch
	if e.Status != http.StatusCreated {
		t.Errorf("trilha gravou status %d; quer 201", e.Status)
	}
	if e.Resource != "notify" {
		t.Errorf("recurso = %q; quer notify", e.Resource)
	}
	if e.Payload["password"] != redactedMark {
		t.Errorf("senha não foi redigida na trilha: %v", e.Payload["password"])
	}
	if e.ActorRole != "anônimo" {
		t.Errorf("sem claims o ator deve ser anônimo, veio %q", e.ActorRole)
	}
}

// TestMiddlewareIgnoraLeitura: GET não gera entrada na trilha.
func TestMiddlewareIgnoraLeitura(t *testing.T) {
	rec := newTestRecorder()
	h := rec.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/hosts", nil))
	select {
	case e := <-rec.ch:
		t.Errorf("GET gerou entrada na trilha: %+v", e)
	default:
	}
}

func TestClientIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	req.RemoteAddr = "10.0.0.5:4444"
	if got := clientIP(req); got != "10.0.0.5" {
		t.Errorf("clientIP sem proxy = %q; quer 10.0.0.5", got)
	}
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
	if got := clientIP(req); got != "203.0.113.9" {
		t.Errorf("clientIP com XFF = %q; quer o primeiro da cadeia", got)
	}
}

// TestCorpoGrandeChegaInteiroAoHandler é o bug medido: o middleware entregava ao
// handler o corpo TRUNCADO em 64 KiB, então todo POST/PUT maior chegava quebrado no
// meio do JSON e o handler devolvia um erro que MENTIA sobre a causa — provado com
// `POST /api/query` levando um `metric` de 100 KB: a resposta era
// `400 metric obrigatório`, com o campo lá, inteiro, no que o cliente mandou. Quem
// visse esse 400 iria depurar o cliente; o problema estava na trilha de auditoria.
func TestCorpoGrandeChegaInteiroAoHandler(t *testing.T) {
	metric := strings.Repeat("m", 100<<10) // 100 KB, bem acima dos 64 KiB da trilha
	corpo := `{"metric":"` + metric + `"}`

	var recebido string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Metric string `json:"metric"`
		}
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			http.Error(w, "corpo quebrado: "+err.Error(), http.StatusBadRequest)
			return
		}
		recebido = v.Metric
		w.WriteHeader(http.StatusOK)
	})

	rec := newTestRecorder()
	srv := rec.Middleware(next)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("POST", "/api/query", strings.NewReader(corpo)))

	if w.Code != http.StatusOK {
		t.Fatalf("esperado 200, obtido %d (%s)", w.Code, w.Body.String())
	}
	if len(recebido) != len(metric) {
		t.Fatalf("o handler recebeu %d bytes de metric; mandaram %d", len(recebido), len(metric))
	}
	// A TRILHA continua limitada: o que vai para o banco não pode ser um blob.
	e := <-rec.ch
	bruto, _ := json.Marshal(e.Payload)
	if len(bruto) > maxBodyBytes+1024 {
		t.Errorf("a trilha guardou %d bytes; deveria truncar em ~%d", len(bruto), maxBodyBytes)
	}
}

// TestCorpoAcimaDoTetoDa413: passando do teto duro, o cliente recebe 413 EXPLÍCITO em
// vez de um erro de validação sobre um campo que ele mandou certo. E o handler nem é
// chamado — não se processa meio pedido.
func TestCorpoAcimaDoTetoDa413(t *testing.T) {
	var chamou bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { chamou = true })
	rec := newTestRecorder()
	w := httptest.NewRecorder()
	corpo := strings.NewReader(strings.Repeat("x", maxRequestBytes+10))
	rec.Middleware(next).ServeHTTP(w, httptest.NewRequest("POST", "/api/dashboards", corpo))

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("esperado 413, obtido %d (%s)", w.Code, w.Body.String())
	}
	if chamou {
		t.Error("o handler não pode ser chamado com corpo acima do teto")
	}
	if !strings.Contains(w.Body.String(), "grande demais") {
		t.Errorf("a mensagem precisa dizer a causa real: %s", w.Body.String())
	}
}

package notify

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

// O vigia existe por causa de dois silêncios: o wuzapi ficou dois dias sem enviar
// em agosto/2026 ("Client outdated") e, em 15/09/2026, dez alertas falharam por
// queda da conexão com o WhatsApp — e ninguém soube, porque o aviso de que o
// WhatsApp caiu teria de sair... pelo WhatsApp. Estes testes trancam as regras
// que tornam o vigia confiável sem virar ruído.

// sessaoFake responde /session/status como o wuzapi. `estado` pode ser trocado
// entre checagens para simular a queda e a volta.
type sessaoFake struct {
	mu        sync.Mutex
	connected bool
	loggedIn  bool
	status    int
}

func (s *sessaoFake) set(connected, loggedIn bool, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected, s.loggedIn, s.status = connected, loggedIn, status
}

func (s *sessaoFake) servidor(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session/status" || r.Header.Get("Token") != "tok" {
			http.Error(w, "rota inesperada", http.StatusNotFound)
			return
		}
		s.mu.Lock()
		c, l, st := s.connected, s.loggedIn, s.status
		s.mu.Unlock()
		if st != 0 && st != http.StatusOK {
			w.WriteHeader(st)
			_, _ = io.WriteString(w, `{"code":500,"error":"falha","success":false}`)
			return
		}
		// Chaves em minúsculas, como o wuzapi 919c72c devolve em produção.
		_, _ = io.WriteString(w, `{"code":200,"data":{"connected":`+boolJSON(c)+`,"loggedIn":`+boolJSON(l)+`},"success":true}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func boolJSON(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

type integFake struct{ base string }

func (i integFake) GetWhatsAppIntegration(context.Context) (store.WhatsAppIntegration, error) {
	return store.WhatsAppIntegration{BaseURL: i.base, Token: "tok"}, nil
}

// senderFake grava o que foi enviado e falha enquanto `falhar` estiver ligado.
type senderFake struct {
	mu             sync.Mutex
	falhar         bool
	semConfirmacao bool
	tentativas     int
	msgs           []Message
	cfgs           []map[string]any
}

func (s *senderFake) Send(_ context.Context, cfg map[string]any, msg Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tentativas++
	if s.falhar {
		return errors.New("whatsapp: sessão fora")
	}
	s.msgs = append(s.msgs, msg)
	s.cfgs = append(s.cfgs, cfg)
	if s.semConfirmacao {
		return &SemConfirmacao{Motivo: "aceito sem LID"}
	}
	return nil
}

func (s *senderFake) assuntos() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.msgs))
	for _, m := range s.msgs {
		out = append(out, m.Subject)
	}
	return out
}

type relogio struct{ t time.Time }

func (r *relogio) agora() time.Time       { return r.t }
func (r *relogio) passar(d time.Duration) { r.t = r.t.Add(d) }
func novoRelogio() *relogio               { return &relogio{t: time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)} }
func descartarLog() *slog.Logger          { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func contem(lista []string, trecho string) bool {
	for _, s := range lista {
		if strings.Contains(s, trecho) {
			return true
		}
	}
	return false
}

type cenarioVigia struct {
	v      *Vigia
	sessao *sessaoFake
	wa     *senderFake
	mail   *senderFake
	rel    *relogio
}

func novoCenario(t *testing.T) cenarioVigia {
	t.Helper()
	sess := &sessaoFake{connected: true, loggedIn: true}
	srv := sess.servidor(t)
	wa, mail, rel := &senderFake{}, &senderFake{}, novoRelogio()
	cfg := VigiaConfig{
		WhatsApp: map[string]any{"to": "5511900000001", "lid": "100000000000001"},
		Email:    map[string]any{"host": "smtp.exemplo", "from": "painel@exemplo", "to": "operador@exemplo.com"},
	}
	v := NewVigia(cfg, integFake{base: srv.URL}, nil, descartarLog())
	v.client = srv.Client()
	v.wa, v.mail, v.agora = wa, mail, rel.agora
	return cenarioVigia{v: v, sessao: sess, wa: wa, mail: mail, rel: rel}
}

func (c cenarioVigia) checar(t *testing.T) {
	t.Helper()
	c.v.Checar(context.Background())
	c.rel.passar(time.Minute)
}

func TestVigiaSaudavelNaoAvisaNinguem(t *testing.T) {
	c := novoCenario(t)
	for i := 0; i < 5; i++ {
		c.checar(t)
	}
	if n := len(c.mail.assuntos()) + len(c.wa.assuntos()); n != 0 {
		t.Fatalf("com o wuzapi saudável o vigia enviou %d mensagens; esperado 0", n)
	}
}

// Uma checagem ruim isolada é reconexão de rotina do whatsmeow: avisar nela seria
// ensinar o operador a ignorar o vigia. São precisas duas seguidas.
func TestVigiaSessaoCaidaAvisaNaSegundaChecagemEUmaVezSo(t *testing.T) {
	c := novoCenario(t)
	c.checar(t)
	c.sessao.set(false, true, http.StatusOK)
	c.wa.falhar = true // com a sessão fora, o WhatsApp também não entrega

	c.checar(t)
	if n := len(c.mail.assuntos()); n != 0 {
		t.Fatalf("avisou na primeira checagem ruim (%d e-mails); esperado esperar a segunda", n)
	}
	c.checar(t)
	c.checar(t)
	c.checar(t)
	got := c.mail.assuntos()
	if len(got) != 1 {
		t.Fatalf("e-mails = %v; esperado exatamente 1 aviso de queda", got)
	}
	if !strings.Contains(got[0], "fora do ar") {
		t.Errorf("assunto %q não diz que o WhatsApp está fora do ar", got[0])
	}
	if !strings.Contains(c.mail.msgs[0].Text, "desconectada") {
		t.Errorf("o texto não explica a causa medida: %q", c.mail.msgs[0].Text)
	}
}

func TestVigiaAvisoVaiSoParaODestinatarioDoVigia(t *testing.T) {
	c := novoCenario(t)
	c.sessao.set(false, false, http.StatusOK)
	c.checar(t)
	c.checar(t)
	if len(c.mail.cfgs) != 1 || c.mail.cfgs[0]["to"] != "operador@exemplo.com" {
		t.Fatalf("e-mail foi para %v; esperado só operador@exemplo.com", c.mail.cfgs)
	}
	if len(c.wa.cfgs) != 1 || c.wa.cfgs[0]["to"] != "5511900000001" || c.wa.cfgs[0]["lid"] != "100000000000001" {
		t.Fatalf("WhatsApp foi para %v; esperado só o número e o LID do vigia", c.wa.cfgs)
	}
	// A conexão com o wuzapi vem da integração global, não do ambiente.
	if c.wa.cfgs[0]["token"] != "tok" || c.wa.cfgs[0]["base_url"] == "" {
		t.Errorf("o envio pelo WhatsApp não herdou a conexão da integração: %v", c.wa.cfgs[0])
	}
	if !strings.Contains(c.mail.msgs[0].Text, "QR code") {
		t.Errorf("sessão deslogada precisa dizer que é para ler o QR code: %q", c.mail.msgs[0].Text)
	}
}

func TestVigiaWuzapiQueNaoRespondeContaComoQueda(t *testing.T) {
	c := novoCenario(t)
	c.sessao.set(true, true, http.StatusBadGateway)
	c.checar(t)
	c.checar(t)
	got := c.mail.assuntos()
	if len(got) != 1 {
		t.Fatalf("e-mails = %v; esperado 1 aviso quando o wuzapi responde 502", got)
	}
	if !strings.Contains(c.mail.msgs[0].Text, "502") {
		t.Errorf("o texto não mostra o que foi medido (HTTP 502): %q", c.mail.msgs[0].Text)
	}
}

// A volta só é anunciada depois de uma mensagem real chegar: a sessão "conectada"
// de 15/09 continuava sem entregar nada.
func TestVigiaVoltaSoComEnvioDeVerdade(t *testing.T) {
	c := novoCenario(t)
	c.sessao.set(false, true, http.StatusOK)
	c.wa.falhar = true
	c.checar(t)
	c.checar(t) // abre

	c.sessao.set(true, true, http.StatusOK) // sessão volta, mas o envio ainda falha
	c.checar(t)
	c.rel.passar(10 * time.Minute)
	c.checar(t)
	if contem(c.mail.assuntos(), "voltou") {
		t.Fatalf("anunciou a volta sem nenhuma mensagem ter saído: %v", c.mail.assuntos())
	}

	c.wa.falhar = false
	c.rel.passar(10 * time.Minute)
	c.checar(t)
	if !contem(c.wa.assuntos(), "voltou") {
		t.Fatalf("o WhatsApp não recebeu a mensagem de volta: %v", c.wa.assuntos())
	}
	if !contem(c.mail.assuntos(), "voltou") {
		t.Fatalf("o e-mail não recebeu a confirmação de volta: %v", c.mail.assuntos())
	}
	c.checar(t)
	c.checar(t)
	if n := len(c.wa.assuntos()); n != 1 {
		t.Errorf("depois da volta o WhatsApp recebeu %d mensagens; esperado 1", n)
	}
}

func TestVigiaEnvioParadoAbreMesmoComSessaoConectada(t *testing.T) {
	c := novoCenario(t)
	falha := errors.New("whatsapp: 5511900000001 (100000000000001@lid: http 500: error sending message: failed to get device list: failed to send usync query: websocket disconnected)")
	c.v.RegistrarEnvio("whatsapp", "Canal", falha)
	c.checar(t)
	if len(c.mail.assuntos()) != 0 {
		t.Fatalf("uma falha isolada já abriu o aviso: %v", c.mail.assuntos())
	}
	c.v.RegistrarEnvio("whatsapp", "Canal", falha)
	c.wa.falhar = true
	c.checar(t)
	got := c.mail.assuntos()
	if len(got) != 1 || !strings.Contains(got[0], "parou de enviar") {
		t.Fatalf("e-mails = %v; esperado 1 aviso de envio parado", got)
	}
	if !strings.Contains(c.mail.msgs[0].Text, "usync") {
		t.Errorf("o aviso não traz o último erro: %q", c.mail.msgs[0].Text)
	}
}

// Recusa do contato (463) e número sem WhatsApp são problemas DO DESTINATÁRIO. O
// wuzapi está funcionando; contar isso como queda faria o vigia gritar à toa. A
// classificação vem do erro tipado do sender, não de procurar "463" no texto — o
// número de um destinatário pode conter "463" e esconder uma queda de verdade.
func TestVigiaRecusaDoContatoNaoContaComoQueda(t *testing.T) {
	c := novoCenario(t)
	recusas := []error{
		&FalhaWhatsApp{destinos: []falhaDestino{{texto: "5511900000002 (http 500: 463 reachout timelocked)", doDestinatario: true}}},
		&FalhaWhatsApp{destinos: []falhaDestino{{texto: "5511000000000 (o número não tem WhatsApp)", doDestinatario: true}}},
		&SemConfirmacao{Motivo: "aceito sem LID"},
	}
	for _, err := range recusas {
		c.v.RegistrarEnvio("whatsapp", "Canal", err)
		c.v.RegistrarEnvio("whatsapp", "Canal", err)
		c.checar(t)
	}
	if n := len(c.mail.assuntos()); n != 0 {
		t.Fatalf("recusas do destinatário abriram aviso: %v", c.mail.assuntos())
	}
}

func TestVigiaNumeroComQuatroSeisTresNaoEscondeQueda(t *testing.T) {
	c := novoCenario(t)
	queda := &FalhaWhatsApp{destinos: []falhaDestino{{texto: "5511900463000 (http 500: failed to send usync query: websocket disconnected)"}}}
	c.v.RegistrarEnvio("whatsapp", "Canal", queda)
	c.v.RegistrarEnvio("whatsapp", "Canal", queda)
	c.wa.falhar = true
	c.checar(t)
	if !contem(c.mail.assuntos(), "parou de enviar") {
		t.Fatalf("queda de destinatário com 463 no número passou calada: %v", c.mail.assuntos())
	}
}

// Um destinatário cuja primeira tentativa caiu por infraestrutura NÃO é problema
// dele, mesmo que a segunda tentativa diga "não tem WhatsApp".
func TestFalhaWhatsAppMisturadaContaComoInfraestrutura(t *testing.T) {
	f := &FalhaWhatsApp{destinos: []falhaDestino{
		{texto: "a (463)", doDestinatario: true},
		{texto: "b (websocket disconnected)"},
	}}
	if f.soDoDestinatario() {
		t.Fatal("um destinatário com falha de infraestrutura tem de contar como falha do wuzapi")
	}
	if got := f.Error(); got != "whatsapp: a (463) | b (websocket disconnected)" {
		t.Errorf("o texto do erro mudou: %q", got)
	}
}

// Canal sem conexão configurada (ex.: o banco falhou ao ler a integração) não é o
// wuzapi fora do ar.
func TestVigiaFaltaDeConexaoConfiguradaNaoContaComoQueda(t *testing.T) {
	c := novoCenario(t)
	c.v.RegistrarEnvio("whatsapp", "Canal", errSemConexaoWhatsApp)
	c.v.RegistrarEnvio("whatsapp", "Canal", errSemConexaoWhatsApp)
	c.checar(t)
	if n := len(c.mail.assuntos()); n != 0 {
		t.Fatalf("falta de configuração abriu aviso de queda: %v", c.mail.assuntos())
	}
}

// O teste de volta aceito "sem confirmação" (número sem LID) prova que o wuzapi
// entrega — o vigia tem de fechar, não mandar "voltou" a cada 5 minutos para sempre.
func TestVigiaVoltaAceitaSemConfirmacaoFecha(t *testing.T) {
	c := novoCenario(t)
	c.sessao.set(false, true, http.StatusOK)
	c.wa.falhar = true
	c.checar(t)
	c.checar(t)
	c.sessao.set(true, true, http.StatusOK)
	c.wa.falhar = false
	c.wa.semConfirmacao = true
	c.rel.passar(10 * time.Minute)
	c.checar(t)
	c.rel.passar(10 * time.Minute)
	c.checar(t)
	c.rel.passar(10 * time.Minute)
	c.checar(t)
	if n := c.wa.tentativas; n != 2 {
		t.Fatalf("o vigia tentou o WhatsApp %d vezes; esperado 2 (o aviso que falhou e 1 volta)", n)
	}
	if !contem(c.mail.assuntos(), "voltou") {
		t.Fatalf("a volta aceita sem confirmação não fechou: %v", c.mail.assuntos())
	}
}

// Sem WhatsApp para testar, a sessão saudável fecha o incidente — mesmo com falhas
// de entrega de ANTES da queda ainda no contador.
func TestVigiaSoEmailFechaQuandoSessaoVolta(t *testing.T) {
	c := novoCenario(t)
	c.v.cfg.WhatsApp = nil
	falha := &FalhaWhatsApp{destinos: []falhaDestino{{texto: "x (websocket disconnected)"}}}
	c.sessao.set(false, true, http.StatusOK)
	c.v.RegistrarEnvio("whatsapp", "Canal", falha)
	c.checar(t)
	c.v.RegistrarEnvio("whatsapp", "Canal", falha)
	c.checar(t) // abre pela sessão
	c.sessao.set(true, true, http.StatusOK)
	c.rel.passar(10 * time.Minute)
	c.checar(t)
	if !contem(c.mail.assuntos(), "voltou") {
		t.Fatalf("com a sessão de volta e sem WhatsApp para testar, não fechou: %v", c.mail.assuntos())
	}
}

// O lembrete sempre diz a última causa medida, inclusive a do teste de volta.
func TestVigiaLembreteNuncaSaiSemCausa(t *testing.T) {
	c := novoCenario(t)
	c.sessao.set(false, true, http.StatusOK)
	c.wa.falhar = true
	c.checar(t)
	c.checar(t)
	c.sessao.set(true, true, http.StatusOK)
	c.rel.passar(10 * time.Minute)
	c.checar(t) // teste de volta falha
	c.rel.passar(vigiaLembrete)
	c.checar(t)
	ultimo := c.mail.msgs[len(c.mail.msgs)-1]
	if !strings.Contains(ultimo.Subject, "continua") || !strings.Contains(ultimo.Text, "sessão fora") {
		t.Fatalf("lembrete sem a causa do teste de volta: %q / %q", ultimo.Subject, ultimo.Text)
	}
}

func TestVigiaSucessoZeraAsFalhasDeEnvio(t *testing.T) {
	c := novoCenario(t)
	falha := errors.New("whatsapp: x (http 500: websocket disconnected)")
	c.v.RegistrarEnvio("whatsapp", "Canal", falha)
	c.v.RegistrarEnvio("whatsapp", "Canal", nil)
	c.v.RegistrarEnvio("whatsapp", "Canal", falha)
	c.v.RegistrarEnvio("smtp", "Canal", falha) // outro tipo de canal não é assunto do vigia
	c.checar(t)
	if n := len(c.mail.assuntos()); n != 0 {
		t.Fatalf("falhas intercaladas com sucesso abriram aviso: %v", c.mail.assuntos())
	}
}

func TestVigiaLembraEnquantoContinuaFora(t *testing.T) {
	c := novoCenario(t)
	c.sessao.set(false, true, http.StatusOK)
	c.wa.falhar = true
	c.checar(t)
	c.checar(t)
	c.rel.passar(vigiaLembrete)
	c.checar(t)
	got := c.mail.assuntos()
	if len(got) != 2 || !strings.Contains(got[1], "continua") {
		t.Fatalf("e-mails = %v; esperado aviso + 1 lembrete", got)
	}
}

func TestVigiaConfigDoAmbiente(t *testing.T) {
	env := map[string]string{
		"REVOADA_VIGIA_WHATSAPP":     "11900000001",
		"REVOADA_VIGIA_WHATSAPP_LID": "100000000000001",
		"REVOADA_VIGIA_EMAIL":        "operador@exemplo.com",
		"REVOADA_SMTP_HOST":          "smtp.gmail.com",
		"REVOADA_SMTP_PORT":          "465",
		"REVOADA_SMTP_USER":          "painel@exemplo.com.br",
		"REVOADA_SMTP_PASSWORD":      "segredo",
	}
	cfg := VigiaConfigDoAmbiente(func(k string) string { return env[k] })
	if cfg.WhatsApp["to"] != "11900000001" || cfg.WhatsApp["lid"] != "100000000000001" {
		t.Errorf("WhatsApp = %v", cfg.WhatsApp)
	}
	if cfg.Email["to"] != "operador@exemplo.com" || cfg.Email["host"] != "smtp.gmail.com" ||
		cfg.Email["port"] != "465" || cfg.Email["username"] != "painel@exemplo.com.br" ||
		cfg.Email["password"] != "segredo" || cfg.Email["from"] != "painel@exemplo.com.br" {
		t.Errorf("Email = %v (o remetente cai no usuário SMTP quando não é informado)", cfg.Email)
	}

	// E-mail sem servidor SMTP não tem por onde sair: fica desligado, não quebrado.
	delete(env, "REVOADA_SMTP_HOST")
	if cfg := VigiaConfigDoAmbiente(func(k string) string { return env[k] }); cfg.Email != nil {
		t.Errorf("e-mail ligado sem REVOADA_SMTP_HOST: %v", cfg.Email)
	}
	if cfg := VigiaConfigDoAmbiente(func(string) string { return "" }); cfg.Ativo() {
		t.Errorf("sem nenhuma variável o vigia deveria ficar desligado")
	}
}

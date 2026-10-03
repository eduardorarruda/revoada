package notify

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/alerting"
)

func TestRenderGroupSubjectAndBody(t *testing.T) {
	items := []AlertItem{
		{RuleName: "CPU alta", Metric: "system.cpu.utilization", State: "firing", Severity: "critical",
			Op: ">", Threshold: 90, Value: 97.5, Labels: map[string]string{"host": "web1"}, Runbook: "https://rb"},
		{RuleName: "CPU alta", Metric: "system.cpu.utilization", State: "firing", Severity: "critical",
			Op: ">", Threshold: 90, Value: 93, Labels: map[string]string{"host": "web2"}},
	}
	msg := render("", items)
	// Assunto humano (severidade em pt-BR + contagem), sem o "[CRITICAL] — DISPAROU".
	if !strings.Contains(msg.Subject, "CRÍTICO") || !strings.Contains(msg.Subject, "2 alertas") {
		t.Errorf("assunto inesperado: %q", msg.Subject)
	}
	// Corpo: frase humana + servidor + limite fraseado + runbook; SEM dump de labels crus.
	if !strings.Contains(msg.Text, "CPU consumida") || !strings.Contains(msg.Text, "97,5%") {
		t.Errorf("corpo sem a frase humana: %q", msg.Text)
	}
	// As três séries de CPU têm nomes DISTINTOS na mensagem. Um alerta que diz só
	// "Uso de CPU" sem dizer qual das três não permite decidir a ação: consumo alto
	// se resolve no seu código, roubo alto só o provedor resolve.
	for metrica, querido := range map[string]string{
		"system.cpu.utilization.total": "Uso de CPU",
		"system.cpu.utilization":       "CPU consumida",
		"system.cpu.steal":             "CPU roubada pelo hipervisor",
	} {
		if got := friendlyMetric(metrica); got != querido {
			t.Errorf("friendlyMetric(%q) = %q; quero %q", metrica, got, querido)
		}
	}
	if !strings.Contains(msg.Text, "Servidor: web1") || !strings.Contains(msg.Text, "acima do limite de 90") {
		t.Errorf("corpo sem servidor/limite: %q", msg.Text)
	}
	if strings.Contains(msg.Text, "host=web1") {
		t.Errorf("corpo não deveria despejar labels crus: %q", msg.Text)
	}
	if !strings.Contains(msg.Text, "Como resolver: https://rb") {
		t.Errorf("corpo sem runbook: %q", msg.Text)
	}
	// O corpo NÃO repete o assunto (evita a duplicação que havia no WhatsApp).
	if strings.HasPrefix(msg.Text, msg.Subject) {
		t.Errorf("corpo não deveria começar com o assunto (duplicação): %q", msg.Text)
	}
	if len(msg.Alerts) != 2 {
		t.Errorf("esperava 2 alerts no payload, obteve %d", len(msg.Alerts))
	}
}

// TestRenderHostDisplayName: quando o item traz o nome amigável (display_name), o texto
// e o HTML mostram "Servidor: <amigável>" — nunca o hostname técnico. Sem alias, cai no
// técnico. Os deep links continuam com o hostname técnico (não são afetados aqui).
func TestRenderHostDisplayName(t *testing.T) {
	// Com alias: exibe o amigável, não o técnico.
	msg := render("", []AlertItem{{
		RuleName: "CPU alta", Metric: "system.cpu.utilization", State: "firing", Severity: "warning",
		Op: ">", Threshold: 10, Value: 11, Labels: map[string]string{"host": "srv-02"}, HostDisplay: "Servidor Secundário",
	}})
	if !strings.Contains(msg.Text, "Servidor: Servidor Secundário") {
		t.Errorf("texto deveria usar o nome amigável: %q", msg.Text)
	}
	if strings.Contains(msg.Text, "Servidor: srv-02") {
		t.Errorf("texto não deveria mostrar o hostname técnico quando há alias: %q", msg.Text)
	}
	if !strings.Contains(msg.HTML, "Servidor Secundário") {
		t.Errorf("HTML deveria usar o nome amigável: %q", msg.HTML)
	}
	// Sem alias: cai no hostname técnico.
	raw := render("", []AlertItem{{
		RuleName: "CPU alta", Metric: "system.cpu.utilization", State: "firing", Severity: "warning",
		Op: ">", Threshold: 10, Value: 11, Labels: map[string]string{"host": "srv-02"},
	}})
	if !strings.Contains(raw.Text, "Servidor: srv-02") {
		t.Errorf("sem alias deveria mostrar o hostname técnico: %q", raw.Text)
	}
}

func TestRenderHumanReasons(t *testing.T) {
	// Container caído: frase clara, sem operador cru, e sem despejar labels técnicos.
	down := render("", []AlertItem{{
		RuleName: "Container caído", Metric: "container.running", State: "firing", Severity: "critical",
		Op: "<", Threshold: 1, Value: 0,
		Labels: map[string]string{"container": "api", "host": "srv1", "state": "exited", "agent.version": "0.7.0", "kernel": "6.8"},
	}})
	if !strings.Contains(down.Text, `O container "api" parou de rodar.`) || !strings.Contains(down.Text, "Servidor: srv1") {
		t.Errorf("container caído mal formatado: %q", down.Text)
	}
	for _, banned := range []string{"[min < 1]", "min < 1", "agent.version", "kernel", "container=api"} {
		if strings.Contains(down.Text, banned) {
			t.Errorf("texto contém ruído %q: %s", banned, down.Text)
		}
	}
	// Container voltou.
	up := render("", []AlertItem{{
		RuleName: "Container caído", Metric: "container.running", State: "resolved", Severity: "critical",
		Value: 1, Duration: "5m", Labels: map[string]string{"container": "api", "host": "srv1"},
	}})
	if !strings.Contains(up.Text, `O container "api" voltou a rodar.`) || !strings.HasPrefix(up.Subject, "✅") {
		t.Errorf("container resolvido mal formatado: subj=%q body=%q", up.Subject, up.Text)
	}
	// Alvo fora do ar: HTTP status legível, sem "≠ 200".
	site := render("", []AlertItem{{
		RuleName: "Fora do ar: Cursos", Metric: "http_status", State: "firing", Severity: "critical",
		Op: "≠", Threshold: 200, Value: 404,
		Labels: map[string]string{"site": "Cursos", "url": "https://x/cursos", "diagnóstico": "http_status"},
	}})
	if !strings.Contains(site.Text, `"Cursos" está fora do ar (respondeu HTTP 404).`) {
		t.Errorf("alvo fora do ar mal formatado: %q", site.Text)
	}
	if strings.Contains(site.Text, "≠") || strings.Contains(site.Text, "200") {
		t.Errorf("texto do alvo não deveria mostrar operador/limiar cru: %q", site.Text)
	}
	// A palavra "site" não pode voltar por conta própria: o mesmo monitor vigia API e
	// endpoint interno, e foi por chamar tudo de site que um aviso correto foi
	// respondido com "errado".
	if strings.Contains(site.Text, "O site") || strings.Contains(site.Subject, "Site DOWN") {
		t.Errorf("a mensagem não pode afirmar que o alvo é um site: subj=%q body=%q", site.Subject, site.Text)
	}
	if !strings.Contains(site.HTML, "Monitorado") || strings.Contains(site.HTML, ">Site<") {
		t.Errorf("o e-mail deve rotular o alvo como Monitorado: %q", site.HTML)
	}
}

// TestRenderSemDadosNaoDizResolvido é o teste do EFEITO, não só da decisão: o que
// realmente sai no canal quando o alerta é encerrado por falta de dados.
//
// Antes, `resolveStale` disparava Notify com State "resolved" e o operador recebia
// "✅ RESOLVIDO" para uma série que simplesmente parou de chegar — medido em dev:
// série interrompida às 14:30:33, alerta fechado às 14:32:51 com motivo "sem dados" e
// mensagem de recuperação. O servidor morreu e o aviso disse que ele melhorou.
func TestRenderSemDadosNaoDizResolvido(t *testing.T) {
	msg := render("", []AlertItem{{
		RuleName: "CPU alta", Metric: "system.cpu.utilization", State: alerting.StateNoData,
		Severity: "critical", Op: ">", Threshold: 90, Value: 97.5, Duration: "12m",
		Labels: map[string]string{"host": "srv1"},
	}})
	if !strings.Contains(msg.Subject, "SEM DADOS") {
		t.Errorf("o assunto precisa dizer SEM DADOS: %q", msg.Subject)
	}
	for _, proibido := range []string{"RESOLVIDO", "✅"} {
		if strings.Contains(msg.Subject, proibido) {
			t.Errorf("assunto de 'sem dados' não pode conter %q: %q", proibido, msg.Subject)
		}
	}
	if strings.Contains(msg.Text, "voltou ao normal") {
		t.Errorf("'sem dados' NÃO é 'voltou ao normal': %q", msg.Text)
	}
	// O texto tem de deixar claro que a medição parou e que pode estar pior.
	for _, exigido := range []string{"deixou de ser reportado", "NÃO por melhora", "pode continuar"} {
		if !strings.Contains(msg.Text, exigido) {
			t.Errorf("texto de 'sem dados' sem %q: %q", exigido, msg.Text)
		}
	}
	if !strings.Contains(msg.Text, "Servidor: srv1") {
		t.Errorf("texto sem o servidor afetado: %q", msg.Text)
	}
	// O HTML segue o mesmo caminho — nada de badge "Resolvido" verde no e-mail.
	if !strings.Contains(msg.HTML, "Sem dados") || strings.Contains(msg.HTML, ">Resolvido<") {
		t.Errorf("HTML de 'sem dados' mal rotulado")
	}
	if strings.Contains(msg.HTML, "#16a34a") {
		t.Error("o verde de recuperação não pode aparecer num alerta encerrado por falta de dados")
	}
}

// TestRenderHeartbeat: a regra de ausência precisa dizer, em português, o que
// aconteceu e por que isso é grave — inclusive que ela cala todas as outras regras.
func TestRenderHeartbeat(t *testing.T) {
	msg := render("", []AlertItem{{
		RuleName: "Servidor parou de reportar", Metric: alerting.HeartbeatMetric,
		State: alerting.StateFiring, Severity: "critical", Op: ">", Threshold: 300, Value: 420,
		Labels: map[string]string{"host": "srv1"}, HostDisplay: "Banco de Produção",
	}})
	if !strings.Contains(msg.Text, `O servidor "Banco de Produção" parou de reportar há 7 min.`) {
		t.Errorf("frase da regra de ausência inesperada: %q", msg.Text)
	}
	if !strings.Contains(msg.Text, "NENHUM alerta deste servidor vai disparar") {
		t.Errorf("o texto precisa dizer que as outras regras ficam cegas: %q", msg.Text)
	}
	// O valor é a idade do silêncio, e só faz sentido como tempo — nunca como "420".
	if strings.Contains(msg.Text, "420") {
		t.Errorf("a idade do silêncio não pode sair em segundos crus: %q", msg.Text)
	}
	volta := render("", []AlertItem{{
		RuleName: "Servidor parou de reportar", Metric: alerting.HeartbeatMetric,
		State: alerting.StateResolved, Severity: "critical", Value: 30, Duration: "9m",
		Labels: map[string]string{"host": "srv1"},
	}})
	if !strings.Contains(volta.Text, `O servidor "srv1" voltou a reportar.`) {
		t.Errorf("volta do servidor mal formatada: %q", volta.Text)
	}
}

// TestRenderSupressaoVisivel: a contagem de transições engolidas pelo amortecedor
// precisa aparecer. Sem ela, quem recebia o "🔁 OSCILANDO" nunca sabia quantos
// eventos foram suprimidos nem que o incidente havia terminado.
func TestRenderSupressaoVisivel(t *testing.T) {
	msg := render("", []AlertItem{{
		RuleName: "Container caído", Metric: "container.running", State: alerting.StateResolved,
		Severity: "critical", Value: 1, Duration: "22m", SuppressedCount: 17,
		Labels: map[string]string{"container": "api", "host": "srv1"},
	}})
	if !strings.Contains(msg.Text, "Mais 17 transições suprimidas na última hora") {
		t.Errorf("a resolução precisa informar quantas transições foram engolidas: %q", msg.Text)
	}
	if !strings.Contains(msg.HTML, "Mais 17 transições suprimidas") {
		t.Error("o e-mail também precisa mostrar a contagem")
	}
	// Singular sem "1 transições".
	um := render("", []AlertItem{{RuleName: "x", Metric: "m", State: alerting.StateFiring,
		Severity: "info", SuppressedCount: 1, Labels: map[string]string{}}})
	if !strings.Contains(um.Text, "Mais 1 transição suprimida") {
		t.Errorf("singular mal formatado: %q", um.Text)
	}
	// Sem supressão, nenhuma linha extra polui a mensagem.
	limpo := render("", []AlertItem{{RuleName: "x", Metric: "m", State: alerting.StateFiring,
		Severity: "info", Labels: map[string]string{}}})
	if strings.Contains(limpo.Text, "suprimid") {
		t.Errorf("mensagem sem supressão não deveria falar de supressão: %q", limpo.Text)
	}
}

func TestHumanDuration(t *testing.T) {
	cases := map[time.Duration]string{
		45 * time.Second:             "45 s",
		5 * time.Minute:              "5 min",
		2*time.Hour + 13*time.Minute: "2 h 13 min",
		1 * time.Hour:                "1 h",
		26 * time.Hour:               "1 d 2 h",
		72 * time.Hour:               "3 d",
	}
	for d, want := range cases {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%v) = %q, quer %q", d, got, want)
		}
	}
}

func TestSplitList(t *testing.T) {
	got := splitList("a@x.com, b@y.com;c@z.com d@w.com")
	if len(got) != 4 {
		t.Fatalf("esperava 4 destinatários, obteve %d: %v", len(got), got)
	}
}

// scriptSender devolve os erros de errs em ordem (nil = sucesso) e conta chamadas.
type scriptSender struct {
	errs  []error
	calls int
}

func (s *scriptSender) Send(_ context.Context, _ map[string]any, _ Message) error {
	i := s.calls
	s.calls++
	if i < len(s.errs) {
		return s.errs[i]
	}
	return nil
}

func TestSendWithRetryTransientThenSuccess(t *testing.T) {
	s := &scriptSender{errs: []error{errors.New("temporário")}}
	if err := sendWithRetry(context.Background(), s, nil, Message{}); err != nil {
		t.Fatalf("esperava sucesso após retry, obteve %v", err)
	}
	if s.calls != 2 {
		t.Fatalf("esperava 2 tentativas, obteve %d", s.calls)
	}
}

func TestSendWithRetryPermanentStops(t *testing.T) {
	// 5xx é permanente: não deve tentar a segunda (que devolveria sucesso).
	s := &scriptSender{errs: []error{&textproto.Error{Code: 550, Msg: "endereço inválido"}, nil}}
	if err := sendWithRetry(context.Background(), s, nil, Message{}); err == nil {
		t.Fatal("esperava erro permanente propagado")
	}
	if s.calls != 1 {
		t.Fatalf("erro permanente não deve repetir; tentativas=%d", s.calls)
	}
}

func TestSendWithRetryExhausts(t *testing.T) {
	s := &scriptSender{errs: []error{errors.New("t1"), errors.New("t2"), errors.New("t3"), nil}}
	if err := sendWithRetry(context.Background(), s, nil, Message{}); err == nil {
		t.Fatal("esperava erro após esgotar as tentativas")
	}
	if s.calls != maxSendAttempts {
		t.Fatalf("esperava %d tentativas, obteve %d", maxSendAttempts, s.calls)
	}
}

func TestIsPermanent(t *testing.T) {
	if !isPermanent(fmt.Errorf("smtp: %w", &textproto.Error{Code: 550})) {
		t.Error("5xx deveria ser permanente")
	}
	if isPermanent(&textproto.Error{Code: 421}) {
		t.Error("4xx (transitório) não deveria ser permanente")
	}
	if isPermanent(errors.New("genérico")) {
		t.Error("erro genérico deveria ser tratado como transitório")
	}
}

func TestSenderForUnknown(t *testing.T) {
	if _, err := senderFor("slack"); err == nil {
		t.Error("tipo desconhecido deveria dar erro")
	}
	for _, typ := range []string{"smtp", "webhook", "telegram", "whatsapp"} {
		if _, err := senderFor(typ); err != nil {
			t.Errorf("sender %s deveria existir: %v", typ, err)
		}
	}
}

// TestNormalizeWhatsAppNumber: garante DDI 55 em números nacionais, idempotente.
func TestNormalizeWhatsAppNumber(t *testing.T) {
	cases := map[string]string{
		"11990000003":       "5511990000003", // nacional 11 díg → +55
		"(11) 99000-0003":   "5511990000003", // formatado → +55
		"5511990000003":     "5511990000003", // já com 55 → mantém
		"+55 11 99000-0003": "5511990000003", // internacional formatado
		"1132654321":        "551132654321",  // fixo 10 díg → +55
		"5511987654321":     "5511987654321", // 13 díg com 55 → mantém
		"":                  "",              // vazio
	}
	for in, want := range cases {
		if got := NormalizeWhatsAppNumber(in); got != want {
			t.Errorf("NormalizeWhatsAppNumber(%q) = %q; quer %q", in, got, want)
		}
	}
}

// --- Texto do WhatsApp em português de verdade ---
//
// A mensagem vai para o celular de quem está de plantão. Ponto decimal ("97.5%"),
// bytes crus ("3456789012") e "2h13m" são legíveis para quem escreveu o código,
// não para quem acorda com o aviso.

func TestRenderAssuntoComDoisPontos(t *testing.T) {
	msg := render("", []AlertItem{{RuleName: "CPU alta", Metric: "system.cpu.utilization.total",
		State: alerting.StateFiring, Severity: "critical", Value: 97, Labels: map[string]string{}}})
	if msg.Subject != "🔴 CRÍTICO: CPU alta" {
		t.Errorf("assunto = %q, quero %q", msg.Subject, "🔴 CRÍTICO: CPU alta")
	}
	ok := render("", []AlertItem{{RuleName: "CPU alta", Metric: "system.cpu.utilization.total",
		State: alerting.StateResolved, Severity: "critical", Value: 40, Labels: map[string]string{}}})
	if ok.Subject != "✅ RESOLVIDO: CPU alta" {
		t.Errorf("assunto resolvido = %q", ok.Subject)
	}
}

func TestRenderNumerosEmPortugues(t *testing.T) {
	inicio := time.Date(2026, 9, 25, 13, 12, 0, 0, time.UTC) // 10:12 em Brasília
	msg := render("", []AlertItem{{
		RuleName: "CPU alta", Metric: "system.cpu.utilization.total", State: alerting.StateFiring,
		Severity: "critical", Op: ">", Threshold: 90, Value: 97.46, Since: inicio,
		Labels: map[string]string{"host": "web1"},
	}})
	quer := "Uso de CPU em 97,5%, acima do limite de 90%.\nServidor: web1\nComeçou: 25/09 10:12"
	if !strings.Contains(msg.Text, quer) {
		t.Errorf("texto:\n%s\nquero conter:\n%s", msg.Text, quer)
	}
	if strings.Contains(msg.Text, "97.5") || strings.Contains(msg.Text, "90.0") {
		t.Errorf("ponto decimal não pode aparecer: %q", msg.Text)
	}
	// Sem Since não há o que dizer — e em resolvido/sem dados a linha não cabe.
	semInicio := render("", []AlertItem{{RuleName: "x", Metric: "system.cpu.utilization.total",
		State: alerting.StateFiring, Severity: "info", Value: 1, Labels: map[string]string{}}})
	if strings.Contains(semInicio.Text, "Começou") {
		t.Errorf("sem Since não pode haver 'Começou': %q", semInicio.Text)
	}
	resolvido := render("", []AlertItem{{RuleName: "x", Metric: "system.cpu.utilization.total",
		State: alerting.StateResolved, Severity: "info", Value: 1, Since: inicio, Duration: "5 min", Labels: map[string]string{}}})
	if strings.Contains(resolvido.Text, "Começou") {
		t.Errorf("resolvido não repete 'Começou': %q", resolvido.Text)
	}
}

func TestFmtValueBytesELimiares(t *testing.T) {
	casos := []struct {
		metric string
		v      float64
		quer   string
	}{
		{"system.memory.used", 512, "512 B"},
		{"system.memory.used", 1536, "1,5 KB"},
		{"system.memory.used", 7 * 1024 * 1024, "7 MB"},
		{"system.memory.available", 3.2 * 1024 * 1024 * 1024, "3,2 GB"},
		{"system.filesystem.total", 122.8 * 1024 * 1024 * 1024, "123 GB"},
		{"container.memory.usage", 256 * 1024 * 1024, "256 MB"},
		{"system.network.io.bytes_recv", 2 * 1024 * 1024, "2 MB"},
		{"system.memory.utilization", 97.46, "97,5%"},
		{"system.memory.utilization", 90, "90%"},
		{"system.processes.count", 391, "391"},
		{"system.cpu.load_average.1m", 3.5, "3,50"},
		{"container.restarts", 3, "3"},
	}
	for _, c := range casos {
		if got := fmtValue(c.metric, c.v); got != c.quer {
			t.Errorf("fmtValue(%q, %v) = %q, quero %q", c.metric, c.v, got, c.quer)
		}
	}
}

// "Ficou fora por" é frase de coisa que cai (site, container, servidor mudo). Para
// CPU alta que voltou ao normal a frase certa é "Durou".
func TestRenderDuracaoConformeOTipo(t *testing.T) {
	cpu := render("", []AlertItem{{RuleName: "CPU alta", Metric: "system.cpu.utilization.total",
		State: alerting.StateResolved, Severity: "warning", Value: 40, Duration: "5 min", Labels: map[string]string{"host": "web1"}}})
	if !strings.Contains(cpu.Text, "Durou: 5 min") || strings.Contains(cpu.Text, "Ficou fora") {
		t.Errorf("métrica resolvida: %q", cpu.Text)
	}
	site := render("", []AlertItem{{RuleName: "Fora do ar: Cursos", Metric: "http_status",
		State: alerting.StateResolved, Severity: "critical", Value: 200, Duration: "5 min",
		Labels: map[string]string{"site": "Cursos", "url": "https://x"}}})
	if !strings.Contains(site.Text, "Ficou fora por: 5 min") {
		t.Errorf("site resolvido: %q", site.Text)
	}
	cont := render("", []AlertItem{{RuleName: "Container caído", Metric: "container.running",
		State: alerting.StateResolved, Severity: "critical", Value: 1, Duration: "5 min",
		Labels: map[string]string{"container": "api", "host": "srv1"}}})
	if !strings.Contains(cont.Text, "Ficou fora por: 5 min") {
		t.Errorf("container resolvido: %q", cont.Text)
	}
}

// Os nomes que a pessoa vê na tela (web/src/metrics/dict.ts) são os que ela lê no
// aviso. "system.memory.available em 700 MB" é para máquina; "RAM disponível" é para gente.
func TestFriendlyMetricEspelhaOPainel(t *testing.T) {
	casos := map[string]string{
		"system.memory.available":      "RAM disponível",
		"system.filesystem.used":       "Disco usado",
		"system.cpu.load_average.5m":   "Carga média (5 min)",
		"system.processes.count":       "Processos",
		"container.memory.usage":       "RAM usada pelo container",
		"system.network.io.bytes_recv": "Rede recebida",
		"metrica.que.ninguem.conhece":  "metrica.que.ninguem.conhece",
	}
	for metrica, quer := range casos {
		if got := friendlyMetric(metrica); got != quer {
			t.Errorf("friendlyMetric(%q) = %q, quero %q", metrica, got, quer)
		}
	}
	msg := render("", []AlertItem{{RuleName: "Memória baixa", Metric: "system.memory.available",
		State: alerting.StateFiring, Severity: "warning", Op: "<", Threshold: 1073741824, Value: 734003200,
		Labels: map[string]string{"host": "mail"}}})
	if !strings.Contains(msg.Text, "RAM disponível em 700 MB, abaixo do limite de 1 GB.") {
		t.Errorf("texto da memória: %q", msg.Text)
	}
}

// Para alertas de site, `Metric` é a própria URL monitorada. Uma URL com "bytes" ou
// "utilization" no caminho não pode mudar como o status HTTP é escrito.
func TestRenderStatusHTTPNaoPassaPelasHeuristicas(t *testing.T) {
	msg := render("", []AlertItem{{RuleName: "Fora do ar: Loja", Metric: "https://bytesclub.exemplo/utilization",
		State: alerting.StateFiring, Severity: "critical", Op: "≠", Threshold: 200, Value: 500,
		Labels: map[string]string{"site": "Loja", "url": "https://bytesclub.exemplo/utilization", "diagnóstico": "http_status"}}})
	if !strings.Contains(msg.Text, `"Loja" está fora do ar (respondeu HTTP 500).`) {
		t.Errorf("status HTTP mal formatado: %q", msg.Text)
	}
	for _, v := range []float64{math.NaN(), math.Inf(1)} {
		if got := fmtValue("system.memory.utilization", v); got != "—" {
			t.Errorf("fmtValue(%v) = %q, quero —", v, got)
		}
	}
}

// O assunto diz o ERRO e ONDE: é a linha em negrito do WhatsApp, a única que muita
// gente lê antes de decidir se levanta da cama.
func TestRenderAssuntoDizOServidor(t *testing.T) {
	casos := []struct {
		nome  string
		items []AlertItem
		quer  string
	}{
		{"servidor com apelido", []AlertItem{{RuleName: "CPU alta", Metric: "system.cpu.utilization.total", State: alerting.StateFiring,
			Severity: "critical", Value: 97, Labels: map[string]string{"host": "srv-01"}, HostDisplay: "VPS Principal"}},
			"🔴 CRÍTICO: CPU alta · VPS Principal"},
		{"container nomeia os dois", []AlertItem{{RuleName: "Container caído", Metric: "container.running", State: alerting.StateFiring,
			Severity: "critical", Value: 0, Labels: map[string]string{"container": "app-web", "host": "srv-reserva-02"}, HostDisplay: "Servidor Reserva"}},
			`🔴 CRÍTICO: Container caído · "app-web" em Servidor Reserva`},
		{"dois servidores são nomeados", []AlertItem{
			{RuleName: "CPU alta", Metric: "system.cpu.utilization.total", State: alerting.StateFiring, Severity: "critical", Value: 97, Labels: map[string]string{"host": "web1"}},
			{RuleName: "CPU alta", Metric: "system.cpu.utilization.total", State: alerting.StateFiring, Severity: "critical", Value: 93, Labels: map[string]string{"host": "web2"}}},
			"🔴 CRÍTICO: CPU alta · web1, web2 (2 alertas)"},
		{"site não repete o alvo", []AlertItem{{RuleName: "Fora do ar: Loja", Metric: "https://loja.exemplo", State: alerting.StateFiring,
			Severity: "critical", Value: 500, Labels: map[string]string{"site": "Loja", "url": "https://loja.exemplo", "diagnóstico": "http_status"}}},
			"🔴 CRÍTICO: Fora do ar: Loja"},
		{"resolvido também diz onde", []AlertItem{{RuleName: "CPU alta", Metric: "system.cpu.utilization.total", State: alerting.StateResolved,
			Severity: "critical", Value: 40, Duration: "5 min", Labels: map[string]string{"host": "web1"}}},
			"✅ RESOLVIDO: CPU alta · web1"},
	}
	for _, c := range casos {
		if got := render("", c.items).Subject; got != c.quer {
			t.Errorf("%s: assunto = %q, quero %q", c.nome, got, c.quer)
		}
	}
}

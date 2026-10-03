package alerting

import (
	"context"
	"io"
	"log/slog"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/query"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// fakeQuerier registra as consultas pedidas e devolve uma resposta fixa. Serve para
// inspecionar a FAIXA DE TEMPO que o avaliador pede — que é onde morava o bug do
// balde parcial — sem precisar de ClickHouse.
type fakeQuerier struct {
	reqs []query.Request
	resp query.Response
	// respFn, quando definida, monta a resposta a partir da requisição — permite
	// simular o efeito do descarte do balde incompleto.
	respFn func(query.Request) query.Response
	err    error
}

func (f *fakeQuerier) QuerySeries(_ context.Context, req query.Request) (query.Response, error) {
	f.reqs = append(f.reqs, req)
	if f.respFn != nil {
		return f.respFn(req), f.err
	}
	return f.resp, f.err
}

// testEvaluator monta um avaliador sem banco. Só os caminhos que não tocam o store
// (evalRule com resposta vazia, containerReporting, decideStale) podem usá-lo.
//
// O `st` nil é PROPOSITAL e funciona como canário: qualquer caminho que chegue ao
// banco antes da hora entra em pânico e derruba o teste. É assim que
// TestPisoDeAvaliacoesConsecutivas prova que uma avaliação isolada não vira consulta.
func testEvaluator(q seriesQuerier) *Evaluator {
	return &Evaluator{
		q:             q,
		log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		pending:       map[string]time.Time{},
		flappedAt:     map[string]time.Time{},
		lastSeen:      map[string]time.Time{},
		streak:        map[string]evalStreak{},
		lastBucket:    map[string]int64{},
		suppress:      map[string][]time.Time{},
		transitions:   map[string][]time.Time{},
		flapClose:     map[string]pendingClose{},
		budget:        map[int64]*notifyBudget{},
		freshRules:    map[int64]int{},
		primeiroCiclo: map[int64][]Notification{},
		purge:         make(chan int64, 1),
	}
}

// notifierEspiao conta o que saiu por cada caminho: mensagem individual (fan-out) e
// resumo agrupado. É como os testes do primeiro ciclo distinguem "126 notificações" de
// "uma mensagem".
type notifierEspiao struct {
	individuais []Notification
	lotes       [][]Notification
}

func (n *notifierEspiao) Notify(_ context.Context, note Notification) {
	n.individuais = append(n.individuais, note)
}

func (n *notifierEspiao) NotifyBatch(_ context.Context, notes []Notification) {
	n.lotes = append(n.lotes, notes)
}

// notifierSemLote é um Notifier que NÃO implementa BatchNotifier — é o que o
// notify.Router é hoje. Serve para provar o modo degradado.
type notifierSemLote struct{ vistas []Notification }

func (n *notifierSemLote) Notify(_ context.Context, note Notification) {
	n.vistas = append(n.vistas, note)
}

// TestEvalRangeContemBaldeFechadoInteiro é a prova de que o defeito do balde parcial
// morreu no avaliador.
//
// Não basta a Query API descartar o balde aberto: pedindo apenas [now-window, now],
// o único balde fechado da faixa costuma estar TRUNCADO À ESQUERDA (a consulta
// filtra ts >= from, e o from cai no meio dele) — às 18:02, uma regra de "média de 5
// minutos" leria o balde 17:55 com só os 3 minutos a partir de 17:57. Aqui a
// propriedade exigida é forte: a faixa pedida SEMPRE contém pelo menos um balde
// alinhado e INTEIRAMENTE dentro dela, para qualquer instante do relógio.
func TestEvalRangeContemBaldeFechadoInteiro(t *testing.T) {
	const window = 300
	base := time.Date(2026, 8, 7, 18, 0, 0, 0, time.UTC)

	// Varre todo segundo dentro de uma janela: nenhum instante pode ficar sem balde.
	for offset := 0; offset < window; offset++ {
		now := base.Add(time.Duration(offset) * time.Second)
		from, to := evalRange(now, window)
		if !temBaldeFechadoInteiro(from, to, window) {
			t.Fatalf("now=%s: a faixa [%s, %s] não contém nenhum balde de %ds inteiro",
				now.Format(time.RFC3339), from.Format(time.RFC3339), to.Format(time.RFC3339), window)
		}
	}

	// Anti-regressão: a faixa de UMA janela (o que existia antes) falha na maior
	// parte dos instantes. Se alguém encolher evalRangeWindows para 1, o teste acima
	// quebra — e este documenta o porquê.
	falhas := 0
	for offset := 1; offset < window; offset++ {
		now := base.Add(time.Duration(offset) * time.Second)
		if !temBaldeFechadoInteiro(now.Add(-window*time.Second), now, window) {
			falhas++
		}
	}
	if falhas == 0 {
		t.Error("a faixa de uma janela só deveria funcionar no relógio alinhado; algo mudou no alinhamento")
	}
}

// temBaldeFechadoInteiro diz se existe um balde alinhado ao relógio (mesmo
// alinhamento do toStartOfInterval do ClickHouse) começando em >= from e terminando
// em <= to.
func temBaldeFechadoInteiro(from, to time.Time, window int64) bool {
	w := window
	primeiro := (from.Unix() + w - 1) / w * w // primeiro início de balde >= from
	return primeiro+w <= to.Unix()
}

// TestEvalRulePedeDuasJanelas trava o que o avaliador manda para a Query API: a
// faixa tem de ser mais larga que a janela da regra (ver evalRangeWindows) e o step
// tem de continuar sendo a janela — é o step que define o balde da agregação.
func TestEvalRulePedeDuasJanelas(t *testing.T) {
	f := &fakeQuerier{}
	e := testEvaluator(f)
	now := time.Date(2026, 8, 7, 18, 5, 12, 0, time.UTC)
	r := store.AlertRule{ID: 1, Metric: "system.cpu.utilization", Agg: "avg", WindowSeconds: 300, ConditionOp: ">", Threshold: 80}

	if ok := e.evalRule(context.Background(), r, now, map[string]bool{}); !ok {
		t.Fatal("consulta sem erro deveria marcar a regra como avaliada")
	}
	if len(f.reqs) != 1 {
		t.Fatalf("esperava 1 consulta, veio %d", len(f.reqs))
	}
	req := f.reqs[0]
	if req.Step != 300 {
		t.Errorf("step = %d; esperava 300 (a janela da regra define o balde)", req.Step)
	}
	from, err := time.Parse(time.RFC3339, req.From)
	if err != nil {
		t.Fatalf("from inválido: %v", err)
	}
	if got := now.Sub(from); got != 600*time.Second {
		t.Errorf("faixa pedida = %v; esperava 600s (2 janelas) — com 1 janela o balde fechado vem truncado à esquerda", got)
	}
	if req.IncludePartial {
		t.Error("a avaliação de VALOR nunca pode incluir o balde aberto (foi o que fez a regra disparar lendo 1 amostra)")
	}
}

// TestContainerReportingPedeBaldeParcial cobre a regressão que o descarte do balde
// incompleto poderia causar: containerReporting pergunta PRESENÇA ("ainda chega
// alguma amostra?"). Se o descarte apagasse a única amostra recente, o reconcile
// concluiria "container removido" e resolveria o alerta — mandando uma notificação
// de "resolvido" para um container que continua caído.
func TestContainerReportingPedeBaldeParcial(t *testing.T) {
	valor := 1.0
	// Simula o efeito do descarte: a única amostra recente caiu no balde que ainda
	// não fechou, então ela só volta na resposta se a consulta pedir IncludePartial.
	f := &fakeQuerier{respFn: func(req query.Request) query.Response {
		if !req.IncludePartial {
			return query.Response{Series: []query.Series{{Values: []*float64{nil}}}}
		}
		return query.Response{Series: []query.Series{{Values: []*float64{&valor}}}}
	}}
	e := testEvaluator(f)
	if !e.containerReporting(context.Background(), "srv1", "web") {
		t.Fatal("amostra no balde aberto prova presença: não pode virar 'container removido'")
	}
	if len(f.reqs) != 1 || !f.reqs[0].IncludePartial {
		t.Fatalf("a checagem de presença precisa pedir IncludePartial, veio %+v", f.reqs)
	}

	// Container de fato removido (nenhuma amostra, nem no balde aberto) => false.
	f2 := &fakeQuerier{resp: query.Response{Series: []query.Series{{Values: []*float64{nil}}}}}
	if testEvaluator(f2).containerReporting(context.Background(), "srv1", "web") {
		t.Error("sem nenhuma amostra na janela o container é considerado removido")
	}

	// Falha de leitura => true (conservador: erro de consulta não resolve alerta).
	f3 := &fakeQuerier{err: context.DeadlineExceeded}
	if !testEvaluator(f3).containerReporting(context.Background(), "srv1", "web") {
		t.Error("erro de consulta NUNCA pode virar 'container removido'")
	}
}

// TestDecideStaleNaoAutoResolvePorEngano é a trava do caminho mais perigoso do
// avaliador. Resolver por engano dispara uma notificação de "resolvido" para um
// problema que continua acontecendo — e o descarte do balde incompleto mexeu
// exatamente na entrada `seen` desta decisão.
func TestDecideStaleNaoAutoResolvePorEngano(t *testing.T) {
	ativo := staleInput{ruleExists: true, ruleEnabled: true, ruleOK: true, windowSeconds: 300}

	// Consulta que falhou, com a série ausente há muito tempo: mesmo assim não resolve.
	consultaFalhou := comAusencia(ativo, time.Hour)
	consultaFalhou.ruleOK = false

	regraRemovida := ativo
	regraRemovida.ruleExists, regraRemovida.ruleEnabled = false, false

	regraDesativada := ativo
	regraDesativada.ruleEnabled, regraDesativada.seen = false, true

	semHistorico := ativo // série ausente neste ciclo e sem lastSeen registrado

	casos := []struct {
		nome string
		in   staleInput
		want staleAction
	}{
		{"série presente neste ciclo => mantém", comSeen(ativo), staleKeep},
		{"consulta FALHOU => mantém (falha de leitura não é ausência de problema)", consultaFalhou, staleKeep},
		{"série sumiu agora, sem histórico => só começa a contar a carência", semHistorico, staleStartClock},
		{"buraco pontual (1 ciclo de 30 s) => mantém", comAusencia(ativo, 30*time.Second), staleKeep},
		{"ausência de 5 min com janela de 5 min => dentro da carência (3 janelas)", comAusencia(ativo, 5*time.Minute), staleKeep},
		{"ausência de 15 min com janela de 5 min => resolve (a série sumiu de verdade)", comAusencia(ativo, 15*time.Minute), staleResolve},
		{"regra removida => resolve na hora", regraRemovida, staleResolve},
		{"regra desativada => resolve na hora", regraDesativada, staleResolve},
	}
	for _, c := range casos {
		if got, _ := decideStale(c.in); got != c.want {
			t.Errorf("%s: decideStale = %v; esperava %v", c.nome, got, c.want)
		}
	}
}

// TestStaleNotificationSeparaMotivos trava as duas confusões que faziam o painel
// mentir ao encerrar um alerta sozinho.
//
//  1. "parou de reportar" saía como "✅ RESOLVIDO": série interrompida às 14:30:33,
//     alerta fechado às 14:32:51 com motivo "sem dados" e o operador recebendo que o
//     servidor tinha voltado ao normal. Agora é um estado próprio (nodata);
//  2. desligar a regra mandava "RESOLVIDO": `decideStale` devolvia staleResolve para
//     regra desativada e o reconcile passava a regra adiante, então o Notify saía.
//     Desligar um monitor não conserta o servidor — fecha calado.
func TestStaleNotificationSeparaMotivos(t *testing.T) {
	casos := []struct {
		reason     staleReason
		wantState  string
		wantNotify bool
	}{
		{reasonNoData, StateNoData, true},
		{reasonRuleDisabled, StateResolved, false},
		{reasonRuleRemoved, StateResolved, false},
		{reasonHostRemoved, StateResolved, false},
		{reasonContainerRemoved, StateResolved, true},
		// Ignorar é decisão de quem opera o painel, não mudança no servidor: fecha calado.
		{reasonContainerIgnored, StateResolved, false},
	}
	for _, c := range casos {
		state, notify := staleNotification(c.reason)
		if state != c.wantState || notify != c.wantNotify {
			t.Errorf("staleNotification(%q) = (%q, %v); esperava (%q, %v)",
				c.reason, state, notify, c.wantState, c.wantNotify)
		}
	}
	// A garantia que importa: "sem dados" NUNCA pode sair como resolvido.
	if state, _ := staleNotification(reasonNoData); state == StateResolved {
		t.Fatal("'sem dados' saindo como 'resolvido' é exatamente o defeito que este teste existe para impedir")
	}
}

// TestPisoDeAvaliacoesConsecutivas reproduz a série 91/89 a cada 30 s que gerou 10
// alert_events em 9 minutos.
//
// Com o piso, nenhum dos sentidos acumula duas avaliações seguidas, e a oscilação
// morre antes de virar alerta. A prova é dupla: os contadores nunca chegam ao piso e
// — porque `st` é nil neste avaliador — nenhuma dessas avaliações chegou sequer a
// consultar o banco (se chegasse, o teste entraria em pânico).
func TestPisoDeAvaliacoesConsecutivas(t *testing.T) {
	if minConsecutiveEvals < 2 {
		t.Fatalf("o piso precisa ser de no mínimo 2 avaliações, é %d", minConsecutiveEvals)
	}
	e := testEvaluator(&fakeQuerier{})
	r := store.AlertRule{ID: 1, Name: "CPU alta", Metric: "system.cpu.utilization",
		Agg: "avg", ConditionOp: ">", Threshold: 90, WindowSeconds: 300, ForSeconds: 0}
	labels := map[string]string{"host": "srv1"}
	fp := fingerprint(r.ID, labels)
	now := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)

	// 18 ciclos de 30 s alternando 91 (viola) e 89 (não viola) = 9 minutos.
	for i := 0; i < 18; i++ {
		now = now.Add(30 * time.Second)
		if i%2 == 0 {
			e.onMet(context.Background(), r, labels, fp, 91, now)
		} else {
			e.onClear(context.Background(), r, labels, fp, 89)
		}
	}
	if got := e.streak[fp]; got.met >= minConsecutiveEvals || got.clear >= minConsecutiveEvals {
		t.Fatalf("a oscilação alcançou o piso (met=%d clear=%d); nenhum sentido pode chegar a %d",
			got.met, got.clear, minConsecutiveEvals)
	}

	// Já a violação SUSTENTADA passa: a segunda avaliação seguida atinge o piso.
	if n := e.bumpStreak("outra", true); n != 1 {
		t.Fatalf("1ª avaliação deveria contar 1, contou %d", n)
	}
	if n := e.bumpStreak("outra", true); n < minConsecutiveEvals {
		t.Fatalf("2ª avaliação consecutiva deveria alcançar o piso, contou %d", n)
	}
	// …e um único ciclo no sentido oposto zera a sequência (é o que mata a oscilação).
	if n := e.bumpStreak("outra", false); n != 1 {
		t.Fatalf("o sentido oposto deveria recomeçar do 1, contou %d", n)
	}
	if e.streak["outra"].met != 0 {
		t.Error("a avaliação oposta tem de zerar o contador do outro sentido")
	}
}

// TestSuppressaoVisivel cobre a parte 7 da auditoria: a supressão precisa ser
// contável e o fim do incidente precisa ser anunciado.
func TestSuppressaoVisivel(t *testing.T) {
	e := testEvaluator(&fakeQuerier{})
	now := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	fp := "1|host=srv1;"

	for i := 0; i < 5; i++ {
		e.recordSuppressed(fp, now.Add(time.Duration(i)*time.Minute))
	}
	if got := e.suppressedCount(fp, now.Add(5*time.Minute)); got != 5 {
		t.Errorf("esperava 5 transições suprimidas na janela, obteve %d", got)
	}
	// Fora da janela de uma hora, a contagem volta a zero (a mensagem diz "na última
	// hora"; um número acumulado desde sempre não significaria nada).
	if got := e.suppressedCount(fp, now.Add(2*time.Hour)); got != 0 {
		t.Errorf("transições de mais de uma hora não deveriam contar, obteve %d", got)
	}

	// A resolução guardada só sai quando o surto passa — mas SAI.
	flapAt := now
	// A janela de oscilação é PROPORCIONAL à janela da regra: com o piso de avaliações
	// contando evidência nova, um ciclo abre→fecha→abre custa ~minConsecutiveEvals
	// janelas, e uma constante de 10 min nunca chegava a conter flapThreshold
	// transições numa regra de 5 min — o amortecedor existia e nunca engatava.
	janela := flapWindowFor(300)
	if flapCloseDue(flapAt, now.Add(janela), janela) {
		t.Error("ainda dentro da janela de oscilação: a resolução não pode ser anunciada")
	}
	if !flapCloseDue(flapAt, now.Add(janela+time.Second), janela) {
		t.Error("passada a janela, a resolução TEM de ser anunciada — senão o incidente termina em silêncio")
	}
	if !flapCloseDue(time.Time{}, now, janela) {
		t.Error("sem surto em curso, nada a esperar")
	}
	// Uma regra de janela curta não pode herdar a janela de uma regra de janela longa:
	// é isso que torna a detecção alcançável em ambas.
	if flapWindowFor(3600) <= flapWindowFor(60) {
		t.Error("a janela de oscilação tem de crescer com a janela da regra")
	}
}

func comSeen(in staleInput) staleInput { in.seen = true; return in }

func comAusencia(in staleInput, d time.Duration) staleInput {
	in.seen = false
	in.hasLastSeen = true
	in.sinceLastSeen = d
	return in
}

// TestStaleGrace: a carência nunca é menor que 90 s e acompanha janelas longas.
func TestStaleGrace(t *testing.T) {
	if got := staleGrace(10); got != noDataMinGrace {
		t.Errorf("janela curta deveria usar a carência mínima (%v), veio %v", noDataMinGrace, got)
	}
	if got := staleGrace(300); got != 15*time.Minute {
		t.Errorf("janela de 5 min => carência de 15 min (3 janelas), veio %v", got)
	}
}

func TestRuleFilterSets(t *testing.T) {
	// Sem hosts: regra global — um único conjunto, os filtros crus.
	global := store.AlertRule{Filters: map[string]string{"env": "prod"}}
	sets := ruleFilterSets(global)
	if len(sets) != 1 || sets[0]["env"] != "prod" || len(sets[0]) != 1 {
		t.Fatalf("global deveria render 1 conjunto = filtros crus, obteve %v", sets)
	}

	// Com hosts: uma consulta por servidor, host=<hostname> somado aos filtros base.
	scoped := store.AlertRule{Filters: map[string]string{"env": "prod"}, Hosts: []string{"srv1", "srv2", ""}}
	sets = ruleFilterSets(scoped)
	if len(sets) != 2 {
		t.Fatalf("2 hosts válidos deveriam render 2 conjuntos, obteve %d: %v", len(sets), sets)
	}
	for i, want := range []string{"srv1", "srv2"} {
		if sets[i]["host"] != want || sets[i]["env"] != "prod" {
			t.Errorf("conjunto %d = %v, esperava host=%s env=prod", i, sets[i], want)
		}
	}
	// Não deve mutar os filtros da regra original.
	if _, ok := global.Filters["host"]; ok {
		t.Error("ruleFilterSets não pode mutar os filtros da regra")
	}

	// Só hosts vazios: cai de volta para global (evita não consultar nada).
	empty := store.AlertRule{Filters: map[string]string{}, Hosts: []string{"", ""}}
	if sets := ruleFilterSets(empty); len(sets) != 1 {
		t.Fatalf("hosts só-vazios deveriam cair para global (1 conjunto), obteve %v", sets)
	}

	// Multi-container global: um filtro `container` com vírgulas vira N conjuntos, um
	// por container (cada um exato). Um só container (sem vírgula) segue como está.
	multi := store.AlertRule{Filters: map[string]string{"container": "web, worker ,db"}}
	sets = ruleFilterSets(multi)
	if len(sets) != 3 {
		t.Fatalf("3 containers deveriam render 3 conjuntos, obteve %d: %v", len(sets), sets)
	}
	gotC := map[string]bool{}
	for _, s := range sets {
		if len(s) != 1 {
			t.Errorf("conjunto multi-container deveria ter só o label container, obteve %v", s)
		}
		gotC[s["container"]] = true
	}
	for _, want := range []string{"web", "worker", "db"} {
		if !gotC[want] {
			t.Errorf("faltou o container %q nos conjuntos: %v", want, sets)
		}
	}

	// Multi-container + hosts: produto cartesiano host × container.
	both := store.AlertRule{Filters: map[string]string{"container": "web,worker"}, Hosts: []string{"srv1", "srv2"}}
	sets = ruleFilterSets(both)
	if len(sets) != 4 {
		t.Fatalf("2 hosts × 2 containers deveriam render 4 conjuntos, obteve %d: %v", len(sets), sets)
	}
	seen := map[string]bool{}
	for _, s := range sets {
		if s["host"] == "" || s["container"] == "" {
			t.Errorf("cada conjunto precisa de host e container, obteve %v", s)
		}
		seen[s["host"]+"/"+s["container"]] = true
	}
	for _, want := range []string{"srv1/web", "srv1/worker", "srv2/web", "srv2/worker"} {
		if !seen[want] {
			t.Errorf("faltou o par %q: %v", want, sets)
		}
	}

	// Um único container (sem vírgula) não expande — mantém o filtro cru.
	one := store.AlertRule{Filters: map[string]string{"container": "web"}}
	if sets := ruleFilterSets(one); len(sets) != 1 || sets[0]["container"] != "web" {
		t.Fatalf("1 container deveria render 1 conjunto container=web, obteve %v", sets)
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		v    float64
		op   string
		th   float64
		want bool
	}{
		{10, ">", 5, true}, {5, ">", 5, false},
		{5, ">=", 5, true}, {4, ">=", 5, false},
		{4, "<", 5, true}, {5, "<", 5, false},
		{5, "<=", 5, true}, {6, "<=", 5, false},
		{5, "==", 5, true}, {5, "==", 6, false},
		{5, "??", 5, false}, // operador inválido nunca dispara
	}
	for _, c := range cases {
		if got := compare(c.v, c.op, c.th); got != c.want {
			t.Errorf("compare(%v %s %v) = %v, quer %v", c.v, c.op, c.th, got, c.want)
		}
	}
}

func TestLastValue(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	if v, ok := lastValue([]*float64{f(1), nil, f(3)}); !ok || v != 3 {
		t.Errorf("esperava 3, obteve %v ok=%v", v, ok)
	}
	if v, ok := lastValue([]*float64{f(1), nil}); !ok || v != 1 {
		t.Errorf("esperava 1 (pula nil final), obteve %v ok=%v", v, ok)
	}
	if _, ok := lastValue([]*float64{nil, nil}); ok {
		t.Error("tudo nil deveria devolver ok=false")
	}
	if _, ok := lastValue(nil); ok {
		t.Error("vazio deveria devolver ok=false")
	}
}

func TestFingerprintStable(t *testing.T) {
	// ordem dos labels não pode alterar o fingerprint (senão duplica alertas).
	a := fingerprint(7, map[string]string{"host": "h1", "job": "web"})
	b := fingerprint(7, map[string]string{"job": "web", "host": "h1"})
	if a != b {
		t.Errorf("fingerprint instável: %q != %q", a, b)
	}
	if same := fingerprint(8, map[string]string{"host": "h1"}); same == a {
		t.Error("regras diferentes deveriam ter fingerprints diferentes")
	}
}

// TestFingerprintIgnoraLabelsDeEstado é a prova de que a identidade do alerta parou
// de mudar junto com o problema.
//
// Reproduzido ao vivo antes da correção: mantendo o mesmo problema (valor 100) e
// trocando só `state=running` → `state=exited, exit_code=137`, o painel abriu um
// alerta DUPLICADO e auto-resolveu o antigo com "✅ RESOLVIDO" — com a condição ainda
// verdadeira. E como detectFlapping conta reaberturas POR FINGERPRINT, o crash-loop
// (onde o exit_code gira a cada reinício) nunca chegava a flapThreshold: 26 mensagens
// em 6 minutos, ~130/canal/hora, nenhuma marcada como oscilante.
func TestFingerprintIgnoraLabelsDeEstado(t *testing.T) {
	rodando := map[string]string{
		"host": "srv1", "container": "api",
		"image": "app:1.2.0", "state": "running", "health": "healthy", "exit_code": "0",
	}
	caido := map[string]string{
		"host": "srv1", "container": "api",
		"image": "app:1.3.0", "state": "exited", "health": "unhealthy", "exit_code": "137",
	}
	if fingerprint(9, rodando) != fingerprint(9, caido) {
		t.Fatalf("o estado do container não pode mudar a identidade do alerta:\n  %q\n  %q",
			fingerprint(9, rodando), fingerprint(9, caido))
	}

	// Reinício seguinte do crash-loop: só o exit_code muda. Mesmo incidente.
	outroCodigo := map[string]string{"host": "srv1", "container": "api", "state": "exited", "exit_code": "1"}
	semEstado := map[string]string{"host": "srv1", "container": "api"}
	if fingerprint(9, outroCodigo) != fingerprint(9, semEstado) {
		t.Error("um exit_code diferente a cada reinício não pode render um alerta novo a cada reinício")
	}

	// Atualização do agente/kernel também não pode reabrir alerta.
	antes := map[string]string{"host": "srv1", "agent.version": "0.7.0", "kernel": "6.8.0"}
	depois := map[string]string{"host": "srv1", "agent.version": "0.8.1", "kernel": "6.9.2"}
	if fingerprint(9, antes) != fingerprint(9, depois) {
		t.Error("atualizar o agente não pode duplicar os alertas abertos daquele servidor")
	}

	// O outro lado, que é o mais perigoso: labels de IDENTIDADE continuam separando
	// séries. Fundir duas identidades transformaria dois problemas reais em um só, e
	// um deles simplesmente sumiria da tela.
	base := map[string]string{"host": "srv1", "container": "api", "state": "running"}
	distintos := []struct {
		nome   string
		labels map[string]string
	}{
		{"outro servidor", map[string]string{"host": "srv2", "container": "api", "state": "running"}},
		{"outro container", map[string]string{"host": "srv1", "container": "worker", "state": "running"}},
		{"outro ponto de montagem", map[string]string{"host": "srv1", "container": "api", "mount": "/var", "state": "running"}},
		{"rótulo próprio do usuário", map[string]string{"host": "srv1", "container": "api", "env": "prod", "state": "running"}},
	}
	for _, d := range distintos {
		if fingerprint(9, base) == fingerprint(9, d.labels) {
			t.Errorf("%s deveria ser outro alerta, veio com a mesma impressão digital", d.nome)
		}
	}
}

// TestCompareNaN: defesa em profundidade. NaN/±Inf nunca podem virar comparação
// verdadeira — e, sobretudo, nunca podem passar despercebidos por operador nenhum.
func TestCompareNaN(t *testing.T) {
	nan := math.NaN()
	inf := math.Inf(1)
	for _, op := range []string{">", ">=", "<", "<=", "=="} {
		if compare(nan, op, 90) {
			t.Errorf("compare(NaN %s 90) não pode ser verdadeiro", op)
		}
		if compare(inf, op, 90) {
			t.Errorf("compare(+Inf %s 90) não pode ser verdadeiro", op)
		}
		if compare(50, op, nan) {
			t.Errorf("compare(50 %s NaN) não pode ser verdadeiro (limiar corrompido)", op)
		}
	}
}

// TestPontoIsoladoNaoViraAlerta é a prova de que o piso de avaliações consecutivas
// conta EVIDÊNCIA NOVA, e não tique de relógio.
//
// O piso sozinho não bastava, e isso foi medido na pilha viva: o avaliador roda a
// cada 30 s e pede DUAS janelas (evalRangeWindows), então um balde fechado continua
// sendo o "último valor não-nulo" nos ciclos seguintes enquanto nada mais recente
// chega. Uma ÚNICA amostra (qa.spike, um ponto às 19:01:05) foi lida por dois tiques
// consecutivos, o piso deu-se por satisfeito e o alerta abriu às 19:02:01 — fechando
// às 19:03:31 como "sem dados". Um pico de um segundo custou 1 alert_event e 4
// mensagens. O piso existia justamente para impedir isso; ele só chegava um ciclo
// atrasado.
//
// A propriedade exigida aqui é a que fecha o buraco: reler o MESMO balde não conta
// como segunda avaliação. Sem `st`, qualquer tentativa de abrir alerta entra em
// pânico — é o canário que prova que nada foi persistido.
func TestPontoIsoladoNaoViraAlerta(t *testing.T) {
	const window = 30
	base := time.Date(2026, 8, 9, 19, 1, 0, 0, time.UTC)
	valor := 100.0

	// A resposta devolve SEMPRE o mesmo balde (19:01:00), como acontece de verdade
	// quando a série morreu depois de uma amostra só.
	f := &fakeQuerier{respFn: func(query.Request) query.Response {
		return query.Response{
			TS: []int64{base.Unix()},
			Series: []query.Series{{
				Labels: map[string]string{"host": "qa-spike"},
				Values: []*float64{&valor},
			}},
		}
	}}
	e := testEvaluator(f)
	r := store.AlertRule{ID: 28, Metric: "qa.spike", Agg: "avg", WindowSeconds: window,
		ConditionOp: ">", Threshold: 90}

	// Seis ciclos de 30 s lendo o mesmo balde: nenhum pode abrir alerta.
	for i := 0; i < 6; i++ {
		now := base.Add(time.Duration(30*(i+1)) * time.Second)
		e.evalRule(context.Background(), r, now, map[string]bool{})
	}
	fp := fingerprint(r.ID, map[string]string{"host": "qa-spike"})
	if got := e.streak[fp].met; got != 1 {
		t.Errorf("avaliações consecutivas = %d; esperava 1 — o mesmo balde relido não é evidência nova", got)
	}

	// Contraprova: baldes DISTINTOS acima do limiar têm de somar. Aqui o piso é
	// alcançado e o caminho chega ao banco (st nil) — o pânico é o sinal de que
	// abriu, e é o que separa "não abre nunca" de "não abre por um ponto só".
	novoBalde := base.Add(60 * time.Second)
	f.respFn = func(query.Request) query.Response {
		return query.Response{
			TS: []int64{novoBalde.Unix()},
			Series: []query.Series{{
				Labels: map[string]string{"host": "qa-spike"},
				Values: []*float64{&valor},
			}},
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("dois baldes DISTINTOS acima do limiar deveriam abrir alerta (e tocar o store)")
		}
	}()
	e.evalRule(context.Background(), r, novoBalde.Add(30*time.Second), map[string]bool{})
}

// TestColapsoDeSeriesPorFingerprint é a prova do item 2: N séries que colapsam no
// MESMO fingerprint rendem UMA decisão por ciclo.
//
// O defeito: o fingerprint ignora volatileLabels, então o `container.running` de um
// container só chega como DUAS séries (state=running e state=exited). Antes, as duas
// eram avaliadas em sequência no mesmo ciclo — onMet e depois onClear — e o bumpStreak
// zerava o contador do sentido oposto. O piso de minConsecutiveEvals nunca era
// alcançado: o alerta não abria NUNCA por esse caminho, e só saía quando uma das séries
// desaparecia. Medido: uma série cruza o limiar e o alerta abre em 23 s; duas séries do
// mesmo fingerprint, com o mesmo problema, levam 5 MINUTOS.
//
// Duas propriedades exigidas:
//   - progresso de UM ciclo por ciclo (met=1 depois do primeiro; antes ficava met=0,
//     clear=1, para sempre);
//   - o lastBucket não anda para trás: fica com o balde MAIS RECENTE entre as séries.
//     Regravá-lo com o mais antigo fazia o ciclo seguinte descartar a leitura nova como
//     "mesmo balde já avaliado" — o freio do balde relido travando o alerta.
func TestColapsoDeSeriesPorFingerprint(t *testing.T) {
	base := time.Date(2026, 8, 9, 21, 0, 0, 0, time.UTC)
	baldeVelho, baldeNovo := base.Unix(), base.Add(60*time.Second).Unix()
	caido, dePe := 0.0, 1.0

	// Mesmo container, duas séries: a antiga (ainda "running", balde velho) e a atual
	// ("exited", balde novo). Só o label volátil difere → mesmo fingerprint.
	f := &fakeQuerier{respFn: func(query.Request) query.Response {
		return query.Response{
			TS: []int64{baldeVelho, baldeNovo},
			Series: []query.Series{
				{
					Labels: map[string]string{"host": "srv1", "container": "api", "state": "running"},
					Values: []*float64{&dePe, nil},
				},
				{
					Labels: map[string]string{"host": "srv1", "container": "api", "state": "exited", "exit_code": "137"},
					Values: []*float64{nil, &caido},
				},
			},
		}
	}}
	e := testEvaluator(f)
	r := store.AlertRule{ID: 7, Name: "Container caído", Metric: containerRunningMetric,
		Agg: "min", WindowSeconds: 60, ConditionOp: "<", Threshold: 1}

	seen := map[string]bool{}
	e.evalRule(context.Background(), r, base.Add(90*time.Second), seen)

	fp := fingerprint(r.ID, map[string]string{"host": "srv1", "container": "api"})
	if len(seen) != 1 || !seen[fp] {
		t.Fatalf("as duas séries têm de colapsar num fingerprint só, veio %v", seen)
	}
	st := e.streak[fp]
	if st.met != 1 || st.clear != 0 {
		t.Fatalf("um ciclo tem de render UMA decisão (met=1 clear=0); veio met=%d clear=%d — "+
			"é o cancelamento mútuo que fazia o alerta demorar 5 min em vez de 23 s", st.met, st.clear)
	}
	if got := e.lastBucket[fp]; got != baldeNovo {
		t.Fatalf("lastBucket = %d; esperava o balde MAIS RECENTE (%d) — andar para trás faz o "+
			"ciclo seguinte descartar a leitura nova como balde relido", got, baldeNovo)
	}

	// Segundo ciclo, balde ainda mais novo: o piso é alcançado e o caminho chega ao
	// banco (st nil) — o pânico prova que o alerta ABRE em dois ciclos, e não nunca.
	maisNovo := base.Add(120 * time.Second)
	f.respFn = func(query.Request) query.Response {
		return query.Response{
			TS: []int64{maisNovo.Unix()},
			Series: []query.Series{
				{Labels: map[string]string{"host": "srv1", "container": "api", "state": "exited"}, Values: []*float64{&caido}},
				{Labels: map[string]string{"host": "srv1", "container": "api", "state": "running"}, Values: []*float64{&caido}},
			},
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("duas avaliações seguidas violando deveriam abrir o alerta (e tocar o store)")
		}
	}()
	e.evalRule(context.Background(), r, maisNovo.Add(30*time.Second), map[string]bool{})
}

// TestEscolheSerie trava os critérios do colapso, um a um.
func TestEscolheSerie(t *testing.T) {
	velha := serieColapsada{value: 1, bucket: 100, hasTS: true}
	nova := serieColapsada{value: 0, bucket: 200, hasTS: true, viola: true}

	if got := escolheSerie(velha, nova); got.bucket != 200 {
		t.Error("o balde mais recente tem de vencer, em qualquer ordem de chegada")
	}
	if got := escolheSerie(nova, velha); got.bucket != 200 {
		t.Error("o balde mais recente tem de vencer também quando chega primeiro")
	}

	// Sem eixo de tempo não dá para saber se a leitura é velha: perde para quem tem.
	semTS := serieColapsada{value: 0, hasTS: false, viola: true}
	if got := escolheSerie(velha, semTS); !got.hasTS {
		t.Error("leitura sem balde não pode desbancar uma com balde conhecido")
	}
	if got := escolheSerie(semTS, semTS); got.hasTS {
		t.Error("sem nenhuma com balde, fica a que havia (determinismo)")
	}

	// Empate no MESMO balde: vence quem viola. Se uma das séries diz que o container
	// esteve fora naquele intervalo, ele esteve fora — esconder isso perde o incidente.
	ok := serieColapsada{value: 1, bucket: 300, hasTS: true, viola: false}
	ruim := serieColapsada{value: 0, bucket: 300, hasTS: true, viola: true}
	if got := escolheSerie(ok, ruim); !got.viola {
		t.Error("empate no mesmo balde tem de ficar com a leitura que viola")
	}
	if got := escolheSerie(ruim, ok); !got.viola {
		t.Error("empate no mesmo balde tem de ficar com a leitura que viola, em qualquer ordem")
	}
}

// TestPrimeiroCicloEntregaResumoUnico é a prova do item 3: a regra recém-semeada não
// faz fan-out no primeiro ciclo.
//
// Medido no dev: a regra de ausência nasce ligada, global e com ChannelIDs vazio (=
// todos os canais); no primeiro ciclo ela encontrou 69 hosts do inventário que não
// reportavam → 69 eventos e 126 notificações em dois minutos, todas com alert_count=1.
// Em produção, isso é uma mensagem por canal por servidor desativado, no minuto do
// deploy. O exigido aqui: UMA entrega agrupada, ZERO mensagens individuais.
func TestPrimeiroCicloEntregaResumoUnico(t *testing.T) {
	espiao := &notifierEspiao{}
	e := testEvaluator(&fakeQuerier{})
	e.notifier = espiao
	r := store.AlertRule{ID: 26, Name: heartbeatRuleName, Metric: HeartbeatMetric}

	e.marcaRecemSemeada(r.ID)
	if _, largada := e.freshRules[r.ID]; !largada {
		t.Fatal("a regra semeada tem de entrar no regime de primeiro ciclo")
	}
	const hosts = 69
	for i := 0; i < hosts; i++ {
		e.primeiroCiclo[r.ID] = append(e.primeiroCiclo[r.ID], Notification{
			Rule: r, Labels: map[string]string{"host": "srv" + strconv.Itoa(i)}, State: StateFiring,
		})
	}

	// O regime dura primeiroCicloCiclos avaliações: com o piso de minConsecutiveEvals,
	// NADA abre no primeiro ciclo, e um regime de um ciclo só entregaria um resumo vazio
	// e deixaria a enxurrada acontecer no segundo (medido ao vivo: 69 alertas, 0 resumos).
	now := time.Date(2026, 8, 9, 4, 1, 0, 0, time.UTC)
	for i := 0; i < primeiroCicloCiclos; i++ {
		if i > 0 && len(espiao.lotes) > 0 {
			t.Fatalf("o resumo saiu no ciclo %d; tem de sair no fim do regime", i)
		}
		e.flushPrimeiroCiclo(context.Background(), now.Add(time.Duration(i)*30*time.Second))
	}

	if n := len(espiao.individuais); n != 0 {
		t.Fatalf("%d mensagens individuais no primeiro ciclo; tem de ser 0 (era 1 por host, por canal)", n)
	}
	if n := len(espiao.lotes); n != 1 {
		t.Fatalf("esperava UM resumo agrupado, veio %d", n)
	}
	if n := len(espiao.lotes[0]); n != hosts {
		t.Fatalf("o resumo tem de citar os %d servidores, citou %d — agrupar não pode perder informação", hosts, n)
	}

	// Degradado: notificador SEM suporte a lote (é o notify.Router hoje) entrega UMA
	// mensagem com a contagem dos demais — nunca 69, nunca silêncio.
	simples := &notifierSemLote{}
	e2 := testEvaluator(&fakeQuerier{})
	e2.notifier = simples
	e2.marcaRecemSemeada(r.ID)
	for i := 0; i < hosts; i++ {
		e2.primeiroCiclo[r.ID] = append(e2.primeiroCiclo[r.ID], Notification{Rule: r, State: StateFiring})
	}
	for i := 0; i < primeiroCicloCiclos; i++ {
		e2.flushPrimeiroCiclo(context.Background(), now)
	}
	if len(simples.vistas) != 1 {
		t.Fatalf("sem suporte a lote, esperava 1 mensagem, veio %d", len(simples.vistas))
	}
	if got := simples.vistas[0].ThrottledCount; got != hosts-1 {
		t.Fatalf("a mensagem única tem de dizer que há mais %d; disse %d", hosts-1, got)
	}

	// …e o regime especial acaba aí: do segundo ciclo em diante a regra notifica normal.
	if _, ainda := e.freshRules[r.ID]; ainda || len(e.primeiroCiclo[r.ID]) != 0 {
		t.Fatal("a regra tem de sair do regime de primeiro ciclo depois do resumo")
	}
}

// TestTetoHorarioPorRegra: passado o teto, a regra para de mandar mensagem e o que foi
// engolido vira CONTAGEM na próxima mensagem que couber. A diferença entre "não te
// avisei" e "te avisei que houve mais 37".
func TestTetoHorarioPorRegra(t *testing.T) {
	e := testEvaluator(&fakeQuerier{})
	now := time.Date(2026, 8, 9, 4, 0, 0, 0, time.UTC)
	n := Notification{Rule: store.AlertRule{ID: 26, Name: heartbeatRuleName}}

	for i := 0; i < maxNotifyPerRuleHour; i++ {
		if !e.gastaOrcamento(26, now, &n) {
			t.Fatalf("a mensagem %d estava dentro do teto de %d e foi barrada", i+1, maxNotifyPerRuleHour)
		}
	}
	// Meia hora depois o surto continua: as 20 do começo ainda contam na janela, então
	// tudo é engolido — mas engolido é diferente de esquecido.
	surto := now.Add(30 * time.Minute)
	for i := 0; i < 37; i++ {
		if e.gastaOrcamento(26, surto, &n) {
			t.Fatal("passado o teto horário, a regra não pode mais entregar mensagem")
		}
	}
	// Passada a hora das primeiras 20, a janela deslizou: entrega de novo, e a mensagem
	// leva a conta do que foi agrupado no meio do caminho.
	if !e.gastaOrcamento(26, now.Add(time.Hour+time.Second), &n) {
		t.Fatal("passada a hora, o teto tem de liberar de novo")
	}
	if n.ThrottledCount != 37 {
		t.Fatalf("ThrottledCount = %d; esperava 37 — o teto não pode ser censura silenciosa", n.ThrottledCount)
	}
}

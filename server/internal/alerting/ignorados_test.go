package alerting

import (
	"context"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/query"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

func TestIgnoradosCasaHostEContainerExatos(t *testing.T) {
	ig := novosIgnorados([]store.IgnoredContainer{
		{Host: "srv-reserva", Container: "app-web"},
	})
	casos := []struct {
		nome   string
		labels map[string]string
		quer   bool
	}{
		{"o próprio container", map[string]string{"host": "srv-reserva", "container": "app-web"}, true},
		{"mesmo nome em outro servidor continua vigiado", map[string]string{"host": "outro", "container": "app-web"}, false},
		{"outro container do mesmo servidor continua vigiado", map[string]string{"host": "srv-reserva", "container": "worker"}, false},
		{"alerta do servidor (sem container) não é afetado", map[string]string{"host": "srv-reserva"}, false},
		{"nome parecido não conta", map[string]string{"host": "srv-reserva", "container": "app-web-2"}, false},
		{"sem rótulos", nil, false},
	}
	for _, c := range casos {
		if got := ig.contem(c.labels); got != c.quer {
			t.Errorf("%s: contem(%v) = %v; esperava %v", c.nome, c.labels, got, c.quer)
		}
	}
	var vazio ignorados
	if vazio.contem(map[string]string{"host": "a", "container": "b"}) {
		t.Fatal("conjunto vazio (nil) não ignora nada")
	}
}

// O container ignorado some do ciclo: sem `seen`, sem estado. Não pode abrir alerta
// nem manter um aberto vivo. O vizinho do mesmo servidor segue avaliado.
func TestEvalRulePulaContainerIgnorado(t *testing.T) {
	zero := 0.0
	base := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	ignorado := map[string]string{"host": "srv-reserva", "container": "app-web"}
	vizinho := map[string]string{"host": "srv-reserva", "container": "worker"}
	f := &fakeQuerier{resp: query.Response{
		TS: []int64{base.Unix()},
		Series: []query.Series{
			{Labels: ignorado, Values: []*float64{&zero}},
			{Labels: vizinho, Values: []*float64{&zero}},
		},
	}}
	e := testEvaluator(f)
	e.ignorados = novosIgnorados([]store.IgnoredContainer{{Host: "srv-reserva", Container: "app-web"}})
	r := store.AlertRule{ID: 7, Metric: containerRunningMetric, Agg: "min", WindowSeconds: 60,
		ConditionOp: "<", Threshold: 1}

	seen := map[string]bool{}
	e.evalRule(context.Background(), r, base.Add(90*time.Second), seen)

	if seen[fingerprint(r.ID, ignorado)] {
		t.Fatal("container ignorado não pode entrar no ciclo")
	}
	if _, ok := e.streak[fingerprint(r.ID, ignorado)]; ok {
		t.Fatal("container ignorado não acumula avaliações (não abre alerta)")
	}
	if !seen[fingerprint(r.ID, vizinho)] {
		t.Fatal("o outro container do mesmo servidor continua vigiado")
	}
}

func TestValidarIgnorado(t *testing.T) {
	casos := []struct {
		host, container string
		ok              bool
	}{
		{"srv-reserva", "app-web", true},
		{"srv-reserva", "standby-pg", true},
		{"srv.exemplo.com", "app_1.web", true},
		{"", "app-web", false},
		{"srv-reserva", "", false},
		{"srv-reserva", "   ", false},
		{"srv-reserva", "a,b", false}, // vírgula é o separador de multi-container nas regras
		{"srv-reserva", "../x", false},
		{"srv-reserva", string(make([]byte, 300)), false},
	}
	for _, c := range casos {
		_, _, err := validarIgnorado(c.host, c.container)
		if (err == nil) != c.ok {
			t.Errorf("validarIgnorado(%q, %q) err=%v; esperava ok=%v", c.host, c.container, err, c.ok)
		}
	}
	h, ctr, err := validarIgnorado("  srv-reserva ", " app-web ")
	if err != nil || h != "srv-reserva" || ctr != "app-web" {
		t.Fatalf("espaços nas pontas são aparados: %q %q %v", h, ctr, err)
	}
}

// Ignorar precisa ESQUECER o que o avaliador sabia do container. Sem isso, voltar a
// vigiar um container ainda parado reabria o alerta na primeira avaliação (a
// sequência congelada já estava no piso) e com o início antigo — o piso de 2
// avaliações seguidas contaria duas leituras separadas pelo período ignorado.
func TestIgnorarEsqueceEstadoDoContainer(t *testing.T) {
	zero := 0.0
	base := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	ignorado := map[string]string{"host": "srv-reserva", "container": "app-web"}
	f := &fakeQuerier{resp: query.Response{
		TS:     []int64{base.Unix()},
		Series: []query.Series{{Labels: ignorado, Values: []*float64{&zero}}},
	}}
	e := testEvaluator(f)
	r := store.AlertRule{ID: 7, Metric: containerRunningMetric, Agg: "min", WindowSeconds: 60,
		ConditionOp: "<", Threshold: 1}
	fp := fingerprint(r.ID, ignorado)
	// Estado de um alerta que estava disparando antes de ignorar.
	e.streak[fp] = evalStreak{met: 5}
	e.pending[fp] = base.Add(-2 * time.Hour)
	e.lastBucket[fp] = base.Add(-time.Minute).Unix()
	e.lastSeen[fp] = base.Add(-time.Minute)
	e.flapClose[fp] = pendingClose{rule: r, labels: ignorado}

	e.ignorados = novosIgnorados([]store.IgnoredContainer{{Host: "srv-reserva", Container: "app-web"}})
	e.evalRule(context.Background(), r, base.Add(90*time.Second), map[string]bool{})

	if _, ok := e.streak[fp]; ok {
		t.Fatal("a sequência de avaliações do container ignorado tem de ser esquecida")
	}
	if _, ok := e.pending[fp]; ok {
		t.Fatal("o início antigo não pode sobreviver ao período ignorado")
	}
	if _, ok := e.flapClose[fp]; ok {
		t.Fatal("aviso de oscilação pendente de container ignorado tem de ser descartado")
	}
}

type notificadorGravador struct{ enviadas []Notification }

func (n *notificadorGravador) Notify(_ context.Context, x Notification) {
	n.enviadas = append(n.enviadas, x)
}

// Container que parou de reportar ENQUANTO um "resolvido" de oscilação esperava o
// surto passar: não passa pelo evalRule, então o flush é quem tem de calar.
func TestFlapCloseDeContainerIgnoradoNaoNotifica(t *testing.T) {
	e := testEvaluator(&fakeQuerier{})
	gravador := &notificadorGravador{}
	e.notifier = gravador
	labels := map[string]string{"host": "srv-reserva", "container": "app-web"}
	r := store.AlertRule{ID: 7, Metric: containerRunningMetric, WindowSeconds: 60}
	fp := fingerprint(r.ID, labels)
	agora := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	e.flapClose[fp] = pendingClose{rule: r, labels: labels, since: agora.Add(-time.Hour)}
	e.flappedAt[fp] = agora.Add(-24 * time.Hour) // surto já passou: estaria vencido

	e.ignorados = novosIgnorados([]store.IgnoredContainer{{Host: "srv-reserva", Container: "app-web"}})
	e.flushFlapClosures(context.Background(), agora)

	if len(gravador.enviadas) != 0 {
		t.Fatalf("container ignorado não pode gerar \"resolvido\": %+v", gravador.enviadas)
	}
	if _, ok := e.flapClose[fp]; ok {
		t.Fatal("o aviso pendente tem de ser descartado")
	}
}

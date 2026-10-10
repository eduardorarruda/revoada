package alerting

import (
	"context"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/query"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

func TestBaseBlueGreen(t *testing.T) {
	casos := []struct {
		nome string
		base string
		ok   bool
	}{
		{"talk-blue", "talk", true},
		{"talk-green", "talk", true},
		{"api_blue", "api", true},
		{"app-green-1", "app", true},
		{"app_blue_2", "app", true},
		{"Talk-Blue", "talk", true},
		{"worker-app", "", false},
		{"blue", "", false},          // sem nome-base: não é um par
		{"blueprint", "", false},     // só o sufixo conta
		{"talk-blue-x", "", false},   // sufixo seguido de outra coisa
		{"talk-greenish", "", false}, // não é a cor exata
	}
	for _, c := range casos {
		base, ok := baseBlueGreen(c.nome)
		if ok != c.ok || base != c.base {
			t.Errorf("baseBlueGreen(%q) = (%q, %v); esperava (%q, %v)", c.nome, base, ok, c.base, c.ok)
		}
	}
}

// avaliaContainers roda UMA avaliação da regra "Container caído" com os containers
// dados (nome → rodando?) num mesmo servidor e devolve o `seen` do ciclo.
func avaliaContainers(t *testing.T, e *Evaluator, containers map[string]bool) map[string]bool {
	t.Helper()
	base := time.Date(2026, 10, 10, 13, 30, 0, 0, time.UTC)
	var series []query.Series
	for nome, rodando := range containers {
		v := 0.0
		if rodando {
			v = 1
		}
		series = append(series, query.Series{Labels: map[string]string{"host": "srv-app", "container": nome}, Values: []*float64{&v}})
	}
	e.q = &fakeQuerier{resp: query.Response{TS: []int64{base.Unix()}, Series: series}}
	e.cobertosBG = nil
	seen := map[string]bool{}
	e.evalRule(context.Background(), regraContainer, base.Add(90*time.Second), seen)
	return seen
}

var regraContainer = store.AlertRule{ID: 3, Metric: containerRunningMetric, Agg: "min", WindowSeconds: 60,
	ConditionOp: "<", Threshold: 1}

func fpContainer(nome string) string {
	return fingerprint(regraContainer.ID, map[string]string{"host": "srv-app", "container": nome})
}

// Num deploy blue/green a cor antiga é parada de propósito a cada deploy; sem a
// regra, isso rendia centenas de "Container caído" por dia com o serviço no ar.
func TestBlueGreenCorParadaComAOutraNoArNaoAlerta(t *testing.T) {
	e := testEvaluator(&fakeQuerier{})
	avaliaContainers(t, e, map[string]bool{"talk-blue": false, "talk-green": true})
	if _, ok := e.streak[fpContainer("talk-blue")]; ok {
		t.Fatal("a cor parada no deploy não pode acumular avaliações (não abre alerta)")
	}
	if !e.cobertosBG.contem(map[string]string{"host": "srv-app", "container": "talk-blue"}) {
		t.Fatal("a cor parada fica marcada como coberta (o reconcile fecha o alerta aberto, calado)")
	}
	if _, ok := e.streak[fpContainer("talk-green")]; !ok {
		t.Fatal("a cor no ar continua avaliada normalmente")
	}
}

func TestBlueGreenAsDuasCoresParadasAlerta(t *testing.T) {
	e := testEvaluator(&fakeQuerier{})
	avaliaContainers(t, e, map[string]bool{"talk-blue": false, "talk-green": false})
	if e.streak[fpContainer("talk-blue")].met != 1 || e.streak[fpContainer("talk-green")].met != 1 {
		t.Fatalf("serviço inteiro fora do ar tem de alertar: blue=%+v green=%+v",
			e.streak[fpContainer("talk-blue")], e.streak[fpContainer("talk-green")])
	}
	if len(e.cobertosBG) != 0 {
		t.Fatal("nada é coberto quando nenhuma cor está no ar")
	}
}

func TestContainerSemParContinuaAlertando(t *testing.T) {
	e := testEvaluator(&fakeQuerier{})
	avaliaContainers(t, e, map[string]bool{"worker-app": false, "talk-green": true})
	if e.streak[fpContainer("worker-app")].met != 1 {
		t.Fatal("container fora do padrão blue/green segue a regra de sempre")
	}
}

// Par em OUTRO servidor não cobre: a cor de reserva tem de estar no mesmo host.
func TestBlueGreenSoCobreNoMesmoServidor(t *testing.T) {
	cob := cobertosBlueGreen(map[string]serieColapsada{
		"a": {labels: map[string]string{"host": "srv-1", "container": "api-blue"}, viola: true},
		"b": {labels: map[string]string{"host": "srv-2", "container": "api-green"}, viola: false},
	})
	if len(cob) != 0 {
		t.Fatalf("par em servidores diferentes não cobre: %v", cob)
	}
}

func TestStaleBlueGreenFechaCalado(t *testing.T) {
	if state, notify := staleNotification(reasonBlueGreen); state != StateResolved || notify {
		t.Fatalf("troca de cor no deploy fecha calado: (%q, %v)", state, notify)
	}
}

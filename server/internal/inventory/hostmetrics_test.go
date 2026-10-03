package inventory

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/eduardorarruda/revoada/server/internal/health"
)

// TestNetJSONOmiteLacuna trava o CONTRATO no fio: taxa não calculável não pode
// aparecer como 0 no JSON — a lista de Servidores desenha travessão quando o campo
// não vem, e imprimiria "↓ 0 B/s" se viesse zerado.
func TestNetJSONOmiteLacuna(t *testing.T) {
	semTaxa, err := json.Marshal(HostMetrics{Host: "h", Net: NetRate{}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(semTaxa), "rx_bps") || strings.Contains(string(semTaxa), "tx_bps") {
		t.Errorf("sem taxa calculável os campos têm de ser omitidos, veio %s", semTaxa)
	}
	comTaxa, err := json.Marshal(HostMetrics{Host: "h", Net: NetRate{RxBps: ptr(0), TxBps: ptr(1024)}})
	if err != nil {
		t.Fatal(err)
	}
	// Zero MEDIDO continua no fio: é um valor, não uma lacuna.
	if !strings.Contains(string(comTaxa), `"rx_bps":0`) || !strings.Contains(string(comTaxa), `"tx_bps":1024`) {
		t.Errorf("taxa medida (inclusive 0) tem de ir no fio, veio %s", comTaxa)
	}
}

// TestNetRate trava a distinção entre "taxa medida e deu zero" (host quieto, um
// valor legítimo) e "não deu para calcular a taxa" — que antes saía como o MESMO 0 e
// virava "↓ 0 B/s ↑ 0 B/s" na lista de Servidores para um host sendo medido.
func TestNetRate(t *testing.T) {
	cases := []struct {
		name               string
		delta, recuo, secs float64
		want               *float64 // nil = lacuna (não calculável)
	}{
		{"taxa normal", 1000, 0, 10, ptr(100)},
		{"sem variação é ZERO medido, não lacuna", 0, 0, 60, ptr(0)},
		{"um ponto só na janela (secs=0) => sem taxa", 400, 0, 0, nil},
		{"secs negativo => sem taxa", 400, 0, -5, nil},
		{"avanço negativo é dado corrompido, não medição", -100, 0, 60, nil},
		{"fracionário", 512, 0, 0.5, ptr(1024)},
		// O degrau como ÚLTIMO passo da janela (o instante seguinte a um reboot): não
		// sobra passo positivo, o avanço é 0 — e publicar isso como "0 B/s" seria dizer
		// "não trafegou" sobre um host que está transmitindo. É lacuna.
		{"contador reiniciou e não sobrou avanço => lacuna, não 0", 0, -88000, 60, nil},
		{"reiniciou no MEIO da janela, com avanço depois => taxa vale", 900, -88500, 60, ptr(15)},
	}
	for _, c := range cases {
		got := netRate(c.delta, c.recuo, c.secs)
		switch {
		case c.want == nil && got != nil:
			t.Errorf("%s: netRate(%v,%v) = %v; want nil (lacuna, nunca 0)", c.name, c.delta, c.secs, *got)
		case c.want != nil && got == nil:
			t.Errorf("%s: netRate(%v,%v) = nil; want %v", c.name, c.delta, c.secs, *c.want)
		case c.want != nil && got != nil && *got != *c.want:
			t.Errorf("%s: netRate(%v,%v) = %v; want %v", c.name, c.delta, c.secs, *got, *c.want)
		}
	}
}

// TestSomaDegrausPositivos reproduz, em Go, a conta que a consulta faz no
// ClickHouse — para que a REGRA fique travada mesmo que ninguém consiga rodar o
// ClickHouse no CI. Os três casos são os que importam:
//
//   - contador monotônico: o resultado tem de ser idêntico ao antigo (último−primeiro),
//     senão a correção mudaria o número de todo mundo para consertar um caso raro;
//   - degrau negativo no meio (atualização de agente, reboot, restart de interface):
//     só o degrau some, o resto da janela continua valendo — antes a janela inteira
//     virava "sem dados" e a coluna Rede mostrava "— · —" por 5 minutos;
//   - um ponto só: continua sem taxa, porque taxa exige dois pontos.
func TestSomaDegrausPositivos(t *testing.T) {
	somaPositivos := func(vals []float64) float64 {
		var s float64
		for i := 1; i < len(vals); i++ {
			if d := vals[i] - vals[i-1]; d > 0 {
				s += d
			}
		}
		return s
	}
	cases := []struct {
		name string
		vals []float64
		want float64
	}{
		{"monotônico bate com último−primeiro", []float64{100, 200, 350, 500}, 400},
		{"degrau negativo no meio não condena a janela", []float64{120_000, 120_500, 32_000, 32_400}, 900},
		{"um ponto só não tem avanço", []float64{700}, 0},
		{"tudo igual: avanço zero, e zero medido é um valor", []float64{5, 5, 5}, 0},
	}
	for _, c := range cases {
		if got := somaPositivos(c.vals); got != c.want {
			t.Errorf("%s: soma dos degraus positivos de %v = %v; quero %v", c.name, c.vals, got, c.want)
		}
	}
	// O caso monotônico TEM de coincidir com a conta antiga — é o que garante que
	// nenhum host correto muda de número por causa desta correção.
	vals := []float64{100, 200, 350, 500}
	if antiga := vals[len(vals)-1] - vals[0]; somaPositivos(vals) != antiga {
		t.Errorf("monotônico divergiu da conta antiga: %v vs %v", somaPositivos(vals), antiga)
	}
}

// fs monta um mountFS com todos os campos presentes (atalho para os testes).
func fs(mount string, pct, used, total float64) *mountFS {
	return &mountFS{Mount: mount, Pct: ptr(pct), Used: ptr(used), Total: ptr(total)}
}

func TestWorstMount(t *testing.T) {
	// O mount de maior pct vence, e used/total devolvidos são os DESSE mount.
	mounts := map[string]*mountFS{
		"/":     fs("/", 40, 200, 500),
		"/var":  fs("/var", 92, 920, 1000),
		"/home": fs("/home", 10, 10, 100),
	}
	w := worstMount(mounts)
	if w.Mount != "/var" || *w.Pct != 92 || *w.Used != 920 || *w.Total != 1000 {
		t.Errorf("worstMount = %+v; want /var 92%% 920/1000 (used/total pareados ao mesmo mount)", w)
	}
}

func TestWorstMountSingle(t *testing.T) {
	// Agente antigo (sem label mount) => um único filesystem "/".
	w := worstMount(map[string]*mountFS{"/": fs("/", 55, 5, 10)})
	if w.Mount != "/" || *w.Pct != 55 || *w.Used != 5 {
		t.Errorf("worstMount único = %+v; want / 55%% 5/10", w)
	}
}

func TestWorstMountEmpty(t *testing.T) {
	if w := worstMount(nil); w.Pct != nil || w.Mount != "" {
		t.Errorf("worstMount vazio deve ser zero-value com Pct nil, veio %+v", w)
	}
}

// TestWorstMountSemUtilization cobre o disco que aparece no resultado (por causa de
// used/total) mas sem utilization: não pode ser eleito "pior" com pct 0, senão o
// card afirma "disco livre" sem ter medido ocupação nenhuma.
func TestWorstMountSemUtilization(t *testing.T) {
	semPct := &mountFS{Mount: "/backup", Used: ptr(1.0), Total: ptr(2.0)}
	w := worstMount(map[string]*mountFS{"/backup": semPct, "/": fs("/", 30, 3, 10)})
	if w.Mount != "/" || w.Pct == nil || *w.Pct != 30 {
		t.Errorf("mount sem utilization não pode vencer a escolha, veio %+v", w)
	}
	apenasSemPct := map[string]*mountFS{"/backup": semPct}
	if w := worstMount(apenasSemPct); w.Pct != nil {
		t.Errorf("só mounts sem utilization => Pct nil (sem dado), veio %+v", w)
	}
}

func TestDisksFrom(t *testing.T) {
	mounts := map[string]*mountFS{
		"/var": fs("/var", 92, 920, 1000),
		"/":    fs("/", 40, 200, 500),
	}
	// disk defaults: warn=75, crit=90.
	disks := disksFrom(mounts, 75, 90)
	if len(disks) != 2 {
		t.Fatalf("esperado 2 discos, veio %d", len(disks))
	}
	// Ordenado por mount: "/" antes de "/var".
	if disks[0].Mount != "/" || disks[0].State != "ok" {
		t.Errorf("disks[0] = %+v; want mount=/ state=ok", disks[0])
	}
	if disks[1].Mount != "/var" || disks[1].State != "crit" {
		t.Errorf("disks[1] = %+v; want mount=/var state=crit", disks[1])
	}
	if disks[1].Used == nil || *disks[1].Used != 920 {
		t.Errorf("used_bytes de /var deve ser 920 (pareado), veio %v", disks[1].Used)
	}
}

// TestDisksFromSemUtilization: montagem sem utilization sai como "nodata", não como
// disco 0% verde.
func TestDisksFromSemUtilization(t *testing.T) {
	mounts := map[string]*mountFS{"/dados": {Mount: "/dados", Used: ptr(10.0), Total: ptr(100.0)}}
	disks := disksFrom(mounts, 75, 90)
	if len(disks) != 1 {
		t.Fatalf("esperado 1 disco, veio %d", len(disks))
	}
	if disks[0].State != health.StateNoData {
		t.Errorf("disco sem utilization deveria ser %q, veio %q (pct %v)", health.StateNoData, disks[0].State, disks[0].Pct)
	}
	// Os bytes que CHEGARAM continuam sendo mostrados — a ausência é só do pct.
	if disks[0].Used == nil || *disks[0].Used != 10 {
		t.Errorf("used_bytes reportado deveria sobreviver, veio %v", disks[0].Used)
	}
}

// TestHealthMetricFromAusente é o coração do defeito "métrica ausente vira 0% verde":
// sem valor, o estado tem de ser "nodata" — nunca "ok".
func TestHealthMetricFromAusente(t *testing.T) {
	m := healthMetricFrom(nil, nil, nil, 70, 90, nil)
	if m.State != health.StateNoData {
		t.Errorf("métrica ausente deveria ser %q, veio %q", health.StateNoData, m.State)
	}
	if m.State == health.StateOK {
		t.Error("métrica ausente NUNCA pode virar ok verde (era o bug: 'CPU 0% — verde')")
	}
	// Presente e baixa continua ok, com o pct real.
	if got := healthMetricFrom(ptr(3.0), nil, nil, 70, 90, nil); got.State != health.StateOK || got.Pct != 3 {
		t.Errorf("3%% deveria ser ok com pct=3, veio %+v", got)
	}
	// NaN (ex.: divisão 0/0 em algum coletor) é ausência, não saúde.
	if got := healthMetricFrom(ptr(math.NaN()), nil, nil, 70, 90, nil); got.State != health.StateNoData {
		t.Errorf("NaN deveria virar %q, veio %q", health.StateNoData, got.State)
	}
}

// TestHealthMetricDataOValor: o número precisa sair DATADO. Sem ts/step, a tela
// repetia o último valor por toda a janela do servidor (5 min no host-metrics, 2 min
// no Mural) com a cor do limiar e sem dizer de quando era — um pico crítico já
// passado seguia vermelho.
func TestHealthMetricDataOValor(t *testing.T) {
	at := &amostra{TS: 1770000000, Step: 15}
	got := healthMetricFrom(ptr(90.7), nil, nil, 70, 90, at)
	if got.TS == nil || *got.TS != 1770000000 {
		t.Fatalf("o valor tem de sair com o instante da amostra, veio %v", got.TS)
	}
	if got.StepSecs == nil || *got.StepSecs != 15 {
		t.Fatalf("o passo REAL da série tem de acompanhar o valor, veio %v", got.StepSecs)
	}
	// Passo indeterminado (um ponto só) não vira número inventado.
	semPasso := healthMetricFrom(ptr(10.0), nil, nil, 70, 90, &amostra{TS: 1770000000})
	if semPasso.StepSecs != nil {
		t.Errorf("com um ponto só o passo é desconhecido; veio %v", *semPasso.StepSecs)
	}
	// Sem medida não há o que datar.
	if ausente := healthMetricFrom(nil, nil, nil, 70, 90, at); ausente.TS != nil {
		t.Error("métrica ausente não pode sair datada (daria aparência de medida)")
	}
	// NaN cai em nodata: também não pode sair datado.
	if nan := healthMetricFrom(ptr(math.NaN()), nil, nil, 70, 90, at); nan.TS != nil {
		t.Error("NaN é ausência: não pode sair datado")
	}
}

// TestAmostraDe cobre a leitura do metadado de amostragem vindo do ClickHouse,
// incluindo o passo derivado do span/pontos (e não de uma constante do servidor).
func TestAmostraDe(t *testing.T) {
	a := amostraDe(map[string]any{"at": 1770000000.0, "n": 21.0, "span": 300.0})
	if a == nil || a.TS != 1770000000 {
		t.Fatalf("instante da amostra não foi lido: %+v", a)
	}
	if a.Step != 15 {
		t.Errorf("passo = span/(n-1) = 300/20 = 15; veio %v", a.Step)
	}
	if u := amostraDe(map[string]any{"at": 1770000000.0, "n": 1.0, "span": 0.0}); u == nil || u.Step != 0 {
		t.Errorf("um ponto só => passo indeterminado (0), veio %+v", u)
	}
	if z := amostraDe(map[string]any{"at": 0.0}); z != nil {
		t.Error("sem instante válido não há amostra")
	}
}

// TestToFloatRecusaNaoMedida trava a conversão que alimenta o semáforo: null, NaN,
// Inf e lixo não podem virar 0 silencioso.
func TestToFloatRecusaNaoMedida(t *testing.T) {
	if v, ok := toFloat(42.5); !ok || v != 42.5 {
		t.Errorf("número normal deveria passar, veio %v ok=%v", v, ok)
	}
	if v, ok := toFloat("42.5"); !ok || v != 42.5 {
		t.Errorf("número em texto deveria passar, veio %v ok=%v", v, ok)
	}
	recusar := []any{nil, math.NaN(), math.Inf(1), math.Inf(-1), "nan", "inf", "-inf", "abc", true}
	for _, in := range recusar {
		if v, ok := toFloat(in); ok {
			t.Errorf("toFloat(%v) deveria recusar, devolveu %v", in, v)
		}
	}
}

// TestProcsAusenteNaoEhZero: `procs` era float64 com zero-value, ao contrário de
// cpu/mem/swap/disco, que já eram ponteiro justamente para separar ausência de zero.
// O agente OMITE system.processes.count quando a leitura de /proc falha, então um
// servidor vivo — reportando CPU e RAM — aparecia no card como "Processos 0", ou
// seja, um servidor sem nenhum processo, o que é impossível (o agente é um processo).
func TestProcsAusenteNaoEhZero(t *testing.T) {
	semProcs := &hostAgg{cpuTotal: ptr(12.0), has: true}
	if semProcs.procs != nil {
		t.Fatal("o zero-value de procs tem de ser nil (ausência), não 0")
	}
	// A conversão para o campo exposto preserva a ausência.
	var hm HostMetrics
	if semProcs.procs != nil {
		n := int64(*semProcs.procs)
		hm.Procs = &n
	}
	if hm.Procs != nil {
		t.Errorf("sem medida, HostMetrics.Procs tem de ser nil; veio %d", *hm.Procs)
	}

	// Com medida, o número passa inteiro.
	comProcs := &hostAgg{procs: ptr(341.0), has: true}
	n := int64(*comProcs.procs)
	hm.Procs = &n
	if hm.Procs == nil || *hm.Procs != 341 {
		t.Errorf("com medida, Procs tem de ser 341; veio %v", hm.Procs)
	}
}

// A CPU do cartão tem DUAS fontes possíveis durante o rollout, e escolher a errada é
// invisível: os dois números são plausíveis e ninguém desconfia olhando a tela.
//   - 0.8.1+  publica `system.cpu.utilization.total` (consumo + roubo);
//   - anterior publica `system.cpu.utilization` já COM o roubo dentro.
//
// Preferir o `.total` e cair no legado quando ele falta é o que mantém o mural e os
// cartões medindo A MESMA COISA numa frota misturada. Sem a reserva, os hosts que
// ainda não atualizaram apareceriam sem CPU nenhuma.
func TestCPUExigidaPreferOTotalECaiNoLegado(t *testing.T) {
	amostraA := &amostra{TS: 100}
	amostraB := &amostra{TS: 200}

	casos := []struct {
		nome   string
		agg    hostAgg
		quer   *float64
		querAt *amostra
	}{
		{"agente 0.8.1: usa o total", hostAgg{cpuTotal: ptr(16.5), cpuTotalAt: amostraA, cpuLegado: ptr(3.8), cpuLegadoAt: amostraB}, ptr(16.5), amostraA},
		{"agente antigo (sem steal): o legado JÁ é a soma", hostAgg{cpuLegado: ptr(13.9), cpuLegadoAt: amostraB}, ptr(13.9), amostraB},
		// O caso que custou 1h37m de número errado em produção: agente 0.8.1 publicando
		// consumo e roubo, mas com o `.total` ausente naquele balde. Cair no legado puro
		// publicaria 3,86 como "CPU exigida" — subestimação do tamanho exato do roubo.
		// O steal é a testemunha da era: presente, a soma se reconstrói.
		{"agente 0.8.1 sem .total no balde: soma as parcelas", hostAgg{cpuLegado: ptr(3.86), cpuSteal: ptr(12.66), cpuLegadoAt: amostraB}, ptr(16.52), amostraB},
		{"steal presente mas sem legado: continua sem medida", hostAgg{cpuSteal: ptr(9.0)}, nil, nil},
		{"host mudo: continua sem medida (nil, não zero)", hostAgg{}, nil, nil},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			v, at := c.agg.cpuExigida()
			switch {
			case c.quer == nil && v != nil:
				t.Fatalf("esperava ausência de medida, veio %v", *v)
			case c.quer != nil && v == nil:
				t.Fatalf("esperava %v, veio ausência", *c.quer)
			case c.quer != nil && *v != *c.quer:
				t.Errorf("valor = %v, quero %v", *v, *c.quer)
			}
			if at != c.querAt {
				t.Errorf("a datação tem de acompanhar o valor escolhido")
			}
		})
	}
}

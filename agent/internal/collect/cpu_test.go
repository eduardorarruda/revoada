package collect

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// snap monta uma leitura acumulada: `busy` segundos ocupados de `total` segundos
// contabilizados, no instante `at`.
func snap(busy, total float64, at time.Time) cpuSnapshot {
	return cpuSnapshot{Busy: busy, Total: total, At: at.UnixNano()}
}

// snapSteal é o mesmo, com o acumulado de tempo roubado pelo hipervisor.
func snapSteal(busy, total, steal float64, at time.Time) cpuSnapshot {
	s := snap(busy, total, at)
	s.Steal = steal
	return s
}

func TestCPUBetweenMedeIntervaloInteiro(t *testing.T) {
	base := time.Now()
	// 15 s de intervalo em 4 núcleos = 60 s de CPU contabilizada; 18 s ocupados = 30%.
	prev := snap(1000, 5000, base)
	cur := snap(1018, 5060, base.Add(15*time.Second))

	v, ok := cpuBetween(prev, cur)
	if !ok {
		t.Fatal("medida deveria valer")
	}
	if v < 29.9 || v > 30.1 {
		t.Errorf("esperava ~30%%, veio %.2f", v)
	}
}

func TestCPUBetweenRecusaOQueNaoDaParaAfirmar(t *testing.T) {
	base := time.Now()
	casos := []struct {
		nome      string
		prev, cur cpuSnapshot
		porqueNao string
	}{
		{
			nome: "sem leitura anterior",
			prev: cpuSnapshot{}, cur: snap(1018, 5060, base),
			porqueNao: "primeira coleta do processo não tem base",
		},
		{
			nome: "intervalo longo demais",
			prev: snap(1000, 5000, base), cur: snap(2000, 9000, base.Add(maxCPUGap+time.Minute)),
			porqueNao: "a média deixaria de descrever agora",
		},
		{
			nome: "contador andou para trás",
			prev: snap(1000, 5000, base), cur: snap(10, 5060, base.Add(15*time.Second)),
			porqueNao: "a máquina reiniciou e zerou /proc/stat",
		},
		{
			nome: "intervalo curto demais",
			prev: snap(1000, 5000, base), cur: snap(1000.01, 5000.05, base.Add(10*time.Millisecond)),
			porqueNao: "o quociente seria arredondamento de jiffy",
		},
		{
			nome: "relógio andou para trás",
			prev: snap(1000, 5000, base), cur: snap(1018, 5060, base.Add(-time.Second)),
			porqueNao: "intervalo negativo não é intervalo",
		},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			if _, ok := cpuBetween(c.prev, c.cur); ok {
				t.Errorf("deveria recusar: %s", c.porqueNao)
			}
		})
	}
}

func TestCPUBetweenGrampeiaEmCemPorCento(t *testing.T) {
	base := time.Now()
	// Ocupado maior que o total só acontece por arredondamento; não pode virar 103%.
	prev := snap(1000, 5000, base)
	cur := snap(1062, 5060, base.Add(15*time.Second))

	v, ok := cpuBetween(prev, cur)
	if !ok {
		t.Fatal("medida deveria valer")
	}
	if v != 100 {
		t.Errorf("esperava grampeado em 100, veio %.2f", v)
	}
}

// Steal é o tempo que o hipervisor tomou da máquina virtual. Ele está DENTRO do
// `Busy` bruto de /proc/stat, e é ele que separa "meu software pesou" de "meu
// provedor me estrangulou": numa medição de produção, dos 15,4% de CPU, 10,45
// pontos eram steal e só 4,9% era trabalho real. A partir da 0.8.1 é essa
// subtração que vira `system.cpu.utilization` (ver Pura).
func TestStealBetweenSeparaOQueOHipervisorRoubou(t *testing.T) {
	base := time.Now()
	// 15 s em 4 núcleos = 60 s contabilizados. 9,24 s ocupados = 15,4%; desses,
	// 6,27 s roubados = 10,45%.
	prev := snapSteal(1000, 5000, 300, base)
	cur := snapSteal(1009.24, 5060, 306.27, base.Add(15*time.Second))

	v, ok := stealBetween(prev, cur)
	if !ok {
		t.Fatal("medida deveria valer")
	}
	if v < 10.4 || v > 10.5 {
		t.Errorf("esperava ~10,45%%, veio %.2f", v)
	}
	// cpuBetween continua devolvendo o OCUPADO bruto (steal incluído) — é a matéria
	// prima. Quem separa as duas séries publicadas é Pura, um nível acima.
	u, ok := cpuBetween(prev, cur)
	if !ok || u < 15.3 || u > 15.5 {
		t.Errorf("o ocupado bruto deveria ser ~15,4%% (steal incluído): %.2f ok=%v", u, ok)
	}
}

// Em máquina física o steal é sempre 0 — e emitir 0 é informação boa: prova que a
// CPU medida é a CPU que o servidor realmente teve.
func TestStealZeroEmMaquinaFisicaEEmitido(t *testing.T) {
	base := time.Now()
	v, ok := stealBetween(snapSteal(1000, 5000, 0, base), snapSteal(1018, 5060, 0, base.Add(15*time.Second)))
	if !ok {
		t.Fatal("steal 0 é mensurável e deve ser emitido, não omitido")
	}
	if v != 0 {
		t.Errorf("esperava 0, veio %v", v)
	}
}

func TestStealBetweenRecusaOQueNaoDaParaAfirmar(t *testing.T) {
	base := time.Now()
	casos := []struct {
		nome      string
		prev, cur cpuSnapshot
		porqueNao string
	}{
		{
			nome: "sem leitura anterior",
			prev: cpuSnapshot{}, cur: snapSteal(1018, 5060, 300, base),
			porqueNao: "primeira coleta do processo não tem base",
		},
		{
			nome: "estado gravado antes do campo Steal existir",
			prev: snap(1000, 5000, base), cur: snapSteal(1018, 5060, 900, base.Add(15*time.Second)),
			porqueNao: "prev.Steal=0 faria a diferença virar o acumulado desde o boot",
		},
		{
			nome: "contador andou para trás",
			prev: snapSteal(1000, 5000, 300, base), cur: snapSteal(1018, 5060, 10, base.Add(15*time.Second)),
			porqueNao: "a máquina reiniciou e zerou /proc/stat",
		},
		{
			nome: "intervalo longo demais",
			prev: snapSteal(1000, 5000, 300, base), cur: snapSteal(2000, 9000, 500, base.Add(maxCPUGap+time.Minute)),
			porqueNao: "a média deixaria de descrever agora",
		},
		{
			nome: "intervalo curto demais",
			prev: snapSteal(1000, 5000, 300, base), cur: snapSteal(1000.01, 5000.05, 300.01, base.Add(10*time.Millisecond)),
			porqueNao: "o quociente seria arredondamento de jiffy",
		},
		{
			nome: "relógio andou para trás",
			prev: snapSteal(1000, 5000, 300, base), cur: snapSteal(1018, 5060, 306, base.Add(-time.Second)),
			porqueNao: "intervalo negativo não é intervalo",
		},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			if _, ok := stealBetween(c.prev, c.cur); ok {
				t.Errorf("deveria recusar: %s", c.porqueNao)
			}
		})
	}
}

// Utilização e steal têm de sair da MESMA diferença — senão descreveriam intervalos
// diferentes e a subtração "uso real = utilization - steal" deixaria de fechar.
func TestReadAllMedeOMesmoIntervaloParaAsDuasSeries(t *testing.T) {
	m := &CPUMeter{}
	ctx := context.Background()
	if _, ok := m.ReadAll(ctx); !ok {
		t.Skip("sem leitura de CPU neste ambiente")
	}
	time.Sleep(300 * time.Millisecond)

	r, ok := m.ReadAll(ctx)
	if !ok {
		t.Fatal("a segunda leitura deveria valer")
	}
	if r.Busy < 0 || r.Busy > 100 {
		t.Errorf("ocupado fora da faixa: %v", r.Busy)
	}
	if !r.StealOK {
		t.Skip("sem base de steal neste ambiente")
	}
	if r.Steal < 0 || r.Steal > 100 {
		t.Errorf("steal fora da faixa: %v", r.Steal)
	}
	if r.Steal > r.Busy+0.001 {
		t.Errorf("steal (%v) não pode passar do ocupado (%v): ele está contido nele", r.Steal, r.Busy)
	}
	// A soma tem de fechar: é essa identidade que permite ao operador reconstruir a
	// leitura antiga (pura + steal) a partir das duas séries publicadas.
	pura, ok := r.Pura()
	if !ok {
		t.Fatal("com StealOK a pura tem de sair")
	}
	if diff := math.Abs((pura + r.Steal) - r.Busy); diff > 0.001 {
		t.Errorf("pura(%v) + steal(%v) = %v, mas o ocupado é %v", pura, r.Steal, pura+r.Steal, r.Busy)
	}
}

// A 0.8.1 tornou `system.cpu.utilization` disjunta do steal. Estes casos travam as
// duas garantias que a mudança precisa manter: a subtração acontece de fato, e
// sem base de steal NADA é afirmado (em vez de publicar o ocupado com o rótulo novo).
func TestPuraSubtraiOStealENaoInventaSemBase(t *testing.T) {
	casos := []struct {
		nome   string
		r      CPUReading
		quer   float64
		querOK bool
	}{
		{"VPS estrangulado: 13,9 de ocupado com 9,9 de steal", CPUReading{Busy: 13.881, Steal: 9.888, StealOK: true}, 3.993, true},
		{"máquina física: steal zero, pura = ocupado", CPUReading{Busy: 18.4, Steal: 0, StealOK: true}, 18.4, true},
		{"sem base de steal: não afirma nada", CPUReading{Busy: 13.881, Steal: 0, StealOK: false}, 0, false},
		{"arredondamento não pode virar negativo", CPUReading{Busy: 4.0, Steal: 4.0000001, StealOK: true}, 0, true},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got, ok := c.r.Pura()
			if ok != c.querOK {
				t.Fatalf("ok = %v, quero %v", ok, c.querOK)
			}
			if ok && math.Abs(got-c.quer) > 0.0005 {
				t.Errorf("pura = %v, quero %v", got, c.quer)
			}
		})
	}
}

// O modo `once` (cron) é um processo novo a cada coleta: sem estado em disco,
// toda leitura cairia na janela de bloqueio — o defeito que este medidor corrige.
func TestEstadoEmDiscoSobreviveAoProcesso(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "sub", "cpu.state")
	base := time.Now()
	gravada := snap(1000, 5000, base)

	if err := saveCPUState(caminho, gravada); err != nil {
		t.Fatalf("gravar estado: %v", err)
	}

	lida := loadCPUState(caminho)
	if lida != gravada {
		t.Fatalf("estado voltou diferente: %+v != %+v", lida, gravada)
	}
	// E ele serve de base para a coleta seguinte, sem bloquear.
	if v, ok := cpuBetween(lida, snap(1018, 5060, base.Add(15*time.Second))); !ok || v < 29.9 || v > 30.1 {
		t.Errorf("estado em disco deveria servir de base: v=%.2f ok=%v", v, ok)
	}
}

func TestEstadoCorrompidoNaoDerrubaAColeta(t *testing.T) {
	dir := t.TempDir()
	quebrado := filepath.Join(dir, "cpu.state")
	if err := os.WriteFile(quebrado, []byte("{isto não é json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s := loadCPUState(quebrado); s.valid() {
		t.Error("estado corrompido deveria ser descartado, não aceito")
	}
	if s := loadCPUState(filepath.Join(dir, "nao-existe")); s.valid() {
		t.Error("estado ausente deveria ser descartado")
	}
	// Caminho impossível: o erro tem de VOLTAR, não ser engolido — é ele que
	// dispara o aviso ao operador.
	if err := saveCPUState("/proc/1/nao-da-para-gravar/cpu.state", snap(1, 2, time.Now())); err == nil {
		t.Error("gravação impossível deveria devolver erro, não silêncio")
	}
}

func TestSaveCPUStateNaoDeixaArquivoPelaMetade(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "cpu.state")
	for _, s := range []cpuSnapshot{snap(1000, 5000, time.Now()), snap(2000, 9000, time.Now())} {
		if err := saveCPUState(caminho, s); err != nil {
			t.Fatalf("gravar estado: %v", err)
		}
	}

	b, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatal(err)
	}
	var s cpuSnapshot
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("arquivo final deve ser JSON íntegro: %v", err)
	}
	if s.Busy != 2000 {
		t.Errorf("esperava a última gravação (2000), veio %v", s.Busy)
	}
	if _, err := os.Stat(caminho + ".tmp"); !os.IsNotExist(err) {
		t.Error("o temporário deveria ter sido renomeado, não deixado para trás")
	}
}

// A primeira leitura de um processo não tem base e cai na janela de bloqueio; a
// SEGUNDA já tem de medir o intervalo inteiro, sem bloquear.
func TestReadPrimeiraBloqueiaSegundaNao(t *testing.T) {
	m := &CPUMeter{}
	ctx := context.Background()

	inicio := time.Now()
	if _, ok := m.Read(ctx); !ok {
		t.Skip("sem leitura de CPU neste ambiente")
	}
	primeira := time.Since(inicio)
	if primeira < cpuFallbackWindow {
		t.Errorf("a primeira leitura deveria usar a janela de %v, levou %v", cpuFallbackWindow, primeira)
	}

	// Espera o bastante para o delta passar de minCPUDelta mesmo numa máquina de
	// 1 núcleo (o ciclo real é de 15 s; aqui só precisa cruzar o piso).
	time.Sleep(300 * time.Millisecond)
	inicio = time.Now()
	if _, ok := m.Read(ctx); !ok {
		t.Fatal("a segunda leitura deveria valer")
	}
	if segunda := time.Since(inicio); segunda >= cpuFallbackWindow {
		t.Errorf("a segunda leitura não deveria bloquear, levou %v", segunda)
	}
}

func TestReadDevolvePorcentagemPlausivel(t *testing.T) {
	m := &CPUMeter{}
	ctx := context.Background()
	if _, ok := m.Read(ctx); !ok {
		t.Skip("sem leitura de CPU neste ambiente")
	}
	time.Sleep(300 * time.Millisecond)
	v, ok := m.Read(ctx)
	if !ok {
		t.Fatal("leitura deveria valer")
	}
	if v < 0 || v > 100 {
		t.Errorf("porcentagem fora da faixa: %v", v)
	}
}

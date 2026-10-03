package collect

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// csnap monta uma leitura acumulada de CPU de container: `total` ns consumidos pelo
// container contra `system` ns de CPU do host inteiro, com `online` núcleos.
func csnap(total, system, online float64, at time.Time) containerSnapshot {
	return containerSnapshot{Total: total, System: system, Online: online, At: at.UnixNano()}
}

// O defeito mais grave da auditoria: o agente reportou 160,73% para o container do
// ClickHouse enquanto o `docker stats` mostrava 27,77% — 5,8x. Duas causas somadas:
// a janela de ~1s do `stream=false` (medida a cada 15s) e a escala do Docker, em que
// 100% = um núcleo cheio. Agora a diferença é sobre o intervalo REAL e a escala é a
// mesma de system.cpu.utilization: 0–100 da máquina inteira.
func TestContainerCPUBetweenMedeIntervaloRealNaEscalaDoHost(t *testing.T) {
	base := time.Now()
	const seg = 1e9
	// 15 s de intervalo em 8 núcleos = 120 s de CPU do host contabilizados.
	// O container consumiu 12 s de CPU = 10% da máquina inteira (80% de um núcleo).
	prev := csnap(100*seg, 10_000*seg, 8, base)
	cur := csnap(112*seg, 10_120*seg, 8, base.Add(15*time.Second))

	v, ok := containerCPUBetween(prev, cur)
	if !ok {
		t.Fatal("medida deveria valer")
	}
	if v < 9.9 || v > 10.1 {
		t.Errorf("esperava ~10%% da máquina inteira, veio %.2f", v)
	}
	// Na convenção antiga (docker stats) o mesmo consumo daria 80% — é essa
	// contradição de escala que fazia a soma dos containers (170,9%) não bater com
	// o host (11,59%) dentro do mesmo ciclo.
	if antiga := dockerCPUPercent(12*seg, 120*seg, 8); antiga < 79.9 || antiga > 80.1 {
		t.Errorf("a convenção do Docker deveria continuar valendo 80%%, veio %.2f", antiga)
	}
}

func TestContainerCPUBetweenRecusaOQueNaoDaParaAfirmar(t *testing.T) {
	base := time.Now()
	const seg = 1e9
	prev := csnap(100*seg, 10_000*seg, 8, base)

	casos := []struct {
		nome      string
		prev, cur containerSnapshot
		porqueNao string
	}{
		{
			nome: "sem leitura anterior",
			prev: containerSnapshot{}, cur: csnap(112*seg, 10_120*seg, 8, base),
			porqueNao: "container novo ou primeira coleta do processo não tem base",
		},
		{
			nome: "leitura atual sem system_cpu_usage",
			prev: prev, cur: csnap(112*seg, 0, 8, base.Add(15*time.Second)),
			porqueNao: "sem o contador do host não há denominador",
		},
		{
			nome: "container recriado zerou o contador",
			prev: prev, cur: csnap(2*seg, 10_120*seg, 8, base.Add(15*time.Second)),
			porqueNao: "total_usage andou para trás",
		},
		{
			nome: "intervalo longo demais",
			prev: prev, cur: csnap(400*seg, 20_000*seg, 8, base.Add(maxContainerCPUGap+time.Minute)),
			porqueNao: "a média deixaria de descrever agora",
		},
		{
			nome: "intervalo curto demais",
			prev: prev, cur: csnap(100.001*seg, 10_000.01*seg, 8, base.Add(10*time.Millisecond)),
			porqueNao: "o quociente seria ruído de arredondamento",
		},
		{
			nome: "relógio andou para trás",
			prev: prev, cur: csnap(112*seg, 10_120*seg, 8, base.Add(-time.Second)),
			porqueNao: "intervalo negativo não é intervalo",
		},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			if _, ok := containerCPUBetween(c.prev, c.cur); ok {
				t.Errorf("deveria recusar: %s", c.porqueNao)
			}
		})
	}
}

// A escala tem de ser a MESMA de system.cpu.utilization: 0–100 da máquina inteira.
func TestContainerCPUHostPercent(t *testing.T) {
	casos := []struct {
		nome                          string
		cpuDelta, sysDelta, onlineCPU float64
		quero                         float64
	}{
		{"um núcleo cheio em 4 = 25% da máquina", 25, 100, 4, 25},
		{"máquina inteira saturada = 100%", 100, 100, 4, 100},
		{"metade de 1 núcleo em 1 = 50%", 50, 100, 1, 50},
		{"online zero cai para 1", 25, 100, 0, 25},
		{"system delta zero => 0", 50, 0, 2, 0},
		{"cpu delta negativo => 0", -5, 100, 2, 0},
		{"sem uso => 0", 0, 100, 2, 0},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got := containerCPUHostPercent(c.cpuDelta, c.sysDelta, c.onlineCPU)
			if got != c.quero {
				t.Fatalf("containerCPUHostPercent(%v,%v,%v) = %v, quero %v",
					c.cpuDelta, c.sysDelta, c.onlineCPU, got, c.quero)
			}
		})
	}
}

// O mapa de estado não pode crescer para sempre: num host que recria containers a
// cada deploy, uma entrada morta por deploy vira vazamento num processo de meses.
func TestPodarCPUStateDescartaContainersQueSumiram(t *testing.T) {
	agora := time.Now()
	trocarCPUState("vivo", csnap(1, 2, 1, agora))
	trocarCPUState("morto", csnap(1, 2, 1, agora))

	podarCPUState(map[string]bool{"vivo": true})

	containerCPUState.mu.Lock()
	defer containerCPUState.mu.Unlock()
	if _, ok := containerCPUState.prev["vivo"]; !ok {
		t.Error("o container que o daemon ainda conhece não podia perder a base de medição")
	}
	if _, ok := containerCPUState.prev["morto"]; ok {
		t.Error("o container que sumiu deveria ter saído do mapa")
	}
}

// trocarCPUState guarda a nova leitura e devolve a anterior — é o que torna a
// medida "intervalo real" possível entre dois ciclos de coleta.
func TestTrocarCPUStateDevolveALeituraAnterior(t *testing.T) {
	id := "troca-" + t.Name()
	defer podarCPUState(map[string]bool{})

	base := time.Now()
	const seg = 1e9
	primeira := csnap(10*seg, 1000*seg, 2, base)
	if prev := trocarCPUState(id, primeira); prev.valid() {
		t.Errorf("a primeira coleta não pode ter base, veio %+v", prev)
	}
	segunda := csnap(13*seg, 1030*seg, 2, base.Add(15*time.Second))
	prev := trocarCPUState(id, segunda)
	if prev != primeira {
		t.Fatalf("esperava a leitura anterior %+v, veio %+v", primeira, prev)
	}
	if _, ok := containerCPUBetween(prev, segunda); !ok {
		t.Error("com base guardada a medida do intervalo deveria valer")
	}
}

func TestDockerCPUPercent(t *testing.T) {
	cases := []struct {
		name                          string
		cpuDelta, sysDelta, onlineCPU float64
		want                          float64
	}{
		{"metade de 1 cpu", 50, 100, 1, 50},
		{"cheio em 4 cpus", 100, 100, 4, 400},
		{"online zero cai para 1", 25, 100, 0, 25},
		{"system delta zero => 0", 50, 0, 2, 0},
		{"cpu delta negativo => 0", -5, 100, 2, 0},
		{"sem uso => 0", 0, 100, 2, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := dockerCPUPercent(c.cpuDelta, c.sysDelta, c.onlineCPU)
			if got != c.want {
				t.Fatalf("dockerCPUPercent(%v,%v,%v) = %v, quero %v", c.cpuDelta, c.sysDelta, c.onlineCPU, got, c.want)
			}
		})
	}
}

// TestCompletedOneShot cobre a supressão de jobs one-shot concluídos (ex.: o
// container de migração), que não devem virar série nem disparar "Container caído".
func TestCompletedOneShot(t *testing.T) {
	cases := []struct {
		name          string
		inspected     bool
		running       bool
		state         string
		exitCode      string
		restartPolicy string
		want          bool
	}{
		{"migrate concluído (exit0, no)", true, false, "exited", "0", "no", true},
		{"política vazia = no", true, false, "exited", "0", "", true},
		{"one-shot NÃO gerenciado que falhou (exit1, no) é suprimido", true, false, "exited", "1", "no", true},
		{"descartável parado/morto (exit137, no) é suprimido", true, false, "exited", "137", "no", true},
		{"serviço parado (exit0, unless-stopped) alerta", true, false, "exited", "0", "unless-stopped", false},
		{"serviço gerenciado que falhou (exit1, always) alerta", true, false, "exited", "1", "always", false},
		{"serviço always exit0 alerta", true, false, "exited", "0", "always", false},
		{"on-failure exit0 não é one-shot puro", true, false, "exited", "0", "on-failure", false},
		{"em execução nunca suprime", true, true, "running", "0", "no", false},
		{"dead não é conclusão limpa", true, false, "dead", "0", "no", false},
		// `created` era tratado como "não é exited, logo alerta". Mas container criado e
		// nunca iniciado, sem política de reinício, não caiu: ele nunca subiu. Medido no
		// dev: 12 containers `scopeflow-*` nesse estado, 5.625 amostras cada, cada um
		// abrindo um CRÍTICO na regra de fábrica "Container caído" — enquanto o card do
		// mesmo container dizia "criado · neutro". O painel se contradizia sozinho.
		{"criado e nunca iniciado (policy no) é suprimido", true, false, "created", "0", "no", true},
		{"criado com política de reinício alerta", true, false, "created", "0", "always", false},
		{"pausado sem política de reinício é suprimido", true, false, "paused", "0", "no", true},
		{"sem inspect confiável não suprime", false, false, "exited", "0", "no", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := completedOneShot(c.inspected, c.running, c.state, c.exitCode, c.restartPolicy)
			if got != c.want {
				t.Fatalf("completedOneShot(%v,%v,%q,%q,%q) = %v, quero %v",
					c.inspected, c.running, c.state, c.exitCode, c.restartPolicy, got, c.want)
			}
		})
	}
}

// TestContainersNoDockerSocket garante que a coleta é best-effort: sem socket (ou
// erro) devolve nil, nunca panica nem bloqueia a coleta de host.
func TestContainersNoDockerSocket(t *testing.T) {
	// Em ambientes de CI sem Docker, Containers deve simplesmente devolver nil.
	// Se houver um socket real, o teste apenas confirma que não panica.
	_ = Containers(t.Context(), 0)
}

// O host `mail.exemplo.com.br` roda por CRON e tem containers: cada coleta é um
// processo novo. Sem lembrar a leitura anterior de cada container, a utilização
// nunca teria intervalo para ser medida e `container.cpu.utilization` sumiria
// daquele host — e um alerta aberto sobre ela seria auto-resolvido como "sem dados".
func TestEstadoDeContainerSobreviveAoProcesso(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "sub", "containers.state")
	base := time.Now()

	// "Processo 1": grava a leitura do ciclo.
	reiniciarEstadoDeContainer(caminho)
	trocarCPUState("abc123", containerSnapshot{Total: 1e9, System: 40e9, Online: 4, At: base.UnixNano()})
	podarCPUState(map[string]bool{"abc123": true})

	// "Processo 2": nasce vazio e tem de encontrar a leitura anterior no disco.
	reiniciarEstadoDeContainer(caminho)
	prev := trocarCPUState("abc123", containerSnapshot{Total: 1.6e9, System: 100e9, Online: 4, At: base.Add(15 * time.Second).UnixNano()})
	if !prev.valid() {
		t.Fatal("a segunda execução não encontrou a leitura anterior — no modo cron a CPU de container deixaria de existir")
	}
	if prev.Total != 1e9 || prev.Online != 4 {
		t.Errorf("estado voltou diferente do gravado: %+v", prev)
	}
}

// Container que o daemon não conhece mais não pode ficar no arquivo para sempre.
func TestEstadoDeContainerNaoAcumulaMortos(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "containers.state")
	agora := time.Now().UnixNano()

	reiniciarEstadoDeContainer(caminho)
	trocarCPUState("vivo", containerSnapshot{Total: 1, System: 2, Online: 1, At: agora})
	trocarCPUState("morto", containerSnapshot{Total: 1, System: 2, Online: 1, At: agora})
	podarCPUState(map[string]bool{"vivo": true})

	reiniciarEstadoDeContainer(caminho)
	if prev := trocarCPUState("morto", containerSnapshot{Total: 9, System: 9, Online: 1, At: agora}); prev.valid() {
		t.Error("container removido continuou no arquivo de estado")
	}
	reiniciarEstadoDeContainer(caminho)
	if prev := trocarCPUState("vivo", containerSnapshot{Total: 9, System: 9, Online: 1, At: agora}); !prev.valid() {
		t.Error("container vivo perdeu o estado na poda")
	}
}

// reiniciarEstadoDeContainer simula um processo novo: mapa vazio, arquivo mantido.
func reiniciarEstadoDeContainer(caminho string) {
	containerCPUState.mu.Lock()
	containerCPUState.prev = map[string]containerSnapshot{}
	containerCPUState.carregado = false
	containerCPUState.stateFile = caminho
	containerCPUState.mu.Unlock()
}

// PROVA: um ciclo em que NENHUM stats deu certo não pode apagar as bases do ciclo
// anterior.
//
// O caminho do defeito: carregarSeVazio() só era chamado dentro de trocarCPUState, e
// trocarCPUState só roda quando um stats devolve snapshot válido. Se o daemon
// estivesse ocupado, o orçamento de 10s estourasse, ou todos os containers estivessem
// parados, `prev` continuava vazio — e gravarEstado() escrevia `{}` por cima do
// containers.state. No modo cron (mail.exemplo.com.br, 1 execução por minuto) isso é um
// buraco de 1 minuto em container.cpu.utilization de TODOS os containers, e silencioso:
// a métrica simplesmente falta, em vez de errar.
func TestCicloSemStatsNaoApagaOEstadoAnterior(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "containers.state")
	agora := time.Now().UnixNano()

	// Ciclo 1: leituras normais, gravadas em disco.
	reiniciarEstadoDeContainer(caminho)
	trocarCPUState("abc", containerSnapshot{Total: 1e9, System: 40e9, Online: 4, At: agora})
	podarCPUState(map[string]bool{"abc": true})

	// Ciclo 2 (processo novo do cron): o daemon respondeu a listagem mas NENHUM
	// containerStats deu certo — nenhum trocarCPUState, logo `prev` vazio.
	reiniciarEstadoDeContainer(caminho)
	podarCPUState(map[string]bool{"abc": true})

	// Ciclo 3: a base do ciclo 1 tem de continuar lá.
	reiniciarEstadoDeContainer(caminho)
	if prev := trocarCPUState("abc", containerSnapshot{Total: 2e9, System: 100e9, Online: 4, At: agora}); !prev.valid() {
		t.Fatal("um ciclo sem stats apagou o containers.state — a CPU de todos os containers sumiria do painel por um ciclo inteiro")
	}
}

// PROVA: o containers.state não é legível por qualquer usuário do host do cliente.
// O conteúdo não é "só números": as chaves do mapa são os IDs dos containers, ou seja,
// um inventário do que roda ali.
func TestEstadoDeContainerNaoELegivelPorTodos(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("o Windows não tem permissão Unix: a proteção ali é a ACL da pasta do serviço")
	}
	caminho := filepath.Join(t.TempDir(), "containers.state")
	reiniciarEstadoDeContainer(caminho)
	trocarCPUState("abc", containerSnapshot{Total: 1, System: 2, Online: 1, At: time.Now().UnixNano()})
	podarCPUState(map[string]bool{"abc": true})

	fi, err := os.Stat(caminho)
	if err != nil {
		t.Fatalf("estado não foi gravado: %v", err)
	}
	if modo := fi.Mode().Perm(); modo != 0o600 {
		t.Fatalf("containers.state gravado como %04o; esperado 0600", modo)
	}
}

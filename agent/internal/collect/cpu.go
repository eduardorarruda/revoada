package collect

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
)

// Utilização de CPU é a fração do tempo em que os núcleos estiveram ocupados
// ENTRE duas leituras de /proc/stat — ou seja, ela não existe sem um intervalo.
// Quanto mais curto o intervalo, menos ela mede carga e mais mede o acaso: em
// meio segundo, um servidor com 30% de ocupação real devolve qualquer coisa
// entre 0% e 100%, porque meio segundo é menor que a rajada típica de um
// PHP-FPM, de um cron ou de um compactador. Amostrar 500 ms a cada 15 s é olhar
// 3% do tempo e chamar o resultado de média.
//
// Foi o que o agente fez até aqui, e o efeito era visível: o painel mostrava
// picos de 100% que o hipervisor não via, e a regra de alerta lia esses picos
// como saturação.
//
// Este medidor guarda a leitura anterior e mede o intervalo INTEIRO entre
// coletas (~15 s), que é o que o `top`, o `mpstat` e o hipervisor reportam.
// A janela curta de bloqueio continua existindo, mas só como exceção: quando
// não há leitura anterior utilizável. Uma amostra ruim vale mais que nenhuma,
// e ela acontece no máximo uma vez por processo.
type CPUMeter struct {
	mu        sync.Mutex
	prev      cpuSnapshot
	stateFile string
	aviso     func(error) // chamado UMA vez, na primeira falha de gravação
	avisou    bool
}

// cpuSnapshot é uma leitura acumulada de /proc/stat. Busy, Total e Steal contam
// desde o boot da máquina; o que interessa é sempre a diferença entre duas leituras.
type cpuSnapshot struct {
	Busy  float64 `json:"busy"`  // segundos de CPU ocupada desde o boot (somando núcleos)
	Total float64 `json:"total"` // segundos de CPU contabilizados desde o boot
	Steal float64 `json:"steal"` // segundos roubados pelo hipervisor desde o boot
	At    int64   `json:"at"`    // unix nano da leitura
}

func (s cpuSnapshot) valid() bool { return s.At != 0 && s.Total > 0 }

const (
	// cpuFallbackWindow é a janela de bloqueio usada só quando não há leitura
	// anterior. Curta para não atrasar o ciclo, e boa o bastante para uma
	// amostra única.
	cpuFallbackWindow = 500 * time.Millisecond

	// maxCPUGap é o maior intervalo que ainda descreve "agora". Acima disso a
	// média passaria a descrever a última hora, e cresce a chance de o contador
	// ter zerado no meio (reinício da máquina).
	maxCPUGap = 10 * time.Minute

	// minCPUDelta é o mínimo de tempo de CPU acumulado no intervalo, em segundos
	// somando os núcleos, para a divisão ter resolução. Abaixo disso o quociente
	// é arredondamento de jiffy, não medida.
	minCPUDelta = 0.2
)

// defaultCPUMeter é o medidor usado por Host. É estado de processo de propósito:
// a medida depende da coleta anterior, então precisa sobreviver entre ciclos.
var defaultCPUMeter = &CPUMeter{}

// UseCPUStateFile faz o medidor lembrar a última leitura entre execuções do
// binário. Serve ao modo `once` (cron), em que cada coleta é um processo novo —
// sem isso, TODA leitura cairia na janela curta de bloqueio, que é exatamente o
// problema que este arquivo existe para resolver. No loop contínuo não é
// preciso: a leitura anterior já está em memória entre os ciclos.
//
// `aviso` é chamado uma única vez por processo, na primeira falha de gravação.
// Não gravar não interrompe a coleta, mas no modo cron rebaixa a medida de volta
// à janela de 500 ms — e isso o operador precisa saber, senão o defeito volta
// sem deixar rastro.
func UseCPUStateFile(path string, aviso func(error)) {
	defaultCPUMeter.mu.Lock()
	defer defaultCPUMeter.mu.Unlock()
	defaultCPUMeter.stateFile = path
	defaultCPUMeter.aviso = aviso
}

// CPUStateInfo diz há quanto tempo o estado de CPU foi gravado. Serve ao
// `doctor`: estado ausente, velho ou ilegível no modo cron significa que a CPU
// voltou a ser medida por janela curta.
func CPUStateInfo(path string) (idade time.Duration, ok bool) {
	s := loadCPUState(path)
	if !s.valid() {
		return 0, false
	}
	return time.Since(time.Unix(0, s.At)), true
}

// CPUReading é o que uma medição de intervalo produz. Busy e Steal saem da MESMA
// diferença entre duas leituras, então descrevem exatamente o mesmo intervalo — é
// isso que torna a subtração de Pura uma conta legítima, e não a comparação de dois
// instantes diferentes.
//
// StealOK existe porque `steal` nem sempre tem base: o campo entrou no estado em
// disco depois que ele já existia em produção, então um arquivo gravado pela versão
// anterior traz Steal=0 e a diferença contra o acumulado desde o boot daria um
// número absurdo. Nesse ciclo (um só, o da atualização) preferimos não emitir.
type CPUReading struct {
	Busy    float64 // 0–100 do intervalo, tudo que não foi ocioso — steal INCLUÍDO
	Steal   float64 // 0–100 do intervalo em que o hipervisor tomou a CPU
	StealOK bool    // false = sem base confiável para o steal; não emita
}

// Pura é a CPU que a máquina realmente CONSUMIU: o ocupado menos o que o
// hipervisor levou. É ela que vai para `system.cpu.utilization` desde a 0.8.1, e
// é a convenção do painel do provedor (medida do lado do hipervisor) e do que
// `top` mostra como us+sy+ni.
//
// A soma continua disponível sem perder nada: `Pura + Steal` é o antigo Busy —
// "quanto tempo meus processos passaram sem poder rodar". As duas leituras saem
// do MESMO par de snapshots, então somar uma na outra fecha exatamente.
//
// O segundo retorno é falso quando não há base confiável para o steal. Nesse
// ciclo não dá para afirmar nem a pura nem a soma, e o certo é não emitir: um
// Busy publicado como se fosse pura seria ~10 pontos alto num VPS estrangulado,
// e ninguém teria como saber quais pontos da série estão inflados.
func (r CPUReading) Pura() (float64, bool) {
	if !r.StealOK {
		return 0, false
	}
	return math.Max(0, r.Busy-r.Steal), true
}

// Read devolve a CPU pura do intervalo desde a leitura anterior, em porcentagem
// (0–100). O segundo retorno é falso quando não deu para medir — e aí é melhor
// não emitir a métrica do que emitir um número inventado.
func (m *CPUMeter) Read(ctx context.Context) (float64, bool) {
	r, ok := m.ReadAll(ctx)
	if !ok {
		return 0, false
	}
	return r.Pura()
}

// ReadAll é o Read completo: mede o intervalo uma única vez e devolve utilização e
// steal juntos. Existe porque as duas séries têm de sair da MESMA diferença — medir
// steal numa segunda leitura de /proc/stat descreveria outro intervalo, e a conta
// "quanto do meu uso é roubo do hipervisor" deixaria de fechar.
func (m *CPUMeter) ReadAll(ctx context.Context) (CPUReading, bool) {
	cur, ok := readCPUSnapshot(ctx)
	if !ok {
		return CPUReading{}, false
	}

	m.mu.Lock()
	prev := m.prev
	if !prev.valid() && m.stateFile != "" {
		prev = loadCPUState(m.stateFile)
	}
	m.prev = cur
	m.mu.Unlock()

	if v, ok := cpuBetween(prev, cur); ok {
		m.persist(cur)
		return leitura(v, prev, cur), true
	}
	return m.blockingSample(ctx, cur)
}

// leitura junta ocupação e steal do mesmo par de snapshots.
func leitura(busy float64, prev, cur cpuSnapshot) CPUReading {
	r := CPUReading{Busy: busy}
	r.Steal, r.StealOK = stealBetween(prev, cur)
	return r
}

// blockingSample mede uma janela curta bloqueando o ciclo. Caminho de exceção:
// primeira coleta do processo, contador reiniciado ou estado velho demais.
// Reaproveita como início da janela a leitura que Read acabou de fazer, e guarda
// só a final — que é a base da coleta seguinte.
func (m *CPUMeter) blockingSample(ctx context.Context, before cpuSnapshot) (CPUReading, bool) {
	select {
	case <-ctx.Done():
		return CPUReading{}, false
	case <-time.After(cpuFallbackWindow):
	}
	after, ok := readCPUSnapshot(ctx)
	if !ok {
		return CPUReading{}, false
	}

	m.mu.Lock()
	m.prev = after
	m.mu.Unlock()
	m.persist(after)

	v, ok := cpuBetween(before, after)
	if !ok {
		return CPUReading{}, false
	}
	return leitura(v, before, after), true
}

// persist grava o estado, quando há arquivo configurado, e avisa UMA vez se não
// conseguir. Silêncio aqui seria o pior desfecho: no modo cron, estado que não
// grava reintroduz o defeito de medição sem deixar nenhum rastro.
func (m *CPUMeter) persist(s cpuSnapshot) {
	m.mu.Lock()
	path, aviso := m.stateFile, m.aviso
	m.mu.Unlock()
	if path == "" {
		return
	}
	err := saveCPUState(path, s)
	if err == nil {
		return
	}
	m.mu.Lock()
	primeira := !m.avisou
	m.avisou = true
	m.mu.Unlock()
	if primeira && aviso != nil {
		aviso(err)
	}
}

// cpuBetween calcula a ocupação entre duas leituras acumuladas. Recusa o que não
// dá para afirmar: intervalo negativo, longo demais, curto demais, ou contador
// que andou para trás (o que só acontece quando a máquina reiniciou).
func cpuBetween(prev, cur cpuSnapshot) (float64, bool) {
	if !prev.valid() || !cur.valid() {
		return 0, false
	}
	gap := time.Duration(cur.At - prev.At)
	if gap <= 0 || gap > maxCPUGap {
		return 0, false
	}
	total := cur.Total - prev.Total
	busy := cur.Busy - prev.Busy
	if total < minCPUDelta || busy < 0 {
		return 0, false
	}
	return math.Max(0, math.Min(100, 100*busy/total)), true
}

// stealBetween devolve a porcentagem do intervalo em que a CPU foi TOMADA pelo
// hipervisor — tempo em que a máquina virtual estava pronta para rodar e não
// recebeu núcleo, porque outro inquilino do mesmo host físico estava usando.
//
// Ela existe porque `system.cpu.utilization` funde as duas coisas: seguindo a
// convenção do node_exporter, `busy = total - idle - iowait` inclui o steal. Numa
// medição de produção, dos 15,4% de CPU do servidor 10,45 pontos eram steal e só
// 4,9% era trabalho do software instalado. Sem esta série o operador não consegue
// distinguir "meu sistema ficou pesado" de "meu provedor está me estrangulando" —
// e as duas conclusões levam a ações opostas (otimizar código vs. trocar de plano).
//
// Em máquina física o steal é sempre 0, e emitir 0 é informação boa: prova que a
// CPU medida é a CPU que o servidor realmente teve.
//
// A recusa quando `steal > total` não é paranoia: o campo Steal entrou no estado em
// disco depois que ele já rodava em produção, então o primeiro ciclo após a
// atualização lê um estado antigo com Steal=0 e a diferença viraria o acumulado
// desde o boot. Melhor pular esse ciclo do que publicar um número inventado.
func stealBetween(prev, cur cpuSnapshot) (float64, bool) {
	if !prev.valid() || !cur.valid() {
		return 0, false
	}
	gap := time.Duration(cur.At - prev.At)
	if gap <= 0 || gap > maxCPUGap {
		return 0, false
	}
	total := cur.Total - prev.Total
	steal := cur.Steal - prev.Steal
	if total < minCPUDelta || steal < 0 || steal > total {
		return 0, false
	}
	return math.Max(0, math.Min(100, 100*steal/total)), true
}

// readCPUSnapshot lê /proc/stat (ou o equivalente do SO) somando todos os núcleos.
func readCPUSnapshot(ctx context.Context) (cpuSnapshot, bool) {
	t, err := cpu.TimesWithContext(ctx, false)
	if err != nil || len(t) == 0 {
		return cpuSnapshot{}, false
	}
	busy, total := busyTotal(t[0])
	if total <= 0 {
		return cpuSnapshot{}, false
	}
	return cpuSnapshot{Busy: busy, Total: total, Steal: t[0].Steal, At: time.Now().UnixNano()}, true
}

// busyTotal separa tempo ocupado de tempo total, com a mesma contabilidade que o
// gopsutil usa internamente: no Linux, `guest` e `guest_nice` já estão somados
// dentro de `user` e `nice` no /proc/stat, então entram uma vez só; e `iowait`
// conta como ocioso, porque CPU esperando disco está livre para outro processo.
func busyTotal(t cpu.TimesStat) (busy, total float64) {
	total = t.User + t.System + t.Idle + t.Nice + t.Iowait + t.Irq +
		t.Softirq + t.Steal + t.Guest + t.GuestNice
	if runtime.GOOS == "linux" {
		total -= t.Guest + t.GuestNice
	}
	return total - t.Idle - t.Iowait, total
}

func loadCPUState(path string) cpuSnapshot {
	b, err := os.ReadFile(path) // #nosec G304 -- caminho vem da config do próprio agente
	if err != nil {
		return cpuSnapshot{}
	}
	var s cpuSnapshot
	if json.Unmarshal(b, &s) != nil {
		return cpuSnapshot{}
	}
	return s
}

// saveCPUState grava por arquivo temporário + rename para que uma coleta
// interrompida nunca deixe estado pela metade. As permissões acompanham as do
// diretório de buffer (0755/0644): um `once` rodado à mão com sudo não pode
// deixar para trás um arquivo que o usuário do cron não consiga ler.
func saveCPUState(path string, s cpuSnapshot) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil { // #nosec G306 -- só números, e precisa ser legível pelo usuário do cron
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// dockerSocket é o socket unix padrão do Docker (mesmo caminho usado pelo discover).
const dockerSocket = "/var/run/docker.sock"

// dockerHTTP é um client de nível de pacote reusado a cada ciclo de coleta — criar
// um Transport novo a cada 15s (como antes) vazava conexões idle (fd + goroutine)
// no pool até o dockerd fechar. Um único Transport reaproveita as conexões.
var dockerHTTP = &http.Client{
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", dockerSocket)
		},
		MaxIdleConns:    4,
		IdleConnTimeout: 30 * time.Second,
	},
	Timeout: 3 * time.Second,
}

// dockerGET faz um GET no socket do Docker e decodifica o JSON em v.
func dockerGET(ctx context.Context, c *http.Client, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// Containers coleta métricas por container Docker (best-effort). Inclui containers
// parados (`all=1`) para que falhas apareçam. Se o socket não existe ou há erro,
// devolve nil — jamais bloqueia a coleta de host nem entra em pânico.
//
// ESCALA: `container.cpu.utilization` é 0–100 sobre a MÁQUINA INTEIRA, a mesma
// escala de `system.cpu.utilization` — e NÃO a convenção do `docker stats`, em que
// 100% significa um núcleo cheio. Ver containerCPUHostPercent para o porquê.
//
// A CPU de um container só é emitida quando existe leitura anterior daquele
// container neste processo (ver containerCPUState): sem base não há intervalo, e
// sem intervalo não há utilização. No primeiro ciclo, e no ciclo em que um
// container sobe, a métrica simplesmente falta.
//
// maxContainers limita quantos containers são coletados por ciclo (salvaguarda de
// não-sobrecarga em hosts com muitos containers): <=0 desliga o teto e conta apenas
// com o orçamento de 10s. Quando o teto corta, os parados são descartados primeiro
// (os em execução importam mais) e o corte é logado uma vez por ciclo.
func Containers(ctx context.Context, maxContainers int) []Point {
	if _, err := os.Stat(dockerSocket); err != nil {
		return nil
	}
	// Orçamento de tempo próprio: a coleta de container roda inline no ciclo de
	// métricas; sem teto, N containers (1 inspect + 1 stats cada) atrasariam ou
	// perderiam o tick de 15s. Passado o budget, entrega o que já coletou.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c := dockerHTTP

	var list []struct {
		ID    string   `json:"Id"`
		Names []string `json:"Names"`
		Image string   `json:"Image"`
		State string   `json:"State"`
	}
	if err := dockerGET(ctx, c, "http://docker/containers/json?all=1", &list); err != nil {
		return nil
	}

	// Antes de qualquer corte: os IDs que o daemon ainda conhece. Tudo que não está
	// nesta lista some do estado de CPU — senão um host que recria containers a cada
	// deploy (o nosso caso) acumularia uma entrada morta por deploy, para sempre.
	// A poda usa a lista COMPLETA de propósito: um container só descartado pelo teto
	// de `maxContainers` continua existindo e não pode perder a base de medição.
	vivos := make(map[string]bool, len(list))
	for _, ct := range list {
		vivos[ct.ID] = true
	}
	defer podarCPUState(vivos)

	// Teto configurável: prioriza os containers em execução (state=="running") e
	// descarta o excedente (normalmente os parados) para não estourar o ciclo.
	if maxContainers > 0 && len(list) > maxContainers {
		sort.SliceStable(list, func(i, j int) bool {
			return (list[i].State == "running") && (list[j].State != "running")
		})
		list = list[:maxContainers]
	}

	// Cada container faz 1 inspect + (se running) 1 stats. O stats agora usa
	// `one-shot=1`, então NÃO bloqueia mais ~1s no daemon: o delta de CPU vem da
	// nossa própria leitura anterior (ver containerCPUState). Mesmo assim a coleta
	// segue paralela com concorrência limitada — são 2 round-trips por container no
	// socket, e sequencialmente 50 containers ainda comeriam o ciclo de 15s.
	const parallel = 8
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var pts []Point

	for _, ct := range list {
		if ctx.Err() != nil {
			break // budget estourado: entrega o parcial em vez de segurar o ciclo
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() { // Go 1.22+: ct é uma variável nova a cada iteração (seguro fechar sobre ela)
			defer wg.Done()
			defer func() { <-sem }()
			cp := collectContainer(ctx, c, ct.ID, ct.State, ct.Image, ct.Names)
			if len(cp) > 0 {
				mu.Lock()
				pts = append(pts, cp...)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return pts
}

// collectContainer coleta os Points de um container (inspect + stats se running).
func collectContainer(ctx context.Context, c *http.Client, id, listState, image string, names []string) []Point {
	name := image
	if len(names) > 0 {
		name = strings.TrimPrefix(names[0], "/")
	}

	// Inspect para state confiável, health, exit_code, restart_count e política de
	// reinício. `inspected` distingue "li o estado de verdade" de "caí no fallback
	// da listagem" — só suprimimos um one-shot concluído quando temos certeza.
	state, health, exitCode, restarts := listState, "none", "0", 0.0
	restartPolicy, inspected := "", false
	var insp struct {
		State struct {
			Status   string `json:"Status"`
			ExitCode int    `json:"ExitCode"`
			Health   *struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
		RestartCount int `json:"RestartCount"`
		HostConfig   struct {
			RestartPolicy struct {
				Name string `json:"Name"`
			} `json:"RestartPolicy"`
		} `json:"HostConfig"`
	}
	if err := dockerGET(ctx, c, "http://docker/containers/"+id+"/json", &insp); err == nil {
		inspected = true
		if insp.State.Status != "" {
			state = insp.State.Status
		}
		if insp.State.Health != nil && insp.State.Health.Status != "" {
			health = insp.State.Health.Status
		}
		exitCode = strconv.Itoa(insp.State.ExitCode)
		restarts = float64(insp.RestartCount)
		restartPolicy = insp.HostConfig.RestartPolicy.Name
	}
	running := state == "running"

	// Job one-shot que terminou com sucesso NÃO é "caído": não emitimos série para
	// ele (ver completedOneShot). Um one-shot que FALHA segue emitindo e alerta.
	if completedOneShot(inspected, running, state, exitCode, restartPolicy) {
		return nil
	}

	// SÉRIE ESTÁVEL vs. SÉRIE DESCRITIVA — a separação abaixo é a correção inteira.
	//
	// No caminho de GRÁFICO, a série é o MAPA DE LABELS INTEIRO. Enquanto `image`,
	// `state`, `health` e `exit_code` viajavam junto com o valor, o mesmo container
	// virava N séries diferentes: `revoada-server` tinha duas séries que só
	// diferiam no `image` (nome × digest), e o container `api` produziu 24 conjuntos
	// de labels distintos num único crash-loop.
	//
	// A consequência é a pior possível para um painel de monitoramento: a linha de
	// `container.running` NÃO cai de 1 para 0 quando o container morre. A série de
	// labels antiga (state=running) simplesmente ACABA e uma série nova (state=exited)
	// começa em 0. No gráfico isso se lê como "parou de medir", não como "caiu" — e
	// "parou de medir" é exatamente o que um agente morto também parece.
	//
	// Então: os labels do NÚMERO são só o que identifica o objeto medido (container).
	// O que descreve o estado — que muda o tempo todo e é o que causava a explosão —
	// sai numa métrica própria, `container.state`, onde a rotatividade de labels é
	// esperada e não quebra nenhuma linha.
	idLabels := map[string]string{"container": name}
	// `container.state` é a série DESCRITIVA: o valor é o código numérico do estado
	// (graficável mesmo ignorando os labels) e os labels carregam o detalhe legível.
	// `image` fica aqui, e só aqui: o inventário do servidor precisa dela, mas ela
	// não pode contaminar as séries numéricas — trocar `app:1.2` por `app@sha256:...`
	// num deploy quebrava a continuidade de TODAS as métricas do container.
	stateLabels := map[string]string{
		"container": name, "image": image,
		"state": state, "health": health, "exit_code": exitCode,
	}

	runVal := 0.0
	if running {
		runVal = 1
	}
	pts := []Point{
		{Name: "container.running", Value: runVal, Labels: idLabels},
		{Name: "container.state", Value: codigoEstado(state), Labels: stateLabels},
		{Name: "container.restarts", Value: restarts, Labels: idLabels},
	}

	// Uso de CPU/memória só faz sentido para containers em execução.
	if running {
		if s, ok := containerStats(ctx, c, id); ok {
			// CPU sai da lista só quando há leitura anterior daquele container. Na
			// primeira coleta do processo, ou num container que acabou de subir, a
			// métrica FALTA — e faltar é melhor que mentir: era exatamente inventar
			// um número aqui que fazia o painel mostrar 160,73% para o ClickHouse
			// enquanto o `docker stats` mostrava 27,77%.
			if s.cpuOK {
				pts = append(pts, Point{Name: "container.cpu.utilization", Value: s.cpuPct, Labels: idLabels})
			}
			// Mesma regra da CPU acima: sem memória informada pelo daemon, a
			// métrica FALTA. Ver o comentário em containerStats.
			if s.memOK {
				pts = append(pts,
					Point{Name: "container.memory.utilization", Value: s.memPct, Labels: idLabels},
					Point{Name: "container.memory.usage", Value: s.memUsed, Labels: idLabels},
					Point{Name: "container.memory.limit", Value: s.memLimit, Labels: idLabels},
				)
			}
		}
	}
	return pts
}

// completedOneShot decide se um container `exited` é NÃO GERENCIADO (efêmero/one-off)
// e, portanto, NÃO deve virar série de métrica (função pura, testável).
//
// A regra "Container caído" (container.running < 1) deve valer só para SERVIÇOS
// GERENCIADOS — os que têm política de reinício `always`/`unless-stopped`/`on-failure`
// e, portanto, deveriam estar de pé. Um container com política `no` (ou vazia, o
// padrão do `docker run`) que saiu é um job/one-off ou algo que alguém parou de
// propósito — NÃO é um serviço "caído", independentemente do código de saída. Emitir
// `container.running=0` para ele geraria alerta CRÍTICO em falso (ex.: um `docker run`
// descartável com nome auto-gerado como "romantic_gagarin"). Regras (todas valem):
//   - inspected: só com estado confiável do inspect (erro transitório de coleta não
//     pode esconder uma queda real).
//   - !running: em execução nunca é suprimido.
//   - state=="exited": terminou (parado/saiu). Serviços que caem seguem `exited`/`dead`
//     mas com política de reinício ≠ "no", então continuam alertando.
//   - política "no" ou "" : não é um serviço que deveria estar de pé.
//
// `exitCode` deixou de ser condição (antes só suprimia exit 0): agora um one-off que
// FALHA com política "no" também não vira "Container caído". Mantido na assinatura por
// compatibilidade e para eventual uso futuro.
func completedOneShot(inspected, running bool, state, exitCode, restartPolicy string) bool {
	_ = exitCode
	if !inspected || running {
		return false
	}
	if !estadoParadoSemJuizo(state) {
		return false
	}
	return restartPolicy == "no" || restartPolicy == ""
}

// estadoParadoSemJuizo são os estados de Docker em que o container NÃO está em
// execução e isso, por si só, não é falha de nada.
//
// Por que `created` e `paused` entraram junto com `exited`: a supressão cobria só
// `exited`, então um container em `created` com política `no`/"" seguia emitindo
// `container.running=0` e a regra de fábrica "Container caído" abria CRÍTICO. Não é
// hipótese — foram 12 containers `scopeflow-*` presos em `created`, com 5.625
// amostras cada, gerando crítico contínuo. Pior: o painel se contradizia sozinho,
// porque server/internal/inventory/containers.go já classifica `created` (e `paused`)
// como NEUTRAL. A mesma máquina, na mesma tela, dizia "neutro" na lista de containers
// e "crítico" no alerta.
//
// `created` é um container que nunca chegou a rodar (docker create sem start, ou um
// `docker compose` interrompido); `paused` é uma pausa DELIBERADA de operador. Nenhum
// dos dois é "o serviço caiu". `dead` continua de fora de propósito: dead é falha do
// próprio daemon ao remover o container, e isso o operador precisa ver.
func estadoParadoSemJuizo(state string) bool {
	return state == "exited" || state == "created" || state == "paused"
}

// codigoEstado traduz o estado do Docker num número estável, o valor da métrica
// `container.state`.
//
// Existe porque um gráfico precisa de um NÚMERO. Se `container.state` valesse sempre
// 1 e só os labels mudassem, voltaríamos ao mesmo problema que a separação corrige:
// para saber o que aconteceu seria obrigatório ler o mapa de labels, que é justamente
// o que quebra a continuidade da série. Com o código numérico dá para ver a transição
// running(1) → exited(5) numa linha só, sem depender de label nenhum.
//
// Os números são CONTRATO com o servidor e com a UI: só se acrescenta no fim, nunca
// se renumera. 0 é "desconhecido" de propósito — um estado novo do Docker aparece
// como 0 em vez de se disfarçar de outro estado.
func codigoEstado(state string) float64 {
	switch state {
	case "running":
		return 1
	case "created":
		return 2
	case "paused":
		return 3
	case "restarting":
		return 4
	case "exited":
		return 5
	case "dead":
		return 6
	case "removing":
		return 7
	default:
		return 0
	}
}

// dockerStats é o subconjunto de /containers/{id}/stats que usamos. `precpu_stats`
// não é mais lido: com `one-shot=1` o daemon devolve UMA amostra e deixa esse bloco
// zerado. A segunda amostra passou a ser a NOSSA leitura do ciclo anterior.
type dockerStats struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage float64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage float64 `json:"system_cpu_usage"`
		OnlineCPUs     float64 `json:"online_cpus"`
	} `json:"cpu_stats"`
	MemoryStats struct {
		Usage float64 `json:"usage"`
		Limit float64 `json:"limit"`
		Stats struct {
			InactiveFile float64 `json:"inactive_file"`
		} `json:"stats"`
	} `json:"memory_stats"`
}

type containerSample struct {
	cpuPct                    float64 // 0–100 da MÁQUINA INTEIRA (ver containerCPUHostPercent)
	cpuOK                     bool    // false = sem leitura anterior; não emita a métrica
	memPct, memUsed, memLimit float64
	memOK                     bool // false = o daemon não informou memória; não emita
}

// containerSnapshot é uma leitura acumulada do contador de CPU de um container.
// Os dois campos contam desde o boot (o do host) e desde a subida do container; o
// que interessa, como em cpu.go, é sempre a diferença entre duas leituras.
type containerSnapshot struct {
	// Campos exportados porque este snapshot é gravado em disco no modo cron
	// (ver UseContainerStateFile) — cada execução por cron é um processo novo, e
	// sem a leitura anterior a utilização de container simplesmente não existiria.
	Total  float64 `json:"total"`  // nanossegundos de CPU consumidos pelo container
	System float64 `json:"system"` // nanossegundos de CPU do host inteiro, somando núcleos, no mesmo instante
	Online float64 `json:"online"` // núcleos que o container enxergava na leitura
	At     int64   `json:"at"`     // unix nano da leitura
}

func (s containerSnapshot) valid() bool { return s.At != 0 && s.System > 0 }

const (
	// maxContainerCPUGap é o maior intervalo entre leituras que ainda descreve
	// "agora". Acima disso a média passaria a descrever a última hora, e cresce a
	// chance de o container ter sido recriado com o mesmo ID no meio.
	maxContainerCPUGap = 10 * time.Minute

	// minContainerSysDelta é o mínimo de tempo de CPU do host acumulado no intervalo
	// (em nanossegundos, somando núcleos) para a divisão ter resolução. Mesmo papel
	// do minCPUDelta em cpu.go: abaixo disso o quociente é ruído de arredondamento.
	minContainerSysDelta = 0.2 * 1e9
)

// containerCPUState guarda a última leitura de CPU POR CONTAINER (chave = ID). É
// estado de processo de propósito, exatamente como o CPUMeter de cpu.go: a medida
// não existe sem um intervalo, e o intervalo que interessa é o ciclo inteiro de
// coleta (~15s), não uma janela arbitrária dentro dele.
var containerCPUState = struct {
	mu        sync.Mutex
	prev      map[string]containerSnapshot
	stateFile string
	carregado bool
}{prev: map[string]containerSnapshot{}}

// UseContainerStateFile faz a CPU de container lembrar a leitura anterior entre
// execuções do binário, como UseCPUStateFile faz para a CPU do host.
//
// Sem isto, no modo `once` (cron) a métrica `container.cpu.utilization` NUNCA seria
// emitida: cada execução é um processo novo, nunca há leitura anterior, e a medida
// é recusada por não ter intervalo. Não é hipótese — o host `mail.exemplo.com.br` roda
// por cron E tem containers; sem este arquivo, a CPU dos containers dele sumiria do
// painel, e um alerta aberto sobre ela seria auto-resolvido como "sem dados".
func UseContainerStateFile(path string) {
	containerCPUState.mu.Lock()
	defer containerCPUState.mu.Unlock()
	containerCPUState.stateFile = path
}

// carregarSeVazio traz o estado do disco na primeira consulta do processo. Chamada
// com o lock preso.
func carregarSeVazio() {
	if containerCPUState.carregado || containerCPUState.stateFile == "" {
		return
	}
	containerCPUState.carregado = true
	b, err := os.ReadFile(containerCPUState.stateFile) // #nosec G304 -- caminho vem da config do próprio agente
	if err != nil {
		return
	}
	var lido map[string]containerSnapshot
	if json.Unmarshal(b, &lido) != nil {
		return
	}
	for id, s := range lido {
		if s.valid() {
			containerCPUState.prev[id] = s
		}
	}
}

// gravarEstado persiste o mapa inteiro. Chamada com o lock preso. Falha em silêncio
// de propósito: quem avisa o operador é o medidor de CPU do host, que grava no mesmo
// diretório e no mesmo ciclo — dois avisos idênticos só fariam ruído.
func gravarEstado() {
	if containerCPUState.stateFile == "" {
		return
	}
	b, err := json.Marshal(containerCPUState.prev)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(containerCPUState.stateFile), 0o755) != nil {
		return
	}
	tmp := containerCPUState.stateFile + ".tmp"
	// 0600, não 0644: quem lê e escreve este arquivo é sempre o MESMO usuário (o
	// `revoada` do serviço, ou o `revoada` do cron — a linha de cron é `crontab -u revoada`),
	// então nada precisa que ele seja legível por todo mundo. E o conteúdo não é
	// "só números": a chave do mapa é o ID de cada container do host, ou seja, o
	// arquivo é um inventário de o que roda ali, legível por qualquer usuário da
	// máquina do cliente. Restringir é grátis e tira uma pista de reconhecimento.
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	if os.Rename(tmp, containerCPUState.stateFile) != nil {
		_ = os.Remove(tmp)
	}
}

// trocarCPUState guarda a leitura nova e devolve a anterior daquele container.
func trocarCPUState(id string, cur containerSnapshot) containerSnapshot {
	containerCPUState.mu.Lock()
	defer containerCPUState.mu.Unlock()
	carregarSeVazio()
	prev := containerCPUState.prev[id]
	containerCPUState.prev[id] = cur
	return prev
}

// podarCPUState descarta o estado de containers que o daemon não conhece mais.
// Sem isso o mapa cresceria para sempre num host que recria containers a cada
// deploy — um vazamento pequeno por ciclo, mas num processo que roda por meses.
func podarCPUState(vivos map[string]bool) {
	containerCPUState.mu.Lock()
	defer containerCPUState.mu.Unlock()
	// CONFIRMADO e corrigido: sem esta carga, um ciclo em que NENHUM containerStats
	// deu certo apagava o arquivo de estado inteiro.
	//
	// O caminho era este: carregarSeVazio() só era chamado dentro de trocarCPUState,
	// e trocarCPUState só roda quando um stats devolve um snapshot válido. Se o
	// daemon estivesse ocupado, o budget de 10s estourasse, ou todos os containers
	// estivessem parados, `prev` continuava vazio — e gravarEstado() logo abaixo
	// escrevia `{}` por cima do containers.state, jogando fora as bases do ciclo
	// anterior. No modo cron (mail.exemplo.com.br, 1 execução por minuto) isso é um
	// buraco de 1 minuto em `container.cpu.utilization` de TODOS os containers, e
	// pior: silencioso, porque a métrica simplesmente falta em vez de errar.
	//
	// Carregar antes de podar/gravar fecha o buraco — e é barato, porque o próprio
	// carregarSeVazio só lê o disco uma vez por processo.
	carregarSeVazio()
	for id := range containerCPUState.prev {
		if !vivos[id] {
			delete(containerCPUState.prev, id)
		}
	}
	// Uma gravação por ciclo, depois da poda: o arquivo nunca guarda container morto.
	gravarEstado()
}

// containerStats lê UMA amostra e calcula CPU%/mem do container.
//
// `one-shot=1` é o ponto principal: sem ele, `stream=false` faz o daemon tirar duas
// amostras separadas por ~1s e devolver o par pronto em `precpu_stats`. Isso media
// 1s de cada 15s (6,7% do tempo decorrido) e chamava o resultado de média — o mesmo
// defeito que cpu.go corrigiu para o host. Em produção o agente reportou 160,73%
// para o container do ClickHouse enquanto o `docker stats` mostrava 27,77%: 5,8x.
// Agora a amostra anterior é a nossa, do ciclo passado, e o intervalo medido é o
// intervalo real. De quebra o daemon deixa de ficar 1s preso por container.
//
// Em daemons anteriores ao Docker 20.10 o parâmetro `one-shot` é simplesmente
// ignorado (a API descarta chaves que não conhece): a chamada volta a demorar ~1s,
// mas a conta continua certa, porque ela não usa mais `precpu_stats`.
func containerStats(ctx context.Context, c *http.Client, id string) (containerSample, bool) {
	var st dockerStats
	if err := dockerGET(ctx, c, "http://docker/containers/"+id+"/stats?stream=false&one-shot=1", &st); err != nil {
		return containerSample{}, false
	}
	cur := containerSnapshot{
		Total:  st.CPUStats.CPUUsage.TotalUsage,
		System: st.CPUStats.SystemCPUUsage,
		Online: st.CPUStats.OnlineCPUs,
		At:     time.Now().UnixNano(),
	}
	// Só troca o estado quando a leitura serve de base: o daemon responde 200 com
	// `cpu_stats` zerado para container que morreu entre a listagem e o stats, e
	// guardar esse zero como `prev` estragaria também o ciclo SEGUINTE — o delta
	// seria calculado contra uma base falsa. Snapshot inválido não vira base.
	var prev containerSnapshot
	if cur.valid() {
		prev = trocarCPUState(id, cur)
	}

	s := containerSample{memLimit: st.MemoryStats.Limit}
	s.cpuPct, s.cpuOK = containerCPUBetween(prev, cur)

	// A mesma corrida esvazia `memory_stats` — e sem guarda o agente emitia
	// usage=0, limit=0 e utilization=0. Três zeros que se parecem com um container
	// ocioso e são, na verdade, ausência de medida. É o pecado que esta coleta
	// existe para não cometer: faltar é melhor que mentir.
	s.memOK = st.MemoryStats.Limit > 0
	if s.memOK {
		// Memória "real" ≈ usage - inactive_file (o que o `docker stats` mostra).
		s.memUsed = st.MemoryStats.Usage - st.MemoryStats.Stats.InactiveFile
		if s.memUsed < 0 {
			s.memUsed = st.MemoryStats.Usage
		}
		s.memPct = s.memUsed / s.memLimit * 100
	}
	return s, true
}

// containerCPUBetween calcula a ocupação de CPU de um container entre duas leituras
// acumuladas, em 0–100 DA MÁQUINA INTEIRA (função pura, testável). Recusa o que não
// dá para afirmar: sem leitura anterior, intervalo negativo, longo demais, curto
// demais, ou contador que andou para trás (container recriado / daemon reiniciado).
func containerCPUBetween(prev, cur containerSnapshot) (float64, bool) {
	if !prev.valid() || !cur.valid() {
		return 0, false
	}
	gap := time.Duration(cur.At - prev.At)
	if gap <= 0 || gap > maxContainerCPUGap {
		return 0, false
	}
	sysDelta := cur.System - prev.System
	cpuDelta := cur.Total - prev.Total
	if sysDelta < minContainerSysDelta || cpuDelta < 0 {
		return 0, false
	}
	pct := containerCPUHostPercent(cpuDelta, sysDelta, cur.Online)
	return math.Max(0, math.Min(100, pct)), true
}

// dockerCPUPercent aplica a fórmula padrão do Docker stats (função pura, testável):
// (cpu_delta/system_delta) * online_cpus * 100, com guardas contra divisão por zero.
// Nessa convenção 100% = UM núcleo ocupado, e o valor vai até 100*núcleos.
func dockerCPUPercent(cpuDelta, systemDelta, onlineCPUs float64) float64 {
	if onlineCPUs <= 0 {
		onlineCPUs = 1
	}
	if systemDelta <= 0 || cpuDelta <= 0 {
		return 0
	}
	return (cpuDelta / systemDelta) * onlineCPUs * 100
}

// containerCPUHostPercent converte a convenção do Docker para a MESMA escala de
// `system.cpu.utilization`: 0–100 sobre a máquina inteira, onde 100 é todos os
// núcleos saturados. É só dividir por online_cpus.
//
// A mudança de escala é deliberada e muda o significado do número publicado em
// `container.cpu.utilization`. O motivo: num único ciclo medido em produção, a soma
// dos 10 containers deu 170,9% enquanto o host marcou 11,59%. As duas medidas
// estavam "certas" nas suas próprias convenções, e justamente por isso o agente se
// contradizia dentro do mesmo ciclo — na tela ninguém conseguia reconciliar. Com a
// escala unificada, a soma dos containers passa a ser comparável (e menor ou igual)
// à do host, que é a única leitura que um operador consegue interpretar.
func containerCPUHostPercent(cpuDelta, systemDelta, onlineCPUs float64) float64 {
	if onlineCPUs <= 0 {
		onlineCPUs = 1
	}
	return dockerCPUPercent(cpuDelta, systemDelta, onlineCPUs) / onlineCPUs
}

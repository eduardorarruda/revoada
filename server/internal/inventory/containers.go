package inventory

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/eduardorarruda/revoada/server/internal/chquery"
)

// Vocabulário de status de container (super-conjunto do semáforo de recurso: além
// de ok/warn/crit existe "neutral" para paradas intencionais/estados sem juízo).
const (
	statusOK      = "ok"
	statusWarn    = "warn"
	statusCrit    = "crit"
	statusNeutral = "neutral"
)

// ContainerStatus é o estado derivado de um container Docker num host (Fase C).
// Os campos de recurso (CPU/mem) só vêm preenchidos quando o container está em
// execução; ausentes viram nil (o agente só emite essas métricas para running).
type ContainerStatus struct {
	Name     string   `json:"name"`
	Image    string   `json:"image"`
	State    string   `json:"state"`
	Health   string   `json:"health"`
	Running  bool     `json:"running"`
	Restarts int64    `json:"restarts"`
	CPUPct   *float64 `json:"cpu_pct,omitempty"`        // só running
	MemPct   *float64 `json:"mem_pct,omitempty"`        // só running
	MemUsed  *float64 `json:"mem_used_bytes,omitempty"` // só running
	MemLimit *float64 `json:"mem_limit_bytes,omitempty"`
	ExitCode *int64   `json:"exit_code,omitempty"` // código de saída (containers exited)
	Status   string   `json:"status"`              // ok | warn | crit | neutral
	// O NÚMERO SOZINHO NÃO CARREGA A IDADE — mesmo contrato de HealthMetric.TS.
	//
	// Container era o único objeto do painel servido sem carimbo de tempo: a janela
	// de 5 min desta consulta era a única validade, e ela não aparecia na resposta.
	// Efeito medido em 13/08/2026: um container que rodava a ~4% de CPU foi exibido
	// como 52,4% por mais de dois minutos, verde, no MESMO cartão em que o ladrilho
	// de CPU do host — que tem TS — dizia corretamente "sem dado". Depois dos 5 min o
	// container simplesmente sumia da lista, e a tela escrevia "nenhum container em
	// execução": ausência de medida virando afirmação sobre o mundo.
	//
	// TS é o instante (epoch em SEGUNDOS) da amostra mais recente daquele container;
	// StepSecs é o passo REAL observado na janela (0/ausente = indeterminado, e o
	// front cai no piso de validade).
	TS       *int64   `json:"ts,omitempty"`
	StepSecs *float64 `json:"step_seconds,omitempty"`
}

// containerStatus deriva o semáforo de um container a partir do estado bruto do
// Docker (função pura, testável, sem I/O). Regras EXATAS, avaliadas em ordem:
//
//	running && (health=="healthy" || health=="none") => ok
//	running && health=="unhealthy"                    => warn
//	state=="restarting"                               => warn
//	state=="dead" || (state=="exited" && exit!="0")   => crit
//	state=="exited" && exit=="0"                       => neutral (parada intencional)
//	state=="paused" || state=="created"               => neutral
//	qualquer outro                                    => neutral
func containerStatus(running bool, state, health, exitCode string) string {
	switch {
	case running && (health == "healthy" || health == "none"):
		return statusOK
	case running && health == "unhealthy":
		return statusWarn
	case state == "restarting":
		return statusWarn
	case state == "dead" || (state == "exited" && exitCode != "0"):
		return statusCrit
	case state == "exited" && exitCode == "0":
		return statusNeutral
	case state == "paused" || state == "created":
		return statusNeutral
	default:
		return statusNeutral
	}
}

// statusRank ordena para a UI destacar problemas: crit e warn no topo (mesma
// faixa), depois neutral, depois ok. Empate resolvido pelo nome (determinismo).
func statusRank(status string) int {
	switch status {
	case statusCrit, statusWarn:
		return 0
	case statusNeutral:
		return 1
	case statusOK:
		return 2
	}
	return 1 // desconhecido tratado como neutral
}

// sortContainers ordena in-place por (rank do status, nome). Falhas (crit/warn)
// primeiro, depois neutral, depois ok; empate alfabético por nome.
func sortContainers(cs []ContainerStatus) {
	sort.SliceStable(cs, func(i, j int) bool {
		ri, rj := statusRank(cs[i].Status), statusRank(cs[j].Status)
		if ri != rj {
			return ri < rj
		}
		return cs[i].Name < cs[j].Name
	})
}

// containerAgg acumula os últimos valores das métricas de um container.
type containerAgg struct {
	image                   string
	state, health, exitCode string
	// fallback* são os mesmos rótulos vindos de container.running, que é onde o agente
	// ANTIGO ainda os emite. Só valem quando container.state não trouxe nada — durante
	// o rollout a frota fica misturada, e o host que ainda não atualizou não pode ficar
	// com a coluna de estado em branco.
	fallbackState, fallbackHealth, fallbackExit string
	running                                     bool
	restarts                                    float64
	hasCPU, hasMem, hasMemUsed, hasLimit        bool
	cpu, mem, memUsed, memLimit                 float64
	// visto é a amostra mais RECENTE entre as métricas deste container — é ela que
	// responde "de quando é este número". Guardamos a mais nova porque as métricas do
	// mesmo container chegam no mesmo ciclo do agente; se uma delas atrasar, o que
	// importa para a tela é a última vez que ouvimos falar do container.
	visto *amostra
}

// viu registra a amostra se ela for mais nova que a guardada.
func (c *containerAgg) viu(a *amostra) {
	if a == nil {
		return
	}
	if c.visto == nil || a.TS > c.visto.TS {
		c.visto = a
	}
}

// BuildContainerStatuses lê do ClickHouse o último estado por (host, container)
// na janela de 5 min e devolve, por host, a lista de containers já derivada e
// ordenada. Hosts sem containers simplesmente não aparecem no mapa. Erro de
// consulta é propagado (não confundir "sem containers" com "ClickHouse fora").
func BuildContainerStatuses(ctx context.Context, ch *chquery.Client) (map[string][]ContainerStatus, error) {
	// Último ponto (argMax por ts) de cada métrica por (host, container). Os rótulos
	// descritivos — image/state/health/exit_code — vêm da métrica `container.state`,
	// e NÃO mais de `container.running`.
	//
	// A mudança é consequência de um defeito real: enquanto `state`, `health` e
	// `exit_code` viajavam em `container.running`, cada troca de estado criava uma
	// SÉRIE nova (a série é o mapa de labels inteiro). Num crash-loop o container `api`
	// gerou 24 conjuntos de rótulos distintos, e a linha de `container.running` não
	// caía de 1 para 0: a série acabava e outra começava em 0 — que se lê como "parou
	// de medir", não como "caiu". O agente passou a emitir esses rótulos numa métrica
	// própria; se esta consulta continuasse lendo de `container.running`, a lista de
	// containers mostraria imagem, estado e health VAZIOS para toda a frota.
	rows, err := ch.QueryJSON(ctx, `
		SELECT labels['host'] AS host,
		       labels['container'] AS container,
		       metric,
		       argMax(value, ts) AS v,
		       argMax(labels['image'], ts) AS image,
		       argMax(labels['state'], ts) AS state,
		       argMax(labels['health'], ts) AS health,
		       argMax(labels['exit_code'], ts) AS exit_code,
		       toUnixTimestamp(max(ts)) AS at,
		       uniqExact(ts) AS n,
		       toUnixTimestamp(max(ts)) - toUnixTimestamp(min(ts)) AS span
		FROM metrics
		WHERE metric IN (
			'container.running','container.state','container.restarts',
			'container.cpu.utilization','container.memory.utilization',
			'container.memory.usage','container.memory.limit')
		  AND labels['container'] != ''
		  AND ts > now() - INTERVAL 5 MINUTE AND ts <= now()
		GROUP BY host, container, metric`)
	if err != nil {
		return nil, fmt.Errorf("consulta de containers falhou: %w", err)
	}

	// agg[host][container] => estado acumulado.
	agg := map[string]map[string]*containerAgg{}
	get := func(host, name string) *containerAgg {
		if agg[host] == nil {
			agg[host] = map[string]*containerAgg{}
		}
		if agg[host][name] == nil {
			agg[host][name] = &containerAgg{}
		}
		return agg[host][name]
	}

	for _, row := range rows {
		host, _ := row["host"].(string)
		name, _ := row["container"].(string)
		metric, _ := row["metric"].(string)
		if host == "" || name == "" {
			continue
		}
		c := get(host, name)
		c.viu(amostraDe(row))
		if img, _ := row["image"].(string); img != "" {
			c.image = img
		}
		v, ok := toFloat(row["v"])
		if !ok {
			// Sem valor legítimo (null/NaN) não dá para dizer se o container está de
			// pé, quantas vezes reiniciou nem quanto consome. Ignorar a linha deixa o
			// campo ausente (nil/omitido) em vez de inventar "0 reinícios" ou "0% CPU".
			continue
		}
		switch metric {
		case "container.running":
			c.running = v == 1
			// Agente ANTIGO ainda manda os rótulos descritivos aqui. Aceita como reserva,
			// nunca por cima do que veio de container.state: a ordem das linhas do
			// ClickHouse não é garantida, e sem essa assimetria o resultado dependeria de
			// qual métrica saiu primeiro do GROUP BY.
			c.fallbackState, _ = row["state"].(string)
			c.fallbackHealth, _ = row["health"].(string)
			c.fallbackExit, _ = row["exit_code"].(string)
		case "container.state":
			// Fonte autoritativa desde que os rótulos saíram de container.running.
			c.state, _ = row["state"].(string)
			c.health, _ = row["health"].(string)
			c.exitCode, _ = row["exit_code"].(string)
		case "container.restarts":
			c.restarts = v
		case "container.cpu.utilization":
			c.hasCPU, c.cpu = true, v
		case "container.memory.utilization":
			c.hasMem, c.mem = true, v
		case "container.memory.usage":
			c.hasMemUsed, c.memUsed = true, v
		case "container.memory.limit":
			c.hasLimit, c.memLimit = true, v
		}
	}

	out := map[string][]ContainerStatus{}
	for host, byName := range agg {
		list := make([]ContainerStatus, 0, len(byName))
		for name, c := range byName {
			// container.state manda; container.running é a reserva do agente antigo.
			estado, health, exitCode := c.state, c.health, c.exitCode
			if estado == "" {
				estado, health, exitCode = c.fallbackState, c.fallbackHealth, c.fallbackExit
			}
			cs := ContainerStatus{
				Name:     name,
				Image:    c.image,
				State:    estado,
				Health:   health,
				Running:  c.running,
				Restarts: int64(c.restarts),
				Status:   containerStatus(c.running, estado, health, exitCode),
			}
			if c.visto != nil {
				ts := c.visto.TS
				cs.TS = &ts
				if c.visto.Step > 0 {
					step := c.visto.Step
					cs.StepSecs = &step
				}
			}
			// exit_code só faz sentido para containers parados; expõe o código quando
			// o rótulo é um inteiro válido (a UI mostra "(código N)" para exited≠0).
			if estado == "exited" && exitCode != "" {
				if code, err := strconv.ParseInt(exitCode, 10, 64); err == nil {
					cs.ExitCode = &code
				}
			}
			// Métricas de recurso só fazem sentido (e só são emitidas) para running.
			if c.running {
				if c.hasCPU {
					cs.CPUPct = ptr(c.cpu)
				}
				if c.hasMem {
					cs.MemPct = ptr(c.mem)
				}
				if c.hasMemUsed {
					cs.MemUsed = ptr(c.memUsed)
				}
				if c.hasLimit {
					cs.MemLimit = ptr(c.memLimit)
				}
			}
			list = append(list, cs)
		}
		sortContainers(list)
		out[host] = list
	}
	return out, nil
}

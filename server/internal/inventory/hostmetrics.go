package inventory

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/chquery"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// DiskMetric é a utilização de uma montagem (mount) com bytes pareados e semáforo.
type DiskMetric struct {
	Mount string   `json:"mount"`
	Pct   float64  `json:"pct"`
	Used  *float64 `json:"used_bytes,omitempty"`
	Total *float64 `json:"total_bytes,omitempty"`
	State string   `json:"state"`
	// Mesmo contrato de HealthMetric.TS/StepSecs: quando o valor foi medido e com que
	// passo, para o front julgar o frescor de cada ladrilho em vez de confiar numa
	// janela constante do servidor.
	TS       *int64   `json:"ts,omitempty"`
	StepSecs *float64 `json:"step_seconds,omitempty"`
}

// NetRate é a taxa de rede em bytes/s, derivada dos contadores cumulativos.
//
// PONTEIROS pelo mesmo motivo de cpu/mem/swap/disco/procs neste arquivo: taxa é
// DERIVADA de dois pontos do contador, e há dois casos comuns em que ela não pode
// ser calculada — só um ponto na janela (host recém-chegado, coleta esparsa) e
// contador reiniciado (reboot). Nos dois, o valor float não-ponteiro saía 0 e a
// lista de Servidores imprimia "↓ 0 B/s ↑ 0 B/s" para um host que estava sendo
// medido: zero no lugar de lacuna, exatamente o defeito que o resto do arquivo já
// tinha corrigido. Omitido = "não deu para calcular", e a tela desenha travessão.
type NetRate struct {
	RxBps *float64 `json:"rx_bps,omitempty"`
	TxBps *float64 `json:"tx_bps,omitempty"`
}

// HostMetrics é a visão de "servidor inteiro" da tela Hosts (Fase B): utilização
// atual de cpu/mem/swap/disco(por mount), taxa de rede e contagem de processos.
// swap é omitido/null quando o host não tem swap configurado.
type HostMetrics struct {
	Host       string        `json:"host"`
	Up         bool          `json:"up"`
	UptimeSecs float64       `json:"uptime_secs"`
	CPU        HealthMetric  `json:"cpu"`
	Mem        HealthMetric  `json:"mem"`
	Swap       *HealthMetric `json:"swap,omitempty"`
	Disks      []DiskMetric  `json:"disks"`
	Net        NetRate       `json:"net"`
	// Procs é PONTEIRO pelo mesmo motivo de cpu/mem/swap/disco: nil = a contagem de
	// PIDs não chegou na janela, e isso não é a mesma afirmação que "zero processos".
	// O agente omite a métrica quando a leitura de /proc falha (agent/internal/collect/
	// host.go), então um servidor vivo, reportando CPU e RAM, aparecia no card como
	// "Processos 0" — um servidor sem nenhum processo, o que é impossível por
	// construção (o próprio agente é um processo).
	Procs *int64 `json:"procs"`
	// Containers é a lista de containers Docker do host com estado derivado (Fase C);
	// omitida quando o host não reporta nenhum container.
	Containers []ContainerStatus `json:"containers,omitempty"`
}

// HostMetrics devolve, por host do inventário, as métricas de servidor inteiro
// anexadas do ClickHouse (últimos valores em 5 min + taxa de rede na janela).
// Autenticado como qualquer usuário logado (mesmo nível de /api/hosts).
func (h *Handler) HostMetrics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out, err := BuildHostMetrics(ctx, h.st, h.ch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out = scopeFilter(ctx, out, func(m HostMetrics) string { return m.Host })
	writeJSON(w, http.StatusOK, map[string]any{"hosts": out})
}

// hostAgg acumula os últimos valores de um host lidos do ClickHouse.
//
// Os valores de recurso são PONTEIROS: nil = a métrica não chegou na janela de 5
// min. O swap já tinha essa noção (hasSwap) porque nem todo host tem swap; o resto
// não tinha, e o efeito era o inverso exato do bug que o swap evitava — CPU ausente
// virava `cpu float64` zerado e o card dizia "CPU 0%, verde" para um host que
// ninguém estava medindo. Agora toda métrica de recurso distingue ausência de zero.
type hostAgg struct {
	// cpu é a CPU EXIGIDA: o que a máquina consumiu mais o que o hipervisor negou.
	// É o número que responde "esta máquina está sob pressão?", que é a pergunta do
	// cartão. A CPU consumida sozinha subestima gravemente um VPS estrangulado —
	// medido no srv-02: 3,8% de consumo com 12,7% de roubo, ou seja, o cartão
	// diria "tranquilo" sobre uma máquina que passa um sexto do tempo na fila.
	//
	// A fonte depende da versão do agente, e as duas são o MESMO número:
	//   - 0.8.1+  publica `system.cpu.utilization.total` (consumo + roubo);
	//   - anterior publica `system.cpu.utilization` já COM o roubo dentro.
	// Por isso `cpuLegado` existe: durante o rollout a frota é mista, e trocar a
	// leitura sem reserva deixaria sem CPU justamente os hosts que ainda não
	// atualizaram. cpuTotal tem precedência; o legado só entra quando ele falta.
	cpuTotal, cpuLegado, cpuSteal *float64
	cpuTotalAt, cpuLegadoAt       *amostra
	mem, memUsed, memTotal        *float64
	swap, swapUsed                *float64
	swapTotal                     *float64
	// Quando cada percentual foi medido (ver HealthMetric.TS): o número sozinho não
	// diz se ainda vale, e a validade não é uma constante do servidor.
	memAt, swapAt *amostra
	mounts        map[string]*mountFS
	procs         *float64
	uptime        float64
	rxBps, txBps  *float64
	has           bool
}

// cpuExigida resolve a CPU do cartão entre as duas fontes possíveis durante o
// rollout. Função separada e testável porque a escolha errada é invisível: os dois
// números são plausíveis, e só a comparação com o steal denunciaria a troca.
//
// Precedência é do `.total` mesmo quando o legado é mais recente. O agente 0.8.1
// publica os dois na MESMA coleta, então "legado mais novo" não existe na prática;
// e se existisse (relógio torto, ponto atrasado no buffer em disco), preferir o
// legado seria preferir a série que vai parar de existir.
func (a *hostAgg) cpuExigida() (*float64, *amostra) {
	if a.cpuTotal != nil {
		return a.cpuTotal, a.cpuTotalAt
	}
	// Aqui está a sutileza que custou 1h37m de número errado em produção no dia da
	// virada: cair no legado só é correto quando o legado É a soma, e isso depende da
	// ERA DO AGENTE, não de qual série chegou neste balde.
	//
	// O `steal` é a testemunha da era, e ela vem no mesmo ciclo: agente <0.8.1 não
	// publica steal, então legado = soma e a reserva é exata. Agente 0.8.1+ publica
	// steal, e aí o legado é só a PARCELA consumida — publicá-lo como "CPU exigida"
	// subestima a máquina exatamente na medida do roubo (4,25 pontos, medido no
	// srv-02 às 12:29). Nesse caso a soma se reconstrói somando as duas parcelas,
	// que saem do mesmo intervalo e fecham por construção.
	if a.cpuSteal != nil && a.cpuLegado != nil {
		soma := *a.cpuLegado + *a.cpuSteal
		return &soma, a.cpuLegadoAt
	}
	return a.cpuLegado, a.cpuLegadoAt
}

// hasSwap: o host reportou ALGUMA métrica de swap. Host sem swap configurado não
// reporta nenhuma delas e o bloco de swap sai omitido da resposta (comportamento
// preservado); host COM swap que deixou de reportar só a utilização cai em "nodata".
func (a *hostAgg) hasSwap() bool {
	return a.swap != nil || a.swapUsed != nil || a.swapTotal != nil
}

func (a *hostAgg) ensureMounts() {
	if a.mounts == nil {
		a.mounts = map[string]*mountFS{}
	}
}

// BuildHostMetrics lista os hosts do inventário e anexa as métricas do ClickHouse.
// Compartilha o padrão de BuildWallCards (store + ch), sem depender do Handler.
func BuildHostMetrics(ctx context.Context, st *store.Store, ch *chquery.Client) ([]HostMetrics, error) {
	hosts, err := st.HostsDetailed(ctx, "")
	if err != nil {
		return nil, err
	}
	resolve, err := st.ResolvedThresholds(ctx, "default")
	if err != nil {
		return nil, err
	}

	agg := map[string]*hostAgg{}
	get := func(host string) *hostAgg {
		if agg[host] == nil {
			agg[host] = &hostAgg{}
		}
		agg[host].ensureMounts()
		return agg[host]
	}

	// Últimos valores (gauges): cpu/mem/swap/filesystem(por mount)/processos/uptime.
	// Erro de consulta é propagado — silenciá-lo mostraria "sem métricas" com 200.
	// `at`/`n`/`span` datam cada valor e revelam o passo REAL da série. A janela de 5
	// min segue como limite da busca; o veredito de "ainda vale" passa a ser do
	// frescor por ladrilho no front, e não desta constante (ver HealthMetric.TS).
	rows, err := ch.QueryJSON(ctx, `
		SELECT labels['host'] AS host, metric,
		       COALESCE(nullIf(labels['mount'], ''), '/') AS mount,
		       argMax(value, ts) AS v,
		       toUnixTimestamp(max(ts)) AS at,
		       uniqExact(ts) AS n,
		       toUnixTimestamp(max(ts)) - toUnixTimestamp(min(ts)) AS span
		FROM metrics
		WHERE metric IN (
			'system.cpu.utilization.total','system.cpu.utilization','system.cpu.steal',
			'system.memory.utilization','system.memory.used','system.memory.total',
			'system.paging.utilization','system.paging.used','system.paging.total',
			'system.filesystem.utilization','system.filesystem.used','system.filesystem.total',
			'system.processes.count','system.uptime')
		  AND ts > now() - INTERVAL 5 MINUTE AND ts <= now()
		GROUP BY host, metric, mount`)
	if err != nil {
		return nil, fmt.Errorf("consulta de métricas de host falhou: %w", err)
	}
	for _, row := range rows {
		host, _ := row["host"].(string)
		metric, _ := row["metric"].(string)
		mount, _ := row["mount"].(string)
		v, ok := toFloat(row["v"])
		if !ok {
			continue // nulo/NaN/Inf não é medida: a métrica segue "sem dado"
		}
		a := get(host)
		a.has = true
		at := amostraDe(row)
		switch metric {
		case "system.cpu.utilization.total":
			a.cpuTotal = ptr(v)
			a.cpuTotalAt = at
		case "system.cpu.utilization":
			a.cpuLegado = ptr(v)
			a.cpuLegadoAt = at
		case "system.cpu.steal":
			a.cpuSteal = ptr(v)
		case "system.memory.utilization":
			a.mem = ptr(v)
			a.memAt = at
		case "system.memory.used":
			a.memUsed = ptr(v)
		case "system.memory.total":
			a.memTotal = ptr(v)
		case "system.paging.utilization":
			a.swap = ptr(v)
			a.swapAt = at
		case "system.paging.used":
			a.swapUsed = ptr(v)
		case "system.paging.total":
			a.swapTotal = ptr(v)
		case "system.filesystem.utilization":
			fs := mountOf(a.mounts, mount)
			fs.Pct = ptr(v)
			fs.At = at
		case "system.filesystem.used":
			mountOf(a.mounts, mount).Used = ptr(v)
		case "system.filesystem.total":
			mountOf(a.mounts, mount).Total = ptr(v)
		case "system.processes.count":
			a.procs = ptr(v)
		case "system.uptime":
			a.uptime = v
		}
	}

	// Taxa de rede na janela de 5 min, por host: SOMA DOS DEGRAUS POSITIVOS entre
	// pontos consecutivos, dividida pelo span.
	//
	// Antes era (último − primeiro)/segundos, e isso apagava a coluna inteira quando
	// o contador andava para trás UMA vez. Aconteceu em produção em 10/08/2026: o
	// agente 0.8.0 parou de contar as interfaces virtuais do Docker (docker0/veth*/
	// br-*, que faziam o tráfego de container ser contado duas vezes), o acumulado do
	// srv-02 caiu de ~120 GB para ~32,5 GB e a Rede ficou "— · —" por 5 minutos —
	// com quatro minutos e meio de leituras perfeitas dentro da mesma janela. O mesmo
	// degrau acontece em reboot e em restart de interface, ou seja, justo nos momentos
	// em que se quer olhar para o painel.
	//
	// Somando só os degraus positivos, o caso normal (contador monotônico) dá
	// exatamente o mesmo número, e o degrau negativo é ignorado em vez de condenar a
	// janela. Um contador que reinicia perde o tráfego anterior ao reinício — o que é
	// verdade, não estimativa.
	//
	// O cálculo é por SÉRIE (host, metric, labels) e depois se escolhe a série de
	// maior delta com o span DELA (argMax pareia os dois). Um host costuma ter mais de
	// um conjunto de rótulos para a mesma métrica durante um rollout de agente;
	// misturá-los num groupArray só ordenado por ts intercalaria as séries e inventaria
	// degraus positivos que nunca existiram.
	// A ordenação usa MILISSEGUNDOS (`ts` é DateTime64(3)). Ordenar por segundo deixa
	// empate — e empate no arraySort é ordem INDEFINIDA, que numa soma de degraus vira
	// avanço inventado: os mesmos pares, em ordens diferentes, dão 100 ou 88.000.
	//
	// `recuo` sai junto para distinguir os dois zeros: "não trafegou" (avanço 0, sem
	// recuo) de "o contador reiniciou e não sobrou passo positivo" (avanço 0, recuo < 0).
	// Sem ele, a janela cujo ÚLTIMO passo é o degrau — o instante seguinte a um reboot —
	// devolveria 0 B/s para um host que está transmitindo. Ver netRate.
	netRows, err := ch.QueryJSON(ctx, `
		SELECT host, metric, max(avanco) AS delta, argMax(span, avanco) AS secs,
		       argMax(recuo, avanco) AS recuo
		FROM (
			SELECT labels['host'] AS host, metric, labels AS lbl,
			       arrayDifference(arrayMap(p -> p.2, arraySort(p -> p.1,
			           groupArray((toUnixTimestamp64Milli(ts), value))))) AS passos,
			       arraySum(arrayFilter(x -> x > 0, passos)) AS avanco,
			       arraySum(arrayFilter(x -> x < 0, passos)) AS recuo,
			       toUnixTimestamp(max(ts)) - toUnixTimestamp(min(ts)) AS span
			FROM metrics
			WHERE metric IN ('system.network.io.bytes_sent','system.network.io.bytes_recv')
			  AND ts > now() - INTERVAL 5 MINUTE AND ts <= now()
			GROUP BY host, metric, lbl
		)
		GROUP BY host, metric`)
	if err != nil {
		return nil, fmt.Errorf("consulta de taxa de rede falhou: %w", err)
	}
	for _, row := range netRows {
		host, _ := row["host"].(string)
		metric, _ := row["metric"].(string)
		delta, okD := toFloat(row["delta"])
		secs, okS := toFloat(row["secs"])
		recuo, _ := toFloat(row["recuo"])
		if !okD || !okS {
			continue // sem avanço nem span do contador não existe taxa: 0 seria mentira
		}
		rate := netRate(delta, recuo, secs) // nil quando a janela não tem dois pontos usáveis
		if rate == nil {
			continue
		}
		a := get(host)
		switch metric {
		case "system.network.io.bytes_recv":
			a.rxBps = rate
		case "system.network.io.bytes_sent":
			a.txBps = rate
		}
	}

	// Containers por host (Fase C): último estado derivado na janela de 5 min.
	containers, err := BuildContainerStatuses(ctx, ch)
	if err != nil {
		return nil, err
	}

	out := make([]HostMetrics, 0, len(hosts))
	for _, hd := range hosts {
		a := agg[hd.Hostname]
		up := a != nil && a.has
		// Mesmo motivo do Mural (ver semMedida em inventory.go): o servidor mudo saía
		// com `pct:0, state:""` e a tela desenhava "0,0%" — zero silencioso no exato
		// caso em que ninguém mediu nada.
		hm := HostMetrics{Host: hd.Hostname, Up: up, Disks: []DiskMetric{},
			CPU: semMedida(), Mem: semMedida()}
		if cs := containers[hd.Hostname]; len(cs) > 0 {
			hm.Containers = cs
		}
		if !up {
			out = append(out, hm)
			continue
		}
		cpuW, cpuC := resolve(hd.Hostname, "cpu")
		memW, memC := resolve(hd.Hostname, "mem")
		hm.UptimeSecs = a.uptime
		if a.procs != nil {
			n := int64(*a.procs)
			hm.Procs = &n // só afirma um número quando a medida existe
		}
		hm.Net = NetRate{RxBps: a.rxBps, TxBps: a.txBps}
		cpuV, cpuAt := a.cpuExigida()
		hm.CPU = healthMetricFrom(cpuV, nil, nil, cpuW, cpuC, cpuAt)
		hm.Mem = healthMetricFrom(a.mem, a.memUsed, a.memTotal, memW, memC, a.memAt)
		if a.hasSwap() {
			swW, swC := resolve(hd.Hostname, "swap")
			sw := healthMetricFrom(a.swap, a.swapUsed, a.swapTotal, swW, swC, a.swapAt)
			hm.Swap = &sw
		}
		dskW, dskC := resolve(hd.Hostname, "disk")
		hm.Disks = disksFrom(a.mounts, dskW, dskC)
		out = append(out, hm)
	}
	return out, nil
}

// disksFrom converte o mapa de montagens numa lista ordenada por mount, com o
// semáforo aplicado a cada uma (limiares de disco resolvidos por host).
func disksFrom(mounts map[string]*mountFS, warn, crit float64) []DiskMetric {
	names := make([]string, 0, len(mounts))
	for name := range mounts {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]DiskMetric, 0, len(names))
	for _, name := range names {
		fs := mounts[name]
		m := healthMetricFrom(fs.Pct, fs.Used, fs.Total, warn, crit, fs.At)
		out = append(out, DiskMetric{
			Mount:    name,
			Pct:      m.Pct,
			Used:     m.Used,
			Total:    m.Total,
			State:    m.State,
			TS:       m.TS,
			StepSecs: m.StepSecs,
		})
	}
	return out
}

// netRate calcula a taxa (bytes/s) a partir do AVANÇO acumulado do contador na
// janela (soma dos degraus positivos, feita no ClickHouse) e do span em segundos.
//
// Devolve nil — não 0 — quando a taxa NÃO PODE ser calculada: span degenerado
// (secs <= 0, ou seja, um único ponto na janela) ou avanço negativo, que não deveria
// existir vindo de uma soma de positivos e, se existir, é dado corrompido e não uma
// medição. Zero seria uma afirmação ("não trafegou nada") sobre um host que está
// trafegando e apenas não deu para medir; nil é a lacuna, e a tela desenha travessão.
//
// Um contador que reiniciou NO MEIO da janela não é lacuna: o degrau negativo é
// descartado e o resto da janela continua valendo. Mas quando o degrau é o ÚLTIMO
// passo — o instante seguinte a um reboot — não sobra passo positivo nenhum, e o
// avanço é 0. Esse 0 NÃO é "não trafegou": é "o contador zerou e ainda não subiu".
// Publicá-lo como taxa era o zero silencioso de volta, com outra roupa; por isso o
// `recuo` entra na decisão em vez de só o avanço.
func netRate(delta, recuo, secs float64) *float64 {
	if secs <= 0 || delta < 0 {
		return nil
	}
	if delta == 0 && recuo < 0 {
		return nil // contador reiniciado sem avanço medido: lacuna, não 0 B/s
	}
	r := delta / secs
	return &r
}

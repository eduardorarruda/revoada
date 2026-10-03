// Package inventory expõe o inventário de hosts e a timeline de eventos (§3.11, §8.3).
package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/authz"
	"github.com/eduardorarruda/revoada/server/internal/chquery"
	"github.com/eduardorarruda/revoada/server/internal/health"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

type Handler struct {
	st *store.Store
	ch *chquery.Client
}

func NewHandler(st *store.Store, ch *chquery.Client) *Handler { return &Handler{st: st, ch: ch} }

// scopeFilter remove das listas os hosts que o usuário não pode ver. Admin/sem escopo
// (chamada de sistema) passa tudo. Devolve sempre slice não-nil (para JSON virar []).
func scopeFilter[T any](ctx context.Context, items []T, hostOf func(T) string) []T {
	scope, ok := authz.ScopeFrom(ctx)
	if !ok || scope.Admin {
		if items == nil {
			return []T{}
		}
		return items
	}
	out := make([]T, 0, len(items))
	for _, it := range items {
		if scope.CanView(hostOf(it)) {
			out = append(out, it)
		}
	}
	return out
}

// scopeCanView informa se o usuário do contexto pode ver aquele host (para endpoints de
// um host só, como a timeline). Admin/sem escopo → true.
func scopeCanView(ctx context.Context, host string) bool {
	scope, ok := authz.ScopeFrom(ctx)
	return !ok || scope.CanView(host)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Hosts lista o inventário (com busca por ?search=).
func (h *Handler) Hosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := h.st.HostsDetailed(r.Context(), r.URL.Query().Get("search"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	hosts = scopeFilter(r.Context(), hosts, func(h store.HostDetail) string { return h.Hostname })
	writeJSON(w, http.StatusOK, map[string]any{"hosts": hosts})
}

// UpdateHost renomeia um host: grava (ou limpa, com string vazia) o display_name.
// O hostname técnico vem do path e é a chave imutável — não muda aqui.
func (h *Handler) UpdateHost(w http.ResponseWriter, r *http.Request) {
	hostname := r.PathValue("hostname")
	if hostname == "" {
		http.Error(w, "hostname obrigatório", http.StatusBadRequest)
		return
	}
	var req struct {
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "corpo JSON inválido", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.DisplayName)
	if len(name) > 120 {
		http.Error(w, "nome amigável muito longo (máx. 120)", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := h.st.UpdateHostDisplayName(ctx, "default", hostname, name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// HealthMetric é a utilização de um recurso com significado: percentual, bytes
// absolutos (só mem/disco) e o estado de semáforo derivado dos limiares (Fase A).
//
// State == "nodata" significa que a métrica NÃO chegou na janela: nesse caso Pct é
// só o zero-value do JSON e não deve ser exibido como medida. Quem consome tem de
// olhar o State ANTES do Pct.
type HealthMetric struct {
	Pct   float64  `json:"pct"`                   // 0..100 utilização (só vale se State != "nodata")
	Used  *float64 `json:"used_bytes,omitempty"`  // bytes; só mem e disco; nil p/ cpu
	Total *float64 `json:"total_bytes,omitempty"` // bytes; só mem e disco; nil p/ cpu
	State string   `json:"state"`                 // "ok" | "warn" | "crit" | "nodata"
	// O NÚMERO SOZINHO NÃO CARREGA A IDADE.
	//
	// A janela de validade daqui era uma CONSTANTE de servidor (5 min em
	// /api/host-metrics, 2 min no Mural) e não aparecia na resposta. Efeito concreto:
	// o coletor de CPU para de emitir quando não consegue medir, mas RAM e disco
	// continuam chegando — então o host segue "no ar" e a célula de CPU repetia o
	// ÚLTIMO valor por até 5 minutos, pintada com a cor do limiar e sem dizer de
	// quando era. Um pico crítico de 90,7% já passado seguia vermelho na tela.
	//
	// TS é o instante (epoch em SEGUNDOS) da amostra que originou Pct; StepSecs é o
	// passo REAL observado na série na janela consultada (não uma constante). Com os
	// dois, o front aplica o mesmo frescor por ladrilho dos painéis (pointFreshness:
	// vale ~3× o passo) e mostra a IDADE em vez de repetir número velho como atual.
	// Ambos omitidos quando não houve medida (State == "nodata").
	TS       *int64   `json:"ts,omitempty"`
	StepSecs *float64 `json:"step_seconds,omitempty"`
}

// amostra é o metadado de amostragem de UM valor: quando ele foi medido e de quanto
// em quanto tempo aquela série vinha chegando. Anda ao lado do valor (que continua
// sendo *float64 para preservar a distinção ausência-vs-zero) porque a pergunta
// "este número ainda vale?" não é respondível só pelo número.
type amostra struct {
	TS   int64   // epoch em segundos da amostra mais recente
	Step float64 // passo médio observado na janela (0 = indeterminado, um ponto só)
}

// amostraDe lê o metadado de amostragem de uma linha do ClickHouse. Espera as
// colunas `at` (toUnixTimestamp do ts mais novo), `n` (nº de instantes distintos) e
// `span` (segundos entre o mais antigo e o mais novo). Com um ponto só o passo fica
// indeterminado (0) e o front cai no piso de validade — nunca inventamos cadência.
func amostraDe(row map[string]any) *amostra {
	at, ok := toFloat(row["at"])
	if !ok || at <= 0 {
		return nil
	}
	a := &amostra{TS: int64(at)}
	n, okN := toFloat(row["n"])
	span, okS := toFloat(row["span"])
	if okN && okS && n >= 2 && span > 0 {
		a.Step = span / (n - 1)
	}
	return a
}

// healthMetricFrom monta um HealthMetric a partir de valores que podem NÃO ter
// chegado (ponteiro nil = métrica ausente na janela).
//
// Antes desta função, os agregadores usavam structs de campos float64 com
// zero-value: se `system.cpu.utilization` faltasse enquanto o resto do host seguia
// reportando, o card mostrava "CPU 0% — verde", ou seja, "servidor ocioso" onde a
// verdade era "ninguém está medindo". Métrica ausente agora vira estado "nodata",
// que o front pode desenhar como "—".
// semMedida é o HealthMetric de quem não foi medido. Existe para que "sem dado"
// tenha de ser escrito de propósito: o zero-value do struct dá `state:""`, que não
// é nenhum dos quatro estados do contrato e a tela desenha como 0%.
func semMedida() HealthMetric { return HealthMetric{State: health.StateNoData} }

// `at` é o metadado de amostragem do PRÓPRIO pct (quando foi medido e com que
// passo). Vai junto para a tela poder dizer a idade do número; nil quando a métrica
// não chegou, e nesse caso não há o que datar.
func healthMetricFrom(pct, used, total *float64, warn, crit float64, at *amostra) HealthMetric {
	if pct == nil {
		return HealthMetric{Used: used, Total: total, State: health.StateNoData}
	}
	m := HealthMetric{Pct: *pct, Used: used, Total: total, State: health.Classify(*pct, warn, crit)}
	// NaN/Inf caem em "nodata" dentro do Classify: datar um não-valor só daria
	// aparência de medida ao que não é medida.
	if at != nil && m.State != health.StateNoData {
		ts := at.TS
		m.TS = &ts
		if at.Step > 0 {
			step := at.Step
			m.StepSecs = &step
		}
	}
	return m
}

// HealthCard é o estado de saúde de um host para o Health Wall. O State geral é o
// pior entre os semáforos de recurso e a severidade do alerta ativo (Fase A).
type HealthCard struct {
	Host  string       `json:"host"`
	State string       `json:"state"` // ok | warn | crit | nosignal
	CPU   HealthMetric `json:"cpu"`
	Mem   HealthMetric `json:"mem"`
	Disk  HealthMetric `json:"disk"`
	Up    bool         `json:"up"`
	// Containers é a lista de containers Docker do host (Fase C), com o MESMO shape
	// exposto em /api/host-metrics (o tipo ContainerStatus é compartilhado). Assim o
	// Mural e a tela Hosts consomem o mesmo contrato de container sem divergir. Omitida
	// (omitempty) quando o host não reporta nenhum container na janela de 5 min.
	Containers []ContainerStatus `json:"containers,omitempty"`
}

// stateRank ordena "problemas primeiro".
func stateRank(s string) int {
	switch s {
	case "crit":
		return 0
	case "nosignal":
		return 1
	case "warn":
		return 2
	default:
		return 3
	}
}

// HealthWall devolve os cartões de saúde por host, ordenados (problemas no topo).
func (h *Handler) HealthWall(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	cards, err := BuildWallCards(ctx, h.st, h.ch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cards = scopeFilter(ctx, cards, func(c HealthCard) string { return c.Host })
	writeJSON(w, http.StatusOK, map[string]any{"cards": cards})
}

// ListHostThresholds devolve os limiares de saúde persistidos (sem os defaults
// embutidos). Admin — a UI de configuração de limiares consome isto.
func (h *Handler) ListHostThresholds(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	ths, err := h.st.ListHostThresholds(ctx, "default")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if ths == nil {
		ths = []store.HostThreshold{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"thresholds": ths})
}

// UpdateHostThresholds substitui todo o conjunto de limiares (apaga e reinsere).
// Valida metric ∈ {cpu,mem,disk} e 0 <= warn < crit <= 100.
func (h *Handler) UpdateHostThresholds(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Thresholds []store.HostThreshold `json:"thresholds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "corpo JSON inválido", http.StatusBadRequest)
		return
	}
	seen := map[string]bool{}
	for _, t := range req.Thresholds {
		if !validMetric(t.Metric) {
			http.Error(w, "métrica inválida (use cpu, mem, disk ou swap): "+t.Metric, http.StatusBadRequest)
			return
		}
		if !(t.Warn >= 0 && t.Warn < t.Crit && t.Crit <= 100) {
			http.Error(w, "limiares inválidos: exija 0 <= warn < crit <= 100", http.StatusBadRequest)
			return
		}
		// (hostname, metric) é PK — duplicata estouraria a transação com erro cru
		// do Postgres; melhor recusar aqui com mensagem clara.
		k := t.Hostname + "\x00" + t.Metric
		if seen[k] {
			http.Error(w, "limiar duplicado para o mesmo host/métrica: "+t.Hostname+"/"+t.Metric, http.StatusBadRequest)
			return
		}
		seen[k] = true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := h.st.ReplaceHostThresholds(ctx, "default", req.Thresholds); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// validMetric aceita apenas as métricas com semáforo (swap incluída: o backend a
// resolve em /api/host-metrics, então o override precisa ser configurável).
func validMetric(m string) bool {
	switch m {
	case "cpu", "mem", "disk", "swap":
		return true
	}
	return false
}

// BuildWallCards monta os cartões de saúde por host (hosts + últimos valores de
// cpu/mem/disco + severidade do pior alerta ativo), ordenados com os problemas no
// topo. Compartilhado entre o endpoint autenticado (/api/health-wall) e o endpoint
// por token da TV (/api/tv/wall) — por isso recebe store/ch em vez de usar o Handler.
func BuildWallCards(ctx context.Context, st *store.Store, ch *chquery.Client) ([]HealthCard, error) {
	hosts, err := st.HostsDetailed(ctx, "")
	if err != nil {
		return nil, err
	}

	// Limiares resolvidos (host-específico > global > default embutido). Compartilhado
	// pela TV e pelo Wall — por isso é resolvido aqui dentro a partir do store.
	resolve, err := st.ResolvedThresholds(ctx, "default")
	if err != nil {
		return nil, err
	}

	// Últimos valores por host (2 min): percentuais + bytes used/total de mem e disco.
	// Janela ALINHADA com o "up" da Infraestrutura (hosts.last_seen < 2 min, ver
	// store.HostsDetailed) — antes eram 5 min aqui e 2 min lá, então um host caído há
	// 3 min aparecia verde no Mural e vermelho na Infra ao mesmo tempo. Mesma janela
	// nos dois caminhos = mesmo veredito de "no ar".
	// Disco agrupado por montagem (label 'mount'), com COALESCE para "/" quando o
	// agente antigo não envia o label — nesse caso há um único filesystem, o root.
	// Erro de consulta NÃO pode virar "todos os hosts sem sinal" com 200 OK — o
	// operador leria "todos os agentes caíram" quando quem caiu foi o ClickHouse.
	// `at`/`n`/`span` acompanham o valor para a resposta poder DATAR cada número (ver
	// HealthMetric.TS): a janela de 2 min continua limitando a busca, mas quem decide
	// se o valor ainda vale é o frescor calculado sobre o passo real da série, não uma
	// constante escondida no servidor.
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
			'system.filesystem.utilization','system.filesystem.used','system.filesystem.total')
		  AND ts > now() - INTERVAL 2 MINUTE AND ts <= now()
		GROUP BY host, metric, mount`)
	if err != nil {
		return nil, fmt.Errorf("consulta de métricas do mural falhou: %w", err)
	}
	// O disco do card = o mount de MAIOR pct; used/total vêm do MESMO mount (pareados),
	// não mais soltos. cpu/mem ignoram o mount (têm um único valor por host).
	//
	// Os campos são PONTEIROS de propósito: nil = a métrica não chegou nesta janela,
	// e isso é uma informação diferente de "chegou valendo zero" (ver healthMetricFrom).
	// cpuTotal/cpuLegado: mesma resolução do cartão de host (ver hostAgg.cpuExigida).
	// O mural mostra a CPU EXIGIDA — consumo mais o tempo que o hipervisor negou —,
	// e a fonte depende da versão do agente: 0.8.1+ manda `.total`, os anteriores
	// mandam `utilization` já com o roubo dentro. São o mesmo número; a reserva
	// existe para a frota mista do rollout não ficar sem semáforo de CPU.
	type vals struct {
		cpuTotal, cpuLegado, cpuSteal *float64
		cpuTotalAt, cpuLegadoAt       *amostra
		mem, memUsed, memTotal        *float64
		memAt                         *amostra // quando a mem foi medida (ver HealthMetric.TS)
		mounts                        map[string]*mountFS
		has                           bool
	}
	latest := map[string]*vals{}
	for _, row := range rows {
		host, _ := row["host"].(string)
		metric, _ := row["metric"].(string)
		mount, _ := row["mount"].(string)
		v, ok := toFloat(row["v"])
		if !ok {
			continue // nulo/NaN/Inf não é medida: a métrica segue "sem dado"
		}
		if latest[host] == nil {
			latest[host] = &vals{mounts: map[string]*mountFS{}}
		}
		latest[host].has = true
		at := amostraDe(row)
		switch metric {
		case "system.cpu.utilization.total":
			latest[host].cpuTotal = ptr(v)
			latest[host].cpuTotalAt = at
		case "system.cpu.utilization":
			latest[host].cpuLegado = ptr(v)
			latest[host].cpuLegadoAt = at
		case "system.cpu.steal":
			latest[host].cpuSteal = ptr(v)
		case "system.memory.utilization":
			latest[host].mem = ptr(v)
			latest[host].memAt = at
		case "system.memory.used":
			latest[host].memUsed = ptr(v)
		case "system.memory.total":
			latest[host].memTotal = ptr(v)
		case "system.filesystem.utilization":
			fs := mountOf(latest[host].mounts, mount)
			fs.Pct = ptr(v)
			fs.At = at
		case "system.filesystem.used":
			mountOf(latest[host].mounts, mount).Used = ptr(v)
		case "system.filesystem.total":
			mountOf(latest[host].mounts, mount).Total = ptr(v)
		}
	}

	// Severidade do pior alerta ativo por host — combinada (não substituída) com os
	// limiares de recurso, para todo host ganhar semáforo mesmo sem regra de alerta.
	sevByHost := map[string]string{}
	if alerts, err := st.ListAlerts(ctx, true); err == nil {
		for _, a := range alerts {
			host := a.Labels["host"]
			if host == "" {
				host = a.Labels["hostname"]
			}
			if host == "" {
				continue
			}
			if sevRank(a.Severity) > sevRank(sevByHost[host]) {
				sevByHost[host] = a.Severity
			}
		}
	}

	// Containers por host (Fase C): mesmo estado derivado que /api/host-metrics usa,
	// para o Mural exibir os contêineres de cada servidor sem uma segunda fonte. Uma
	// única consulta agregada para todos os hosts; o mapa é indexado por hostname e
	// anexado abaixo (host sem contêineres simplesmente não aparece no mapa → vazio).
	containers, err := BuildContainerStatuses(ctx, ch)
	if err != nil {
		return nil, err
	}

	cards := make([]HealthCard, 0, len(hosts))
	for _, hd := range hosts {
		v := latest[hd.Hostname]
		hasData := v != nil && v.has
		// O host SEM sinal é o caso que mais precisa de `nodata`, e era justamente o
		// que não recebia: sem medida, os três HealthMetric saíam no zero-value
		// (`pct:0, state:""`) — um estado que nem sequer existe no contrato do tipo.
		// A tela lê `state === "nodata"` para escrever "—"; com "" ela caía no ramo
		// normal e desenhava "0,0%" para um servidor mudo, que é exatamente o zero
		// silencioso que este campo existe para matar. Nasce `nodata` e só vira
		// medida se houver medida.
		c := HealthCard{Host: hd.Hostname, Up: hasData,
			CPU:  semMedida(),
			Mem:  semMedida(),
			Disk: semMedida()}
		// Anexa os contêineres do host (mapeados por hostname, igual a BuildHostMetrics).
		// Independe de hasData: um host "sem sinal" de CPU/mem pode ainda ter o último
		// estado de contêiner na janela — e, não havendo, fica omitido pelo omitempty.
		if cs := containers[hd.Hostname]; len(cs) > 0 {
			c.Containers = cs
		}
		if hasData {
			cpuW, cpuC := resolve(hd.Hostname, "cpu")
			memW, memC := resolve(hd.Hostname, "mem")
			dskW, dskC := resolve(hd.Hostname, "disk")
			// Mesma resolução de hostAgg.cpuExigida: o steal é a testemunha da era do
			// agente. Sem ele, o legado JÁ é a soma; com ele, o legado é só a parcela
			// consumida e a soma precisa ser reconstruída — publicar a parcela sob o
			// rótulo da soma subestima a máquina na medida exata do roubo.
			cpuV, cpuAt := v.cpuTotal, v.cpuTotalAt
			if cpuV == nil {
				cpuV, cpuAt = v.cpuLegado, v.cpuLegadoAt
				if v.cpuSteal != nil && v.cpuLegado != nil {
					soma := *v.cpuLegado + *v.cpuSteal
					cpuV = &soma
				}
			}
			c.CPU = healthMetricFrom(cpuV, nil, nil, cpuW, cpuC, cpuAt)
			c.Mem = healthMetricFrom(v.mem, v.memUsed, v.memTotal, memW, memC, v.memAt)
			// Disco = o mount de maior pct; used/total pareados a esse mesmo mount.
			d := worstMount(v.mounts)
			c.Disk = healthMetricFrom(d.Pct, d.Used, d.Total, dskW, dskC, d.At)
		}
		// "sem sinal" = sem métrica recente (5 min); senão o pior entre limiares de
		// recurso e alerta ativo. Corrige o "verde a 95% sem regra de alerta".
		c.State = cardState(hasData, c.CPU.State, c.Mem.State, c.Disk.State, sevByHost[hd.Hostname])
		cards = append(cards, c)
	}
	sort.SliceStable(cards, func(i, j int) bool { return stateRank(cards[i].State) < stateRank(cards[j].State) })
	return cards, nil
}

// ptr devolve um ponteiro para o float (helper para os campos opcionais de bytes).
func ptr(f float64) *float64 { return &f }

// mountFS é o estado de um filesystem (uma montagem): percentual + bytes pareados.
// Campos ponteiro: nil = aquela métrica não chegou para esta montagem (um disco sem
// utilization reportada não é um disco 0% vazio).
type mountFS struct {
	Mount string
	Pct   *float64
	Used  *float64
	Total *float64
	// At data o Pct (ver HealthMetric.TS): sem isso, um disco medido há 4 min saía
	// com a mesma cara de um medido há 5 s.
	At *amostra
}

// mountOf devolve (criando se preciso) o mountFS de um mount no mapa — garante que
// utilization/used/total do mesmo mount fiquem pareados na mesma struct.
func mountOf(m map[string]*mountFS, mount string) *mountFS {
	if m[mount] == nil {
		m[mount] = &mountFS{Mount: mount}
	}
	return m[mount]
}

// worstMount escolhe o filesystem de MAIOR pct entre as montagens de um host; os
// bytes used/total devolvidos são os do mesmo mount escolhido (pareamento correto).
// Montagens SEM utilization reportada são ignoradas na escolha — e, se nenhuma tiver
// utilization, devolve o zero-value com Pct nil, que vira "sem dado" no card (e não
// um disco milagrosamente vazio). Empate: mantém o primeiro mais alto encontrado;
// ordem estável pelo nome do mount para determinismo.
func worstMount(mounts map[string]*mountFS) mountFS {
	names := make([]string, 0, len(mounts))
	for name := range mounts {
		names = append(names, name)
	}
	sort.Strings(names)
	var worst mountFS
	found := false
	for _, name := range names {
		fs := mounts[name]
		if fs.Pct == nil {
			continue
		}
		if !found || *fs.Pct > *worst.Pct {
			worst = *fs
			found = true
		}
	}
	return worst
}

// alertState mapeia a severidade do alerta (critical/warning/…) para o vocabulário
// de semáforo (crit/warn/ok).
func alertState(severity string) string {
	switch severity {
	case "critical":
		return health.StateCrit
	case "warning":
		return health.StateWarn
	default:
		return health.StateOK
	}
}

// cardState combina o estado por recurso com o alerta ativo. Sem dados recentes =
// "nosignal"; senão o pior entre os semáforos de cpu/mem/disco e o alerta ativo.
func cardState(up bool, cpuState, memState, diskState, severity string) string {
	if !up {
		return "nosignal"
	}
	return health.Worst(cpuState, memState, diskState, alertState(severity))
}

func sevRank(s string) int {
	switch s {
	case "critical":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	}
	return 0
}

// toFloat converte o valor de uma linha do ClickHouse e diz se ele é MEDIDA.
//
// Devolve ok=false para tudo que não é número finito: JSON null (o ClickHouse
// serializa NaN/±Inf assim por padrão), "nan"/"inf" em texto e tipos inesperados.
// Antes isso virava 0 em silêncio, e 0 numa utilização não é um valor neutro: é a
// afirmação "este recurso está livre", pintada de verde no mural. Sem medida, o
// caminho certo é "sem dado".
func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, isFinite(x)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return 0, false
		}
		return f, isFinite(f) // ParseFloat aceita "NaN"/"Inf"; aqui eles são barrados
	}
	return 0, false
}

// isFinite barra NaN e ±Inf — os dois valores que passam por número e não descrevem
// medição nenhuma. Sem esta guarda, um NaN escorregava até health.Classify e, como
// toda comparação com NaN é falsa, terminava classificado como "ok" verde.
func isFinite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// Deploy registra uma anotação de deploy (§6.2). Autenticado por service account
// (token de deploy), não por JWT de usuário — chamado pelo CI após deploy ok.
func (h *Handler) Deploy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Service     string `json:"service"`
		Version     string `json:"version"`
		Environment string `json:"environment"`
		Host        string `json:"host"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Service == "" || req.Version == "" {
		http.Error(w, "service e version obrigatórios", http.StatusBadRequest)
		return
	}
	title := "deploy " + req.Service + " " + req.Version
	if req.Environment != "" {
		title += " (" + req.Environment + ")"
	}
	labels := map[string]string{"service": req.Service, "version": req.Version}
	if req.Environment != "" {
		labels["environment"] = req.Environment
	}
	if req.Host != "" {
		labels["host"] = req.Host
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := h.ch.InsertEvent(ctx, "default", "deploy", title, req.Description, labels); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "title": title})
}

// Event é uma entrada da timeline.
type Event struct {
	Kind  string `json:"kind"`
	TS    string `json:"ts"`
	Title string `json:"title"`
}

// chTimeToRFC3339 converte o timestamp "puro" do ClickHouse (UTC, sem marcador de
// fuso — "2006-01-02 15:04:05[.fff]") em RFC3339 com offset explícito. Sem isso, o
// front interpretava a string como hora LOCAL e mostrava os eventos deslocados
// (ex.: "em 3h" no futuro para quem está em UTC-3). Com o offset, o navegador
// converte corretamente para o fuso do usuário.
func chTimeToRFC3339(s string) string {
	for _, layout := range []string{"2006-01-02 15:04:05.999", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return s
}

// Timeline devolve os eventos recentes de um host (tabela events do ClickHouse).
func (h *Handler) Timeline(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Query().Get("host")
	if host == "" {
		http.Error(w, "host obrigatório", http.StatusBadRequest)
		return
	}
	// Enforcement: usuário só vê a timeline de hosts que pode ver.
	if !scopeCanView(r.Context(), host) {
		writeJSON(w, http.StatusOK, map[string]any{"events": []Event{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	esc := strings.NewReplacer("\\", "\\\\", "'", "\\'").Replace(host)
	sql := fmt.Sprintf(
		"SELECT kind, toString(ts) AS ts, title FROM events WHERE labels['host'] = '%s' ORDER BY ts DESC LIMIT 200", esc)
	rows, err := h.ch.QueryJSON(ctx, sql)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := []Event{}
	for _, row := range rows {
		e := Event{}
		if s, ok := row["kind"].(string); ok {
			e.Kind = s
		}
		if s, ok := row["ts"].(string); ok {
			e.TS = chTimeToRFC3339(s)
		}
		if s, ok := row["title"].(string); ok {
			e.Title = s
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

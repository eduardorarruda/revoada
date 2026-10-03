// Package dashboards: CRUD, versionamento e starter packs de dashboards.
package dashboards

import (
	"encoding/json"
	"fmt"
)

// GridPos posiciona um painel na grade (24 colunas).
type GridPos struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// PanelQuery é a consulta de um painel (casa com a Query API).
type PanelQuery struct {
	Metric  string            `json:"metric"`
	Filters map[string]string `json:"filters,omitempty"`
	GroupBy []string          `json:"group_by,omitempty"` // chaves de label para agrupar (ex.: ["host"])
	Agg     string            `json:"agg,omitempty"`
}

// Panel é um painel do dashboard.
type Panel struct {
	ID          int        `json:"id"`
	Type        string     `json:"type"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	GridPos     GridPos    `json:"gridPos"`
	Query       PanelQuery `json:"query"`
}

// Variable é uma variável de dashboard.
type Variable struct {
	Name    string `json:"name"`
	Current string `json:"current"`
}

// TimeRange é a janela padrão do dashboard.
type TimeRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Model é o modelo JSON de um dashboard (documentado em docs/dashboard-schema.md).
type Model struct {
	UID       string     `json:"uid"`
	Title     string     `json:"title"`
	Variables []Variable `json:"variables"`
	TimeRange TimeRange  `json:"timeRange"`
	Panels    []Panel    `json:"panels"`
}

// GenericHostUID é o uid do dashboard de fábrica "Visão do Host (genérico)": um
// único painel reaproveitável por TODOS os servidores, onde o usuário só escolhe o
// servidor num seletor (variável $host). É semeado no boot do server (ensureGeneric
// em main.go) e é o alvo dos deep links das notificações (?var-host=<host>).
const GenericHostUID = "host-visao-geral"

// HostVarPlaceholder é o valor-sentinela que o visualizador substitui pelo servidor
// selecionado. Painéis do dashboard genérico filtram por {"host": "$host"}; o front
// (DashboardView) troca "$host" pelo host escolhido antes de consultar a Query API.
const HostVarPlaceholder = "$host"

// hostPanels monta os painéis padrão da "Visão do Host". `f` é o filtro de host
// aplicado a cada painel: {"host": "<host>"} no dashboard por-servidor, ou
// {"host": "$host"} no dashboard genérico (substituído no cliente pelo servidor
// escolhido). Fonte única para os dois dashboards — mantê-los sempre em sincronia.
func hostPanels(f map[string]string) []Panel {
	return []Panel{
		// CPU do painel = a EXIGIDA (consumo + tempo negado pelo hipervisor). Num VPS
		// estrangulado o consumo sozinho engana: medido no srv-02, 3,8% de consumo
		// com 12,7% de roubo — o gráfico diria "ocioso" sobre uma máquina que passa um
		// sexto do tempo esperando vez. Agentes anteriores à 0.8.1 não publicam esta
		// série (neles a `utilization` já é a soma); o painel fica vazio até o host
		// atualizar, o que é preferível a desenhar duas semânticas na mesma linha.
		{ID: 1, Type: "timeseries", Title: "CPU (%)", Description: "Uso de CPU do servidor inteiro: consumo mais o tempo que o hipervisor negou. Normal < 70%.",
			GridPos: GridPos{X: 0, Y: 0, W: 12, H: 8}, Query: PanelQuery{Metric: "system.cpu.utilization.total", Filters: f, Agg: "avg"}},
		{ID: 8, Type: "timeseries", Title: "CPU roubada pelo hipervisor (%)", Description: "Tempo em que a máquina pediu CPU e o provedor entregou a outro cliente. Acima de ~5% sustentado, a lentidão não é da sua aplicação.",
			GridPos: GridPos{X: 12, Y: 16, W: 12, H: 8}, Query: PanelQuery{Metric: "system.cpu.steal", Filters: f, Agg: "avg"}},
		{ID: 2, Type: "timeseries", Title: "Memória (%)", Description: "RAM usada. Alerta ≥ 85%.",
			GridPos: GridPos{X: 12, Y: 0, W: 12, H: 8}, Query: PanelQuery{Metric: "system.memory.utilization", Filters: f, Agg: "avg"}},
		{ID: 3, Type: "timeseries", Title: "Disco por ponto de montagem (%)", Description: "Uso de cada filesystem. Uma série por ponto de montagem.",
			GridPos: GridPos{X: 0, Y: 8, W: 12, H: 8}, Query: PanelQuery{Metric: "system.filesystem.utilization", Filters: f, GroupBy: []string{"mount"}, Agg: "max"}},
		// Taxa, não o acumulado: `bytes_recv` é um contador que só cresce, e plotá-lo
		// direto desenhava uma reta subindo até 10¹² — bonita e inútil, porque não
		// respondia "quanto de rede este servidor usou às 3h".
		{ID: 4, Type: "timeseries", Title: "Rede, recebido (bytes/s)", Description: "Taxa de recepção: quanto o contador andou dentro de cada intervalo.",
			GridPos: GridPos{X: 12, Y: 8, W: 12, H: 8}, Query: PanelQuery{Metric: "system.network.io.bytes_recv", Filters: f, Agg: "rate"}},
		{ID: 5, Type: "timeseries", Title: "Swap (%)", Description: "Uso de swap (paginação). Ideal perto de 0%, swap alto indica falta de RAM. Vazio quando o host não tem swap.",
			GridPos: GridPos{X: 0, Y: 16, W: 12, H: 8}, Query: PanelQuery{Metric: "system.paging.utilization", Filters: f, Agg: "avg"}},
		{ID: 6, Type: "stat", Title: "Uptime (s)", Description: "Tempo desde o último boot.",
			GridPos: GridPos{X: 12, Y: 16, W: 6, H: 8}, Query: PanelQuery{Metric: "system.uptime", Filters: f, Agg: "max"}},
		{ID: 7, Type: "stat", Title: "Processos", Description: "Número de processos em execução no host.",
			GridPos: GridPos{X: 18, Y: 16, W: 6, H: 8}, Query: PanelQuery{Metric: "system.processes.count", Filters: f, Agg: "max"}},
	}
}

// StarterHost gera o dashboard de fábrica "Visão do Host" para um host específico
// (filtros já fixados no host).
// `label` é o nome amigável (display_name) exibido no TÍTULO; quando vazio, cai no
// hostname técnico. O UID, a variável e os filtros continuam com o hostname técnico
// (chave das métricas) — só o título muda.
func StarterHost(host, label string) Model {
	if label == "" {
		label = host
	}
	return Model{
		UID:       "host-" + host,
		Title:     "Visão do Host, " + label,
		Variables: []Variable{{Name: "host", Current: host}},
		TimeRange: TimeRange{From: "now-24h", To: "now"},
		Panels:    hostPanels(map[string]string{"host": host}),
	}
}

// StarterHostGeneric gera o dashboard de fábrica "Visão do Host (genérico)": um só
// dashboard, com a variável $host, servindo qualquer servidor. O usuário não cria
// nada à mão — o server semeia este dashboard no boot e o usuário só seleciona o
// servidor no topo da tela. Reaproveita exatamente os painéis de StarterHost.
func StarterHostGeneric() Model {
	return Model{
		UID:       GenericHostUID,
		Title:     "Visão do Host (genérico)",
		Variables: []Variable{{Name: "host", Current: ""}},
		TimeRange: TimeRange{From: "now-24h", To: "now"},
		Panels:    hostPanels(map[string]string{"host": HostVarPlaceholder}),
	}
}

// StarterHostGenericJSON devolve o modelo genérico serializado.
func StarterHostGenericJSON() (json.RawMessage, error) {
	b, err := json.Marshal(StarterHostGeneric())
	if err != nil {
		return nil, fmt.Errorf("serializando starter genérico: %w", err)
	}
	return b, nil
}

// servicePanels descreve os painéis específicos de cada serviço conhecido.
// Métrica dedicada (ex: mysql.*) + contexto de host. Ponto de partida a refinar.
var servicePanels = map[string]struct {
	label   string
	metrics [2]string // [0]=principal do serviço, [1]=secundária
	descs   [2]string
}{
	"mysql":    {"MySQL", [2]string{"mysql.connections", "mysql.queries.rate"}, [2]string{"Conexões ativas.", "Queries por segundo."}},
	"postgres": {"PostgreSQL", [2]string{"postgres.connections", "postgres.transactions.rate"}, [2]string{"Conexões ativas.", "Transações por segundo."}},
	"redis":    {"Redis", [2]string{"redis.connected_clients", "redis.ops.rate"}, [2]string{"Clientes conectados.", "Operações por segundo."}},
	"nginx":    {"Nginx", [2]string{"nginx.requests.rate", "nginx.connections.active"}, [2]string{"Requisições por segundo.", "Conexões ativas."}},
	"mongodb":  {"MongoDB", [2]string{"mongodb.connections", "mongodb.ops.rate"}, [2]string{"Conexões.", "Operações por segundo."}},
}

// SupportedServicePack diz se há um starter pack para o serviço.
func SupportedServicePack(kind string) bool {
	_, ok := servicePanels[kind]
	return ok
}

// ServicePack gera um dashboard starter para um serviço num host. Combina a métrica
// do serviço (de um exporter) com CPU/RAM do host, filtrados pelo host.
func ServicePack(host, kind string) (Model, bool) {
	sp, ok := servicePanels[kind]
	if !ok {
		return Model{}, false
	}
	f := map[string]string{"host": host}
	return Model{
		UID:       "svc-" + kind + "-" + host,
		Title:     sp.label + ", " + host,
		Variables: []Variable{{Name: "host", Current: host}},
		TimeRange: TimeRange{From: "now-6h", To: "now"},
		Panels: []Panel{
			{ID: 1, Type: "timeseries", Title: sp.label + ", " + sp.metrics[0], Description: sp.descs[0],
				GridPos: GridPos{X: 0, Y: 0, W: 12, H: 8}, Query: PanelQuery{Metric: sp.metrics[0], Filters: f, Agg: "avg"}},
			{ID: 2, Type: "timeseries", Title: sp.label + ", " + sp.metrics[1], Description: sp.descs[1],
				GridPos: GridPos{X: 12, Y: 0, W: 12, H: 8}, Query: PanelQuery{Metric: sp.metrics[1], Filters: f, Agg: "avg"}},
			{ID: 3, Type: "timeseries", Title: "CPU do host (%)", Description: "Uso de CPU do host que roda o serviço: consumo mais o tempo que o hipervisor negou.",
				GridPos: GridPos{X: 0, Y: 8, W: 12, H: 8}, Query: PanelQuery{Metric: "system.cpu.utilization.total", Filters: f, Agg: "avg"}},
			{ID: 4, Type: "timeseries", Title: "Memória do host (%)", Description: "RAM do host.",
				GridPos: GridPos{X: 12, Y: 8, W: 12, H: 8}, Query: PanelQuery{Metric: "system.memory.utilization", Filters: f, Agg: "avg"}},
		},
	}, true
}

// ServicePackJSON devolve o starter de serviço serializado.
func ServicePackJSON(host, kind string) (json.RawMessage, string, string, bool) {
	m, ok := ServicePack(host, kind)
	if !ok {
		return nil, "", "", false
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, "", "", false
	}
	return b, m.UID, m.Title, true
}

// StarterHostJSON devolve o modelo serializado. `label` é o nome amigável para o
// título (vazio = usa o hostname técnico).
func StarterHostJSON(host, label string) (json.RawMessage, error) {
	b, err := json.Marshal(StarterHost(host, label))
	if err != nil {
		return nil, fmt.Errorf("serializando starter: %w", err)
	}
	return b, nil
}

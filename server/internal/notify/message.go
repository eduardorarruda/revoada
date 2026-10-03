// Package notify entrega notificações de alerta a contact points (SMTP, webhook,
// Telegram), com roteamento por severidade/labels, agrupamento e auditoria.
package notify

import (
	"fmt"
	"html"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/alerting"
)

// AlertItem é um alerta individual dentro de uma notificação agrupada.
type AlertItem struct {
	RuleName     string            `json:"rule"`
	Metric       string            `json:"metric"`
	State        string            `json:"state"` // firing | resolved | nodata
	Severity     string            `json:"severity"`
	Condition    string            `json:"condition"`    // ex: "avg > 90" (mantido no JSON do webhook)
	Op           string            `json:"op,omitempty"` // operador da regra (>, <, >=, <=) p/ frasear o limite
	Threshold    float64           `json:"threshold"`    // limiar da regra p/ frasear o limite
	Value        float64           `json:"value"`
	Labels       map[string]string `json:"labels"`
	Runbook      string            `json:"runbook,omitempty"`
	DashboardURL string            `json:"dashboard_url,omitempty"`
	Since        time.Time         `json:"-"`
	Duration     string            `json:"duration,omitempty"` // preenchido em resolved e nodata
	Flapping     bool              `json:"flapping,omitempty"`
	// SuppressedCount é quantas transições deste alerta o amortecedor de oscilação
	// engoliu na última hora. Aparece no texto porque a supressão precisa ser
	// VISÍVEL: antes, o operador recebia um "🔁 OSCILANDO" e nunca mais ouvia falar
	// do incidente — nem quando ele terminava.
	SuppressedCount int `json:"suppressed_count,omitempty"`
	// Evidence é a frase que diz COMO a conclusão foi medida ("2 tentativas seguidas
	// do nosso teste falharam — a camada segura (TLS) não completou em 10 s"). Sem
	// ela, o aviso é uma afirmação que o destinatário não tem como conferir: quem
	// abriu o endereço no celular e viu a página carregar responde "errado", e com
	// razão, porque nada no aviso explicava de onde e como o painel mediu.
	Evidence string `json:"evidence,omitempty"`
	// HostDisplay é o nome amigável (display_name) do servidor, resolvido na montagem
	// do item. Vazio quando não há alias — aí exibimos o hostname técnico. Afeta só a
	// EXIBIÇÃO: os deep links continuam com o hostname técnico (chave das métricas).
	HostDisplay string `json:"-"`
}

// hostText devolve o nome do servidor para EXIBIÇÃO: o nome amigável quando resolvido,
// senão o hostname técnico dos labels. Não altera links nem a chave das métricas.
func (it AlertItem) hostText() string {
	if it.HostDisplay != "" {
		return it.HostDisplay
	}
	if h := it.Labels["host"]; h != "" {
		return h
	}
	return it.Labels["hostname"]
}

// Message é o resultado renderizado de um grupo, pronto para os senders.
type Message struct {
	Subject string      `json:"subject"`
	Text    string      `json:"text"`           // corpo em texto puro (Telegram/webhook e fallback do e-mail)
	HTML    string      `json:"html,omitempty"` // corpo HTML (e-mail)
	State   string      `json:"state"`          // firing | resolved
	Alerts  []AlertItem `json:"alerts"`
}

// noiseLabels são labels técnicos que não ajudam o leitor humano (inventário do
// agente/host). São escondidos das notificações — o essencial (servidor, container,
// site) aparece em linhas próprias e legíveis.
var noiseLabels = map[string]bool{
	"agent.version": true, "arch": true, "kernel": true, "os": true,
	"platform": true, "host.cpu.cores": true, "host.name": true,
}

// structuralLabels são labels que já viram uma linha própria (Servidor/Site/…) e
// portanto não se repetem na linha de "Detalhes".
var structuralLabels = map[string]bool{
	"host": true, "container": true, "site": true, "url": true,
	"diagnóstico": true, "image": true, "state": true, "health": true, "exit_code": true,
}

// sevPT traduz a severidade para uma palavra em pt-BR.
func sevPT(sev string) string {
	switch strings.ToLower(sev) {
	case "crit", "critical", "critico", "crítico", "emergency":
		return "CRÍTICO"
	case "warning", "warn", "aviso":
		return "ALERTA"
	case "info", "information":
		return "INFO"
	default:
		return strings.ToUpper(sev)
	}
}

// stateEmoji dá um sinal visual rápido (vermelho/laranja/azul p/ disparo, verde p/
// resolvido) — ajuda muito a leitura no WhatsApp/Telegram.
//
// `nodata` ganha símbolo PRÓPRIO, e nunca o ✅. O verde é a única coisa que muita
// gente lê antes de voltar a dormir; usá-lo para "a série sumiu" foi o que fez um
// servidor morto ser anunciado como recuperado.
func stateEmoji(sev, state string, flapping bool) string {
	if state == alerting.StateNoData {
		return "📡"
	}
	if state == alerting.StateResolved {
		return "✅"
	}
	if flapping {
		return "🔁"
	}
	switch strings.ToLower(sev) {
	case "crit", "critical", "critico", "crítico", "emergency":
		return "🔴"
	case "warning", "warn", "aviso":
		return "🟠"
	case "info", "information":
		return "🔵"
	default:
		return "🔔"
	}
}

// friendlyMetric dá um nome humano às métricas mais comuns.
func friendlyMetric(metric string) string {
	switch metric {
	// "Uso de CPU" fica com a SOMA (consumo + roubo), que é o que o painel destaca e
	// o que o operador espera ler num alerta. As parcelas ganham nome próprio e
	// curto: a mensagem vai para WhatsApp, e um rótulo com explicação entre
	// parênteses no meio da frase ("... em 97,5% — acima do limite") não se lê.
	case "system.cpu.utilization.total":
		return "Uso de CPU"
	case "system.cpu.utilization":
		return "CPU consumida"
	case "system.cpu.steal":
		return "CPU roubada pelo hipervisor"
	case "system.memory.utilization":
		return "Uso de memória"
	case "system.filesystem.utilization", "system.disk.utilization":
		return "Uso de disco"
	case "container.restarts":
		return "Reinícios do container"
	}
	// As demais espelham os rótulos do painel (web/src/metrics/dict.ts), para a
	// pessoa ler no WhatsApp o mesmo nome que vê na tela. Sem "(%)" nem
	// "(acumulado)": o valor ao lado já traz a unidade.
	if nome, ok := nomesDoPainel[metric]; ok {
		return nome
	}
	return metric
}

var nomesDoPainel = map[string]string{
	"system.memory.used":           "RAM usada",
	"system.memory.total":          "RAM total",
	"system.memory.available":      "RAM disponível",
	"system.filesystem.used":       "Disco usado",
	"system.filesystem.total":      "Disco total",
	"system.filesystem.available":  "Disco livre",
	"system.paging.utilization":    "Uso de swap",
	"system.paging.used":           "Swap usado",
	"system.paging.total":          "Swap total",
	"system.processes.count":       "Processos",
	"system.uptime":                "Tempo desde o último boot",
	"system.cpu.load_average.1m":   "Carga média (1 min)",
	"system.cpu.load_average.5m":   "Carga média (5 min)",
	"system.cpu.load_average.15m":  "Carga média (15 min)",
	"system.network.io.bytes_recv": "Rede recebida",
	"system.network.io.bytes_sent": "Rede enviada",
	"container.cpu.utilization":    "CPU do container",
	"container.memory.utilization": "RAM do container",
	"container.memory.usage":       "RAM usada pelo container",
	"container.memory.limit":       "Limite de RAM do container",
}

// fmtValue formata o valor como quem lê no celular espera: porcentagem com
// vírgula ("97,5%"), bytes em unidade legível ("3,2 GB"), inteiro quando redondo
// (status HTTP 404, contagem), senão 2 casas — sempre com vírgula decimal, que é
// a do português. "97.5%" e "3456789012" são legíveis para quem escreveu o código,
// não para quem acorda com o aviso.
func fmtValue(metric string, v float64) string {
	// Um valor que não é número não vira "NaN%" no celular de ninguém.
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "—"
	}
	// O "valor" da regra de ausência é a idade do último sinal em segundos — só faz
	// sentido lido como tempo ("7 min"), nunca como "420".
	if metric == alerting.HeartbeatMetric {
		return humanDuration(time.Duration(v) * time.Second)
	}
	if metricaEmPorcento(metric) {
		return fmtPercent(v)
	}
	if metricaEmBytes(metric) {
		return fmtBytes(v)
	}
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return virgula(fmt.Sprintf("%.2f", v))
}

// fmtHTTPStatus: "500", "404" — nunca com casas, unidade ou vírgula.
func fmtHTTPStatus(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "—"
	}
	return fmt.Sprintf("%.0f", v)
}

// virgula troca o ponto decimal do Sprintf pela vírgula do pt-BR.
func virgula(s string) string { return strings.Replace(s, ".", ",", 1) }

// metricaEmPorcento: toda `*utilization*` (inclusive `system.cpu.utilization.total`,
// que termina em ".total" e NÃO é bytes) e o steal de CPU, que é fração do intervalo.
func metricaEmPorcento(metric string) bool {
	return strings.Contains(metric, "utilization") || metric == "system.cpu.steal"
}

// metricaEmBytes reconhece as séries de tamanho pelo sufixo que o agente usa:
// memória/disco (.used/.total/.available), container (.usage/.limit) e rede (bytes_).
func metricaEmBytes(metric string) bool {
	for _, suf := range []string{".used", ".total", ".available", ".usage", ".limit"} {
		if strings.HasSuffix(metric, suf) {
			return true
		}
	}
	return strings.Contains(metric, "bytes")
}

// fmtPercent: uma casa quando há fração ("97,5%"), nenhuma quando é redondo
// ("90%" — é assim que o limiar de uma regra costuma ser escrito).
func fmtPercent(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f%%", v)
	}
	return virgula(fmt.Sprintf("%.1f%%", v))
}

// fmtBytes usa as mesmas unidades de 1024 do painel (formatBytes em web/src/format.ts):
// uma casa abaixo de 10 ("3,2 GB"), inteiro dali para cima ("123 GB").
func fmtBytes(v float64) string {
	if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return "—"
	}
	unidades := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for v >= 1024 && i < len(unidades)-1 {
		v /= 1024
		i++
	}
	if i == 0 || v >= 10 || v == math.Trunc(v) {
		return fmt.Sprintf("%.0f %s", v, unidades[i])
	}
	return virgula(fmt.Sprintf("%.1f %s", v, unidades[i]))
}

// rotuloDuracao: "Ficou fora por" é frase de coisa que CAI (site, container,
// servidor mudo). CPU alta que voltou ao normal não "ficou fora" — durou.
func rotuloDuracao(it AlertItem) string {
	if it.Metric == "container.running" || it.Metric == alerting.HeartbeatMetric ||
		it.Labels["site"] != "" || it.Labels["url"] != "" {
		return "Ficou fora por"
	}
	return "Durou"
}

// opLimitPT frase o operador como "acima/abaixo do limite de".
func opLimitPT(op string) string {
	switch op {
	case ">":
		return "acima do limite de"
	case ">=":
		return "no ou acima do limite de"
	case "<":
		return "abaixo do limite de"
	case "<=":
		return "no ou abaixo do limite de"
	default:
		return "fora do limite de"
	}
}

// humanReason é a frase principal, em português claro, explicando o que houve —
// sem operadores crus (nada de "[min < 1]" ou "status ≠ 200").
func humanReason(it AlertItem) string {
	resolved := it.State == alerting.StateResolved
	l := it.Labels

	// Encerrado por FALTA DE DADOS. Precede tudo: seja qual for a métrica, o que
	// aconteceu aqui não foi melhora — a medição sumiu. Antes, este caso saía com o
	// mesmo texto e o mesmo ✅ de uma recuperação de verdade, e foi assim que um
	// servidor que morreu às 14:30 virou "voltou ao normal" às 14:32.
	if it.State == alerting.StateNoData {
		return nodataReason(it)
	}

	// Servidor mudo (regra de ausência).
	if it.Metric == alerting.HeartbeatMetric {
		srv := "O servidor"
		if h := it.hostText(); h != "" {
			srv = fmt.Sprintf("O servidor \"%s\"", h)
		}
		if resolved {
			return srv + " voltou a reportar."
		}
		return fmt.Sprintf("%s parou de reportar há %s. O agente pode estar parado, ou a máquina inteira. Enquanto isso, NENHUM alerta deste servidor vai disparar.",
			srv, fmtValue(it.Metric, it.Value))
	}

	// Container subiu/caiu.
	if it.Metric == "container.running" {
		name := "O container"
		if c := l["container"]; c != "" {
			name = fmt.Sprintf("O container \"%s\"", c)
		}
		if resolved {
			return name + " voltou a rodar."
		}
		return name + " parou de rodar."
	}

	// Sondagem HTTP (tem site/url/diagnóstico). O alvo é chamado pelo NOME do check,
	// nunca de "o site": o mesmo monitor vigia API, endpoint interno e página, e
	// anunciar uma API fora do ar como "o site" faz o destinatário desconfiar do
	// aviso inteiro — foi o que aconteceu com "Site DOWN: Eduq — Portal".
	if l["site"] != "" || l["url"] != "" || l["diagnóstico"] != "" {
		alvo := "O endereço monitorado"
		if s := l["site"]; s != "" {
			alvo = "\"" + s + "\""
		} else if u := l["url"]; u != "" {
			alvo = "\"" + u + "\""
		}
		if resolved {
			return alvo + " voltou ao ar."
		}
		// O status HTTP é um inteiro e ponto. Aqui `it.Metric` é a URL monitorada
		// (sitecheck usa a URL como nome da métrica), e uma URL com "bytes" no meio
		// faria fmtValue escrever "HTTP 500 B".
		if l["diagnóstico"] == "http_status" {
			return fmt.Sprintf("%s está fora do ar (respondeu HTTP %s).", alvo, fmtHTTPStatus(it.Value))
		}
		return alvo + " está fora do ar."
	}

	// Métrica numérica genérica (CPU, memória, disco, etc.).
	fm := friendlyMetric(it.Metric)
	val := fmtValue(it.Metric, it.Value)
	if resolved {
		return fmt.Sprintf("%s voltou ao normal (%s).", fm, val)
	}
	if it.Op != "" {
		return fmt.Sprintf("%s em %s, %s %s.", fm, val, opLimitPT(it.Op), fmtValue(it.Metric, it.Threshold))
	}
	return fmt.Sprintf("%s: %s.", fm, val)
}

// nodataReason é o texto de um alerta encerrado porque a série PAROU DE CHEGAR.
//
// A frase é deliberadamente pouco tranquilizadora. O alerta saiu da lista de ativos,
// mas ninguém disse que o problema acabou: pararam de chegar as medições, e a causa
// mais provável de as medições pararem é a situação ter piorado.
func nodataReason(it AlertItem) string {
	alvo := friendlyMetric(it.Metric)
	if it.Metric == alerting.HeartbeatMetric {
		alvo = "o sinal do agente"
	}
	onde := ""
	if h := it.hostText(); h != "" {
		onde = fmt.Sprintf(" do servidor \"%s\"", h)
	} else if c := it.Labels["container"]; c != "" {
		onde = fmt.Sprintf(" do container \"%s\"", c)
	}
	return fmt.Sprintf("SEM DADOS: %s%s deixou de ser reportado e o alerta foi encerrado por falta de informação, NÃO por melhora. O problema pode continuar, ou estar pior. Confira se o servidor e o agente estão de pé.",
		alvo, onde)
}

// locationLines dá as linhas de contexto legíveis (Servidor, Endereço do site).
func locationLines(it AlertItem) []string {
	l := it.Labels
	var out []string
	if h := l["host"]; h != "" {
		out = append(out, "Servidor: "+it.hostText())
	}
	if u := l["url"]; u != "" {
		out = append(out, "Endereço: "+u)
	}
	return out
}

// detailLabelKeys devolve as CHAVES dos labels EXTRAS úteis (fora do ruído técnico e
// dos já exibidos como linha própria), em ordem — preserva contexto que o próprio
// usuário definiu (ex.: origem=...).
func detailLabelKeys(labels map[string]string) []string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		if noiseLabels[k] || structuralLabels[k] {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// detailLabels devolve os labels extras como "chave=valor" (usado no texto puro).
func detailLabels(labels map[string]string) []string {
	keys := detailLabelKeys(labels)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return parts
}

// render monta o assunto e o corpo de texto (WhatsApp/Telegram/webhook) de um alerta.
// O corpo NÃO repete o assunto — os senders é que cuidam do cabeçalho (o WhatsApp o
// põe em negrito; o Telegram o adiciona como primeira linha).
func render(_ string, items []AlertItem) Message {
	state := items[0].State
	sev := items[0].Severity
	rule := items[0].RuleName
	flapping := items[0].Flapping && state == alerting.StateFiring

	label := sevPT(sev)
	switch {
	case state == alerting.StateNoData:
		label = "SEM DADOS"
	case state == alerting.StateResolved:
		label = "RESOLVIDO"
	case flapping:
		label = "OSCILANDO"
	}
	// "CRÍTICO: CPU alta · VPS Principal" — dois-pontos porque o que vem depois
	// explica o rótulo, e o ONDE já no assunto: no WhatsApp a linha em negrito é a
	// que se lê primeiro, e "CPU alta" sem dizer de qual servidor obriga a abrir a
	// mensagem para descobrir se é o banco de produção ou a máquina de testes.
	subject := fmt.Sprintf("%s %s: %s", stateEmoji(sev, state, flapping), label, rule)
	if onde := ondeCurto(items); onde != "" {
		subject += " · " + onde
	}
	if len(items) > 1 {
		subject += fmt.Sprintf(" (%d alertas)", len(items))
	}

	var b strings.Builder
	for i, it := range items {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s\n", humanReason(it))
		if it.Evidence != "" {
			fmt.Fprintf(&b, "Como medimos: %s\n", it.Evidence)
		}
		for _, ln := range locationLines(it) {
			fmt.Fprintf(&b, "%s\n", ln)
		}
		// Desde quando: quem lê o aviso às 3h da manhã quer saber se começou agora
		// ou há duas horas. Só no disparo — na resolução a duração já diz isso.
		if state == alerting.StateFiring && !it.Since.IsZero() {
			fmt.Fprintf(&b, "Começou: %s\n", horaLocal(it.Since))
		}
		if det := detailLabels(it.Labels); len(det) > 0 {
			fmt.Fprintf(&b, "Detalhes: %s\n", strings.Join(det, ", "))
		}
		if state == alerting.StateResolved && it.Duration != "" {
			fmt.Fprintf(&b, "%s: %s\n", rotuloDuracao(it), it.Duration)
		}
		if state == alerting.StateNoData && it.Duration != "" {
			fmt.Fprintf(&b, "O alerta estava aberto há: %s\n", it.Duration)
		}
		if s := suppressedLine(it); s != "" {
			fmt.Fprintf(&b, "%s\n", s)
		}
		if it.Runbook != "" {
			fmt.Fprintf(&b, "Como resolver: %s\n", it.Runbook)
		}
		if it.DashboardURL != "" {
			fmt.Fprintf(&b, "Abrir no painel: %s\n", it.DashboardURL)
		}
	}
	return Message{
		Subject: subject,
		Text:    strings.TrimRight(b.String(), "\n"),
		HTML:    renderHTML(state, items),
		State:   state,
		Alerts:  items,
	}
}

// ondeCurto é o lugar do alerta para o assunto: o servidor (nome amigável), com o
// container na frente quando houver ("api" em srv1). Até três servidores são
// nomeados ("web1, web2"); acima disso só a contagem, e o corpo lista cada um.
// Alertas de site já trazem o alvo no nome da regra ("Fora do ar: Loja") e não
// repetem nada aqui.
func ondeCurto(items []AlertItem) string {
	var hosts []string
	visto := map[string]bool{}
	for _, it := range items {
		if h := it.hostText(); h != "" && !visto[h] {
			visto[h] = true
			hosts = append(hosts, h)
		}
	}
	switch {
	case len(hosts) == 0:
		return ""
	case len(hosts) == 1 && len(items) == 1 && items[0].Labels["container"] != "":
		return fmt.Sprintf("%q em %s", items[0].Labels["container"], hosts[0])
	case len(hosts) <= 3:
		return strings.Join(hosts, ", ")
	default:
		return fmt.Sprintf("%d servidores", len(hosts))
	}
}

// suppressedLine explicita quantas transições o amortecedor de oscilação engoliu.
// Sem esse número, a supressão era invisível: o operador recebia UM aviso de
// "oscilando" e o restante do incidente — inclusive o fim — acontecia em silêncio.
func suppressedLine(it AlertItem) string {
	if it.SuppressedCount <= 0 {
		return ""
	}
	if it.SuppressedCount == 1 {
		return "Mais 1 transição suprimida na última hora (alerta oscilante)."
	}
	return fmt.Sprintf("Mais %d transições suprimidas na última hora (alerta oscilante).", it.SuppressedCount)
}

// sevColor devolve a cor de destaque do e-mail conforme severidade/estado.
func sevColor(sev, state string) string {
	if state == alerting.StateNoData {
		return "#b45309" // âmbar: nem "deu tudo certo" (verde), nem alarme novo (vermelho)
	}
	if state == alerting.StateResolved {
		return "#16a34a" // verde
	}
	switch strings.ToLower(sev) {
	case "crit", "critical", "critico", "crítico", "emergency":
		return "#dc2626" // vermelho
	case "warning", "warn", "aviso":
		return "#b45309" // âmbar (escurecido p/ contraste no chip claro)
	case "info", "information":
		return "#2563eb" // azul
	default:
		return "#4b5563" // cinza
	}
}

// sevTint devolve o fundo claro do chip de severidade (padrão Revoada: superfície
// neutra + chip pequeno colorido, em vez de faixa colorida dominante).
func sevTint(sev, state string) string {
	if state == alerting.StateNoData {
		return "#fef3d8" // âmbar claro
	}
	if state == alerting.StateResolved {
		return "#dcfce7" // verde claro
	}
	switch strings.ToLower(sev) {
	case "crit", "critical", "critico", "crítico", "emergency":
		return "#fde8e8" // vermelho claro
	case "warning", "warn", "aviso":
		return "#fef3d8" // âmbar claro
	case "info", "information":
		return "#e0edff" // azul claro
	default:
		return "#eef0f3" // cinza claro
	}
}

func esc(s string) string { return html.EscapeString(s) }

// showValueBox decide se o "valor atual" numérico ajuda: para container caído (0/1)
// e sites (no ar/fora), a frase já diz tudo — o número seria ruído.
func showValueBox(it AlertItem) bool {
	if it.Metric == "container.running" {
		return false
	}
	// Ausência: a frase já diz "parou de reportar há 7m". Um quadro grande com "7m"
	// ao lado seria o mesmo dado duas vezes — e, pior, parecendo uma medida do
	// servidor quando é a idade do silêncio.
	if it.Metric == alerting.HeartbeatMetric || it.State == alerting.StateNoData {
		return false
	}
	if it.Labels["site"] != "" || it.Labels["url"] != "" {
		return false
	}
	return true
}

// renderHTML monta o corpo HTML do e-mail (CSS inline + tabelas, para máxima
// compatibilidade com clientes como Gmail/Outlook). O texto puro (Text) segue como
// alternativa no multipart.
func renderHTML(state string, items []AlertItem) string {
	first := items[0]
	accent := sevColor(first.Severity, state)
	badge := "Disparado"
	switch {
	case state == alerting.StateNoData:
		badge = "Sem dados"
	case state == alerting.StateResolved:
		badge = "Resolvido"
	case first.Flapping:
		badge = "Oscilante"
	}
	sev := sevPT(first.Severity)

	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light"><meta name="supported-color-schemes" content="light"><link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet"></head>`)
	b.WriteString(`<body style="margin:0;padding:0;background:#eef0f3;">`)
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#eef0f3;padding:24px 12px;font-family:'Inter',-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;"><tr><td align="center">`)
	b.WriteString(`<table role="presentation" width="600" cellpadding="0" cellspacing="0" style="width:600px;max-width:100%;background:#ffffff;border-radius:12px;overflow:hidden;box-shadow:0 1px 4px rgba(16,24,40,.12);">`)

	// Faixa de marca Revoada (preto + dourado).
	b.WriteString(`<tr><td style="background:#0a0b0f;padding:14px 26px;text-align:center;">`)
	b.WriteString(`<span style="color:#c4a568;font-size:16px;font-weight:700;letter-spacing:.04em;">Revoada</span></td></tr>`)

	// Cabeçalho neutro com acento dourado + chip de severidade (padrão Revoada:
	// a cor da severidade fica no chip, não numa faixa dominante).
	b.WriteString(`<tr><td style="padding:20px 26px 6px;border-top:3px solid #c4a568;">`)
	b.WriteString(`<div style="margin-bottom:12px;">`)
	b.WriteString(`<span style="display:inline-block;background:` + sevTint(first.Severity, state) + `;color:` + accent + `;font-size:11px;letter-spacing:.08em;font-weight:700;padding:4px 11px;border-radius:999px;text-transform:uppercase;">` + esc(sev) + `</span>`)
	b.WriteString(` <span style="display:inline-block;background:#eef0f3;color:#475467;font-size:11px;font-weight:600;padding:4px 11px;border-radius:999px;">` + esc(badge) + `</span>`)
	if len(items) > 1 {
		b.WriteString(` <span style="color:#98a2b3;font-size:12px;">` + fmt.Sprintf("+%d agrupados", len(items)-1) + `</span>`)
	}
	b.WriteString(`</div>`)
	b.WriteString(`<div style="color:#101828;font-size:21px;font-weight:700;line-height:1.25;">` + esc(first.RuleName) + `</div>`)
	b.WriteString(`</td></tr>`)

	// Resumo: a frase humana em destaque + servidor/endereço quando houver.
	b.WriteString(`<tr><td style="padding:18px 26px 2px;">`)
	b.WriteString(`<div style="font-size:15px;color:#101828;font-weight:600;margin-bottom:10px;">` + esc(humanReason(first)) + `</div>`)
	if first.Evidence != "" {
		b.WriteString(`<div style="font-size:13px;color:#475467;margin:-4px 0 12px;"><b style="color:#667085;font-weight:600;">Como medimos:</b> ` + esc(first.Evidence) + `</div>`)
	}
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="font-size:13px;">`)
	row := func(k, v string) {
		b.WriteString(`<tr><td style="padding:3px 0;width:100px;color:#98a2b3;">` + esc(k) + `</td><td style="padding:3px 0;color:#101828;font-weight:500;">` + v + `</td></tr>`)
	}
	row("Severidade", esc(sev))
	if h := first.Labels["host"]; h != "" {
		row("Servidor", esc(first.hostText()))
	}
	if s := first.Labels["site"]; s != "" {
		row("Monitorado", esc(s)) // pode ser site, API ou endpoint interno — o rótulo não decide por eles
	}
	if u := first.Labels["url"]; u != "" {
		row("Endereço", esc(u))
	}
	row("Estado", esc(badge))
	b.WriteString(`</table></td></tr>`)

	// Um bloco por alerta (o valor bruto só aparece quando ajuda; nada de "Condição"
	// crua nem dump de labels técnicos — só os detalhes extras que o usuário definiu).
	for _, it := range items {
		b.WriteString(`<tr><td style="padding:12px 26px 0;">`)
		b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="border:1px solid #e4e7ec;border-radius:10px;"><tr><td style="padding:16px 18px;">`)
		// Em alertas agrupados, cada bloco repete a frase e o servidor para dar contexto.
		if len(items) > 1 {
			b.WriteString(`<div style="font-size:15px;font-weight:700;color:#101828;">` + esc(humanReason(it)) + `</div>`)
			if h := it.Labels["host"]; h != "" {
				b.WriteString(`<div style="font-size:13px;color:#667085;margin-top:3px;">Servidor: ` + esc(it.hostText()) + `</div>`)
			}
		}
		if showValueBox(it) {
			b.WriteString(`<table role="presentation" cellpadding="0" cellspacing="0" style="margin-top:12px;"><tr>`)
			b.WriteString(`<td style="background:#f9fafb;border:1px solid #eaecf0;border-radius:8px;padding:10px 16px;">`)
			b.WriteString(`<div style="font-size:11px;letter-spacing:.05em;color:#98a2b3;text-transform:uppercase;">Valor atual</div>`)
			b.WriteString(`<div style="font-size:24px;font-weight:700;color:` + accent + `;line-height:1.1;margin-top:2px;">` + esc(fmtValue(it.Metric, it.Value)) + `</div></td>`)
			if state == alerting.StateResolved && it.Duration != "" {
				b.WriteString(`<td style="padding-left:14px;vertical-align:middle;font-size:13px;color:#475467;">` + esc(rotuloDuracao(it)) + ` <b style="color:#101828;">` + esc(it.Duration) + `</b></td>`)
			}
			b.WriteString(`</tr></table>`)
		} else if state == alerting.StateResolved && it.Duration != "" {
			b.WriteString(`<div style="margin-top:12px;font-size:13px;color:#475467;">` + esc(rotuloDuracao(it)) + ` <b style="color:#101828;">` + esc(it.Duration) + `</b></div>`)
		} else if state == alerting.StateNoData && it.Duration != "" {
			b.WriteString(`<div style="margin-top:12px;font-size:13px;color:#475467;">O alerta estava aberto há <b style="color:#101828;">` + esc(it.Duration) + `</b></div>`)
		}
		if s := suppressedLine(it); s != "" {
			b.WriteString(`<div style="margin-top:10px;font-size:13px;color:#475467;">` + esc(s) + `</div>`)
		}
		if det := detailLabelKeys(it.Labels); len(det) > 0 {
			b.WriteString(`<div style="font-size:11px;letter-spacing:.05em;color:#98a2b3;text-transform:uppercase;margin:16px 0 6px;">Detalhes</div>`)
			b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="border-collapse:collapse;font-size:13px;">`)
			for i, k := range det {
				bg := "#ffffff"
				if i%2 == 1 {
					bg = "#f9fafb"
				}
				b.WriteString(`<tr><td style="padding:6px 10px;background:` + bg + `;color:#667085;border:1px solid #eaecf0;white-space:nowrap;">` + esc(k) + `</td>`)
				b.WriteString(`<td style="padding:6px 10px;background:` + bg + `;color:#101828;border:1px solid #eaecf0;word-break:break-all;">` + esc(it.Labels[k]) + `</td></tr>`)
			}
			b.WriteString(`</table>`)
		}
		if it.DashboardURL != "" || it.Runbook != "" {
			b.WriteString(`<div style="margin-top:16px;">`)
			if it.DashboardURL != "" {
				b.WriteString(`<a href="` + esc(it.DashboardURL) + `" style="display:inline-block;background:#c4a568;color:#0a0b0f;text-decoration:none;font-size:13px;font-weight:600;padding:10px 18px;border-radius:8px;">Abrir no Revoada</a>`)
			}
			if it.Runbook != "" {
				b.WriteString(` <a href="` + esc(it.Runbook) + `" style="display:inline-block;color:#8a6d33;text-decoration:none;font-size:13px;font-weight:600;padding:10px 12px;">Como resolver</a>`)
			}
			b.WriteString(`</div>`)
		}
		b.WriteString(`</td></tr></table></td></tr>`)
	}

	// Rodapé.
	b.WriteString(`<tr><td style="padding:18px 26px 24px;"><div style="border-top:1px solid #eaecf0;padding-top:14px;color:#98a2b3;font-size:12px;">Enviado automaticamente pelo <b style="color:#667085;">Revoada</b>.</div></td></tr>`)

	b.WriteString(`</table></td></tr></table></body></html>`)
	return b.String()
}

// humanDuration formata uma duração como se fala: "45 s", "5 min", "2 h 13 min",
// "1 d 2 h". O "2h13m" antigo era notação de programador, e acima de um dia virava
// "26h30m", que ninguém converte de cabeça.
func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d s", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%d h", h)
		}
		return fmt.Sprintf("%d h %02d min", h, m)
	}
	dias := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	if h == 0 {
		return fmt.Sprintf("%d d", dias)
	}
	return fmt.Sprintf("%d d %d h", dias, h)
}

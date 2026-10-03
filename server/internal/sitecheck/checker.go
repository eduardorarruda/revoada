package sitecheck

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/agenda"
	"github.com/eduardorarruda/revoada/server/internal/alerting"
	"github.com/eduardorarruda/revoada/server/internal/chquery"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// Notifier é o roteador de notificações que o checker usa. Além do envio por rotas
// (Notify), expõe entrega direta a canais específicos (NotifyChannels) para checks
// que escolheram seus próprios canais. Satisfeito por *notify.Router.
type Notifier interface {
	Notify(ctx context.Context, n alerting.Notification)
	NotifyChannels(ctx context.Context, n alerting.Notification, channelIDs []int64)
}

// Checker roda o scheduler de sondagens: máquina de estados UP→DEGRADADO→
// SUSPEITO→DOWN, gravação de métricas decompostas e alertas (DOWN, recuperação,
// expiração de certificado).
type Checker struct {
	st        *store.Store
	ch        *chquery.Client
	prober    *Prober
	notifier  Notifier
	orcamento *orcamentoDeAvisos
	log       *slog.Logger
	voo       *agenda.EmVoo // sondagens em andamento (ver tick)
}

func NewChecker(st *store.Store, ch *chquery.Client, notifier Notifier, log *slog.Logger) *Checker {
	return &Checker{st: st, ch: ch, prober: NewProber(), notifier: notifier,
		orcamento: novoOrcamento(), log: log, voo: agenda.NovoEmVoo(maxSondagensSimultaneas)}
}

// emit despacha a notificação de um check. Se o check tem canais próprios, entrega
// direto a eles; senão cai nas Rotas de notificação genéricas (retrocompatível).
//
// É o funil ÚNICO de tudo que a sondagem avisa — queda, recuperação, sitemap,
// certificado, sonda sem reporte — e por isso é aqui que mora o teto horário. Ver
// orcamento.go: as sondagens eram o único caminho de notificação do painel sem
// nenhum freio de oscilação.
func (c *Checker) emit(ctx context.Context, chk *store.SiteCheck, n alerting.Notification) {
	pode, engolidas := c.orcamento.cabe(chk.ID, time.Now())
	if !pode {
		c.log.Warn("teto horário de avisos deste site atingido; a transição vira contagem para a próxima mensagem",
			"site", chk.Name, "teto", maxAvisosPorCheckHora, "agrupadas", engolidas)
		return
	}
	n.ThrottledCount = engolidas
	if len(chk.ChannelIDs) > 0 {
		c.notifier.NotifyChannels(ctx, n, chk.ChannelIDs)
		return
	}
	c.notifier.Notify(ctx, n)
}

// Run verifica checks vencidos a cada 5s.
func (c *Checker) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.tick(ctx)
		}
	}
}

func (c *Checker) tick(ctx context.Context) {
	// Folga: o que vence até 1 s depois deste tick já conta como devido — sem ela, uma
	// sondagem marcada milissegundos depois do tick ficava para o tick seguinte (+5 s).
	due, err := c.st.DueSiteChecks(ctx, time.Now().Add(agenda.Folga))
	if err != nil {
		c.log.Warn("sitecheck: listando", "err", err)
		return
	}
	// Dispara SEM esperar: antes o tick fazia wg.Wait() no lote inteiro, e um site
	// travado até o timeout (20 s) atrasava a sondagem de todos os outros. Um check
	// ainda em andamento não é disparado de novo (EmVoo).
	for _, chk := range due {
		chk := chk
		c.voo.Disparar(ctx, chk.ID, func() {
			if chk.Kind == "sitemap" {
				c.runSitemap(ctx, chk)
			} else {
				c.runOne(ctx, chk)
			}
		})
	}
}

// maxSondagensSimultaneas é o teto de sondagens HTTP em paralelo.
const maxSondagensSimultaneas = 8

func (c *Checker) runOne(ctx context.Context, chk store.SiteCheck) {
	// A cadência é de início a início ("a cada 60 s"): o NextCheckAt que a máquina
	// de estados calcula a partir de `now` (o fim da sondagem) é reancorado em
	// `inicio` antes de gravar — senão cada sondagem empurrava a seguinte pela
	// própria duração e pelo degrau de 5 s do tick.
	inicio := time.Now()
	res := c.prober.Probe(ctx, Target{
		URL: chk.URL, ExpectStatus: chk.ExpectStatus, Keyword: chk.Keyword,
		MaxLatencyMS: chk.MaxLatencyMS,
		// TimeoutMS do check. Faltava aqui, e a falta tornava FALSO o que prober.go
		// e o schema anunciavam: a coluna era lida do banco, nunca chegava à sonda,
		// e todo alvo era cortado pelo default global. Quem precisasse de mais tempo
		// para um sistema pesado só conseguia dando mais tempo a TODOS.
		TimeoutMS: chk.TimeoutMS,
	})
	now := time.Now()
	chk.LastCheckedAt = &now
	interval := store.TierInterval(chk.Tier)

	_ = c.st.InsertSiteCheckResult(ctx, store.SiteCheckResult{
		CheckID: chk.ID, OK: res.OK, Status: res.Status, Diagnosis: res.Diagnosis,
		DNSMs: res.DNSMs, ConnectMs: res.ConnectMs, TLSMs: res.TLSMs, TTFBMs: res.TTFBMs,
		TotalMs: res.TotalMs, CertDaysLeft: res.CertDaysLeft,
	})

	// Bloqueio do NOSSO guard SSRF não é medida do alvo. A sondagem nem saiu daqui,
	// então não pode virar DOWN nem entrar na conta de uptime (a query de uptime
	// exclui este diagnóstico; a cobertura cai, que é a informação verdadeira).
	// Antes, o erro do safehttp caía no `default` de classifyErr e virava
	// `connect_timeout`: o alvo era acusado por uma recusa nossa.
	//
	// A ABSTENÇÃO TAMBÉM ENTRA AQUI, e ela faltava.
	//
	// `Inconclusive` era escrito pelo prober e nunca lido por ninguém — e
	// DiagnosisIsUnmeasured, escrita para ser o teste único dos dois casos, não tinha
	// um chamador sequer. Consequência concreta: uma página maior que o teto de leitura
	// (MaxBodyBytes) com palavra-chave configurada responde 200, tem o corpo cortado
	// antes de a palavra aparecer e sai com `keyword_indeterminado` e OK=false. Isso
	// caía direto em onFailure: duas sondagens seguidas e o painel publicava
	// "🔴 CRÍTICO — Fora do ar" para um site no ar, acusando o cliente pelo tamanho da
	// própria página. O comentário de diagnosis.go já dizia que esse caso "não vira
	// DOWN, não notifica e fica fora do uptime"; agora é verdade.
	//
	// Uma pergunta sem resposta não é um "não".
	if DiagnosisIsUnmeasured(res.Diagnosis) {
		chk.LastDiagnosis = res.Diagnosis
		next := now.Add(interval)
		chk.NextCheckAt = &next
		if res.Blocked {
			c.log.Warn("sitecheck: destino recusado pelo guard SSRF (não conta como DOWN)",
				"site", chk.Name, "url", chk.URL)
		} else {
			c.log.Warn("sitecheck: asserção indeterminada (corpo maior que o teto de leitura); não conta como DOWN",
				"site", chk.Name, "url", chk.URL, "diagnóstico", res.Diagnosis)
		}
		chk.NextCheckAt = agenda.Reancorar(chk.NextCheckAt, inicio, now)
		if err := c.st.UpdateSiteCheckState(ctx, chk); err != nil {
			c.log.Warn("sitecheck: atualizando estado", "err", err)
		}
		return
	}

	c.writeMetrics(ctx, chk, res)

	// Multi-sonda (P6.3): se ≥2 sondas reportam esta URL, a decisão de DOWN passa a
	// exigir ≥2 sondas falhando (uma sonda bloqueada não derruba). Senão, caminho normal.
	if !c.applyProbeConsensus(ctx, &chk, res, now, interval) {
		if res.OK {
			c.onSuccess(ctx, &chk, res, now, interval)
		} else {
			c.onFailure(ctx, &chk, res, now, interval)
		}
	}
	c.certExpiry(ctx, &chk, res.CertDaysLeft)

	chk.NextCheckAt = agenda.Reancorar(chk.NextCheckAt, inicio, now)
	if err := c.st.UpdateSiteCheckState(ctx, chk); err != nil {
		c.log.Warn("sitecheck: atualizando estado", "err", err)
	}
}

// runSitemap processa um check kind='sitemap': busca o sitemap.xml, reconcilia os
// filhos (um por página) e agrega o estado dos filhos no pai, alertando na
// transição (com "N/M páginas fora do ar"). Não sonda a própria URL do sitemap
// como página — o "estar no ar" do sitemap é medido pela busca em si.
func (c *Checker) runSitemap(ctx context.Context, chk store.SiteCheck) {
	now := time.Now()
	chk.LastCheckedAt = &now
	interval := store.TierInterval(chk.Tier)
	next := now.Add(interval)
	chk.NextCheckAt = &next

	// Prazo para buscar o índice. O client do prober NÃO tem Timeout (o prazo é por
	// sondagem, via contexto), e este caminho passava o ctx do processo — que só é
	// cancelado no SIGTERM. Um sitemap.xml servido por um servidor que aceita a
	// conexão e nunca fecha o corpo segurava um dos 8 slots do ciclo PARA SEMPRE (e,
	// quando o tick ainda esperava o lote inteiro, parava a sondagem do parque).
	sctx, cancelSitemap := context.WithTimeout(ctx, timeoutDoCheck(chk))
	urls, err := c.prober.FetchSitemapURLs(sctx, chk.URL)
	cancelSitemap()
	if err != nil {
		// sitemap inacessível → o "serviço" está fora do ar.
		c.markSitemapState(ctx, &chk, "DOWN", "sitemap inacessível: "+err.Error(), now)
		ritmoDoSitemap(&chk, now)
		if e := c.st.UpdateSiteCheckState(ctx, chk); e != nil {
			c.log.Warn("sitemap: atualizando estado", "err", e)
		}
		return
	}

	added, removed, err := c.st.SyncSitemapChildren(ctx, chk, urls)
	if err != nil {
		c.log.Warn("sitemap: sincronizando filhos", "err", err)
	} else if added > 0 || removed > 0 {
		c.log.Info("sitemap: filhos sincronizados", "site", chk.Name, "urls", len(urls), "novos", added, "removidos", removed)
	}

	total, down, degraded, _ := c.st.CountChildStates(ctx, chk.ID)
	state, diag := "UP", ""
	switch {
	case total == 0:
		state = "UP" // recém-criado: os filhos ainda vão sondar
	case down >= total:
		state, diag = "DOWN", fmt.Sprintf("%d/%d páginas fora do ar", down, total)
	case down > 0:
		state, diag = "DEGRADADO", fmt.Sprintf("%d/%d páginas fora do ar", down, total)
	case degraded > 0:
		state, diag = "DEGRADADO", fmt.Sprintf("%d/%d páginas degradadas", degraded, total)
	}
	c.markSitemapState(ctx, &chk, state, diag, now)
	ritmoDoSitemap(&chk, now)
	// O pai herda o MENOR cert_days_left das páginas-filhas. Sem isto, um sitemap
	// inteiro nunca avisava que o certificado ia vencer: o pai não sonda (cert = 0)
	// e todo filho nasce com alerting=false, que era o primeiro `return` de
	// certExpiry — o aviso de expiração, a coisa mais útil que a sonda faz, nunca
	// saía para nenhum site cadastrado como sitemap.
	if dias, ok, err := c.st.MinChildCertDaysLeft(ctx, chk.ID); err != nil {
		c.log.Warn("sitemap: cert dos filhos", "err", err)
	} else if ok {
		c.certExpiry(ctx, &chk, dias)
	}
	if err := c.st.UpdateSiteCheckState(ctx, chk); err != nil {
		c.log.Warn("sitemap: atualizando estado", "err", err)
	}
}

// ritmoDoSitemap: com o agregado em problema, o pai se reavalia a cada 30 s — as
// páginas-filhas já são retestadas nesse ritmo, e sem isto o "RESOLVIDO" do pai só
// sairia no ciclo seguinte do tier, com a duração inflada.
func ritmoDoSitemap(chk *store.SiteCheck, now time.Time) {
	if chk.State == "DOWN" || chk.State == "DEGRADADO" {
		next := now.Add(intervaloReteste)
		chk.NextCheckAt = &next
	}
}

// markSitemapState aplica o estado agregado ao pai e alerta na transição
// sadio↔problema (uma notificação ao começar o problema, outra ao resolver).
func (c *Checker) markSitemapState(ctx context.Context, chk *store.SiteCheck, state, diag string, now time.Time) {
	wasBad := chk.State == "DOWN" || chk.State == "DEGRADADO"
	isBad := state == "DOWN" || state == "DEGRADADO"
	chk.LastDiagnosis = diag
	switch {
	case isBad && !wasBad:
		if chk.DownSince == nil {
			chk.DownSince = &now
		}
		c.notifySitemap(ctx, chk, state, diag, "firing", now)
	case !isBad && wasBad:
		since := now
		if chk.DownSince != nil {
			since = *chk.DownSince
		}
		c.notifySitemap(ctx, chk, state, diag, "resolved", since)
		chk.DownSince = nil
	}
	chk.State = state
}

// notifySitemap dispara a notificação agregada do sitemap (severidade por estado).
func (c *Checker) notifySitemap(ctx context.Context, chk *store.SiteCheck, state, diag, transition string, since time.Time) {
	if !chk.Alerting {
		return
	}
	sev := "warning"
	if state == "DOWN" {
		sev = "critical"
	}
	rule := store.AlertRule{
		Name: "Sitemap DOWN: " + chk.Name, Metric: chk.URL,
		Agg: "páginas", ConditionOp: ">", Threshold: 0, Severity: sev,
	}
	c.emit(ctx, chk, alerting.Notification{
		Rule:   rule,
		Labels: map[string]string{"site": chk.Name, "url": chk.URL, "diagnóstico": diagOrOK(diag)},
		Value:  0, State: transition, Since: since,
	})
}

// ProbeFreshness é a idade máxima de um reporte de sonda para entrar no consenso.
// Também usada pela API (handlers.consensusFor) para a tela dizer quantas das
// sondas designadas estão de fato reportando.
const ProbeFreshness = 2 * time.Minute

// prefixoSemConsenso marca o diagnóstico de "medição suspensa". A tela reconhece
// este prefixo para dizer "N sondas, M reportando" em vez de fingir normalidade.
const prefixoSemConsenso = "sem consenso: "

// applyProbeConsensus decide o estado por consenso de sondas. Devolve true se
// assumiu a decisão (havia ≥2 sondas OU o consenso está suspenso), false para
// cair no caminho single-probe.
func (c *Checker) applyProbeConsensus(ctx context.Context, chk *store.SiteCheck, res Result, now time.Time, interval time.Duration) bool {
	probes, err := c.st.FreshProbeResults(ctx, chk.TenantID, chk.URL, chk.ProbeLocations, now.Add(-ProbeFreshness))
	if err != nil {
		return false
	}
	// inclui a sonda "central" (o próprio checker) na contagem.
	up := map[string]bool{"central": res.OK}
	for _, p := range probes {
		up[p.Location] = p.Up
	}
	if len(up) < 2 {
		// AQUI ficava o pior caso: sem sondas frescas, o código caía no caminho de
		// sonda única, onde a 2ª falha consecutiva de UMA sonda declara DOWN. Ou
		// seja, a proteção contra falso positivo desligava sozinha exatamente
		// quando a malha de sondas estava instável — o momento em que ela mais
		// importa. Se o check TEM sondas designadas, seguramos a decisão.
		if len(chk.ProbeLocations) > 0 {
			c.holdForConsensus(ctx, chk, res, now, interval, probes)
			return true
		}
		return false // check de origem única declarada → lógica single-probe
	}
	var failing []string
	for loc, ok := range up {
		if !ok {
			failing = append(failing, loc)
		}
	}
	sort.Strings(failing)

	newState := "UP"
	switch {
	case len(failing) >= 2:
		newState = "DOWN"
	case len(failing) == 1:
		newState = "DEGRADADO"
	}

	wasDown := chk.State == "DOWN"
	if newState == "DOWN" {
		if !wasDown {
			chk.DownSince = &now
			res.Diagnosis = "down em " + strconv.Itoa(len(failing)) + " sonda(s): " + strings.Join(failing, ", ")
			chk.LastDiagnosis = res.Diagnosis
			c.notify(ctx, chk, res, "firing", now)
			c.log.Info("sitecheck: DOWN por consenso multi-sonda", "site", chk.Name, "sondas_falhando", failing)
		}
		chk.State = "DOWN"
	} else {
		if wasDown && chk.DownSince != nil {
			c.notify(ctx, chk, res, "resolved", *chk.DownSince)
		}
		chk.DownSince = nil
		chk.ConsecutiveFails = 0
		chk.State = newState
		if newState == "DEGRADADO" {
			chk.LastDiagnosis = "degradado: falha em " + strings.Join(failing, ", ")
		} else {
			chk.LastDiagnosis = ""
		}
	}
	// Em queda confirmada o ritmo é o do reteste (ver intervaloReteste): é o que faz
	// a recuperação ser vista na hora também no caminho de várias sondas.
	passo := interval
	if chk.State == "DOWN" {
		passo = intervaloReteste
	}
	next := now.Add(passo)
	chk.NextCheckAt = &next
	return true
}

// holdForConsensus segura a decisão de um check que TEM sondas designadas mas
// não recebeu reporte fresco de nenhuma delas.
//
// Regra: nunca ESCALAR (não vira SUSPEITO/DOWN por conta de uma medição só), mas
// aceitar a recuperação (se a central vê o site no ar, não há motivo para manter
// DOWN). E alertar uma vez que as sondas designadas estão caladas — o silêncio da
// sonda é um incidente do monitoramento, não do site.
func (c *Checker) holdForConsensus(ctx context.Context, chk *store.SiteCheck, res Result,
	now time.Time, interval time.Duration, frescos []store.ProbeResult,
) {
	vistas := make(map[string]bool, len(frescos))
	for _, p := range frescos {
		vistas[p.Location] = true
	}
	var caladas []string
	for _, loc := range chk.ProbeLocations {
		if !vistas[loc] {
			caladas = append(caladas, loc)
		}
	}
	sort.Strings(caladas)
	mudas := strings.Join(caladas, ", ")

	if res.OK && chk.State != "UP" {
		// Recuperação é segura de aceitar: ninguém é prejudicado por sair de DOWN.
		if chk.State == "DOWN" && chk.DownSince != nil {
			c.notify(ctx, chk, res, "resolved", *chk.DownSince)
		}
		chk.DownSince = nil
		chk.ConsecutiveFails = 0
		chk.State = "UP"
	}
	// Avisa UMA vez por episódio: o diagnóstico anterior já dizer "sem consenso"
	// significa que o alerta saiu no ciclo passado (senão seria 1 e-mail a cada
	// intervalo de sondagem enquanto a sonda estivesse fora).
	jaAvisado := strings.HasPrefix(chk.LastDiagnosis, prefixoSemConsenso)
	chk.LastDiagnosis = prefixoSemConsenso + strconv.Itoa(len(caladas)) + "/" +
		strconv.Itoa(len(chk.ProbeLocations)) + " sonda(s) sem reporte (" + mudas + ")"
	// Se o site segue DOWN com o consenso suspenso, a recuperação é o que falta
	// ver: reteste de 30 s, como no caminho de sonda única.
	passo := interval
	if chk.State == "DOWN" {
		passo = intervaloReteste
	}
	next := now.Add(passo)
	chk.NextCheckAt = &next
	if !jaAvisado {
		c.notifyProbeSilent(ctx, chk, caladas, now)
	}
	c.log.Warn("sitecheck: consenso suspenso (sondas designadas caladas)",
		"site", chk.Name, "sondas_caladas", caladas, "central_ok", res.OK)
}

// notifyProbeSilent avisa que sondas designadas pararam de reportar. Usa o mesmo
// roteador dos demais alertas; severidade "warning" porque o alvo pode estar bem
// — quem falhou foi a nossa medição.
func (c *Checker) notifyProbeSilent(ctx context.Context, chk *store.SiteCheck, caladas []string, now time.Time) {
	if !chk.Alerting || len(caladas) == 0 {
		return
	}
	rule := store.AlertRule{
		Name: "Sonda sem reporte: " + chk.Name, Metric: chk.URL,
		Agg: "sondas", ConditionOp: "≥", Threshold: 1, Severity: "warning",
	}
	c.emit(ctx, chk, alerting.Notification{
		Rule: rule,
		Labels: map[string]string{
			"site": chk.Name, "url": chk.URL,
			"diagnóstico": "sondas caladas: " + strings.Join(caladas, ", "),
		},
		Value: float64(len(caladas)), State: "firing", Since: now,
	})
}

func (c *Checker) onSuccess(ctx context.Context, chk *store.SiteCheck, res Result, now time.Time, interval time.Duration) {
	if chk.State == "DOWN" && chk.DownSince != nil {
		c.notify(ctx, chk, res, "resolved", *chk.DownSince) // recuperação com duração do downtime
	}
	chk.ConsecutiveFails = 0
	chk.DownSince = nil
	chk.LastDiagnosis = res.Diagnosis // '' ou 'slow'
	if res.Degraded {
		chk.State = "DEGRADADO"
	} else {
		chk.State = "UP"
	}
	next := now.Add(interval)
	chk.NextCheckAt = &next
}

// intervaloReteste é o ritmo enquanto o site está SUSPEITO ou DOWN. Em 25/09 o
// "RESOLVIDO" de uma queda de 40 s só chegou 5 min depois, no ciclo seguinte do
// tier, e o aviso disse "Ficou fora por 5 min". Retestar a cada 30 s custa 2
// pedidos por minuto a um servidor que já não responde — e devolve a verdade.
const intervaloReteste = 30 * time.Second

// falhasParaConfirmar é quantas tentativas seguidas (30 s entre elas) confirmam
// DOWN. Um site crítico avisa em 30 s; um site padrão espera ~1 min; um básico,
// ~1,5 min. A régua vem do que foi medido: a fila cheia do Apache do WHM dura
// dezenas de segundos e a lentidão noturna da API da Eduq dura ~76 s — quem abriu
// o link 5 min depois viu tudo no ar, e um aviso desses só ensina a ignorar avisos.
func falhasParaConfirmar(tier string) int {
	switch tier {
	case "critico":
		return 2
	case "basico":
		return 4
	default:
		return 3
	}
}

func (c *Checker) onFailure(ctx context.Context, chk *store.SiteCheck, res Result, now time.Time, _ time.Duration) {
	chk.ConsecutiveFails++
	chk.LastDiagnosis = res.Diagnosis
	next := now.Add(intervaloReteste)
	chk.NextCheckAt = &next
	// A queda começa na PRIMEIRA falha, não na confirmação: é essa hora que o aviso
	// diz em "Começou" e é dela que "Ficou fora por" conta. Marcar na confirmação
	// encurtaria toda queda em 30 a 90 s. Enquanto SUSPEITO a tela não mostra
	// "fora do ar" (só em DOWN), e onSuccess limpa a marca se o site voltar antes.
	if chk.DownSince == nil {
		chk.DownSince = &now
	}
	if chk.State != "DOWN" && chk.ConsecutiveFails < falhasParaConfirmar(chk.Tier) {
		chk.State = "SUSPEITO"
		c.log.Info("sitecheck: SUSPEITO", "site", chk.Name, "diag", res.Diagnosis,
			"falhas", chk.ConsecutiveFails, "confirma_em", falhasParaConfirmar(chk.Tier))
		return
	}
	wasDown := chk.State == "DOWN"
	chk.State = "DOWN"
	if !wasDown {
		c.notify(ctx, chk, res, "firing", *chk.DownSince)
		c.log.Info("sitecheck: DOWN confirmado", "site", chk.Name, "diag", res.Diagnosis, "falhas", chk.ConsecutiveFails)
	}
}

func (c *Checker) writeMetrics(ctx context.Context, chk store.SiteCheck, res Result) {
	up := 0.0
	if res.OK {
		up = 1
	}
	labels := map[string]string{"site": chk.Name, "url": chk.URL}
	// FASE QUE NÃO ACONTECEU NÃO É ZERO MILISSEGUNDO.
	//
	// Os tempos de fase vêm dos callbacks do httptrace (ver prober.go): se a resolução
	// de DNS, o handshake TLS ou o primeiro byte da resposta nunca ocorreram, o campo
	// fica no zero-value do struct. Gravar esse zero como medida escreve na série
	// "respondeu em 0 ms" para uma sondagem que não respondeu byte nenhum — e qualquer
	// avg/min/max sobre `synthetic.http.ttfb_ms` passa a misturar não-medida com medida
	// (medido em dev: 556 de 1237 amostras de ttfb_ms valiam 0, as MESMAS 556 com up=0).
	// Omitimos a fase ausente, exatamente como `cert_days_left` já fazia logo abaixo.
	// `total_ms` e `up` continuam sempre presentes: o total é cronometrado até no
	// fracasso (prober.go marca TotalMs nos dois caminhos) e `up` é o próprio veredito.
	pts := []chquery.MetricPoint{
		{Metric: "synthetic.http.total_ms", Labels: labels, Value: res.TotalMs},
		{Metric: "synthetic.http.up", Labels: labels, Value: up},
	}
	fases := []struct {
		metric string
		v      float64
	}{
		{"synthetic.http.dns_ms", res.DNSMs},
		{"synthetic.http.connect_ms", res.ConnectMs},
		{"synthetic.http.tls_ms", res.TLSMs},
		{"synthetic.http.ttfb_ms", res.TTFBMs},
	}
	for _, f := range fases {
		if f.v > 0 {
			pts = append(pts, chquery.MetricPoint{Metric: f.metric, Labels: labels, Value: f.v})
		}
	}
	if res.CertDaysLeft > 0 {
		pts = append(pts, chquery.MetricPoint{Metric: "synthetic.http.cert_days_left", Labels: labels, Value: float64(res.CertDaysLeft)})
	}
	if err := c.ch.InsertMetrics(ctx, chk.TenantID, pts); err != nil {
		c.log.Warn("sitecheck: gravando métricas", "err", err)
	}
}

// certExpiry alerta em 30/15/7 dias, uma vez por limiar (reseta ao renovar).
// `days` vem da própria sondagem (check http) ou do menor cert_days_left das
// páginas-filhas (check sitemap).
func (c *Checker) certExpiry(ctx context.Context, chk *store.SiteCheck, days int) {
	if days <= 0 || !chk.Alerting {
		return // sem TLS/não medido, ou filho silencioso (métricas sem alerta)
	}
	stage := 0
	switch {
	case days <= 7:
		stage = 7
	case days <= 15:
		stage = 15
	case days <= 30:
		stage = 30
	}
	if days > 30 {
		chk.CertAlertStage = 0 // renovado
		return
	}
	// alerta só ao cruzar um limiar mais apertado do que o último alertado.
	if stage != 0 && (chk.CertAlertStage == 0 || stage < chk.CertAlertStage) {
		rule := store.AlertRule{
			Name: "Certificado expirando: " + chk.Name, Metric: chk.URL,
			Agg: "cert_dias", ConditionOp: "≤", Threshold: float64(stage), Severity: "warning",
		}
		c.emit(ctx, chk, alerting.Notification{
			Rule:   rule,
			Labels: map[string]string{"site": chk.Name, "url": chk.URL, "diagnóstico": "cert_expiring"},
			Value:  float64(days), State: "firing",
		})
		chk.CertAlertStage = stage
		c.log.Info("sitecheck: certificado expirando", "site", chk.Name, "dias", days, "stage", stage)
	}
}

// notify envia DOWN/recuperação via o roteador de notificações (rotas/silêncios/agrupamento).
// Filhos de sitemap (alerting=false) só coletam métricas — a notificação fica a
// cargo do check-pai, que agrega ("N/M páginas fora"); evita 1 e-mail por página.
func (c *Checker) notify(ctx context.Context, chk *store.SiteCheck, res Result, state string, since time.Time) {
	if !chk.Alerting {
		return
	}
	// "Fora do ar: <nome>" e NÃO "Site DOWN: <nome>". O monitor de HTTP também
	// vigia API e endpoint interno — chamar tudo de "site" na linha que o
	// destinatário lê primeiro já rendeu um "errado" de quem recebeu o aviso.
	// O nome do check é o que identifica o alvo; a palavra "site" não acrescenta
	// nada e, na metade dos casos, mente.
	rule := store.AlertRule{
		Name: "Fora do ar: " + chk.Name, Metric: chk.URL,
		Agg: "status", ConditionOp: "≠", Threshold: float64(orInt(chk.ExpectStatus, 200)),
		Severity: "critical", Runbook: "",
	}
	c.emit(ctx, chk, alerting.Notification{
		Rule: rule,
		Labels: map[string]string{
			"site": chk.Name, "url": chk.URL, "diagnóstico": diagOrOK(res.Diagnosis),
		},
		Value: float64(res.Status), State: state, Since: since,
		Evidence: evidenciaComTentativas(chk, res, state, c.ultimasSondagens(ctx, chk, state)),
	})
}

// ultimasSondagens busca o histórico recente do check para a lista de tentativas
// da evidência. Best-effort: sem store (testes) ou com erro, a evidência sai sem a
// lista — nunca sem o aviso.
func (c *Checker) ultimasSondagens(ctx context.Context, chk *store.SiteCheck, state string) []store.SiteCheckResultView {
	if state != "firing" || c.st == nil || chk.ID == 0 {
		return nil
	}
	ultimas, err := c.st.SiteCheckHistory(ctx, chk.ID, 10)
	if err != nil {
		return nil
	}
	return ultimas
}

func diagOrOK(d string) string {
	if d == "" {
		return "recuperado"
	}
	return d
}

func orInt(v, d int) int {
	if v <= 0 {
		return d
	}
	return v
}

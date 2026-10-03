package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/textproto"
	"sync"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/alerting"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

const (
	// maxSendAttempts é o total de tentativas de envio por canal (1 inicial + 2 retries).
	maxSendAttempts = 3
	// deliverTotalBudget é o teto de tempo por notificação/canal somando todas as
	// tentativas e backoffs, para nunca travar o loop de flush indefinidamente.
	deliverTotalBudget = 30 * time.Second
	// maxConcurrentDeliveries limita quantos canais entregam em paralelo dentro de
	// um flush, isolando um canal lento sem abrir goroutines sem limite.
	maxConcurrentDeliveries = 4
)

// sendBackoff é o atraso antes de cada retry (backoff exponencial curto). O
// tamanho define os intervalos; tentativas além do último reutilizam o teto.
var sendBackoff = []time.Duration{200 * time.Millisecond, 800 * time.Millisecond}

// statusSemConfirmacao é o status gravado em notification_log quando o provedor
// aceitou a mensagem sem que a entrega seja confirmável. Fica entre `sent` e
// `error` — a coluna é TEXT livre, então não exige migração.
const statusSemConfirmacao = "unverified"

// isPermanent identifica falhas que não adianta repetir (ex.: SMTP 5xx —
// remetente/destinatário/credencial inválidos). Na dúvida trata como transitório.
//
// SemConfirmacao entra aqui porque a mensagem JÁ foi aceita: repetir não confirma
// nada e ainda duplica o aviso no aparelho de quem receber.
func isPermanent(err error) bool {
	var protoErr *textproto.Error
	if errors.As(err, &protoErr) {
		return protoErr.Code >= 500 && protoErr.Code < 600
	}
	var semConf *SemConfirmacao
	return errors.As(err, &semConf)
}

// sendWithRetry tenta o envio até maxSendAttempts vezes com backoff, respeitando
// um teto de tempo total (deliverTotalBudget) e abortando cedo em erros permanentes.
//
// Todos os provedores hoje entregam de forma SÍNCRONA: quando Send devolve nil, o
// destino já aceitou a mensagem. Existiu aqui uma camada de confirmação assíncrona
// para a Evolution API, que respondia "aceito" e só depois deixava o WhatsApp
// recusar — ela saiu junto com a Evolution, porque o wuzapi acusa a recusa na hora.
func sendWithRetry(ctx context.Context, s Sender, cfg map[string]any, msg Message) error {
	ctx, cancel := context.WithTimeout(ctx, deliverTotalBudget)
	defer cancel()
	var err error
	for attempt := 0; attempt < maxSendAttempts; attempt++ {
		if attempt > 0 {
			d := sendBackoff[min(attempt-1, len(sendBackoff)-1)]
			select {
			case <-ctx.Done():
				return err
			case <-time.After(d):
			}
		}
		// Cada tentativa ganha seu próprio timeout de envio, limitado pelo teto total.
		attemptCtx, attemptCancel := context.WithTimeout(ctx, defaultSendTimeout)
		err = s.Send(attemptCtx, cfg, msg)
		attemptCancel()
		if err == nil || isPermanent(err) || ctx.Err() != nil {
			return err
		}
	}
	return err
}

// Router implementa alerting.Notifier: entrega cada transição de alerta aos senders
// e registra o envio para auditoria. As rotas de notificação foram descontinuadas —
// o roteamento por severidade/labels e a janela de agrupamento saíram; hoje o alerta
// cai direto em TODOS os canais habilitados (comportamento "só canal").
type Router struct {
	st          *store.Store
	log         *slog.Logger
	baseURL     string
	hostDashUID string
	// aoEntregar recebe o resultado de cada entrega real (ex.: o vigia do WhatsApp,
	// que descobre por aqui que o envio parou). Nil = ninguém observa.
	aoEntregar func(tipo, canal string, err error)
}

func NewRouter(st *store.Store, log *slog.Logger, baseURL, hostDashUID string) *Router {
	return &Router{st: st, log: log, baseURL: baseURL, hostDashUID: hostDashUID}
}

// ObservarEntregas registra quem quer saber o resultado de cada entrega real de
// alerta. Chamar antes de o router começar a entregar.
func (r *Router) ObservarEntregas(f func(tipo, canal string, err error)) { r.aoEntregar = f }

func (r *Router) dashLink(labels map[string]string) string {
	if r.baseURL == "" {
		return ""
	}
	host := labels["host"]
	if host == "" {
		host = labels["hostname"]
	}
	if host != "" && r.hostDashUID != "" {
		return fmt.Sprintf("%s/#/d/%s?var-host=%s", r.baseURL, r.hostDashUID, host)
	}
	return r.baseURL + "/#/alerts"
}

// buildItem transforma uma transição do evaluator no AlertItem renderizável.
func (r *Router) buildItem(ctx context.Context, n alerting.Notification) AlertItem {
	item := AlertItem{
		RuleName:        n.Rule.Name,
		Metric:          n.Rule.Metric,
		State:           n.State,
		Severity:        n.Rule.Severity,
		Condition:       fmt.Sprintf("%s %s %g", n.Rule.Agg, n.Rule.ConditionOp, n.Rule.Threshold),
		Op:              n.Rule.ConditionOp,
		Threshold:       n.Rule.Threshold,
		Value:           n.Value,
		Labels:          n.Labels,
		Runbook:         n.Rule.Runbook,
		DashboardURL:    r.dashLink(n.Labels),
		Since:           n.Since,
		Flapping:        n.Flapping,
		SuppressedCount: n.SuppressedCount,
		Evidence:        n.Evidence,
	}
	// Resolve o nome amigável do servidor para EXIBIÇÃO (o link e a chave das métricas
	// continuam com o hostname técnico). Best-effort: falha silenciosa cai no técnico.
	host := n.Labels["host"]
	if host == "" {
		host = n.Labels["hostname"]
	}
	if host != "" {
		if disp, err := r.st.HostDisplayName(ctx, host); err == nil {
			item.HostDisplay = disp
		}
	}
	// Duração do incidente. Vale também para `nodata`: o alerta ficou aberto todo
	// esse tempo e a mensagem diz isso — sem prometer que ele acabou.
	if (n.State == alerting.StateResolved || n.State == alerting.StateNoData) && !n.Since.IsZero() {
		item.Duration = humanDuration(time.Since(n.Since))
	}
	return item
}

// NotifyChannels entrega uma transição DIRETAMENTE a um conjunto de canais, ignorando
// o casamento por rotas. Usado por origens que já sabem seus canais (ex.: um website
// com canais próprios escolhidos no formulário). Registra na auditoria.
// Entrega imediata (sem janela de agrupamento) — cada evento de site é um só item.
func (r *Router) NotifyChannels(ctx context.Context, n alerting.Notification, channelIDs []int64) {
	if len(channelIDs) == 0 {
		return
	}
	msg := render(n.Rule.Name, []AlertItem{r.buildItem(ctx, n)})
	r.deliverToChannels(ctx, channelIDs, n.Rule.Name, msg)
}

// Notify recebe uma transição do evaluator e entrega aos canais da REGRA. A seleção de
// canais é por regra (as rotas foram descontinuadas): se a regra lista canais, entrega
// só àqueles que estão habilitados; se a lista está vazia, entrega a TODOS os canais
// habilitados (padrão, compatível com regras antigas). Registra na auditoria.
// Entrega imediata, sem janela de agrupamento — cada transição vira uma mensagem única.
func (r *Router) Notify(ctx context.Context, n alerting.Notification) {
	enabled, err := r.enabledChannelIDs(ctx)
	if err != nil {
		r.log.Warn("notify: listando canais", "err", err)
		return
	}
	// Interseção com a seleção da regra (vazio = todos os habilitados).
	escolhaExplicita := len(n.Rule.ChannelIDs) > 0
	ids := enabled
	if escolhaExplicita {
		want := make(map[int64]bool, len(n.Rule.ChannelIDs))
		for _, id := range n.Rule.ChannelIDs {
			want[id] = true
		}
		ids = ids[:0:0]
		for _, id := range enabled {
			if want[id] {
				ids = append(ids, id)
			}
		}
		// A seleção da regra inclui os canais pessoais que foram marcados: eles ficam
		// fora de `enabled` (para não entrarem no fan-out geral), então entram aqui.
		ids = append(ids, r.pessoaisSelecionados(ctx, n.Rule.ChannelIDs)...)
	}
	msg := render(n.Rule.Name, []AlertItem{r.buildItem(ctx, n)})

	// Canais pessoais por servidor: quem tem notify=true no host do alerta recebe no
	// seu canal. Só vale quando a regra NÃO escolheu canais — regra com escolha
	// explícita entrega exatamente a quem foi marcado, e nada além disso. Marcar três
	// canais e a mensagem chegar a um quarto é o tipo de surpresa que faz ninguém
	// confiar na tela. Alerta sem host (métrica de app) não tem canal pessoal.
	var personal []int64
	if !escolhaExplicita {
		personal = r.personalChannelIDs(ctx, n.Labels["host"], ids)
	}
	all := append(append([]int64{}, ids...), personal...)
	if len(all) == 0 {
		r.log.Info("notify: nenhum canal habilitado para o alerta", "rule", n.Rule.Name, "severity", n.Rule.Severity, "selecionados", len(n.Rule.ChannelIDs))
		return
	}
	r.deliverToChannels(ctx, all, n.Rule.Name, msg)
}

// enabledChannelIDs devolve os IDs dos canais habilitados que entram no fan-out das
// regras. EXCLUI canais pessoais (user_id preenchido): um canal pessoal só recebe os
// alertas dos servidores do seu dono, nunca todos os alertas da regra.
func (r *Router) enabledChannelIDs(ctx context.Context) ([]int64, error) {
	chs, err := r.st.ListChannels(ctx, true)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(chs))
	for _, ch := range chs {
		if ch.Enabled && ch.UserID == nil {
			ids = append(ids, ch.ID)
		}
	}
	return ids, nil
}

// personalChannelIDs devolve os canais pessoais que devem receber o alerta de `host`,
// excluindo os que já estão em `exclude` (canais da regra já selecionados). Best-effort:
// erro de banco não impede a entrega aos canais da regra.
// pessoaisSelecionados devolve, entre os IDs escolhidos na regra, os que são canais
// PESSOAIS habilitados. Existem porque enabledChannelIDs deixa os pessoais de fora
// (para não entrarem no fan-out de toda regra) — mas se o admin marcou um deles na
// regra, ele tem de receber: o que a tela mostra marcado é o que recebe.
func (r *Router) pessoaisSelecionados(ctx context.Context, escolhidos []int64) []int64 {
	chs, err := r.st.ListChannels(ctx, true)
	if err != nil {
		r.log.Warn("notify: listando canais pessoais da regra", "err", err)
		return nil
	}
	want := make(map[int64]bool, len(escolhidos))
	for _, id := range escolhidos {
		want[id] = true
	}
	out := make([]int64, 0, len(escolhidos))
	for _, ch := range chs {
		if ch.Enabled && ch.UserID != nil && want[ch.ID] {
			out = append(out, ch.ID)
		}
	}
	return out
}

func (r *Router) personalChannelIDs(ctx context.Context, host string, exclude []int64) []int64 {
	if host == "" {
		return nil
	}
	ids, err := r.st.PersonalChannelIDsForHost(ctx, host)
	if err != nil {
		r.log.Warn("notify: canais pessoais do host", "host", host, "err", err)
		return nil
	}
	skip := make(map[int64]bool, len(exclude))
	for _, id := range exclude {
		skip[id] = true
	}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !skip[id] {
			out = append(out, id)
		}
	}
	return out
}

// deliverToChannels envia uma mensagem a um conjunto de canais (usado pelo Notify
// e pela entrega direta NotifyChannels). Os canais entregam concorrentemente (limitado a
// maxConcurrentDeliveries): como cada delivery tem teto de tempo próprio, um canal
// lento/pendurado não congela os demais. O pool pgx do store é seguro para uso
// concorrente, então a gravação de auditoria em cada deliver permanece correta.
func (r *Router) deliverToChannels(ctx context.Context, channelIDs []int64, routeName string, msg Message) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrentDeliveries)
	for _, chID := range channelIDs {
		id := chID
		ch, err := r.st.ChannelByID(ctx, id)
		if err != nil {
			r.log.Warn("notify: canal não encontrado", "id", id, "err", err)
			continue
		}
		if !ch.Enabled {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r.deliver(ctx, &id, ch, routeName, msg)
		}()
	}
	wg.Wait()
}

// effectiveConfig devolve o config a passar ao sender. Para WhatsApp, preenche os
// campos de conexão (base_url/token) a partir da Integração WhatsApp global quando o
// canal não os define — assim o canal guarda só os destinatários e o wuzapi é
// configurado uma vez. Um canal que traga esses campos ainda sobrescreve o global.
func (r *Router) effectiveConfig(ctx context.Context, ch store.NotificationChannel) map[string]any {
	if ch.Type != "whatsapp" {
		return ch.Config
	}
	cfg := map[string]any{}
	for k, v := range ch.Config {
		cfg[k] = v
	}
	wa, err := r.st.GetWhatsAppIntegration(ctx)
	if err != nil {
		r.log.Warn("notify: lendo integração whatsapp", "err", err)
		return cfg
	}
	fill := func(key, val string) {
		if s, _ := cfg[key].(string); s == "" && val != "" {
			cfg[key] = val
		}
	}
	fill("base_url", wa.BaseURL)
	fill("token", wa.Token)
	return cfg
}

// deliver envia por um canal e registra o resultado na auditoria.
//
// São TRÊS resultados, não dois. `sent` é afirmação: o provedor confirmou a entrega
// pelo endereçamento que sabidamente chega. `unverified` é o meio-termo honesto — o
// provedor aceitou e ninguém confirma que chegou. `error` é recusa. Misturar os dois
// primeiros foi o que fez o histórico dizer "enviado" para mensagem que nunca saiu.
func (r *Router) deliver(ctx context.Context, channelID *int64, ch store.NotificationChannel, routeName string, msg Message) {
	status, detail := "sent", ""
	s, err := senderFor(ch.Type)
	if err == nil {
		err = sendWithRetry(ctx, s, r.effectiveConfig(ctx, ch), msg)
	}
	if r.aoEntregar != nil {
		r.aoEntregar(ch.Type, ch.Name, err)
	}
	var semConf *SemConfirmacao
	switch {
	case err == nil:
		r.log.Info("notify: enviado", "channel", ch.Name, "type", ch.Type, "alerts", len(msg.Alerts))
	case errors.As(err, &semConf):
		status, detail = statusSemConfirmacao, err.Error()
		r.log.Warn("notify: aceito sem confirmação", "channel", ch.Name, "type", ch.Type, "motivo", err)
	default:
		status, detail = "error", err.Error()
		r.log.Warn("notify: falha no envio", "channel", ch.Name, "type", ch.Type, "err", err)
	}
	// O destino sai do config EFETIVO (o mesmo que o sender usou), para o
	// histórico registrar o número que de fato recebeu — e não o que estava
	// escrito no canal antes de a integração preencher o que faltava.
	destino := DestinoDo(ch.Type, r.effectiveConfig(ctx, ch))
	if err := r.st.LogNotification(ctx, channelID, ch.Type, ch.Name, destino, routeName, msg.Subject, len(msg.Alerts), status, detail); err != nil {
		r.log.Warn("notify: gravando auditoria", "err", err)
	}
}

// SendTest envia uma mensagem de teste imediata por um canal (botão "testar").
func (r *Router) SendTest(ctx context.Context, channelID int64) error {
	ch, err := r.st.ChannelByID(ctx, channelID)
	if err != nil {
		return err
	}
	msg := render("", []AlertItem{{
		RuleName: "Teste de canal", Metric: "revoada.test", State: "firing", Severity: "info",
		Value: 1, Labels: map[string]string{"origem": "botão testar"},
		DashboardURL: r.baseURL,
	}})
	// Mensagem de teste é uma confirmação simples e amigável (não um alerta real).
	msg.Subject = "🔔 Teste de canal, Revoada"
	msg.Text = fmt.Sprintf("Teste de notificação do Revoada.\nSe você recebeu esta mensagem, o canal \"%s\" está funcionando. ✅", ch.Name)
	if r.baseURL != "" {
		msg.Text += "\nAbrir no painel: " + r.baseURL
	}
	s, err := senderFor(ch.Type)
	if err != nil {
		return err
	}
	err = s.Send(ctx, r.effectiveConfig(ctx, ch), msg)
	id := channelID
	status, detail := "sent", ""
	if err != nil {
		status, detail = "error", err.Error()
		var semConf *SemConfirmacao
		if errors.As(err, &semConf) {
			status = statusSemConfirmacao
		}
	}
	_ = r.st.LogNotification(ctx, &id, ch.Type, ch.Name, DestinoDo(ch.Type, r.effectiveConfig(ctx, ch)), "teste", msg.Subject, 1, status, detail)
	return err
}

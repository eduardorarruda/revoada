// Package hostadmin implementa a exclusão completa de um servidor: desinstala o
// agente na máquina (via SSH) e apaga TODOS os dados daquele host no painel
// (Postgres + ClickHouse). É a contraparte do provisionamento (Fase G).
package hostadmin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/audit"
	"github.com/eduardorarruda/revoada/server/internal/chquery"
	"github.com/eduardorarruda/revoada/server/internal/logs"
	"github.com/eduardorarruda/revoada/server/internal/provision"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// chTables são as tabelas do ClickHouse com dados por host (todas guardam o host em
// labels['host']). As materialized views (metrics_1m_mv/1h_mv) só reagem a INSERT,
// então o DELETE precisa rodar em cada tabela BASE — apagar de `metrics` não
// propaga para os rollups.
var chTables = []string{"metrics", "metrics_1m", "metrics_1h", "events", "logs", "spans"}

// tenantPadrao é o único tenant existente hoje. Fica como constante de pacote (e não
// literal espalhado) porque ela entra no PREDICADO de exclusão: no dia em que houver
// um segundo cliente, o compilador mostra todos os lugares que precisam do tenant de
// verdade, em vez de apagar dado alheio em silêncio.
const tenantPadrao = "default"

// sshTimeout limita a etapa de desinstalação remota (conectar + rodar). Um host
// morto não pode travar a exclusão indefinidamente.
const sshTimeout = 40 * time.Second

// chMutationWait é quanto esperamos as mutations do ClickHouse concluírem antes de
// responder. Curto de propósito: host pequeno conclui em segundos e a resposta sai
// honesta ("apagado"); volume grande volta como pendente, com o mutation_id.
const chMutationWait = 6 * time.Second

type Handler struct {
	st     *store.Store
	ch     *chquery.Client
	runner provision.Runner
	log    *slog.Logger
}

func NewHandler(st *store.Store, ch *chquery.Client, runner provision.Runner, log *slog.Logger) *Handler {
	return &Handler{st: st, ch: ch, runner: runner, log: log}
}

type sshCreds struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	AuthType string `json:"auth_type"`
	Secret   string `json:"secret"`
}

type deleteReq struct {
	DisplayName       string    `json:"display_name"`
	ProvisionTargetID int64     `json:"provision_target_id"`
	SSH               *sshCreds `json:"ssh"`
	SkipSSH           bool      `json:"skip_ssh"`
	// AutoUninstall pede ao PRÓPRIO agente que se remova, pelo canal de consulta que
	// ele já usa de hora em hora. É a terceira via entre "tenho SSH" e "deixa o agente
	// rodando órfão para sempre".
	AutoUninstall bool `json:"auto_uninstall"`
}

// quote escapa uma string para uso seguro em SQL do ClickHouse.
func quote(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "'", "\\'")
	return "'" + s + "'"
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Delete apaga o servidor por completo. POST /api/hosts/{hostname}/delete (admin).
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hostname := r.PathValue("hostname")
	if strings.TrimSpace(hostname) == "" {
		http.Error(w, "hostname é obrigatório", http.StatusBadRequest)
		return
	}
	const tenant = tenantPadrao

	var req deleteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "corpo inválido", http.StatusBadRequest)
		return
	}

	// Resolve o alvo de provisionamento (se informado), para credencial guardada e
	// para apagar a serverkey/linha certa.
	var target *store.ProvisionTarget
	if req.ProvisionTargetID > 0 {
		t, err := h.st.GetProvisionTarget(ctx, req.ProvisionTargetID)
		if err == nil {
			target = &t
		} else {
			h.log.Warn("alvo de provisionamento não encontrado ao apagar host", "id", req.ProvisionTargetID, "err", err)
		}
	}
	// Sem id vindo do frontend, mas precisando de SSH: acha o alvo pelo hostname REAL
	// (o mais recente tem a credencial para a desinstalação). Assim a exclusão casa a
	// credencial guardada mesmo quando o cliente não manda o provision_target_id.
	if target == nil && !req.SkipSSH && req.SSH == nil {
		if list, lerr := h.st.ProvisionTargetsByHostname(ctx, tenant, hostname); lerr == nil && len(list) > 0 {
			target = &list[0]
		} else if lerr != nil {
			h.log.Warn("buscar alvo por hostname ao apagar host falhou", "host", hostname, "err", lerr)
		}
	}

	// Guarda: se não há como acessar por SSH (sem credencial guardada e sem credencial
	// no corpo) e o usuário NÃO optou por apagar só os dados nem por pedir ao agente,
	// erro claro — evita apagar os dados achando que o agente também foi removido.
	hasSSH := target != nil || req.SSH != nil
	if !req.SkipSSH && !req.AutoUninstall && !hasSSH {
		http.Error(w, "informe a credencial SSH para desinstalar o agente, peça a auto-desinstalação, ou marque \"apagar só os dados\"", http.StatusBadRequest)
		return
	}

	// 1) Cortar ingestão primeiro (para o host não reaparecer durante a exclusão).
	names := dedupNonEmpty(hostname, req.DisplayName)

	// Auto-desinstalação: a chave é REVOGADA (corta a ingestão na mesma hora) mas a
	// LINHA sobrevive — sem ela o agente não consegue voltar para receber a ordem.
	// Ver store.OrderAgentUninstall e o comentário de agent_uninstalls no schema.
	var autoOrdenadas int
	if req.AutoUninstall {
		autoOrdenadas = h.ordenarAutoRemocao(ctx, tenant, hostname, names, target, quemPediu(r))
	}

	var agentsRemoved int64
	var err error
	if !req.AutoUninstall {
		agentsRemoved, err = h.st.DeleteAgentsByHost(ctx, tenant, names)
		if err != nil {
			h.log.Error("apagar serverkeys do host falhou", "host", hostname, "err", err)
		}
	}
	// Purga também as serverkeys guardadas em TODOS os alvos daquele hostname (o
	// DeleteAgentsByHost casa por agents.hostname; alvos antigos podem ter chaves
	// cujo hostname não bate). Set evita apagar/contar a mesma chave duas vezes.
	deletedKeys := map[string]bool{}
	allTargets, terr := h.st.ProvisionTargetsByHostname(ctx, tenant, hostname)
	if terr != nil {
		h.log.Warn("listar alvos por hostname ao purgar serverkeys falhou", "host", hostname, "err", terr)
	}
	if target != nil {
		allTargets = append(allTargets, *target)
	}
	for _, t := range allTargets {
		if t.Serverkey == "" || deletedKeys[t.Serverkey] {
			continue
		}
		deletedKeys[t.Serverkey] = true
		if req.AutoUninstall {
			continue // a chave do alvo já entrou na ordem de auto-remoção; apagá-la aqui a mataria
		}
		if derr := h.st.DeleteAgent(ctx, t.Serverkey); derr == nil {
			agentsRemoved++
		}
	}

	// 2) Desinstalar o agente na máquina (best-effort). Não aborta o resto.
	sshResult := "pulado"
	switch {
	case req.AutoUninstall:
		sshResult = "auto-desinstalação pedida ao agente"
	case !req.SkipSSH:
		sshResult = h.uninstallAgent(ctx, target, req.SSH)
	}

	// 3) Apagar os demais dados no Postgres.
	//
	// Cada passo que falha entra em `pgFalhas`, e é isso que a resposta reporta. Antes
	// daqui, estes DELETEs só escreviam no log e a resposta devolvia
	// `postgres_deleted: true` fixo — a mesma mentira que o lado do ClickHouse já
	// tinha deixado de contar: o operador lia "apagado" com o limiar, o serviço
	// descoberto ou o escopo da regra intactos no banco.
	var pgFalhas []string
	falhou := func(passo string, err error) {
		if err == nil {
			return
		}
		h.log.Error("apagar dados do host falhou", "passo", passo, "host", hostname, "err", err)
		pgFalhas = append(pgFalhas, passo)
	}

	falhou("host_services", h.st.DeleteHostServices(ctx, tenant, hostname))
	falhou("host_thresholds", h.st.DeleteHostThresholds(ctx, tenant, hostname))
	rulesUpdated, err := h.st.RemoveHostFromAlertRules(ctx, hostname)
	falhou("alert_rules", err)
	// Permissões e grupos: sem isto, quem podia ver/editar/notificar este servidor
	// voltava a poder assim que o hostname reaparecesse.
	permsRemoved, err := h.st.DeleteHostPerms(ctx, hostname)
	falhou("user_server_perms", err)
	groupsRemoved, err := h.st.DeleteHostFromGroups(ctx, hostname)
	falhou("server_group_hosts", err)
	// Alertas abertos deste host são encerrados em silêncio. Tirar o hostname do
	// escopo das regras (acima) NÃO fecha o que já estava aberto: a regra continua
	// habilitada, o avaliador simplesmente para de receber dado, e o alerta órfão
	// vira "📡 SEM DADOS" no WhatsApp de um servidor que o operador acabou de apagar.
	alertsClosed, err := h.st.CloseAlertsOfHost(ctx, hostname)
	falhou("alert_events", err)
	// Remove TODOS os alvos daquele hostname (não deixa órfãos no cofre).
	targetsRemoved, derr := h.st.DeleteProvisionTargetsByHostname(ctx, tenant, hostname)
	falhou("provision_targets", derr)
	if _, perr := h.st.DeleteOrphanAgentUpdatePolicies(ctx); perr != nil {
		falhou("agent_update_policy", perr)
	}
	if err := h.st.DeleteHost(ctx, tenant, hostname); err != nil {
		h.log.Error("apagar host do inventário falhou", "host", hostname, "err", err)
		http.Error(w, "falha ao apagar o host do inventário", http.StatusInternalServerError)
		return
	}

	// 4) Apagar os dados no ClickHouse (mutations assíncronas) — e CONFERIR.
	states, chRows, chErrs := h.purgeClickHouse(ctx, hostname)

	// data_deleted só é true quando as seis mutations concluíram sem erro. Dizer
	// "apagado" com mutation pendente ou falhada é a mentira que este campo produzia.
	var pending, failedMut []string
	for _, st := range states {
		switch {
		case st.FailReason != "":
			failedMut = append(failedMut, st.Table)
		case !st.Done:
			pending = append(pending, st.Table)
		}
	}
	done := len(chErrs) == 0 && len(pending) == 0 && len(failedMut) == 0

	// A trilha guarda o NÚMERO e o estado: meses depois é o que distingue uma exclusão
	// concluída de uma que ficou pela metade.
	// 5) Agendar a varredura de rescaldo (ver agendarRescaldo).
	h.agendarRescaldo(hostname)

	audit.Annotate(ctx, "deleted_rows", chRows)
	audit.Annotate(ctx, "clickhouse_concluido", done)
	if len(pending) > 0 {
		audit.Annotate(ctx, "mutations_pendentes", pending)
	}
	if len(failedMut) > 0 {
		audit.Annotate(ctx, "mutations_com_falha", failedMut)
	}
	if len(pgFalhas) > 0 {
		audit.Annotate(ctx, "postgres_com_falha", pgFalhas)
	}
	if alertsClosed > 0 {
		audit.Annotate(ctx, "alertas_encerrados", alertsClosed)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data_deleted":     done && len(pgFalhas) == 0,
		"ssh":              sshResult,
		"auto_uninstall":   autoOrdenadas,
		"agents_removed":   agentsRemoved,
		"targets_removed":  targetsRemoved,
		"rules_updated":    rulesUpdated,
		"perms_removed":    permsRemoved,
		"groups_removed":   groupsRemoved,
		"alerts_closed":    alertsClosed,
		"ch_tables":        len(chTables),
		"ch_errors":        chErrs,
		"ch_rows":          chRows,
		"ch_mutations":     states,
		"ch_pending":       pending,
		"ch_failed":        failedMut,
		"postgres_deleted": len(pgFalhas) == 0,
		"postgres_failed":  pgFalhas,
		"resweep_seconds":  int(janelaRescaldo.Seconds()),
		"not_purged":       notPurgedHost(),
	})
}

// uninstallAgent conecta por SSH (credencial guardada do alvo, ou a informada no
// corpo) e roda a desinstalação inline. Devolve um resumo legível ("ok"/"falhou: …"
// /"pulado (sem credencial)"). Best-effort: nunca propaga erro.
func (h *Handler) uninstallAgent(ctx context.Context, target *store.ProvisionTarget, creds *sshCreds) string {
	var t provision.Target
	switch {
	case target != nil:
		secret, err := provision.DecryptFromBase64(target.SecretBlob)
		if err != nil {
			return "falhou: não foi possível abrir a credencial do cofre"
		}
		t = provision.NewTarget(target.Host, target.SSHPort, target.SSHUser,
			provision.AuthType(target.AuthType), secret, target.HostKeyFP)
	case creds != nil:
		port := creds.Port
		if port == 0 {
			port = 22
		}
		t = provision.NewTarget(strings.TrimSpace(creds.Host), port, strings.TrimSpace(creds.User),
			provision.AuthType(strings.TrimSpace(creds.AuthType)), []byte(creds.Secret), "")
	default:
		return "pulado (sem credencial)"
	}

	sctx, cancel := context.WithTimeout(ctx, sshTimeout)
	defer cancel()
	sess, err := h.runner.Connect(sctx, t)
	if err != nil {
		return "falhou: conexão SSH, " + redactSecret(err.Error(), creds)
	}
	defer func() { _ = sess.Close() }()

	out, err := sess.Run(sctx, provision.BuildUninstallCommand())
	if err != nil {
		return "falhou: " + redactSecret(trim(out), creds)
	}
	if !strings.Contains(out, "UNINSTALL_OK") {
		return "falhou: desinstalação não confirmou (" + redactSecret(trim(out), creds) + ")"
	}
	return "ok"
}

// purgeClickHouse dispara um ALTER … DELETE por host em cada tabela base e CONFERE
// o resultado.
//
// Antes, este código disparava os seis comandos e devolvia `"data_deleted": true` no
// instante em que eles foram ENFILEIRADOS — sem consultar system.mutations uma única
// vez. Mutation do ClickHouse falha em silêncio depois de aceita (part corrompida,
// disco cheio, latest_fail_reason preenchido), e o painel já teria dito ao operador
// que o servidor do cliente foi apagado. Agora contamos as linhas antes, disparamos,
// e acompanhamos as mutations por alguns segundos; o que ficar pendente sai na
// resposta com o mutation_id, para o operador conferir depois.
func (h *Handler) purgeClickHouse(ctx context.Context, hostname string) (states []logs.MutationState, rows int64, failed []string) {
	// tenant_id no predicado: o schema É multi-tenant (o tenant vem pela serverkey na
	// ingestão), ainda que hoje só exista o "default". Sem ele, apagar `web01` de um
	// cliente levaria junto os logs, spans e métricas do `web01` de OUTRO — e esse é o
	// tipo de erro que só aparece no dia em que o segundo cliente entra.
	pred := "tenant_id=" + quote(tenantPadrao) + " AND labels['host']=" + quote(hostname)

	var sent []string // tabelas cujo ALTER foi ACEITO — só essas têm mutation a conferir
	for _, tbl := range chTables {
		// Contagem por tabela: é o que transforma "apaguei" numa afirmação verificável
		// (e o que a trilha de auditoria guarda como deleted_rows).
		var naTabela int64
		if r, err := h.ch.QueryJSON(ctx, fmt.Sprintf("SELECT count() AS c FROM %s WHERE %s", tbl, pred)); err != nil {
			h.log.Warn("contagem antes do DELETE falhou", "table", tbl, "host", hostname, "err", err)
			naTabela = -1 // não sabemos: manda o DELETE assim mesmo, é a opção segura
		} else if len(r) > 0 {
			naTabela = asInt64(r[0]["c"])
			rows += naTabela
		}
		// Tabela sem uma linha sequer do host NÃO recebe mutation.
		//
		// `ALTER … DELETE` reescreve todos os parts mesmo quando não apaga nada:
		// medido em dev, um host com ZERO linhas custou 24 partes mutadas e 13,97 MiB
		// reescritos, 1,1 s. Essas mutations disputam o mesmo pool de merge da
		// ingestão, então a conta de apagar um servidor era cobrada de TODOS os
		// outros. E o caso "zero linhas" é o caso NORMAL: das seis tabelas, a maioria
		// nunca viu aquele host, e os dois passes de rescaldo repetem a rodada.
		if naTabela == 0 {
			continue
		}
		if err := h.ch.Exec(ctx, fmt.Sprintf("ALTER TABLE %s DELETE WHERE %s", tbl, pred)); err != nil {
			h.log.Error("ALTER DELETE no ClickHouse falhou", "table", tbl, "host", hostname, "err", err)
			failed = append(failed, tbl)
			continue
		}
		sent = append(sent, tbl)
	}

	// Acompanha as mutations disparadas (mesmo helper do expurgo de logs). Prazo curto:
	// a exclusão de um host não pode segurar a requisição, então o que não concluir
	// volta como pendente — com o id para consulta posterior.
	deadline := time.Now().Add(chMutationWait)
	for {
		states = states[:0]
		pending := false
		// Tabela cujo comando nem foi aceito não tem mutation nossa: perguntar a
		// system.mutations traria a mutation ANTERIOR (de outra exclusão) e o painel
		// diria "concluído" para uma tabela em que nada foi apagado.
		for _, tbl := range failed {
			states = append(states, logs.MutationState{Table: tbl, FailReason: "o comando de exclusão não foi aceito pelo ClickHouse"})
		}
		for _, tbl := range sent {
			// Casa pelo HOSTNAME dentro do comando, não por "a mais recente da tabela":
			// rescaldos e exclusões concorrentes disputam as mesmas seis tabelas, e ler
			// a mutation do vizinho já concluída faria o painel dizer "apagado" para
			// dados que continuam inteiros. Ver logs.MutationStatusDe.
			st, err := logs.MutationStatusDe(ctx, h.ch, tbl, hostname)
			if err != nil {
				h.log.Warn("consultar system.mutations falhou", "table", tbl, "err", err)
				st = logs.MutationState{Table: tbl, FailReason: "não foi possível consultar system.mutations"}
			}
			states = append(states, st)
			if !st.Done && st.FailReason == "" {
				pending = true
			}
		}
		if !pending || time.Now().After(deadline) || ctx.Err() != nil {
			return states, rows, failed
		}
		select {
		case <-ctx.Done():
			return states, rows, failed
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// asInt64 lê a contagem do ClickHouse (UInt64 vem como string no JSONEachRow).
func asInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	case json.Number:
		n, _ := t.Int64()
		return n
	}
	return 0
}

// notPurgedHost declara o que a exclusão do servidor NÃO alcança. Exclusão que não
// alcança o backup é adiamento, não exclusão — e é onde o procedimento deixa de
// cumprir um pedido de LGPD.
func notPurgedHost() []map[string]string {
	return []map[string]string{
		{"store": "backup diário no MinIO (ClickHouse + pg_dump)", "retention": "sem expiração automática configurada",
			"note": "cópia SEM cifra dos dados deste servidor; precisa ser tratada fora do painel."},
		{"store": "notification_log (Postgres)", "retention": "120 dias (REVOADA_RETENTION_DAYS)",
			"note": "as mensagens enviadas sobre este servidor continuam gravadas até a poda."},
		{"store": "audit_log (Postgres)", "retention": "permanente",
			"note": "por projeto: a trilha guarda o hostname da própria exclusão."},
		{"store": "system.query_log do ClickHouse", "retention": "3 dias",
			"note": "consultas recentes que citam este servidor continuam registradas."},
		{"store": "alert_events (Postgres)", "retention": "poda por tempo (REVOADA_RETENTION_DAYS)",
			"note": "os alertas abertos foram encerrados agora, mas o HISTÓRICO de incidentes deste servidor continua gravado até a poda."},
	}
}

func dedupNonEmpty(vals ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func trim(s string) string {
	s = strings.TrimSpace(s)
	const max = 400
	if len(s) > max {
		return s[len(s)-max:]
	}
	return s
}

// redactSecret evita ecoar a senha/chave SSH informada em mensagens de erro.
func redactSecret(s string, creds *sshCreds) string {
	if creds != nil && creds.Secret != "" {
		s = strings.ReplaceAll(s, creds.Secret, "***")
	}
	return s
}

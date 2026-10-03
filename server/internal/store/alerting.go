package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// AlertRule é uma regra de alerta.
type AlertRule struct {
	ID      int64             `json:"id"`
	Name    string            `json:"name"`
	Metric  string            `json:"metric"`
	Filters map[string]string `json:"filters"`
	// Hosts restringe a regra a servidores específicos (por hostname). Vazio = regra
	// global: vale para todos os servidores que reportam a métrica. Com um ou mais
	// hostnames, a regra só vigia esses servidores.
	Hosts              []string `json:"hosts"`
	Agg                string   `json:"agg"`
	ConditionOp        string   `json:"condition_op"`
	Threshold          float64  `json:"threshold"`
	WindowSeconds      int      `json:"window_seconds"`
	ForSeconds         int      `json:"for_seconds"`
	Severity           string   `json:"severity"`
	Runbook            string   `json:"runbook"`
	EscalationPolicyID *int64   `json:"escalation_policy_id"`
	// ChannelIDs são os canais de notificação que ESTA regra avisa quando dispara.
	// Vazio = todos os canais habilitados (comportamento padrão, compatível com regras
	// antigas). Com um ou mais IDs, a regra entrega só àqueles (interseção com os
	// habilitados). O casamento por rotas foi descontinuado — a seleção agora é por regra.
	ChannelIDs []int64 `json:"channel_ids"`
	Enabled    bool    `json:"enabled"`
}

func (s *Store) CreateAlertRule(ctx context.Context, r AlertRule) (int64, error) {
	f, _ := json.Marshal(r.Filters)
	h, _ := json.Marshal(nonNilStrings(r.Hosts))
	ch, _ := json.Marshal(orInts(r.ChannelIDs))
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO alert_rules (name, metric, filters, hosts, agg, condition_op, threshold, window_seconds, for_seconds, severity, runbook, escalation_policy_id, channel_ids)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING id`,
		r.Name, r.Metric, f, h, orStr(r.Agg, "avg"), orStr(r.ConditionOp, ">"), r.Threshold,
		orInt(r.WindowSeconds, 300), nonNeg(r.ForSeconds), orStr(r.Severity, "warning"), r.Runbook, r.EscalationPolicyID, ch).Scan(&id)
	return id, err
}

func (s *Store) ListAlertRules(ctx context.Context) ([]AlertRule, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, metric, filters, hosts, agg, condition_op, threshold, window_seconds, for_seconds, severity, COALESCE(runbook,''), escalation_policy_id, COALESCE(channel_ids,'[]'), enabled
		FROM alert_rules ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertRule
	for rows.Next() {
		var r AlertRule
		var f, h, ch []byte
		if err := rows.Scan(&r.ID, &r.Name, &r.Metric, &f, &h, &r.Agg, &r.ConditionOp, &r.Threshold,
			&r.WindowSeconds, &r.ForSeconds, &r.Severity, &r.Runbook, &r.EscalationPolicyID, &ch, &r.Enabled); err != nil {
			return nil, err
		}
		r.Filters = map[string]string{}
		_ = json.Unmarshal(f, &r.Filters)
		r.Hosts = []string{}
		_ = json.Unmarshal(h, &r.Hosts)
		r.ChannelIDs = []int64{}
		_ = json.Unmarshal(ch, &r.ChannelIDs)
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateAlertRule edita uma regra existente (o "U" do CRUD).
func (s *Store) UpdateAlertRule(ctx context.Context, id int64, r AlertRule) error {
	f, _ := json.Marshal(r.Filters)
	h, _ := json.Marshal(nonNilStrings(r.Hosts))
	ch, _ := json.Marshal(orInts(r.ChannelIDs))
	ct, err := s.pool.Exec(ctx, `
		UPDATE alert_rules SET name=$2, metric=$3, filters=$4, hosts=$5, agg=$6, condition_op=$7,
			threshold=$8, window_seconds=$9, for_seconds=$10, severity=$11, runbook=$12,
			escalation_policy_id=$13, channel_ids=$14, enabled=$15
		WHERE id=$1`,
		id, r.Name, r.Metric, f, h, orStr(r.Agg, "avg"), orStr(r.ConditionOp, ">"), r.Threshold,
		orInt(r.WindowSeconds, 300), nonNeg(r.ForSeconds), orStr(r.Severity, "warning"), r.Runbook,
		r.EscalationPolicyID, ch, r.Enabled)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteAlertRule(ctx context.Context, id int64) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM alert_rules WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AlertEvent é uma instância de alerta (disparo).
type AlertEvent struct {
	ID       int64  `json:"id"`
	RuleID   int64  `json:"rule_id"`
	RuleName string `json:"rule_name"`
	// Metric vem da regra no mesmo JOIN: é o que deixa a TV escrever o valor com
	// unidade ("12 min sem sinal", "94%") sem uma consulta a mais por chamada.
	Metric      string            `json:"metric,omitempty"`
	Fingerprint string            `json:"fingerprint"`
	Labels      map[string]string `json:"labels"`
	State       string            `json:"state"`
	Value       float64           `json:"value"`
	Severity    string            `json:"severity"`
	StartedAt   time.Time         `json:"started_at"`
	EndedAt     *time.Time        `json:"ended_at"`
	AckedBy     *string           `json:"acked_by"`
	// ResolvedBy só vem preenchido quando alguém encerrou o alerta à mão.
	ResolvedBy *string `json:"resolved_by"`
	Flapping   bool    `json:"flapping"`
}

// ActiveAlertByFingerprint devolve o alerta ativo (não resolvido) de um fingerprint.
func (s *Store) ActiveAlertByFingerprint(ctx context.Context, fp string) (AlertEvent, error) {
	var e AlertEvent
	err := s.pool.QueryRow(ctx,
		`SELECT id, rule_id, value, severity, started_at FROM alert_events WHERE fingerprint=$1 AND ended_at IS NULL LIMIT 1`, fp,
	).Scan(&e.ID, &e.RuleID, &e.Value, &e.Severity, &e.StartedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	e.Fingerprint = fp
	return e, err
}

// OpenAlert cria um evento de alerta ativo (firing).
func (s *Store) OpenAlert(ctx context.Context, e AlertEvent) (int64, error) {
	l, _ := json.Marshal(e.Labels)
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO alert_events (rule_id, fingerprint, labels, state, value, severity)
		VALUES ($1,$2,$3,'firing',$4,$5) RETURNING id`,
		e.RuleID, e.Fingerprint, l, e.Value, e.Severity).Scan(&id)
	return id, err
}

// ResolveAlert encerra um alerta ativo gravando o ESTADO FINAL recebido.
//
// O estado é parâmetro (e não a constante 'resolved' que estava aqui) porque a tela e
// a mensagem precisam contar a MESMA história. Medido em dev: um alerta encerrado por
// falta de dado saía no WhatsApp como "📡 SEM DADOS" — o servidor tinha parado de
// reportar — e a tela de Alertas, lendo alert_events.state, exibia "resolvido" para o
// MESMO evento. Quem abria o painel depois do aviso concluía que o problema havia se
// resolvido sozinho; ninguém tinha resolvido nada, a informação é que havia sumido.
//
// Estado desconhecido cai em 'resolved' (comportamento antigo): é melhor gravar o
// valor conservador do que recusar o encerramento e deixar o alerta aberto para sempre.
func (s *Store) ResolveAlert(ctx context.Context, fp, state string) (AlertEvent, error) {
	switch state {
	case "resolved", "nodata":
	default:
		state = "resolved"
	}
	var e AlertEvent
	err := s.pool.QueryRow(ctx, `
		UPDATE alert_events SET state=$2, ended_at=now()
		WHERE fingerprint=$1 AND ended_at IS NULL
		RETURNING id, rule_id, value, severity, started_at`, fp, state,
	).Scan(&e.ID, &e.RuleID, &e.Value, &e.Severity, &e.StartedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	e.Fingerprint = fp
	e.State = state
	return e, err
}

// OpenAlertRow é uma linha ATIVA de alert_events com o mínimo necessário para
// recalcular o fingerprint dela (ver alerting.MigrateOpenFingerprints).
type OpenAlertRow struct {
	ID          int64
	RuleID      int64
	Fingerprint string
	Labels      map[string]string
	StartedAt   time.Time
}

// ListOpenAlertRows devolve TODOS os alertas ainda abertos (ended_at IS NULL), sem
// LIMIT e sem JOIN, em ordem estável. Diferente de ListAlerts (que é a listagem da
// tela, com teto de 200 linhas), aqui não pode faltar linha: é a entrada da migração
// de fingerprint do boot, e uma linha esquecida vira um alerta duplicado.
func (s *Store) ListOpenAlertRows(ctx context.Context) ([]OpenAlertRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, rule_id, fingerprint, labels, started_at
		FROM alert_events WHERE ended_at IS NULL ORDER BY started_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OpenAlertRow
	for rows.Next() {
		var r OpenAlertRow
		var l []byte
		if err := rows.Scan(&r.ID, &r.RuleID, &r.Fingerprint, &l, &r.StartedAt); err != nil {
			return nil, err
		}
		r.Labels = map[string]string{}
		_ = json.Unmarshal(l, &r.Labels)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetAlertFingerprint reescreve o fingerprint de um alerta ainda aberto. Só toca a
// linha que continua aberta e cujo fingerprint ainda é o antigo — assim rodar a
// migração duas vezes (ou em paralelo com o avaliador) não desfaz nada.
func (s *Store) SetAlertFingerprint(ctx context.Context, id int64, from, to string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE alert_events SET fingerprint=$3 WHERE id=$1 AND fingerprint=$2 AND ended_at IS NULL`, id, from, to)
	return err
}

// CloseRedundantAlert encerra em silêncio um alerta aberto que, depois do recálculo do
// fingerprint, virou a MESMA identidade de outro alerta aberto mais antigo. Não
// notifica ninguém: não houve mudança no mundo, só a fusão de duas linhas que sempre
// foram o mesmo incidente. `resolved_by` fica com o motivo para a auditoria não achar
// que alguém encerrou à mão.
func (s *Store) CloseRedundantAlert(ctx context.Context, id int64, motivo string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE alert_events SET state='resolved', ended_at=now(), resolved_by=$2
		WHERE id=$1 AND ended_at IS NULL`, id, motivo)
	return err
}

// ListAlerts lista alertas (ativos se onlyActive; senão histórico recente).
func (s *Store) ListAlerts(ctx context.Context, onlyActive bool) ([]AlertEvent, error) {
	q := `
		SELECT e.id, e.rule_id, r.name, r.metric, e.fingerprint, e.labels, e.state, COALESCE(e.value,0), e.severity,
		       e.started_at, e.ended_at, e.acked_by, e.resolved_by, e.flapping
		FROM alert_events e JOIN alert_rules r ON r.id = e.rule_id `
	if onlyActive {
		q += `WHERE e.ended_at IS NULL `
	}
	q += `ORDER BY e.started_at DESC LIMIT 200`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertEvent
	for rows.Next() {
		var e AlertEvent
		var l []byte
		if err := rows.Scan(&e.ID, &e.RuleID, &e.RuleName, &e.Metric, &e.Fingerprint, &l, &e.State, &e.Value,
			&e.Severity, &e.StartedAt, &e.EndedAt, &e.AckedBy, &e.ResolvedBy, &e.Flapping); err != nil {
			return nil, err
		}
		e.Labels = map[string]string{}
		_ = json.Unmarshal(l, &e.Labels)
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountRecentOpens conta quantos disparos deste fingerprint começaram desde `since`
// (base da detecção de flapping).
func (s *Store) CountRecentOpens(ctx context.Context, fp string, since time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM alert_events WHERE fingerprint=$1 AND started_at >= $2`, fp, since).Scan(&n)
	return n, err
}

// SetFlapping marca um alerta ativo como oscilante.
func (s *Store) SetFlapping(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE alert_events SET flapping=TRUE WHERE id=$1`, id)
	return err
}

// Escalatable é um alerta ativo, não reconhecido, elegível a escalonamento.
type Escalatable struct {
	ID                 int64
	Fingerprint        string
	RuleName           string
	Severity           string
	Value              float64
	Labels             map[string]string
	StartedAt          time.Time
	EscalationLevel    int
	EscalationPolicyID int64
}

// ListEscalatable devolve alertas firing e sem ack, cuja regra tem política de
// escalonamento.
func (s *Store) ListEscalatable(ctx context.Context) ([]Escalatable, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT e.id, e.fingerprint, r.name, e.severity, COALESCE(e.value,0), e.labels,
		       e.started_at, e.escalation_level, r.escalation_policy_id
		FROM alert_events e JOIN alert_rules r ON r.id = e.rule_id
		WHERE e.ended_at IS NULL AND e.acked_by IS NULL
		  AND r.escalation_policy_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Escalatable
	for rows.Next() {
		var e Escalatable
		var l []byte
		if err := rows.Scan(&e.ID, &e.Fingerprint, &e.RuleName, &e.Severity, &e.Value, &l,
			&e.StartedAt, &e.EscalationLevel, &e.EscalationPolicyID); err != nil {
			return nil, err
		}
		e.Labels = map[string]string{}
		_ = json.Unmarshal(l, &e.Labels)
		out = append(out, e)
	}
	return out, rows.Err()
}

// BumpEscalationLevel avança o nível de escalonamento (só se ainda estiver no nível esperado).
func (s *Store) BumpEscalationLevel(ctx context.Context, id int64, from, to int) (bool, error) {
	ct, err := s.pool.Exec(ctx,
		`UPDATE alert_events SET escalation_level=$3 WHERE id=$1 AND escalation_level=$2 AND ended_at IS NULL AND acked_by IS NULL`,
		id, from, to)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() > 0, nil
}

// AlertByID devolve um alerta com o nome da regra.
func (s *Store) AlertByID(ctx context.Context, id int64) (AlertEvent, error) {
	var e AlertEvent
	var l []byte
	err := s.pool.QueryRow(ctx, `
		SELECT e.id, e.rule_id, r.name, e.fingerprint, e.labels, e.state, COALESCE(e.value,0), e.severity,
		       e.started_at, e.ended_at, e.acked_by, e.flapping
		FROM alert_events e JOIN alert_rules r ON r.id = e.rule_id WHERE e.id=$1`, id,
	).Scan(&e.ID, &e.RuleID, &e.RuleName, &e.Fingerprint, &l, &e.State, &e.Value, &e.Severity,
		&e.StartedAt, &e.EndedAt, &e.AckedBy, &e.Flapping)
	if err != nil {
		return e, err
	}
	e.Labels = map[string]string{}
	_ = json.Unmarshal(l, &e.Labels)
	return e, nil
}

// AckAlert marca um alerta como reconhecido.
func (s *Store) AckAlert(ctx context.Context, id int64, by string) error {
	ct, err := s.pool.Exec(ctx, `UPDATE alert_events SET acked_by=$2, acked_at=now() WHERE id=$1 AND ended_at IS NULL`, id, by)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ResolveAlertManually encerra um alerta ativo à mão e registra quem fez isso.
// (O ResolveAlert acima é o do avaliador, que fecha por fingerprint quando a
// condição deixa de valer.)
//
// Serve para o alerta que não vai se resolver sozinho porque o mundo mudou: um
// container removido de propósito, um servidor desativado. O avaliador continua
// mandando: se a condição voltar a ser verdadeira, um alerta NOVO dispara — isto
// fecha a ocorrência atual, não silencia a regra.
func (s *Store) ResolveAlertManually(ctx context.Context, id int64, by string) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE alert_events SET state='resolved', ended_at=now(), resolved_by=$2
		WHERE id=$1 AND ended_at IS NULL`, id, by)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func orStr(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
func orInt(v, d int) int {
	if v <= 0 {
		return d
	}
	return v
}

// nonNeg mantém 0 (dispara imediatamente, estilo Prometheus `for: 0`), só corrige negativos.
func nonNeg(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

// nonNilStrings normaliza um slice possivelmente nil e sem entradas vazias para
// serializar como `[]` (nunca `null`) no JSONB de hosts, mantendo a coluna consistente.
func nonNilStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

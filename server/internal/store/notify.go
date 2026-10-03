package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// NotificationChannel é um contact point (SMTP, webhook, Telegram, WhatsApp, …).
type NotificationChannel struct {
	ID      int64          `json:"id"`
	Name    string         `json:"name"`
	Type    string         `json:"type"`
	Config  map[string]any `json:"config"`
	Enabled bool           `json:"enabled"`
	// UserID vincula o canal a um usuário (canal pessoal). NULL = canal da regra
	// (comportamento clássico: entra no fan-out das regras). Canal pessoal recebe só os
	// alertas dos servidores em que o usuário tem notify=true e NÃO entra no fan-out.
	UserID *int64 `json:"user_id"`
}

func (s *Store) CreateChannel(ctx context.Context, c NotificationChannel) (int64, error) {
	cfg, _ := json.Marshal(c.Config)
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO notification_channels (name, type, config, user_id) VALUES ($1,$2,$3,$4) RETURNING id`,
		c.Name, c.Type, cfg, c.UserID).Scan(&id)
	return id, err
}

// ListChannels devolve os canais. Com redact=true, apaga segredos do config
// (para exibir na UI sem vazar senha/token).
func (s *Store) ListChannels(ctx context.Context, redact bool) ([]NotificationChannel, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, type, config, enabled, user_id FROM notification_channels ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NotificationChannel
	for rows.Next() {
		var c NotificationChannel
		var cfg []byte
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &cfg, &c.Enabled, &c.UserID); err != nil {
			return nil, err
		}
		c.Config = map[string]any{}
		_ = json.Unmarshal(cfg, &c.Config)
		if redact {
			redactSecrets(c.Config)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ChannelByID devolve um canal com o config completo (sem redação — uso interno do sender).
func (s *Store) ChannelByID(ctx context.Context, id int64) (NotificationChannel, error) {
	var c NotificationChannel
	var cfg []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, type, config, enabled, user_id FROM notification_channels WHERE id=$1`, id,
	).Scan(&c.ID, &c.Name, &c.Type, &cfg, &c.Enabled, &c.UserID)
	if err != nil {
		return c, err
	}
	c.Config = map[string]any{}
	_ = json.Unmarshal(cfg, &c.Config)
	return c, nil
}

// UpdateChannel atualiza name, type e config de um canal. Como a listagem redige
// segredos (senha/token), o formulário de edição pode devolver esses campos vazios;
// para não apagar o segredo, mesclamos: partimos do config atual (completo, sem
// redação) e sobrescrevemos só com os valores recebidos que vierem preenchidos.
// Valores vazios/ausentes (ou o marcador de redação) preservam o valor atual.
func (s *Store) UpdateChannel(ctx context.Context, id int64, c NotificationChannel) error {
	cur, err := s.ChannelByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	merged := map[string]any{}
	for k, v := range cur.Config {
		merged[k] = v
	}
	for k, v := range c.Config {
		if sv, ok := v.(string); ok && (sv == "" || sv == "••••••") {
			continue // vazio/redigido: mantém o valor atual
		}
		merged[k] = v
	}
	cfg, _ := json.Marshal(merged)
	ct, err := s.pool.Exec(ctx,
		`UPDATE notification_channels SET name=$2, type=$3, config=$4, user_id=$5 WHERE id=$1`,
		id, c.Name, c.Type, cfg, c.UserID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteChannel apaga o canal e tira o ID dele de quem o referenciava.
//
// Sem a limpeza, o ID apagado ficava para sempre dentro de `alert_rules.channel_ids`
// e `site_checks.channel_ids`: a lista de regras contava esse fantasma ("1 canal(is)"
// numa regra sem nenhum canal marcado) e não havia caixa para desmarcá-lo, porque o
// canal não existe mais. Pior: uma regra que só apontava para fantasmas parecia
// configurada e não avisava ninguém.
func (s *Store) DeleteChannel(ctx context.Context, id int64) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM notification_channels WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := s.removerCanalDasReferencias(ctx, id); err != nil {
		return err
	}
	return nil
}

// removerCanalDasReferencias tira um ID de canal dos arrays JSONB que o citam.
// Idempotente: rodar de novo sobre quem já foi limpo não muda nada.
func (s *Store) removerCanalDasReferencias(ctx context.Context, id int64) error {
	const limpar = `
		UPDATE %s SET channel_ids = COALESCE((
			SELECT jsonb_agg(v) FROM jsonb_array_elements(channel_ids) AS v WHERE v <> to_jsonb($1::bigint)
		), '[]'::jsonb)
		WHERE channel_ids @> to_jsonb(ARRAY[$1::bigint])`
	for _, tabela := range []string{"alert_rules", "site_checks"} {
		if _, err := s.pool.Exec(ctx, fmt.Sprintf(limpar, tabela), id); err != nil {
			return err
		}
	}
	return nil
}

// secretKeys são os nomes de campo cujo VALOR nunca sai na listagem de canais.
// Comparação em minúsculas, em QUALQUER profundidade do config.
//
// `headers` entrou porque faltava: o canal do tipo webhook guarda os cabeçalhos em
// `config.headers`, e é ali que mora o `Authorization: Bearer …` do sistema de
// terceiro. Medido em dev: `GET /api/notify/channels` devolvia
// `config.headers.Authorization` LEGÍVEL — a redação só trocava strings do primeiro
// nível, então um objeto aninhado passava inteiro. A trilha de auditoria já redigia
// `headers` desde sempre (audit.redactedFields); o store é que não.
var secretKeys = map[string]bool{
	"password":      true,
	"bot_token":     true,
	"token":         true,
	"api_key":       true,
	"apikey":        true,
	"headers":       true, // webhook: costuma carregar Authorization
	"authorization": true,
	"secret":        true,
	"private_key":   true,
	"auth":          true,
}

const redactedMark = "••••••"

// redactSecrets troca os segredos do config por um marcador, percorrendo o JSON
// INTEIRO (objetos e arrays aninhados). A versão anterior só olhava as chaves do
// primeiro nível e só trocava se o valor fosse string — bastava um nível de
// aninhamento para o segredo sair em claro.
func redactSecrets(cfg map[string]any) {
	for k, v := range cfg {
		if secretKeys[strings.ToLower(k)] {
			cfg[k] = redactedMark
			continue
		}
		cfg[k] = redactValue(v)
	}
}

func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if secretKeys[strings.ToLower(k)] {
				out[k] = redactedMark
				continue
			}
			out[k] = redactValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = redactValue(val)
		}
		return out
	default:
		return v
	}
}

// AlertRoute é uma rota da árvore de roteamento.
type AlertRoute struct {
	ID               int64             `json:"id"`
	Name             string            `json:"name"`
	MinSeverity      string            `json:"min_severity"`
	Matchers         map[string]string `json:"matchers"`
	ChannelIDs       []int64           `json:"channel_ids"`
	GroupWaitSeconds int               `json:"group_wait_seconds"`
	Continue         bool              `json:"continue_matching"`
	Priority         int               `json:"priority"`
	Enabled          bool              `json:"enabled"`
}

func (s *Store) CreateRoute(ctx context.Context, r AlertRoute) (int64, error) {
	m, _ := json.Marshal(r.Matchers)
	ch, _ := json.Marshal(r.ChannelIDs)
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO alert_routes (name, min_severity, matchers, channel_ids, group_wait_seconds, continue_matching, priority)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		r.Name, orStr(r.MinSeverity, "info"), m, ch, orInt(r.GroupWaitSeconds, 30), r.Continue, orInt(r.Priority, 100)).Scan(&id)
	return id, err
}

func (s *Store) ListRoutes(ctx context.Context) ([]AlertRoute, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, min_severity, matchers, channel_ids, group_wait_seconds, continue_matching, priority, enabled
		FROM alert_routes ORDER BY priority, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertRoute
	for rows.Next() {
		var r AlertRoute
		var m, ch []byte
		if err := rows.Scan(&r.ID, &r.Name, &r.MinSeverity, &m, &ch, &r.GroupWaitSeconds, &r.Continue, &r.Priority, &r.Enabled); err != nil {
			return nil, err
		}
		r.Matchers = map[string]string{}
		_ = json.Unmarshal(m, &r.Matchers)
		_ = json.Unmarshal(ch, &r.ChannelIDs)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DeleteRoute(ctx context.Context, id int64) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM alert_routes WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// LogNotification registra um envio (auditoria).
//
// `channelName` e `destination` são o nome do canal e o endereçamento (número,
// e-mail, chat, host do webhook) NO MOMENTO DO ENVIO — ver o comentário das
// colunas em schema.sql. `destination` nunca carrega segredo: quem o monta é
// notify.DestinoDo, que só lê campos de endereço.
func (s *Store) LogNotification(ctx context.Context, channelID *int64, channelType, channelName, destination, routeName, subject string, count int, status, detail string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO notification_log (channel_id, channel_type, channel_name, destination, route_name, subject, alert_count, status, detail)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		channelID, channelType, channelName, destination, routeName, subject, count, status, detail)
	return err
}

// NotificationLogEntry é uma linha de auditoria.
type NotificationLogEntry struct {
	ID          int64  `json:"id"`
	ChannelType string `json:"channel_type"`
	// Vazios nos registros anteriores a esta coluna existir — a tela mostra "—"
	// em vez de inventar o destino de hoje para um envio de ontem.
	ChannelName string `json:"channel_name"`
	Destination string `json:"destination"`
	RouteName   string `json:"route_name"`
	Subject     string `json:"subject"`
	AlertCount  int    `json:"alert_count"`
	Status      string `json:"status"`
	Detail      string `json:"detail"`
	SentAt      string `json:"sent_at"`
}

func (s *Store) ListNotificationLog(ctx context.Context, limit int) ([]NotificationLogEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, channel_type, COALESCE(channel_name,''), COALESCE(destination,''),
		       COALESCE(route_name,''), subject, alert_count, status, COALESCE(detail,''), sent_at
		FROM notification_log ORDER BY sent_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NotificationLogEntry
	for rows.Next() {
		var e NotificationLogEntry
		var sentAt time.Time
		if err := rows.Scan(&e.ID, &e.ChannelType, &e.ChannelName, &e.Destination, &e.RouteName, &e.Subject, &e.AlertCount, &e.Status, &e.Detail, &sentAt); err != nil {
			return nil, err
		}
		e.SentAt = sentAt.Format(time.RFC3339)
		out = append(out, e)
	}
	return out, rows.Err()
}

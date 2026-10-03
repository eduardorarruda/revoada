package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/config"
)

// Retenção automática das tabelas de série que crescem sem TTL (uma linha por
// sondagem / por notificação). Sem isso, `site_check_results` e `notification_log`
// crescem indefinidamente e acabam sobrecarregando o Postgres.

const (
	// pruneBatchSize limita cada DELETE para não travar a tabela com um DELETE
	// gigante segurando locks; repetimos em lotes até esvaziar a janela.
	pruneBatchSize = 5000
	// defaultRetentionDays é o fallback quando a config vem inválida.
	defaultRetentionDays = 120
	// pruneFirstDelay adia a 1ª varredura para pouco depois do boot.
	pruneFirstDelay = 30 * time.Second
	// pruneInterval é a cadência das varreduras subsequentes.
	pruneInterval = 24 * time.Hour
)

// pruneBatched apaga em lotes as linhas de `table` cuja coluna de tempo `tsCol`
// é anterior a `before`. table/tsCol são constantes internas (nunca entrada do
// usuário), portanto a interpolação é segura. Devolve o total apagado.
func (s *Store) pruneBatched(ctx context.Context, table, tsCol string, before time.Time) (int64, error) {
	q := fmt.Sprintf(
		`DELETE FROM %s WHERE ctid IN (SELECT ctid FROM %s WHERE %s < $1 LIMIT %d)`,
		table, table, tsCol, pruneBatchSize)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		ct, err := s.pool.Exec(ctx, q, before)
		if err != nil {
			return total, err
		}
		n := ct.RowsAffected()
		total += n
		if n < pruneBatchSize {
			return total, nil
		}
	}
}

// PruneSiteCheckResults apaga sondagens (site_check_results.ts) anteriores a `before`.
func (s *Store) PruneSiteCheckResults(ctx context.Context, before time.Time) (int64, error) {
	return s.pruneBatched(ctx, "site_check_results", "ts", before)
}

// PruneNotificationLog apaga registros de envio (notification_log.sent_at) anteriores a `before`.
func (s *Store) PruneNotificationLog(ctx context.Context, before time.Time) (int64, error) {
	return s.pruneBatched(ctx, "notification_log", "sent_at", before)
}

// PruneProbeResults apaga sondagens de site (probe_results.reported_at) anteriores
// a `before`. É a série que mais cresce no Postgres — 1 linha por sondagem, por URL,
// por localidade, sem TTL — então precisa de poda como as demais.
func (s *Store) PruneProbeResults(ctx context.Context, before time.Time) (int64, error) {
	return s.pruneBatched(ctx, "probe_results", "reported_at", before)
}

// PruneAlertEvents apaga transições de alerta ANTIGAS E JÁ RESOLVIDAS
// (ended_at < before). Nunca toca eventos ativos (ended_at IS NULL): um alerta
// aberto há meses precisa continuar visível. Uma regra em flapping gera milhares
// de linhas/dia, todas resolvidas — é o que esta poda limpa.
func (s *Store) PruneAlertEvents(ctx context.Context, before time.Time) (int64, error) {
	q := fmt.Sprintf(
		`DELETE FROM alert_events WHERE ctid IN (
			SELECT ctid FROM alert_events
			WHERE ended_at IS NOT NULL AND ended_at < $1
			LIMIT %d)`, pruneBatchSize)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		ct, err := s.pool.Exec(ctx, q, before)
		if err != nil {
			return total, err
		}
		n := ct.RowsAffected()
		total += n
		if n < pruneBatchSize {
			return total, nil
		}
	}
}

// RunRetention roda o pruner em background: uma vez pouco depois do boot e depois
// 1x/dia, apagando linhas mais antigas que retentionDays. Respeita o cancelamento
// de ctx (shutdown). Conservador: retentionDays inválido cai no default.
func (s *Store) RunRetention(ctx context.Context, retentionDays int, log *slog.Logger) {
	if retentionDays <= 0 {
		log.Warn("retenção: REVOADA_RETENTION_DAYS inválido, usando default", "default", defaultRetentionDays)
		retentionDays = defaultRetentionDays
	}
	first := time.NewTimer(pruneFirstDelay)
	defer first.Stop()
	select {
	case <-ctx.Done():
		return
	case <-first.C:
	}
	s.pruneOnce(ctx, retentionDays, log)

	tk := time.NewTicker(pruneInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			s.pruneOnce(ctx, retentionDays, log)
		}
	}
}

// pruneOnce executa uma passada de retenção nas duas tabelas.
func (s *Store) pruneOnce(ctx context.Context, retentionDays int, log *slog.Logger) {
	before := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	// Guarda: nunca apagar se o corte não ficou no passado (protege contra erro
	// de cálculo/relógio, que apagaria a tabela inteira).
	if !before.Before(time.Now()) {
		log.Warn("retenção abortada: janela de corte inválida", "before", before, "dias", retentionDays)
		return
	}
	if n, err := s.PruneSiteCheckResults(ctx, before); err != nil {
		log.Error("retenção site_check_results", "err", err, "apagadas_ate_erro", n)
	} else {
		log.Info("retenção site_check_results", "apagadas", n, "antes_de", before)
	}
	if n, err := s.PruneNotificationLog(ctx, before); err != nil {
		log.Error("retenção notification_log", "err", err, "apagadas_ate_erro", n)
	} else {
		log.Info("retenção notification_log", "apagadas", n, "antes_de", before)
	}
	if n, err := s.PruneProbeResults(ctx, before); err != nil {
		log.Error("retenção probe_results", "err", err, "apagadas_ate_erro", n)
	} else {
		log.Info("retenção probe_results", "apagadas", n, "antes_de", before)
	}
	if n, err := s.PruneAlertEvents(ctx, before); err != nil {
		log.Error("retenção alert_events", "err", err, "apagadas_ate_erro", n)
	} else {
		log.Info("retenção alert_events", "apagadas", n, "antes_de", before)
	}
	// Auditoria tem retenção PRÓPRIA (mais longa): a trilha de quem alterou o quê
	// precisa sobreviver bem além da janela das séries temporais.
	auditDays, ok := config.AuditRetentionDays()
	if !ok {
		log.Warn("retenção: REVOADA_AUDIT_RETENTION_DAYS inválido, usando default", "default", auditDays)
	}
	auditBefore := time.Now().Add(-time.Duration(auditDays) * 24 * time.Hour)
	if n, err := s.PruneAudit(ctx, auditBefore); err != nil {
		log.Error("retenção audit_log", "err", err, "apagadas_ate_erro", n)
	} else {
		log.Info("retenção audit_log", "apagadas", n, "antes_de", auditBefore)
	}
}

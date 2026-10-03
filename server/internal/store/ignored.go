package store

import (
	"context"
	"fmt"
	"time"
)

// IgnoredContainer é um container que o painel deixou de vigiar (parado de propósito).
type IgnoredContainer struct {
	Host      string    `json:"host"`
	Container string    `json:"container"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

const tenantPadrao = "default"

// ListIgnoredContainers devolve os containers ignorados, por servidor e nome.
func (s *Store) ListIgnoredContainers(ctx context.Context) ([]IgnoredContainer, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT hostname, container, created_by, created_at
		FROM ignored_containers
		WHERE tenant_id=$1
		ORDER BY hostname, container`, tenantPadrao)
	if err != nil {
		return nil, fmt.Errorf("listando containers ignorados: %w", err)
	}
	defer rows.Close()
	out := []IgnoredContainer{}
	for rows.Next() {
		var c IgnoredContainer
		if err := rows.Scan(&c.Host, &c.Container, &c.CreatedBy, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("lendo container ignorado: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// IgnoreContainer passa a ignorar (host, container) e, na MESMA transação, encerra os
// alertas abertos dele — senão a tela mostraria o alerta até o próximo ciclo do
// avaliador. Encerra calado: `resolved_by` diz quem decidiu e o motivo (a tela lê
// "à mão, por <quem> (ignorou o container)"), para o
// histórico não ler isso como o container tendo voltado. Ignorar de novo é inofensivo.
// Devolve quantos alertas foram encerrados.
func (s *Store) IgnoreContainer(ctx context.Context, host, container, by string) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("ignorando container: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO ignored_containers (tenant_id, hostname, container, created_by)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (tenant_id, hostname, container) DO NOTHING`,
		tenantPadrao, host, container, by); err != nil {
		return 0, fmt.Errorf("gravando container ignorado: %w", err)
	}
	ct, err := tx.Exec(ctx, `
		UPDATE alert_events SET state='resolved', ended_at=now(), resolved_by=$3
		WHERE ended_at IS NULL AND labels->>'host' = $1 AND labels->>'container' = $2`,
		host, container, by+" (ignorou o container)")
	if err != nil {
		return 0, fmt.Errorf("encerrando alertas do container ignorado: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("ignorando container: %w", err)
	}
	return ct.RowsAffected(), nil
}

// UnignoreContainer volta a vigiar (host, container). ErrNotFound se não estava ignorado.
func (s *Store) UnignoreContainer(ctx context.Context, host, container string) error {
	ct, err := s.pool.Exec(ctx, `
		DELETE FROM ignored_containers WHERE tenant_id=$1 AND hostname=$2 AND container=$3`,
		tenantPadrao, host, container)
	if err != nil {
		return fmt.Errorf("voltando a vigiar container: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

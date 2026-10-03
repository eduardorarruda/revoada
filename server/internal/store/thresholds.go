package store

import (
	"context"

	"github.com/eduardorarruda/revoada/server/internal/health"
)

// HostThreshold é uma linha de limiar de saúde persistida (Fase A). Hostname ”
// representa o default global do tenant; um hostname específico é um override.
type HostThreshold struct {
	Hostname string  `json:"hostname"`
	Metric   string  `json:"metric"` // cpu | mem | disk
	Warn     float64 `json:"warn"`
	Crit     float64 `json:"crit"`
}

// ListHostThresholds devolve as linhas persistidas do tenant (sem os defaults
// embutidos). Ordena por hostname/métrica para uma listagem estável.
func (s *Store) ListHostThresholds(ctx context.Context, tenant string) ([]HostThreshold, error) {
	if tenant == "" {
		tenant = "default"
	}
	rows, err := s.pool.Query(ctx, `
		SELECT hostname, metric, warn, crit
		FROM host_thresholds
		WHERE tenant_id=$1
		ORDER BY hostname, metric`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HostThreshold
	for rows.Next() {
		var t HostThreshold
		if err := rows.Scan(&t.Hostname, &t.Metric, &t.Warn, &t.Crit); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ReplaceHostThresholds substitui, numa transação, todo o conjunto de limiares do
// tenant: apaga as linhas atuais e reinsere as fornecidas. Passar uma lista vazia
// limpa a configuração (voltando aos defaults embutidos).
func (s *Store) ReplaceHostThresholds(ctx context.Context, tenant string, ths []HostThreshold) error {
	if tenant == "" {
		tenant = "default"
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM host_thresholds WHERE tenant_id=$1`, tenant); err != nil {
		return err
	}
	for _, t := range ths {
		if _, err := tx.Exec(ctx, `
			INSERT INTO host_thresholds (tenant_id, hostname, metric, warn, crit)
			VALUES ($1,$2,$3,$4,$5)`,
			tenant, t.Hostname, t.Metric, t.Warn, t.Crit); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ResolvedThresholds carrega os limiares do tenant e devolve um resolvedor que,
// para (hostname, metric), aplica a precedência: override por host > global
// (hostname=”) > default embutido do pacote health. O resolvedor é um snapshot
// (não consulta o banco a cada chamada) e é seguro para uso concorrente em leitura.
func (s *Store) ResolvedThresholds(ctx context.Context, tenant string) (func(hostname, metric string) (warn, crit float64), error) {
	rows, err := s.ListHostThresholds(ctx, tenant)
	if err != nil {
		return nil, err
	}
	return buildThresholdResolver(rows), nil
}

// buildThresholdResolver monta o resolvedor puro (sem banco) a partir das linhas
// persistidas, aplicando a precedência host-específico > global > default embutido.
func buildThresholdResolver(rows []HostThreshold) func(hostname, metric string) (warn, crit float64) {
	// Indexa por hostname->metric->threshold; '' guarda os globais.
	byHost := map[string]map[string]health.Threshold{}
	for _, r := range rows {
		if byHost[r.Hostname] == nil {
			byHost[r.Hostname] = map[string]health.Threshold{}
		}
		byHost[r.Hostname][r.Metric] = health.Threshold{Warn: r.Warn, Crit: r.Crit}
	}
	return func(hostname, metric string) (float64, float64) {
		if m := byHost[hostname]; m != nil {
			if t, ok := m[metric]; ok {
				return t.Warn, t.Crit
			}
		}
		if m := byHost[""]; m != nil {
			if t, ok := m[metric]; ok {
				return t.Warn, t.Crit
			}
		}
		d := health.Default(metric)
		return d.Warn, d.Crit
	}
}

package store

import "context"

// LogMetric é uma "métrica derivada de busca": conta periodicamente as linhas de
// log que casam com o filtro e grava como série em `metrics` (alimenta alertas).
type LogMetric struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`        // rótulo amigável
	MetricName  string `json:"metric_name"` // ex: logs.errors.checkout
	Service     string `json:"service"`
	SeverityMin int    `json:"severity_min"` // número OTLP mínimo (0 = qualquer)
	Query       string `json:"query"`        // termo (body ILIKE %query%)
	Enabled     bool   `json:"enabled"`
}

func (s *Store) CreateLogMetric(ctx context.Context, m LogMetric) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO log_metrics (name, metric_name, service, severity_min, query)
		VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		m.Name, m.MetricName, m.Service, m.SeverityMin, m.Query).Scan(&id)
	return id, err
}

func (s *Store) ListLogMetrics(ctx context.Context) ([]LogMetric, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, metric_name, COALESCE(service,''), severity_min, COALESCE(query,''), enabled
		FROM log_metrics ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogMetric
	for rows.Next() {
		var m LogMetric
		if err := rows.Scan(&m.ID, &m.Name, &m.MetricName, &m.Service, &m.SeverityMin, &m.Query, &m.Enabled); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) DeleteLogMetric(ctx context.Context, id int64) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM log_metrics WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

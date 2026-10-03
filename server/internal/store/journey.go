package store

import (
	"context"
	"encoding/json"
	"time"
)

// JourneyStep é um passo do roteiro (navegar/preencher/asserir).
type JourneyStep struct {
	Name           string            `json:"name"`
	Method         string            `json:"method"` // GET|POST
	URL            string            `json:"url"`
	Form           map[string]string `json:"form,omitempty"`
	AssertStatus   int               `json:"assert_status,omitempty"`
	AssertContains string            `json:"assert_contains,omitempty"`
}

// Journey é uma jornada de browser (roteiro multi-passo).
type Journey struct {
	ID               int64         `json:"id"`
	Name             string        `json:"name"`
	Steps            []JourneyStep `json:"steps"`
	IntervalSeconds  int           `json:"interval_seconds"`
	Enabled          bool          `json:"enabled"`
	State            string        `json:"state"`
	ConsecutiveFails int           `json:"consecutive_fails"`
	LastDiagnosis    string        `json:"last_diagnosis"`
	LastCheckedAt    *time.Time    `json:"last_checked_at"`
	NextCheckAt      *time.Time    `json:"next_check_at"`
}

func (s *Store) CreateJourney(ctx context.Context, j Journey) (int64, error) {
	steps, _ := json.Marshal(j.Steps)
	interval := j.IntervalSeconds
	if interval <= 0 {
		interval = 300
	}
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO journeys (name, steps, interval_seconds, next_check_at)
		VALUES ($1,$2,$3, now()) RETURNING id`, j.Name, steps, interval).Scan(&id)
	return id, err
}

func (s *Store) ListJourneys(ctx context.Context) ([]Journey, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, steps, interval_seconds, enabled, state, consecutive_fails,
		       COALESCE(last_diagnosis,''), last_checked_at, next_check_at
		FROM journeys ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanJourneys(rows)
}

// DueJourneys devolve jornadas habilitadas cujo next_check_at já passou.
func (s *Store) DueJourneys(ctx context.Context, now time.Time) ([]Journey, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, steps, interval_seconds, enabled, state, consecutive_fails,
		       COALESCE(last_diagnosis,''), last_checked_at, next_check_at
		FROM journeys WHERE enabled AND (next_check_at IS NULL OR next_check_at <= $1)`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanJourneys(rows)
}

func scanJourneys(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]Journey, error) {
	var out []Journey
	for rows.Next() {
		var j Journey
		var steps []byte
		if err := rows.Scan(&j.ID, &j.Name, &steps, &j.IntervalSeconds, &j.Enabled, &j.State,
			&j.ConsecutiveFails, &j.LastDiagnosis, &j.LastCheckedAt, &j.NextCheckAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(steps, &j.Steps)
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) UpdateJourneyState(ctx context.Context, j Journey) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE journeys SET state=$2, consecutive_fails=$3, last_diagnosis=$4,
			last_checked_at=$5, next_check_at=$6 WHERE id=$1`,
		j.ID, j.State, j.ConsecutiveFails, j.LastDiagnosis, j.LastCheckedAt, j.NextCheckAt)
	return err
}

func (s *Store) DeleteJourney(ctx context.Context, id int64) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM journeys WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

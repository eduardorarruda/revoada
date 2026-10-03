package store

import (
	"context"
	"encoding/json"
	"time"
)

// EscalationLevel: espera antes de acionar + canais desse nível.
type EscalationLevel struct {
	WaitSeconds int     `json:"wait_seconds"`
	ChannelIDs  []int64 `json:"channel_ids"`
}

// EscalationPolicy: escada de níveis acionada por não-ack.
type EscalationPolicy struct {
	ID     int64             `json:"id"`
	Name   string            `json:"name"`
	Levels []EscalationLevel `json:"levels"`
}

func (s *Store) CreateEscalationPolicy(ctx context.Context, p EscalationPolicy) (int64, error) {
	lv, _ := json.Marshal(p.Levels)
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO escalation_policies (name, levels) VALUES ($1,$2) RETURNING id`, p.Name, lv).Scan(&id)
	return id, err
}

func (s *Store) ListEscalationPolicies(ctx context.Context) ([]EscalationPolicy, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, levels FROM escalation_policies ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EscalationPolicy
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) EscalationPolicyByID(ctx context.Context, id int64) (EscalationPolicy, error) {
	row := s.pool.QueryRow(ctx, `SELECT id, name, levels FROM escalation_policies WHERE id=$1`, id)
	return scanPolicy(row)
}

func (s *Store) DeleteEscalationPolicy(ctx context.Context, id int64) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM escalation_policies WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type scannable interface{ Scan(dest ...any) error }

func scanPolicy(row scannable) (EscalationPolicy, error) {
	var p EscalationPolicy
	var lv []byte
	if err := row.Scan(&p.ID, &p.Name, &lv); err != nil {
		return p, err
	}
	_ = json.Unmarshal(lv, &p.Levels)
	return p, nil
}

// OncallRotation: rotação semanal simples de plantonistas.
type OncallRotation struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Members       []string  `json:"members"`
	RotationStart time.Time `json:"rotation_start"`
}

func (s *Store) CreateOncallRotation(ctx context.Context, r OncallRotation) (int64, error) {
	m, _ := json.Marshal(r.Members)
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO oncall_rotations (name, members, rotation_start) VALUES ($1,$2,$3) RETURNING id`,
		r.Name, m, r.RotationStart).Scan(&id)
	return id, err
}

func (s *Store) ListOncallRotations(ctx context.Context) ([]OncallRotation, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, members, rotation_start FROM oncall_rotations ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OncallRotation
	for rows.Next() {
		var r OncallRotation
		var m []byte
		if err := rows.Scan(&r.ID, &r.Name, &m, &r.RotationStart); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(m, &r.Members)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DeleteOncallRotation(ctx context.Context, id int64) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM oncall_rotations WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// OncallOverride substitui o plantonista num intervalo pontual.
type OncallOverride struct {
	ID         int64     `json:"id"`
	RotationID int64     `json:"rotation_id"`
	Member     string    `json:"member"`
	StartsAt   time.Time `json:"starts_at"`
	EndsAt     time.Time `json:"ends_at"`
}

func (s *Store) CreateOncallOverride(ctx context.Context, o OncallOverride) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO oncall_overrides (rotation_id, member, starts_at, ends_at) VALUES ($1,$2,$3,$4) RETURNING id`,
		o.RotationID, o.Member, o.StartsAt, o.EndsAt).Scan(&id)
	return id, err
}

func (s *Store) listOverrides(ctx context.Context, rotationID int64) ([]OncallOverride, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, rotation_id, member, starts_at, ends_at FROM oncall_overrides WHERE rotation_id=$1`, rotationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OncallOverride
	for rows.Next() {
		var o OncallOverride
		if err := rows.Scan(&o.ID, &o.RotationID, &o.Member, &o.StartsAt, &o.EndsAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// CurrentOncall resolve o plantonista de uma rotação no instante `now`
// (override pontual tem prioridade sobre a rotação semanal).
func (s *Store) CurrentOncall(ctx context.Context, rotationID int64, now time.Time) (string, error) {
	overrides, err := s.listOverrides(ctx, rotationID)
	if err != nil {
		return "", err
	}
	for _, o := range overrides {
		if !now.Before(o.StartsAt) && now.Before(o.EndsAt) {
			return o.Member, nil
		}
	}
	var members []byte
	var start time.Time
	if err := s.pool.QueryRow(ctx,
		`SELECT members, rotation_start FROM oncall_rotations WHERE id=$1`, rotationID).Scan(&members, &start); err != nil {
		return "", err
	}
	var list []string
	_ = json.Unmarshal(members, &list)
	return OncallMember(list, start, now), nil
}

// OncallMember calcula o plantonista pela semana corrente (rotação circular).
func OncallMember(members []string, rotationStart, now time.Time) string {
	if len(members) == 0 {
		return ""
	}
	weeks := int(now.Sub(rotationStart).Hours()) / (24 * 7)
	if weeks < 0 {
		weeks = 0
	}
	return members[weeks%len(members)]
}

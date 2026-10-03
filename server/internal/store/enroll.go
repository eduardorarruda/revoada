package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// EnrollToken é o segredo que o instalador universal carrega no lugar de uma chave
// de ingestão. Ele não envia telemetria nem lê nada: só serve para um servidor novo
// se apresentar ao painel e receber a SUA chave. Ver schema.sql.
type EnrollToken struct {
	Token      string     `json:"token"`
	TenantID   string     `json:"tenant_id"`
	Label      string     `json:"label"`
	Revoked    bool       `json:"revoked"`
	Uses       int        `json:"uses"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
}

// ErrEnrollTokenInvalido cobre token inexistente E token revogado — de propósito.
// Quem chama a rota de inscrição não é autenticado; distinguir "não existe" de
// "existe mas foi revogado" só ajudaria quem está adivinhando token.
var ErrEnrollTokenInvalido = errors.New("token de inscrição inválido")

// CreateEnrollToken cadastra um token novo.
func (s *Store) CreateEnrollToken(ctx context.Context, token, label, createdBy string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO enrollment_tokens (token, tenant_id, label, created_by)
		VALUES ($1,'default',$2,$3)`, token, label, createdBy)
	return err
}

// ListEnrollTokens devolve todos os tokens, mais novos primeiro.
func (s *Store) ListEnrollTokens(ctx context.Context) ([]EnrollToken, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT token, tenant_id, label, revoked, uses, last_used_at, created_by, created_at
		FROM enrollment_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EnrollToken
	for rows.Next() {
		var t EnrollToken
		if err := rows.Scan(&t.Token, &t.TenantID, &t.Label, &t.Revoked, &t.Uses, &t.LastUsedAt, &t.CreatedBy, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetEnrollTokenRevoked liga/desliga a revogação. Revogar não mexe nas chaves já
// entregues: os agentes que entraram por este token continuam reportando (cada um
// tem a sua chave). O que para é a entrada de servidores NOVOS.
func (s *Store) SetEnrollTokenRevoked(ctx context.Context, token string, revoked bool) error {
	ct, err := s.pool.Exec(ctx, `UPDATE enrollment_tokens SET revoked=$2 WHERE token=$1`, token, revoked)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrEnrollTokenInvalido
	}
	return nil
}

// DeleteEnrollToken apaga o token de vez.
func (s *Store) DeleteEnrollToken(ctx context.Context, token string) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM enrollment_tokens WHERE token=$1`, token)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrEnrollTokenInvalido
	}
	return nil
}

// ConsumeEnrollToken valida o token e contabiliza o uso, tudo num UPDATE só: a
// condição `NOT revoked` viaja junto com a escrita, então não existe janela entre
// "conferi que vale" e "usei" em que uma revogação simultânea passasse batido.
// Devolve o tenant a que o token pertence.
func (s *Store) ConsumeEnrollToken(ctx context.Context, token string) (tenant string, err error) {
	err = s.pool.QueryRow(ctx, `
		UPDATE enrollment_tokens
		SET uses = uses + 1, last_used_at = now()
		WHERE token = $1 AND NOT revoked
		RETURNING tenant_id`, token).Scan(&tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrEnrollTokenInvalido
	}
	if err != nil {
		return "", err
	}
	return tenant, nil
}

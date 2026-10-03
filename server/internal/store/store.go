// Package store encapsula o acesso ao PostgreSQL (usuários e sessões).
package store

import (
	"context"
	"errors"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ pool *pgxpool.Pool }

// Connect abre o pool e aplica as migrations pendentes (o boot normal do painel).
func Connect(ctx context.Context, dsn string) (*Store, error) {
	s, err := ConectarSemMigrar(ctx, dsn)
	if err != nil {
		return nil, err
	}
	// Migrations versionadas (store/migracoes): aplica as pendentes, em ordem.
	if _, err := s.Migrar(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// ConectarSemMigrar abre o pool sem tocar no schema (comando de rollback).
func ConectarSemMigrar(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	// Tuning do pool: teto configurável, poucas conexões ociosas, e reciclagem
	// para evitar conexões velhas/vazando recursos no Postgres sob carga.
	cfg.MaxConns = config.PGMaxConns()
	cfg.MinConns = 2
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 30 * time.Minute
	cfg.HealthCheckPeriod = time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close()                         { s.pool.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// User é um usuário do sistema.
type User struct {
	ID                int64
	Username          string
	PasswordHash      string
	Role              string
	MustResetPassword bool
	Disabled          bool
	MFAAtivo          bool       // 2FA ativado e confirmado
	BloqueadoAte      *time.Time // login bloqueado por excesso de falhas, até este instante
}

// colunasUsuario é a lista lida por UserByUsername/UserByID (mesma ordem do scanUsuario).
const colunasUsuario = `id, username, password_hash, role, must_reset_password, disabled, mfa_ativo, bloqueado_ate`

func scanUsuario(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.MustResetPassword, &u.Disabled, &u.MFAAtivo, &u.BloqueadoAte)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

var ErrNotFound = errors.New("não encontrado")

func (s *Store) CreateUser(ctx context.Context, username, passwordHash, role string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (username, password_hash, role) VALUES ($1,$2,$3) RETURNING id`,
		username, passwordHash, role).Scan(&id)
	return id, err
}

func (s *Store) UserByUsername(ctx context.Context, username string) (User, error) {
	return scanUsuario(s.pool.QueryRow(ctx, `SELECT `+colunasUsuario+` FROM users WHERE username=$1`, username))
}

func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	return scanUsuario(s.pool.QueryRow(ctx, `SELECT `+colunasUsuario+` FROM users WHERE id=$1`, id))
}

func (s *Store) SetPassword(ctx context.Context, id int64, hash string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET password_hash=$2, must_reset_password=FALSE WHERE id=$1`, id, hash)
	return err
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// --- sessões (refresh tokens) ---

// CreateSession grava um refresh token (por hash). `mfa` lembra se a sessão foi
// aberta com o segundo fator, para o refresh manter isso.
// `inicio` é a hora do login original (a rotação do refresh preserva) — base do
// limite absoluto da sessão.
func (s *Store) CreateSession(ctx context.Context, userID int64, tokenHash string, expires time.Time, mfa bool, inicio time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO sessions (user_id, token_hash, expires_at, mfa, familia_inicio) VALUES ($1,$2,$3,$4,$5)`, userID, tokenHash, expires, mfa, inicio)
	return err
}

// SessionByHash resolve um refresh token (por hash), validando expiração/revogação.
func (s *Store) SessionByHash(ctx context.Context, tokenHash string) (userID int64, mfa bool, inicio time.Time, err error) {
	var expires time.Time
	var revoked bool
	err = s.pool.QueryRow(ctx,
		`SELECT user_id, expires_at, revoked, mfa, familia_inicio FROM sessions WHERE token_hash=$1`, tokenHash,
	).Scan(&userID, &expires, &revoked, &mfa, &inicio)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, time.Time{}, ErrNotFound
	}
	if err != nil {
		return 0, false, time.Time{}, err
	}
	if revoked || time.Now().After(expires) {
		return 0, false, time.Time{}, ErrNotFound
	}
	return userID, mfa, inicio, nil
}

func (s *Store) RevokeSession(ctx context.Context, tokenHash string) error {
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET revoked=TRUE WHERE token_hash=$1`, tokenHash)
	return err
}

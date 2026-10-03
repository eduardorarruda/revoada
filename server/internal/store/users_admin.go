package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// UserAdmin é a visão administrativa de um usuário (lista da tela Usuários & Acessos).
// Não carrega o hash de senha. HostCount é o nº de servidores que o usuário enxerga
// (permissão efetiva); PersonalChannels são os nomes dos canais pessoais vinculados.
type UserAdmin struct {
	ID                int64    `json:"id"`
	Username          string   `json:"username"`
	FullName          string   `json:"full_name"`
	Email             string   `json:"email"`
	Phone             string   `json:"phone"`
	Role              string   `json:"role"`
	Disabled          bool     `json:"disabled"`
	MustResetPassword bool     `json:"must_reset_password"`
	MFAAtivo          bool     `json:"mfa_ativo"`
	HostCount         int      `json:"host_count"`
	PersonalChannels  []string `json:"personal_channels"`
}

// ListUsers devolve todos os usuários com dados de administração (sem hash). O nº de
// hosts é a contagem de hostnames distintos da permissão efetiva (diretos ∪ via grupos).
func (s *Store) ListUsers(ctx context.Context) ([]UserAdmin, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.username, COALESCE(u.full_name,''), COALESCE(u.email,''), COALESCE(u.phone,''),
		       u.role, u.disabled, u.must_reset_password, u.mfa_ativo,
		       COALESCE(hc.n, 0) AS host_count,
		       COALESCE(ch.names, '{}') AS channels
		FROM users u
		LEFT JOIN (
			SELECT user_id, count(DISTINCT hostname) AS n FROM (
				SELECT user_id, hostname FROM user_server_perms
				UNION
				SELECT ug.user_id, gh.hostname
				FROM user_server_groups ug
				JOIN server_group_hosts gh ON gh.group_id = ug.group_id
			) e GROUP BY user_id
		) hc ON hc.user_id = u.id
		LEFT JOIN (
			SELECT user_id, array_agg(name ORDER BY name) AS names
			FROM notification_channels WHERE user_id IS NOT NULL GROUP BY user_id
		) ch ON ch.user_id = u.id
		ORDER BY u.username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserAdmin
	for rows.Next() {
		var u UserAdmin
		if err := rows.Scan(&u.ID, &u.Username, &u.FullName, &u.Email, &u.Phone,
			&u.Role, &u.Disabled, &u.MustResetPassword, &u.MFAAtivo, &u.HostCount, &u.PersonalChannels); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// NewUser reúne os campos de criação de um usuário pela tela de admin. Username é o
// identificador de login (para novos usuários = Email); FullName/Email/Phone compõem o
// perfil. Email/Phone podem ser vazios (usuários legados), mas a tela sempre os envia.
type NewUser struct {
	Username     string
	PasswordHash string
	Role         string
	MustReset    bool
	FullName     string
	Email        string
	Phone        string
}

// CreateUserFull cria um usuário com perfil completo (nome, email, celular), papel e
// must_reset_password. A tela de admin sempre cria com senha provisória. Distingue a
// colisão de username (ErrUsernameTaken) da de email (ErrEmailTaken) para a UI explicar.
func (s *Store) CreateUserFull(ctx context.Context, u NewUser) (int64, error) {
	// Guarda vazios como NULL (não ''), para o índice único parcial de email e para não
	// confundir "sem email" com "email em branco".
	email := nullIfEmpty(u.Email)
	phone := nullIfEmpty(u.Phone)
	fullName := nullIfEmpty(u.FullName)
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (username, password_hash, role, must_reset_password, full_name, email, phone)
		 VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		u.Username, u.PasswordHash, u.Role, u.MustReset, fullName, email, phone).Scan(&id)
	if err != nil {
		if c, ok := uniqueViolationConstraint(err); ok {
			if c == "users_email_unique" {
				return 0, ErrEmailTaken
			}
			return 0, ErrUsernameTaken
		}
		return 0, err
	}
	return id, nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// UpdateUserRole troca o papel (admin|user).
func (s *Store) UpdateUserRole(ctx context.Context, id int64, role string) error {
	ct, err := s.pool.Exec(ctx, `UPDATE users SET role=$2 WHERE id=$1`, id, role)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUserDisabled ativa/desativa um usuário. Ao desativar, o chamador deve revogar as
// sessões (RevokeUserSessions) para derrubar tokens de refresh em circulação.
func (s *Store) SetUserDisabled(ctx context.Context, id int64, disabled bool) error {
	ct, err := s.pool.Exec(ctx, `UPDATE users SET disabled=$2 WHERE id=$1`, id, disabled)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeUserSessions invalida todos os refresh tokens de um usuário (usado ao desativar
// ou ao redefinir senha). O access token (15 min) expira sozinho.
func (s *Store) RevokeUserSessions(ctx context.Context, userID int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET revoked=TRUE WHERE user_id=$1`, userID)
	return err
}

// ResetUserPassword define uma nova senha (hash) e força a troca no próximo login.
func (s *Store) ResetUserPassword(ctx context.Context, id int64, hash string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE users SET password_hash=$2, must_reset_password=TRUE WHERE id=$1`, id, hash)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteUser remove um usuário de vez. A cascata do schema apaga junto as permissões
// diretas, os vínculos de grupo, as sessões e o canal pessoal de notificação
// (notification_channels.user_id ON DELETE CASCADE). O histórico de envios
// (notification_log) não tem FK, então fica preservado para auditoria.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CountAdmins conta os administradores ativos — usado para impedir remover/desativar o
// último admin (senão o sistema fica sem quem administre).
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM users WHERE role='admin' AND NOT disabled`).Scan(&n)
	return n, err
}

// ErrUsernameTaken indica colisão de nome de usuário na criação.
var ErrUsernameTaken = errors.New("usuário já existe")

// ErrEmailTaken indica colisão de email na criação (login por email exige email único).
var ErrEmailTaken = errors.New("email já cadastrado")

// isUniqueViolation detecta a violação de UNIQUE do Postgres (código 23505) para
// traduzir num erro de domínio amigável.
func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}

// uniqueViolationConstraint devolve o NOME da constraint violada num 23505, para
// distinguir qual campo colidiu (username vs. email). ok=false se não for 23505.
func uniqueViolationConstraint(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return pgErr.ConstraintName, true
	}
	return "", false
}

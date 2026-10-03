package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// TVToken é um token de TV. `Token` é o valor em claro (para recopiar o link);
// fica vazio em tokens antigos, criados antes da coluna existir.
type TVToken struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	Location     string     `json:"location"`
	DashboardUID string     `json:"dashboard_uid"`
	PlaylistID   *int64     `json:"playlist_id"`
	Revoked      bool       `json:"revoked"`
	LastSeen     time.Time  `json:"last_seen"`
	ReloadAt     *time.Time `json:"reload_at"`
	CreatedAt    time.Time  `json:"created_at"`
	Token        string     `json:"token"` // valor em claro; "" se desconhecido (token legado)
}

func (s *Store) CreateTVToken(ctx context.Context, tokenHash, token, name, location, dashboardUID string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO tv_tokens (token_hash, token, name, location, dashboard_uid) VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		tokenHash, token, name, location, dashboardUID).Scan(&id)
	return id, err
}

// RegenerateTVToken rotaciona o token de uma TV existente: grava o novo hash + o
// valor em claro e reativa a TV. Usado para recuperar o link de tokens legados
// (que não têm o valor em claro guardado) — o link antigo deixa de valer.
func (s *Store) RegenerateTVToken(ctx context.Context, id int64, tokenHash, token string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE tv_tokens SET token_hash=$2, token=$3, revoked=false WHERE id=$1`,
		id, tokenHash, token)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListTVTokens(ctx context.Context) ([]TVToken, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, COALESCE(location,''), dashboard_uid, playlist_id, revoked,
		        COALESCE(last_seen, to_timestamp(0)), reload_at, created_at, COALESCE(token,'')
		 FROM tv_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TVToken
	for rows.Next() {
		var t TVToken
		if err := rows.Scan(&t.ID, &t.Name, &t.Location, &t.DashboardUID, &t.PlaylistID, &t.Revoked,
			&t.LastSeen, &t.ReloadAt, &t.CreatedAt, &t.Token); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RequestReload marca que a TV deve recarregar (watchdog / ação remota).
func (s *Store) RequestReload(ctx context.Context, id int64) error {
	ct, err := s.pool.Exec(ctx, `UPDATE tv_tokens SET reload_at = now() WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetTokenPlaylist troca a playlist de um token (ação remota).
func (s *Store) SetTokenPlaylist(ctx context.Context, id, playlistID int64) error {
	ct, err := s.pool.Exec(ctx, `UPDATE tv_tokens SET playlist_id=$2 WHERE id=$1`, id, playlistID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Playlist é uma sequência de dashboards.
type Playlist struct {
	ID    int64           `json:"id"`
	Name  string          `json:"name"`
	Items json.RawMessage `json:"items"` // [{dashboard_uid, duration_seconds}]
}

func (s *Store) CreatePlaylist(ctx context.Context, name string, items json.RawMessage) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO playlists (name, items) VALUES ($1,$2) RETURNING id`, name, items).Scan(&id)
	return id, err
}

func (s *Store) ListPlaylists(ctx context.Context) ([]Playlist, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, items FROM playlists ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Playlist
	for rows.Next() {
		var p Playlist
		if err := rows.Scan(&p.ID, &p.Name, &p.Items); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetPlaylist(ctx context.Context, id int64) (Playlist, error) {
	var p Playlist
	err := s.pool.QueryRow(ctx, `SELECT id, name, items FROM playlists WHERE id=$1`, id).Scan(&p.ID, &p.Name, &p.Items)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func (s *Store) RevokeTVToken(ctx context.Context, id int64) error {
	ct, err := s.pool.Exec(ctx, `UPDATE tv_tokens SET revoked=TRUE WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteTVToken remove a linha do token (hard-delete, diferente de RevokeTVToken).
func (s *Store) DeleteTVToken(ctx context.Context, id int64) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM tv_tokens WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePlaylist desvincula a playlist das TVs que a usam (voltam ao dashboard
// fixo) e então remove a playlist. As duas etapas correm numa transação.
func (s *Store) DeletePlaylist(ctx context.Context, id int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE tv_tokens SET playlist_id=NULL WHERE playlist_id=$1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM playlists WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ResolveTVToken valida um token (por hash) e devolve o alvo (dashboard ou playlist)
// + o sinal de reload; toca last_seen e registra a versão do bundle.
func (s *Store) ResolveTVToken(ctx context.Context, tokenHash, bundleVersion string) (TVToken, error) {
	var t TVToken
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, COALESCE(location,''), dashboard_uid, playlist_id, revoked, reload_at
		 FROM tv_tokens WHERE token_hash=$1`, tokenHash,
	).Scan(&t.ID, &t.Name, &t.Location, &t.DashboardUID, &t.PlaylistID, &t.Revoked, &t.ReloadAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	if t.Revoked {
		return t, ErrNotFound
	}
	_, _ = s.pool.Exec(ctx, `UPDATE tv_tokens SET last_seen=now(), bundle_version=$2 WHERE id=$1`, t.ID, bundleVersion)
	return t, nil
}

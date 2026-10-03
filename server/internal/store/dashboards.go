package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Dashboard é um dashboard com seu modelo JSON.
type Dashboard struct {
	UID       string          `json:"uid"`
	Title     string          `json:"title"`
	Folder    string          `json:"folder"`
	Model     json.RawMessage `json:"model"`
	Version   int             `json:"version"`
	UpdatedBy string          `json:"updated_by"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// DashboardMeta é a linha de listagem (sem o modelo completo).
type DashboardMeta struct {
	UID       string    `json:"uid"`
	Title     string    `json:"title"`
	Folder    string    `json:"folder"`
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Store) ListDashboards(ctx context.Context, search string) ([]DashboardMeta, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT uid, title, folder, version, updated_at FROM dashboards
		WHERE ($1 = '' OR title ILIKE '%'||$1||'%' OR folder ILIKE '%'||$1||'%')
		ORDER BY folder, title`, search)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DashboardMeta
	for rows.Next() {
		var m DashboardMeta
		if err := rows.Scan(&m.UID, &m.Title, &m.Folder, &m.Version, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) GetDashboard(ctx context.Context, uid string) (Dashboard, error) {
	var d Dashboard
	err := s.pool.QueryRow(ctx,
		`SELECT uid, title, folder, model, version, updated_by, updated_at FROM dashboards WHERE uid=$1`, uid,
	).Scan(&d.UID, &d.Title, &d.Folder, &d.Model, &d.Version, &d.UpdatedBy, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// CreateDashboard cria e registra a versão 1.
func (s *Store) CreateDashboard(ctx context.Context, uid, title, folder string, model json.RawMessage, by string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO dashboards (uid,title,folder,model,version,updated_by) VALUES ($1,$2,$3,$4,1,$5) RETURNING id`,
		uid, title, folder, model, by).Scan(&id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO dashboard_versions (dashboard_id,version,model,created_by) VALUES ($1,1,$2,$3)`,
		id, model, by); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UpdateDashboard salva uma nova versão (incrementa version, guarda histórico).
func (s *Store) UpdateDashboard(ctx context.Context, uid, title, folder string, model json.RawMessage, by string) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var id int64
	var ver int
	err = tx.QueryRow(ctx, `SELECT id, version FROM dashboards WHERE uid=$1 FOR UPDATE`, uid).Scan(&id, &ver)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	ver++
	if _, err := tx.Exec(ctx,
		`UPDATE dashboards SET title=$2, folder=$3, model=$4, version=$5, updated_by=$6, updated_at=now() WHERE id=$1`,
		id, title, folder, model, ver, by); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO dashboard_versions (dashboard_id,version,model,created_by) VALUES ($1,$2,$3,$4)`,
		id, ver, model, by); err != nil {
		return 0, err
	}
	return ver, tx.Commit(ctx)
}

func (s *Store) DeleteDashboard(ctx context.Context, uid string) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM dashboards WHERE uid=$1`, uid)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DashboardVersion é uma entrada do histórico.
type DashboardVersion struct {
	Version   int       `json:"version"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) ListVersions(ctx context.Context, uid string) ([]DashboardVersion, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT v.version, v.created_by, v.created_at
		FROM dashboard_versions v JOIN dashboards d ON d.id = v.dashboard_id
		WHERE d.uid = $1 ORDER BY v.version DESC`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DashboardVersion
	for rows.Next() {
		var v DashboardVersion
		if err := rows.Scan(&v.Version, &v.CreatedBy, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Rollback restaura o modelo de uma versão antiga como uma NOVA versão.
func (s *Store) Rollback(ctx context.Context, uid string, version int, by string) (int, error) {
	var model json.RawMessage
	err := s.pool.QueryRow(ctx, `
		SELECT v.model FROM dashboard_versions v JOIN dashboards d ON d.id=v.dashboard_id
		WHERE d.uid=$1 AND v.version=$2`, uid, version).Scan(&model)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	d, err := s.GetDashboard(ctx, uid)
	if err != nil {
		return 0, err
	}
	return s.UpdateDashboard(ctx, uid, d.Title, d.Folder, model, by)
}

// ActiveHosts lista os hosts vistos recentemente (para starter packs).
func (s *Store) ActiveHosts(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT hostname FROM hosts ORDER BY hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// HostDetail é o inventário completo de um host.
type HostDetail struct {
	Hostname string `json:"hostname"`
	// DisplayName é o alias amigável; "" quando não definido. O Hostname técnico
	// continua sendo a chave (das métricas e do inventário).
	DisplayName  string    `json:"display_name"`
	OS           string    `json:"os"`
	Kernel       string    `json:"kernel"`
	Arch         string    `json:"arch"`
	CPUModel     string    `json:"cpu_model"`
	CPUCores     int       `json:"cpu_cores"`
	IPs          string    `json:"ips"`
	AgentVersion string    `json:"agent_version"`
	UptimeSecs   float64   `json:"uptime_secs"`
	LastSeen     time.Time `json:"last_seen"`
	// Up é derivado: visto há menos de 2 min = up.
	Up bool `json:"up"`
}

// HostsDetailed devolve o inventário completo, com filtro de busca opcional.
func (s *Store) HostsDetailed(ctx context.Context, search string) ([]HostDetail, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT hostname, COALESCE(display_name,''), COALESCE(os,''), COALESCE(kernel,''), COALESCE(arch,''),
		       COALESCE(cpu_model,''), COALESCE(cpu_cores,0), COALESCE(ips,''),
		       COALESCE(agent_version,''), COALESCE(uptime_secs,0), COALESCE(last_seen, to_timestamp(0))
		FROM hosts
		WHERE ($1 = '' OR hostname ILIKE '%'||$1||'%' OR display_name ILIKE '%'||$1||'%' OR os ILIKE '%'||$1||'%' OR kernel ILIKE '%'||$1||'%')
		ORDER BY hostname`, search)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HostDetail
	for rows.Next() {
		var h HostDetail
		if err := rows.Scan(&h.Hostname, &h.DisplayName, &h.OS, &h.Kernel, &h.Arch, &h.CPUModel, &h.CPUCores,
			&h.IPs, &h.AgentVersion, &h.UptimeSecs, &h.LastSeen); err != nil {
			return nil, err
		}
		h.Up = time.Since(h.LastSeen) < 2*time.Minute
		out = append(out, h)
	}
	return out, rows.Err()
}

// HostDisplayName devolve o nome amigável (display_name) de um host, ou "" quando não
// há alias definido (ou o host não existe no inventário). O hostname técnico é a
// chave; leitura sem filtro de tenant, igual a HostsDetailed (inventário single-tenant).
// Usado para EXIBIR o nome amigável em notificações e dashboards, sem trocar a chave
// técnica que indexa as métricas.
func (s *Store) HostDisplayName(ctx context.Context, hostname string) (string, error) {
	var name string
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(display_name,'') FROM hosts WHERE hostname=$1`, hostname).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return name, nil
}

// UpdateHostDisplayName define (ou limpa, com string vazia) o alias amigável de um
// host. O hostname técnico é a chave e permanece imutável.
func (s *Store) UpdateHostDisplayName(ctx context.Context, tenant, hostname, name string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE hosts SET display_name=$3 WHERE tenant_id=$1 AND hostname=$2`,
		tenant, hostname, name)
	return err
}

// PainelComTravessao é um candidato à troca de travessão por vírgula: painel com
// UID de sistema ("host-*", "svc-*") que ainda tem " — " no título ou no modelo.
// AutorV1 é quem criou a versão 1 — a prova de ORIGEM, que o UID sozinho não dá
// (nada impede alguém de criar à mão um painel "host-notas").
type PainelComTravessao struct {
	UID     string
	Title   string
	Folder  string
	Model   json.RawMessage
	AutorV1 string
}

// PaineisComTravessao lista os candidatos. Só lê: quem decide e salva é
// dashboards.NormalizarTravessao, pelo UpdateDashboard (versão nova + histórico).
func (s *Store) PaineisComTravessao(ctx context.Context) ([]PainelComTravessao, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.uid, d.title, d.folder, d.model,
		       COALESCE((SELECT v.created_by FROM dashboard_versions v
		                 WHERE v.dashboard_id = d.id ORDER BY v.version LIMIT 1), '')
		FROM dashboards d
		WHERE (d.uid LIKE 'host-%' OR d.uid LIKE 'svc-%')
		  AND (d.title LIKE '% — %' OR d.model::text LIKE '% — %')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PainelComTravessao
	for rows.Next() {
		var p PainelComTravessao
		if err := rows.Scan(&p.UID, &p.Title, &p.Folder, &p.Model, &p.AutorV1); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

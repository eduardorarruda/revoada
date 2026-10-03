package store

import "context"

// ServerPerm é a permissão de um usuário sobre um servidor (hostname técnico).
// CanEdit implica CanView (normalizado na escrita). Notify = recebe alerta daquele
// servidor no canal pessoal.
type ServerPerm struct {
	Hostname string `json:"hostname"`
	CanView  bool   `json:"can_view"`
	CanEdit  bool   `json:"can_edit"`
	Notify   bool   `json:"notify"`
}

// ServerGroup é um grupo de servidores (atalho de administração).
type ServerGroup struct {
	ID    int64    `json:"id"`
	Name  string   `json:"name"`
	Hosts []string `json:"hosts"`
}

// UserGroupLink é a participação de um usuário num grupo, com as flags herdadas.
type UserGroupLink struct {
	GroupID int64 `json:"group_id"`
	CanEdit bool  `json:"can_edit"`
	Notify  bool  `json:"notify"`
}

// EffectiveServerPerms devolve a permissão EFETIVA de um usuário: a união das permissões
// diretas (user_server_perms) com as herdadas dos grupos que ele participa. É a fonte de
// verdade do escopo — usada pelo authz.Resolver (enforcement) e pela tela de admin.
func (s *Store) EffectiveServerPerms(ctx context.Context, userID int64) ([]ServerPerm, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT hostname, bool_or(can_view) AS can_view, bool_or(can_edit) AS can_edit, bool_or(notify) AS notify
		FROM (
			SELECT hostname, can_view, can_edit, notify FROM user_server_perms WHERE user_id=$1
			UNION ALL
			SELECT gh.hostname, TRUE, ug.can_edit, ug.notify
			FROM user_server_groups ug
			JOIN server_group_hosts gh ON gh.group_id = ug.group_id
			WHERE ug.user_id=$1
		) x
		GROUP BY hostname
		ORDER BY hostname`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServerPerm
	for rows.Next() {
		var p ServerPerm
		if err := rows.Scan(&p.Hostname, &p.CanView, &p.CanEdit, &p.Notify); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DirectServerPerms devolve APENAS as permissões diretas de um usuário (sem herança de
// grupo) — o que a tela de admin edita na matriz por servidor.
func (s *Store) DirectServerPerms(ctx context.Context, userID int64) ([]ServerPerm, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT hostname, can_view, can_edit, notify FROM user_server_perms WHERE user_id=$1 ORDER BY hostname`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServerPerm
	for rows.Next() {
		var p ServerPerm
		if err := rows.Scan(&p.Hostname, &p.CanView, &p.CanEdit, &p.Notify); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetUserServerPerms substitui TODO o conjunto de permissões diretas de um usuário numa
// transação (apaga e reinsere). Normaliza can_edit ⇒ can_view; ignora linhas sem nenhuma
// flag ligada (não guarda "acesso zero"). O chamador deve invalidar o cache do authz.
func (s *Store) SetUserServerPerms(ctx context.Context, userID int64, perms []ServerPerm) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM user_server_perms WHERE user_id=$1`, userID); err != nil {
		return err
	}
	for _, p := range perms {
		view := p.CanView || p.CanEdit || p.Notify // editar/notificar exige ver
		if !view {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO user_server_perms (user_id, hostname, can_view, can_edit, notify) VALUES ($1,$2,$3,$4,$5)`,
			userID, p.Hostname, true, p.CanEdit, p.Notify); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// --- grupos de servidores ---

// ListServerGroups devolve os grupos com seus hostnames.
func (s *Store) ListServerGroups(ctx context.Context) ([]ServerGroup, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT g.id, g.name, COALESCE(array_remove(array_agg(gh.hostname ORDER BY gh.hostname), NULL), '{}')
		FROM server_groups g
		LEFT JOIN server_group_hosts gh ON gh.group_id = g.id
		GROUP BY g.id, g.name
		ORDER BY g.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServerGroup
	for rows.Next() {
		var g ServerGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Hosts); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// CreateServerGroup cria um grupo vazio.
func (s *Store) CreateServerGroup(ctx context.Context, name string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO server_groups (name) VALUES ($1) RETURNING id`, name).Scan(&id)
	if err != nil && isUniqueViolation(err) {
		return 0, ErrUsernameTaken // reuso do erro de colisão (nome de grupo único)
	}
	return id, err
}

// RenameServerGroup e SetServerGroupHosts substituem nome/hosts do grupo.
func (s *Store) RenameServerGroup(ctx context.Context, id int64, name string) error {
	ct, err := s.pool.Exec(ctx, `UPDATE server_groups SET name=$2 WHERE id=$1`, id, name)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrUsernameTaken
		}
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetServerGroupHosts(ctx context.Context, groupID int64, hosts []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM server_group_hosts WHERE group_id=$1`, groupID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, h := range hosts {
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		if _, err := tx.Exec(ctx,
			`INSERT INTO server_group_hosts (group_id, hostname) VALUES ($1,$2)`, groupID, h); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// DeleteServerGroup remove o grupo (cascata apaga hosts e vínculos de usuário).
func (s *Store) DeleteServerGroup(ctx context.Context, id int64) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM server_groups WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- vínculo usuário → grupos ---

// UserGroups devolve os grupos de que um usuário participa (com flags herdadas).
func (s *Store) UserGroups(ctx context.Context, userID int64) ([]UserGroupLink, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT group_id, can_edit, notify FROM user_server_groups WHERE user_id=$1 ORDER BY group_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserGroupLink
	for rows.Next() {
		var l UserGroupLink
		if err := rows.Scan(&l.GroupID, &l.CanEdit, &l.Notify); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// SetUserGroups substitui todo o conjunto de grupos de um usuário numa transação.
func (s *Store) SetUserGroups(ctx context.Context, userID int64, links []UserGroupLink) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM user_server_groups WHERE user_id=$1`, userID); err != nil {
		return err
	}
	for _, l := range links {
		if _, err := tx.Exec(ctx,
			`INSERT INTO user_server_groups (user_id, group_id, can_edit, notify) VALUES ($1,$2,$3,$4)
			 ON CONFLICT (user_id, group_id) DO UPDATE SET can_edit=EXCLUDED.can_edit, notify=EXCLUDED.notify`,
			userID, l.GroupID, l.CanEdit, l.Notify); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// HostsWithoutPermission devolve os hostnames do inventário que NENHUM usuário não-admin
// enxerga (sem permissão direta e fora de qualquer grupo). Alimenta o aviso "N servidores
// sem permissão atribuída" na tela de admin.
func (s *Store) HostsWithoutPermission(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT h.hostname FROM hosts h
		WHERE NOT EXISTS (SELECT 1 FROM user_server_perms p WHERE p.hostname = h.hostname)
		  AND NOT EXISTS (SELECT 1 FROM server_group_hosts gh WHERE gh.hostname = h.hostname)
		ORDER BY h.hostname`)
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

// PersonalChannelIDsForHost devolve os IDs dos canais pessoais habilitados que devem
// receber o alerta de um servidor: canais com user_id de usuários ATIVOS cuja permissão
// efetiva tem notify=true para aquele hostname. Usado no dispatch de notificações (Fase 3).
func (s *Store) PersonalChannelIDsForHost(ctx context.Context, hostname string) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id
		FROM notification_channels c
		JOIN users u ON u.id = c.user_id
		WHERE c.enabled AND c.user_id IS NOT NULL AND NOT u.disabled
		  AND EXISTS (
			SELECT 1 FROM (
				SELECT hostname, notify FROM user_server_perms WHERE user_id = u.id
				UNION ALL
				SELECT gh.hostname, ug.notify
				FROM user_server_groups ug
				JOIN server_group_hosts gh ON gh.group_id = ug.group_id
				WHERE ug.user_id = u.id
			) e WHERE e.hostname = $1 AND e.notify
		  )`, hostname)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

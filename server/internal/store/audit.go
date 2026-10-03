package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// AuditEntry é uma linha da trilha de auditoria: uma alteração feita pela API.
// O Payload já chega redigido (sem segredos) — quem grava é o middleware.
type AuditEntry struct {
	ID        int64          `json:"id"`
	ActorID   *int64         `json:"actor_id,omitempty"`
	ActorName string         `json:"actor_name"`
	ActorRole string         `json:"actor_role"`
	Method    string         `json:"method"`
	Path      string         `json:"path"`
	Resource  string         `json:"resource"`
	Target    string         `json:"target"`
	Status    int            `json:"status"`
	Payload   map[string]any `json:"payload"`
	IP        string         `json:"ip"`
	CreatedAt string         `json:"created_at"` // RFC3339
	// Origem: ui | api | mcp | agente (vazio = ui). CorrelacaoID liga a ação aos logs
	// do painel e do agente que a executou.
	Origem       string `json:"origem,omitempty"`
	CorrelacaoID string `json:"correlacao_id,omitempty"`
}

// InsertAudit grava uma entrada da trilha. Payload nil vira '{}'.
func (s *Store) InsertAudit(ctx context.Context, e AuditEntry) error {
	payload := []byte("{}")
	if e.Payload != nil {
		if b, err := json.Marshal(e.Payload); err == nil {
			payload = b
		}
	}
	origem := e.Origem
	if origem == "" {
		origem = "ui"
	}
	// Encadeamento: a inserção é serializada (trava de transação) para que cada linha
	// pegue o hash da anterior sem corrida. O hash é calculado pelo PRÓPRIO Postgres
	// (revoada_audit_hash) sobre os valores gravados — a verificação usa a mesma
	// função, então JSONB normalizado e fuso horário nunca divergem.
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, travaAuditoria); err != nil {
			return err
		}
		var id int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO audit_log (actor_id, actor_name, actor_role, method, path, resource, target, status, payload, ip,
			                       origem, correlacao_id, hash_anterior)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,
			        coalesce((SELECT hash FROM audit_log ORDER BY id DESC LIMIT 1), ''))
			RETURNING id`,
			e.ActorID, e.ActorName, e.ActorRole, e.Method, e.Path, e.Resource, e.Target, e.Status, payload, e.IP,
			origem, e.CorrelacaoID).Scan(&id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE audit_log a SET hash = revoada_audit_hash(a) WHERE a.id = $1`, id)
		return err
	})
}

// travaAuditoria é a chave da trava consultiva que serializa a corrente de hashes.
const travaAuditoria = 0x52_45_56_41_55_44 // "REVAUD"

// ResultadoIntegridade é o laudo da verificação da trilha.
type ResultadoIntegridade struct {
	Integra      bool   `json:"integra"`
	Verificadas  int64  `json:"verificadas"`
	PrimeiraRuim *int64 `json:"primeira_ruim,omitempty"` // id da primeira linha adulterada ou fora da corrente
	Motivo       string `json:"motivo,omitempty"`
}

// VerificarIntegridadeAuditoria recalcula a corrente inteira. Detecta linha editada
// (hash não confere) e linha apagada no meio (hash_anterior não bate com a anterior).
// A linha mais antiga pode apontar para uma que a retenção já apagou — isso é normal.
func (s *Store) VerificarIntegridadeAuditoria(ctx context.Context) (ResultadoIntegridade, error) {
	var r ResultadoIntegridade
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE hash <> ''`).Scan(&r.Verificadas); err != nil {
		return r, err
	}
	var id int64
	var editada bool
	err := s.pool.QueryRow(ctx, `
		SELECT id, editada FROM (
		    SELECT a.id,
		           revoada_audit_hash(a) <> a.hash AS editada,
		           lag(a.hash) OVER (ORDER BY a.id) AS anterior,
		           a.hash_anterior,
		           row_number() OVER (ORDER BY a.id) AS n
		      FROM audit_log a WHERE a.hash <> ''
		) t
		WHERE editada OR (n > 1 AND hash_anterior <> anterior)
		ORDER BY id LIMIT 1`).Scan(&id, &editada)
	if errors.Is(err, pgx.ErrNoRows) {
		r.Integra = true
		return r, nil
	}
	if err != nil {
		return r, err
	}
	r.PrimeiraRuim = &id
	r.Motivo = "uma linha anterior foi apagada ou inserida fora da ordem"
	if editada {
		r.Motivo = "o conteúdo desta linha foi alterado depois de gravado"
	}
	return r, nil
}

// AuditFilter são os filtros da tela de auditoria. Campos vazios não filtram.
type AuditFilter struct {
	Actor    string // casa por nome do ator (parcial, case-insensitive)
	Resource string
	Method   string
	From, To time.Time
	Limit    int
	Offset   int
}

// ListAudit devolve as entradas mais recentes primeiro e o total que casa com o
// filtro (para a paginação da tela).
func (s *Store) ListAudit(ctx context.Context, f AuditFilter) ([]AuditEntry, int64, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	// WHERE dinâmico com argumentos posicionais — nada de concatenar valor em SQL.
	var conds []string
	var args []any
	add := func(cond string, val any) {
		args = append(args, val)
		conds = append(conds, strings.Replace(cond, "?", "$"+itoa(len(args)), 1))
	}
	if f.Actor != "" {
		add("actor_name ILIKE ?", "%"+f.Actor+"%")
	}
	if f.Resource != "" {
		add("resource = ?", f.Resource)
	}
	if f.Method != "" {
		add("method = ?", strings.ToUpper(f.Method))
	}
	if !f.From.IsZero() {
		add("created_at >= ?", f.From)
	}
	if !f.To.IsZero() {
		add("created_at < ?", f.To)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM audit_log"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, f.Limit, f.Offset)
	rows, err := s.pool.Query(ctx, `
		SELECT id, actor_id, actor_name, actor_role, method, path, resource, target, status, payload, ip, created_at
		FROM audit_log`+where+` ORDER BY created_at DESC, id DESC LIMIT $`+itoa(len(args)-1)+` OFFSET $`+itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var payload []byte
		var ts time.Time
		if err := rows.Scan(&e.ID, &e.ActorID, &e.ActorName, &e.ActorRole, &e.Method, &e.Path,
			&e.Resource, &e.Target, &e.Status, &payload, &e.IP, &ts); err != nil {
			return nil, 0, err
		}
		e.CreatedAt = ts.Format(time.RFC3339)
		if len(payload) > 0 {
			_ = json.Unmarshal(payload, &e.Payload)
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// AuditResources lista os recursos já registrados — alimenta o filtro da tela sem
// precisar de uma lista fixa no front (recurso novo aparece sozinho).
func (s *Store) AuditResources(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT resource FROM audit_log WHERE resource <> '' ORDER BY resource`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneAudit apaga entradas mais antigas que `before` (retenção própria, mais longa
// que a das séries: a trilha é o registro de quem mexeu no quê).
func (s *Store) PruneAudit(ctx context.Context, before time.Time) (int64, error) {
	return s.pruneBatched(ctx, "audit_log", "created_at", before)
}

// itoa evita importar strconv só para o índice do placeholder.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

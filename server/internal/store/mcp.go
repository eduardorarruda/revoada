package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// TokenMCP é um token de acesso ao MCP. O valor do token nunca é guardado.
type TokenMCP struct {
	ID        string     `json:"id"`
	Nome      string     `json:"nome"`
	Hash      string     `json:"-"`
	Escopos   []string   `json:"escopos"`
	CriadoPor string     `json:"criado_por"`
	CriadoEm  time.Time  `json:"criado_em"`
	ExpiraEm  time.Time  `json:"expira_em"`
	UltimoUso *time.Time `json:"ultimo_uso,omitempty"`
	Revogado  bool       `json:"revogado"`
}

const colunasTokenMCP = `id, nome, hash, escopos, criado_por, criado_em, expira_em, ultimo_uso, revogado`

func scanTokenMCP(row pgx.Row) (TokenMCP, error) {
	var t TokenMCP
	err := row.Scan(&t.ID, &t.Nome, &t.Hash, &t.Escopos, &t.CriadoPor, &t.CriadoEm, &t.ExpiraEm, &t.UltimoUso, &t.Revogado)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

func (s *Store) CriarTokenMCP(ctx context.Context, t TokenMCP) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO mcp_tokens (id, nome, hash, escopos, criado_por, expira_em) VALUES ($1,$2,$3,$4,$5,$6)`,
		t.ID, t.Nome, t.Hash, t.Escopos, t.CriadoPor, t.ExpiraEm)
	return err
}

// TokenMCPPorHash devolve o token ATIVO (não revogado, não vencido) com esse hash.
func (s *Store) TokenMCPPorHash(ctx context.Context, hash string) (TokenMCP, error) {
	return scanTokenMCP(s.pool.QueryRow(ctx, `SELECT `+colunasTokenMCP+` FROM mcp_tokens
		WHERE hash = $1 AND NOT revogado AND expira_em > now()`, hash))
}

func (s *Store) ListarTokensMCP(ctx context.Context) ([]TokenMCP, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+colunasTokenMCP+` FROM mcp_tokens ORDER BY criado_em DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TokenMCP{}
	for rows.Next() {
		t, err := scanTokenMCP(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) RevogarTokenMCP(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE mcp_tokens SET revogado = true WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// MarcarUsoTokenMCP grava o último uso (no máximo uma vez por minuto por token).
func (s *Store) MarcarUsoTokenMCP(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE mcp_tokens SET ultimo_uso = now()
		WHERE id = $1 AND (ultimo_uso IS NULL OR ultimo_uso < now() - interval '1 minute')`, id)
	return err
}

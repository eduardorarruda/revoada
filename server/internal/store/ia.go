package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// PrecoLLM é uma linha da tabela de preços dos modelos de IA (US$ por 1 mi de tokens).
type PrecoLLM struct {
	ID                int64     `json:"id"`
	Provedor          string    `json:"provedor"`
	Modelo            string    `json:"modelo"`
	EntradaPor1M      float64   `json:"entrada_por_1m"`
	SaidaPor1M        float64   `json:"saida_por_1m"`
	CacheLeituraPor1M *float64  `json:"cache_leitura_por_1m"`
	CacheEscritaPor1M *float64  `json:"cache_escrita_por_1m"`
	Moeda             string    `json:"moeda"`
	VigenteDesde      time.Time `json:"vigente_desde"`
	Origem            string    `json:"origem"`
	CriadoPor         string    `json:"criado_por"`
}

// ErrPrecoDuplicado: já existe preço para o mesmo provedor, modelo e vigência.
var ErrPrecoDuplicado = errors.New("já existe um preço para este modelo com esta vigência")

const colunasPrecoLLM = `id, provedor, modelo, entrada_por_1m::float8, saida_por_1m::float8,
	cache_leitura_por_1m::float8, cache_escrita_por_1m::float8, moeda, vigente_desde, origem, criado_por`

func scanPrecoLLM(row pgx.Row) (PrecoLLM, error) {
	var p PrecoLLM
	err := row.Scan(&p.ID, &p.Provedor, &p.Modelo, &p.EntradaPor1M, &p.SaidaPor1M,
		&p.CacheLeituraPor1M, &p.CacheEscritaPor1M, &p.Moeda, &p.VigenteDesde, &p.Origem, &p.CriadoPor)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// ListarPrecosLLM devolve todas as linhas, da vigência mais recente para a mais antiga.
func (s *Store) ListarPrecosLLM(ctx context.Context) ([]PrecoLLM, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+colunasPrecoLLM+` FROM llm_precos
		WHERE tenant_id = 'default' ORDER BY vigente_desde DESC, provedor, modelo`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PrecoLLM{}
	for rows.Next() {
		p, err := scanPrecoLLM(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CriarPrecoLLM grava uma linha nova. ErrPrecoDuplicado se a vigência já existir.
func (s *Store) CriarPrecoLLM(ctx context.Context, p PrecoLLM) (PrecoLLM, error) {
	p = normalizarPreco(p)
	criado, err := scanPrecoLLM(s.pool.QueryRow(ctx, `INSERT INTO llm_precos
		(provedor, modelo, entrada_por_1m, saida_por_1m, cache_leitura_por_1m, cache_escrita_por_1m,
		 vigente_desde, origem, criado_por)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (tenant_id, provedor, modelo, vigente_desde) DO NOTHING
		RETURNING `+colunasPrecoLLM,
		p.Provedor, p.Modelo, p.EntradaPor1M, p.SaidaPor1M, p.CacheLeituraPor1M, p.CacheEscritaPor1M,
		p.VigenteDesde, p.Origem, p.CriadoPor))
	if errors.Is(err, ErrNotFound) {
		return criado, ErrPrecoDuplicado
	}
	return criado, err
}

// normalizarPreco põe provedor e modelo em minúsculas: é como a busca de preço compara,
// e duas linhas que só diferem na caixa seriam o mesmo preço com duas vigências.
func normalizarPreco(p PrecoLLM) PrecoLLM {
	p.Provedor = strings.ToLower(strings.TrimSpace(p.Provedor))
	p.Modelo = strings.ToLower(strings.TrimSpace(p.Modelo))
	return p
}

// ApagarPrecoLLM remove uma linha. ErrNotFound se não existir.
func (s *Store) ApagarPrecoLLM(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM llm_precos WHERE id = $1 AND tenant_id = 'default'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SemearPrecosLLM grava a tabela de referência só se aquela origem ainda não foi
// importada. Rodar de novo (a cada boot) não duplica nada, e preço cadastrado à mão
// nunca é sobrescrito: a referência só ACRESCENTA linhas com a própria origem. Se o
// administrador apagar uma linha de referência, ela não volta no próximo boot.
func (s *Store) SemearPrecosLLM(ctx context.Context, origem string, precos []PrecoLLM) (int, error) {
	var ja bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM llm_precos
		WHERE tenant_id = 'default' AND origem = $1)`, origem).Scan(&ja); err != nil {
		return 0, err
	}
	if ja || len(precos) == 0 {
		return 0, nil
	}
	// Um INSERT só, com unnest das colunas: a tabela inteira entra ou nada entra.
	var prov, mod []string
	var ent, sai []float64
	var cl, ce []*float64
	var vig []time.Time
	for _, p := range precos {
		p = normalizarPreco(p)
		prov, mod = append(prov, p.Provedor), append(mod, p.Modelo)
		ent, sai = append(ent, p.EntradaPor1M), append(sai, p.SaidaPor1M)
		cl, ce = append(cl, p.CacheLeituraPor1M), append(ce, p.CacheEscritaPor1M)
		vig = append(vig, p.VigenteDesde)
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO llm_precos
		(provedor, modelo, entrada_por_1m, saida_por_1m, cache_leitura_por_1m, cache_escrita_por_1m,
		 vigente_desde, origem, criado_por)
		SELECT u.*, $8, 'referência'
		FROM unnest($1::text[], $2::text[], $3::float8[], $4::float8[], $5::float8[], $6::float8[], $7::timestamptz[]) AS u
		ON CONFLICT (tenant_id, provedor, modelo, vigente_desde) DO NOTHING`,
		prov, mod, ent, sai, cl, ce, vig, origem)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

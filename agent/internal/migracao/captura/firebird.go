package captura

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
)

// capturarFirebird lê os catálogos RDB$ (compatíveis do 2.1 ao 5.0).
func capturarFirebird(ctx context.Context, db *sql.DB) (esquema.Esquema, error) {
	var e esquema.Esquema
	if err := db.QueryRowContext(ctx,
		`SELECT TRIM(rdb$get_context('SYSTEM','ENGINE_VERSION')) FROM RDB$DATABASE`).Scan(&e.Versao); err != nil {
		return e, fmt.Errorf("lendo a versão: %w", err)
	}
	var cs sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT TRIM(RDB$CHARACTER_SET_NAME) FROM RDB$DATABASE`).Scan(&cs); err != nil {
		return e, fmt.Errorf("lendo o charset: %w", err)
	}
	e.Charset = cs.String
	if e.Charset == "" {
		e.Charset = "NONE"
	}
	var dialeto sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MON$SQL_DIALECT FROM MON$DATABASE`).Scan(&dialeto); err == nil {
		e.Dialeto = int(dialeto.Int64)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT TRIM(rf.RDB$RELATION_NAME), TRIM(rf.RDB$FIELD_NAME), f.RDB$FIELD_TYPE,
		       COALESCE(f.RDB$FIELD_SUB_TYPE, 0), COALESCE(f.RDB$FIELD_SCALE, 0), COALESCE(f.RDB$FIELD_PRECISION, 0),
		       COALESCE(f.RDB$CHARACTER_LENGTH, f.RDB$FIELD_LENGTH, 0), COALESCE(rf.RDB$NULL_FLAG, f.RDB$NULL_FLAG, 0),
		       TRIM(cs.RDB$CHARACTER_SET_NAME), CAST(COALESCE(rf.RDB$DEFAULT_SOURCE, f.RDB$DEFAULT_SOURCE) AS VARCHAR(500))
		  FROM RDB$RELATION_FIELDS rf
		  JOIN RDB$FIELDS f ON f.RDB$FIELD_NAME = rf.RDB$FIELD_SOURCE
		  JOIN RDB$RELATIONS r ON r.RDB$RELATION_NAME = rf.RDB$RELATION_NAME
		  LEFT JOIN RDB$CHARACTER_SETS cs ON cs.RDB$CHARACTER_SET_ID = f.RDB$CHARACTER_SET_ID
		 WHERE COALESCE(r.RDB$SYSTEM_FLAG, 0) = 0 AND r.RDB$VIEW_BLR IS NULL
		 ORDER BY rf.RDB$RELATION_NAME, rf.RDB$FIELD_POSITION`)
	if err != nil {
		return e, fmt.Errorf("lendo colunas: %w", err)
	}
	defer rows.Close()
	colunas := map[string][]esquema.Coluna{}
	var ordem []string
	for rows.Next() {
		var tabela, nome string
		var tipo, sub, escala, precisao, tamanho, naoNulo int
		var charset, padrao sql.NullString
		if err := rows.Scan(&tabela, &nome, &tipo, &sub, &escala, &precisao, &tamanho, &naoNulo, &charset, &padrao); err != nil {
			return e, fmt.Errorf("lendo colunas: %w", err)
		}
		logico, nativo := esquema.TipoFirebird(tipo, sub, escala, precisao, tamanho)
		c := esquema.Coluna{Nome: nome, TipoNativo: nativo, Tipo: logico, Nulavel: naoNulo == 0,
			Padrao: strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(padrao.String), "DEFAULT")), Charset: charset.String}
		switch logico {
		case esquema.Texto:
			c.Tamanho = tamanho
		case esquema.Decimal:
			c.Precisao, c.Escala = precisao, -escala
		}
		if _, ok := colunas[tabela]; !ok {
			ordem = append(ordem, tabela)
		}
		colunas[tabela] = append(colunas[tabela], c)
	}
	if err := rows.Err(); err != nil {
		return e, err
	}

	rs, err := restricoesFirebird(ctx, db)
	if err != nil {
		return e, err
	}
	e.Tabelas = montarTabelas(ordem, colunas, rs)
	return e, nil
}

func restricoesFirebird(ctx context.Context, db *sql.DB) ([]restricao, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT TRIM(rc.RDB$RELATION_NAME), TRIM(rc.RDB$CONSTRAINT_NAME), TRIM(rc.RDB$CONSTRAINT_TYPE),
		       TRIM(s.RDB$FIELD_NAME), TRIM(refc.RDB$CONST_NAME_UQ)
		  FROM RDB$RELATION_CONSTRAINTS rc
		  JOIN RDB$INDEX_SEGMENTS s ON s.RDB$INDEX_NAME = rc.RDB$INDEX_NAME
		  LEFT JOIN RDB$REF_CONSTRAINTS refc ON refc.RDB$CONSTRAINT_NAME = rc.RDB$CONSTRAINT_NAME
		 WHERE rc.RDB$CONSTRAINT_TYPE IN ('PRIMARY KEY', 'UNIQUE', 'FOREIGN KEY')
		 ORDER BY rc.RDB$CONSTRAINT_NAME, s.RDB$FIELD_POSITION`)
	if err != nil {
		return nil, fmt.Errorf("lendo chaves: %w", err)
	}
	defer rows.Close()
	var out []restricao
	for rows.Next() {
		var tabela, nome, tipo, coluna string
		var ref sql.NullString
		if err := rows.Scan(&tabela, &nome, &tipo, &coluna, &ref); err != nil {
			return nil, fmt.Errorf("lendo chaves: %w", err)
		}
		if n := len(out); n > 0 && out[n-1].nome == nome {
			out[n-1].colunas = append(out[n-1].colunas, coluna)
			continue
		}
		out = append(out, restricao{tabela: tabela, nome: nome, tipo: tipo, ref: ref.String, colunas: []string{coluna}})
	}
	return out, rows.Err()
}

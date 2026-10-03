package captura

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
)

// capturarPostgres lê o information_schema de um schema (padrão "public").
func capturarPostgres(ctx context.Context, db *sql.DB, schema string) (esquema.Esquema, error) {
	var e esquema.Esquema
	if err := db.QueryRowContext(ctx, `SHOW server_version`).Scan(&e.Versao); err != nil {
		return e, fmt.Errorf("lendo a versão: %w", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT pg_encoding_to_char(encoding) FROM pg_database WHERE datname = current_database()`).Scan(&e.Charset); err != nil {
		return e, fmt.Errorf("lendo o charset: %w", err)
	}
	rows, err := db.QueryContext(ctx, `
		SELECT c.table_name, c.column_name, c.data_type, COALESCE(c.character_maximum_length, 0),
		       COALESCE(c.numeric_precision, 0), COALESCE(c.numeric_scale, 0), c.is_nullable = 'YES',
		       COALESCE(c.column_default, ''), (c.is_identity = 'YES' OR COALESCE(c.column_default, '') LIKE 'nextval(%')
		  FROM information_schema.columns c
		  JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		 WHERE c.table_schema = $1 AND t.table_type = 'BASE TABLE'
		 ORDER BY c.table_name, c.ordinal_position`, schema)
	if err != nil {
		return e, fmt.Errorf("lendo colunas: %w", err)
	}
	defer rows.Close()
	colunas := map[string][]esquema.Coluna{}
	var ordem []string
	for rows.Next() {
		var tabela, nome, tipo, padrao string
		var tamanho, precisao, escala int
		var nulavel, identidade bool
		if err := rows.Scan(&tabela, &nome, &tipo, &tamanho, &precisao, &escala, &nulavel, &padrao, &identidade); err != nil {
			return e, fmt.Errorf("lendo colunas: %w", err)
		}
		c := esquema.Coluna{Nome: nome, TipoNativo: tipo, Tipo: esquema.TipoPostgres(tipo), Nulavel: nulavel,
			Padrao: padrao, Identidade: identidade, Charset: e.Charset}
		switch c.Tipo {
		case esquema.Texto:
			c.Tamanho = tamanho
			if tamanho > 0 {
				c.TipoNativo = fmt.Sprintf("%s(%d)", tipo, tamanho)
			}
		case esquema.Decimal:
			c.Precisao, c.Escala = precisao, escala
		}
		if _, ok := colunas[tabela]; !ok {
			ordem = append(ordem, tabela)
		}
		colunas[tabela] = append(colunas[tabela], c)
	}
	if err := rows.Err(); err != nil {
		return e, err
	}
	rs, err := restricoesPostgres(ctx, db, schema)
	if err != nil {
		return e, err
	}
	e.Tabelas = montarTabelas(ordem, colunas, rs)
	if err := estimarLinhasPostgres(ctx, db, schema, e.Tabelas); err != nil {
		return e, err
	}
	return e, nil
}

func restricoesPostgres(ctx context.Context, db *sql.DB, schema string) ([]restricao, error) {
	// pg_constraint traz as colunas na ordem (conkey) e a restrição única referenciada (FK).
	rows, err := db.QueryContext(ctx, `
		SELECT rel.relname, con.conname,
		       CASE con.contype WHEN 'p' THEN 'PRIMARY KEY' WHEN 'u' THEN 'UNIQUE' ELSE 'FOREIGN KEY' END,
		       array_to_string(ARRAY(SELECT att.attname FROM unnest(con.conkey) WITH ORDINALITY k(n, i)
		                             JOIN pg_attribute att ON att.attrelid = con.conrelid AND att.attnum = k.n ORDER BY k.i), ','),
		       COALESCE(refrel.relname, ''),
		       COALESCE(array_to_string(ARRAY(SELECT att.attname FROM unnest(con.confkey) WITH ORDINALITY k(n, i)
		                             JOIN pg_attribute att ON att.attrelid = con.confrelid AND att.attnum = k.n ORDER BY k.i), ','), '')
		  FROM pg_constraint con
		  JOIN pg_class rel ON rel.oid = con.conrelid
		  JOIN pg_namespace ns ON ns.oid = rel.relnamespace
		  LEFT JOIN pg_class refrel ON refrel.oid = con.confrelid
		 WHERE ns.nspname = $1 AND con.contype IN ('p', 'u', 'f')
		 ORDER BY rel.relname, con.conname`, schema)
	if err != nil {
		return nil, fmt.Errorf("lendo chaves: %w", err)
	}
	defer rows.Close()
	var out []restricao
	// FK no Postgres aponta para tabela+colunas direto; criamos uma restrição
	// sintética do alvo para reaproveitar montarTabelas.
	for rows.Next() {
		var tabela, nome, tipo, cols, refTabela, refCols string
		if err := rows.Scan(&tabela, &nome, &tipo, &cols, &refTabela, &refCols); err != nil {
			return nil, fmt.Errorf("lendo chaves: %w", err)
		}
		r := restricao{tabela: tabela, nome: nome, tipo: tipo, colunas: strings.Split(cols, ",")}
		if tipo == "FOREIGN KEY" {
			alvo := "ref:" + nome
			r.ref = alvo
			out = append(out, restricao{tabela: refTabela, nome: alvo, tipo: "REF", colunas: strings.Split(refCols, ",")})
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func estimarLinhasPostgres(ctx context.Context, db *sql.DB, schema string, tabs []esquema.Tabela) error {
	rows, err := db.QueryContext(ctx, `
		SELECT c.relname, GREATEST(c.reltuples, 0)::bigint FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relkind = 'r'`, schema)
	if err != nil {
		return fmt.Errorf("estimando linhas: %w", err)
	}
	defer rows.Close()
	est := map[string]int64{}
	for rows.Next() {
		var n string
		var l int64
		if err := rows.Scan(&n, &l); err != nil {
			return err
		}
		est[n] = l
	}
	for i := range tabs {
		tabs[i].LinhasEstimadas = est[tabs[i].Nome]
	}
	return rows.Err()
}

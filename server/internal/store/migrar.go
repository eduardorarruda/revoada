package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Migrations versionadas do Postgres (itens 8 e 9 da lista de segurança/operação):
// cada mudança de schema é um arquivo NNNN_nome.up.sql com o seu NNNN_nome.down.sql
// (rollback). O painel aplica as pendentes no boot, em ordem, cada uma numa
// transação, e registra em schema_migracoes com o checksum — um arquivo alterado
// depois de aplicado é recusado (o banco não pode divergir do código em silêncio).

//go:embed migracoes/*.sql
var arquivosMigracoes embed.FS

// Migracao é um par sobe/desce.
type Migracao struct {
	Versao   int
	Nome     string
	Sobe     string
	Desce    string
	Checksum string
}

// EstadoMigracao é o que o comando `revoada-painel migracoes` mostra.
type EstadoMigracao struct {
	Versao     int        `json:"versao"`
	Nome       string     `json:"nome"`
	Aplicada   bool       `json:"aplicada"`
	AplicadaEm *time.Time `json:"aplicada_em,omitempty"`
}

// travaMigracoes serializa painéis subindo ao mesmo tempo — e o gateway, que cria
// agents/hosts no próprio boot com a MESMA trava (gateway/internal/pg, ensureSchema).
// Mudou o valor aqui? Mude lá.
const travaMigracoes = 0x52_45_56_4D_49_47 // "REVMIG"

func carregarMigracoes() ([]Migracao, error) {
	nomes, err := fs.Glob(arquivosMigracoes, "migracoes/*.up.sql")
	if err != nil {
		return nil, err
	}
	var out []Migracao
	for _, n := range nomes {
		base := strings.TrimSuffix(path.Base(n), ".up.sql")
		num, nome, ok := strings.Cut(base, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil {
			return nil, fmt.Errorf("migration com nome inválido: %s (use NNNN_nome.up.sql)", n)
		}
		sobe, err := arquivosMigracoes.ReadFile(n)
		if err != nil {
			return nil, err
		}
		desce, err := arquivosMigracoes.ReadFile("migracoes/" + base + ".down.sql")
		if err != nil {
			return nil, fmt.Errorf("migration %s sem o rollback (.down.sql)", base)
		}
		soma := sha256.Sum256(sobe)
		out = append(out, Migracao{Versao: v, Nome: nome, Sobe: string(sobe), Desce: string(desce), Checksum: hex.EncodeToString(soma[:])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Versao < out[j].Versao })
	for i := 1; i < len(out); i++ {
		if out[i].Versao == out[i-1].Versao {
			return nil, fmt.Errorf("duas migrations com a versão %d", out[i].Versao)
		}
	}
	return out, nil
}

const criarTabelaMigracoes = `CREATE TABLE IF NOT EXISTS schema_migracoes (
	versao      INT PRIMARY KEY,
	nome        TEXT NOT NULL,
	checksum    TEXT NOT NULL,
	aplicada_em TIMESTAMPTZ NOT NULL DEFAULT now()
)`

// Migrar aplica as migrations pendentes e devolve as versões aplicadas agora.
func (s *Store) Migrar(ctx context.Context) ([]int, error) {
	ms, err := carregarMigracoes()
	if err != nil {
		return nil, err
	}
	var aplicadas []int
	err = s.comTravaMigracoes(ctx, func(conn *pgx.Conn) error {
		feitas, err := lerAplicadas(ctx, conn)
		if err != nil {
			return err
		}
		for _, m := range ms {
			if soma, ok := feitas[m.Versao]; ok {
				if soma != m.Checksum {
					return fmt.Errorf("a migration %04d_%s foi alterada depois de aplicada (checksum diferente); crie uma migration nova em vez de editar a antiga", m.Versao, m.Nome)
				}
				continue
			}
			if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, m.Sobe); err != nil {
					return fmt.Errorf("migration %04d_%s: %w", m.Versao, m.Nome, err)
				}
				_, err := tx.Exec(ctx, `INSERT INTO schema_migracoes (versao, nome, checksum) VALUES ($1,$2,$3)`, m.Versao, m.Nome, m.Checksum)
				return err
			}); err != nil {
				return err
			}
			aplicadas = append(aplicadas, m.Versao)
		}
		return nil
	})
	return aplicadas, err
}

// ErrSemRollback: a versão alvo é anterior à base.
var ErrSemRollback = errors.New("não há rollback abaixo da base")

// Desfazer roda os .down.sql das versões MAIORES que `ateVersao`, da mais nova para
// a mais antiga, cada uma numa transação. Devolve as versões desfeitas.
func (s *Store) Desfazer(ctx context.Context, ateVersao int) ([]int, error) {
	if ateVersao < 1 {
		return nil, ErrSemRollback
	}
	ms, err := carregarMigracoes()
	if err != nil {
		return nil, err
	}
	var desfeitas []int
	err = s.comTravaMigracoes(ctx, func(conn *pgx.Conn) error {
		feitas, err := lerAplicadas(ctx, conn)
		if err != nil {
			return err
		}
		for i := len(ms) - 1; i >= 0; i-- {
			m := ms[i]
			if m.Versao <= ateVersao {
				break
			}
			if _, ok := feitas[m.Versao]; !ok {
				continue
			}
			if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, m.Desce); err != nil {
					return fmt.Errorf("rollback %04d_%s: %w", m.Versao, m.Nome, err)
				}
				_, err := tx.Exec(ctx, `DELETE FROM schema_migracoes WHERE versao=$1`, m.Versao)
				return err
			}); err != nil {
				return err
			}
			desfeitas = append(desfeitas, m.Versao)
		}
		return nil
	})
	return desfeitas, err
}

// EstadoMigracoes lista todas as migrations do código e se já foram aplicadas.
func (s *Store) EstadoMigracoes(ctx context.Context) ([]EstadoMigracao, error) {
	ms, err := carregarMigracoes()
	if err != nil {
		return nil, err
	}
	if _, err := s.pool.Exec(ctx, criarTabelaMigracoes); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT versao, aplicada_em FROM schema_migracoes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	quando := map[int]time.Time{}
	for rows.Next() {
		var v int
		var t time.Time
		if err := rows.Scan(&v, &t); err != nil {
			return nil, err
		}
		quando[v] = t
	}
	out := make([]EstadoMigracao, 0, len(ms))
	for _, m := range ms {
		e := EstadoMigracao{Versao: m.Versao, Nome: m.Nome}
		if t, ok := quando[m.Versao]; ok {
			e.Aplicada, e.AplicadaEm = true, &t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) comTravaMigracoes(ctx context.Context, f func(*pgx.Conn) error) error {
	c, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer c.Release()
	conn := c.Conn()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, travaMigracoes); err != nil {
		return err
	}
	defer func() { _, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, travaMigracoes) }()
	if _, err := conn.Exec(ctx, criarTabelaMigracoes); err != nil {
		return err
	}
	return f(conn)
}

func lerAplicadas(ctx context.Context, conn *pgx.Conn) (map[int]string, error) {
	rows, err := conn.Query(ctx, `SELECT versao, checksum FROM schema_migracoes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var v int
		var c string
		if err := rows.Scan(&v, &c); err != nil {
			return nil, err
		}
		out[v] = c
	}
	return out, rows.Err()
}

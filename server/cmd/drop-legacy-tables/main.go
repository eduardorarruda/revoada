// Comando drop-legacy-tables: derruba as tabelas das funcionalidades DESCONTINUADAS
// (Silêncios e Incidentes) e a coluna alert_events.incident_id, que o schema
// idempotente do boot não remove sozinho.
//
// SEGURANÇA: por padrão é DRY-RUN — só relata o que faria e a contagem de linhas de
// cada tabela. Só derruba com -execute. E RECUSA derrubar uma tabela NÃO VAZIA (a
// não ser com -force), justamente para não apagar dado sem querer. Rode primeiro sem
// flags para conferir que estão vazias; depois com -execute.
//
//	REVOADA_PG_DSN   Postgres (mesma config do server)
//
//	go run ./server/cmd/drop-legacy-tables            # dry-run: só relata
//	go run ./server/cmd/drop-legacy-tables -execute   # derruba (se vazias)
package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

// tabelas descontinuadas a derrubar (ordem: filhas antes das pais, se houver FK).
var legacyTables = []string{"silences", "incident_timeline", "incidents"}

func main() {
	execute := flag.Bool("execute", false, "executa os DROP (sem esta flag é só dry-run)")
	force := flag.Bool("force", false, "permite derrubar mesmo tabela NÃO vazia (perigoso)")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, config.Postgres())
	if err != nil {
		log.Fatalf("Postgres: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("Postgres indisponível: %v", err)
	}

	if *execute {
		log.Println("== MODO EXECUÇÃO, vai derrubar objetos ==")
	} else {
		log.Println("== DRY-RUN, nada será alterado (use -execute para derrubar) ==")
	}

	for _, t := range legacyTables {
		exists, count, err := tableState(ctx, pool, t)
		if err != nil {
			log.Fatalf("checando %s: %v", t, err)
		}
		if !exists {
			log.Printf("tabela %-18s ausente (nada a fazer)", t)
			continue
		}
		log.Printf("tabela %-18s existe · %d linha(s)", t, count)
		if !*execute {
			continue
		}
		if count > 0 && !*force {
			log.Printf("  RECUSADO: %s não está vazia (%d linhas). Use -force para derrubar mesmo assim.", t, count)
			continue
		}
		if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS "+t+" CASCADE"); err != nil {
			log.Fatalf("  erro ao derrubar %s: %v", t, err)
		}
		log.Printf("  DERRUBADA: %s", t)
	}

	// Coluna alert_events.incident_id — vínculo com incidentes (feature removida).
	hasCol, err := columnExists(ctx, pool, "alert_events", "incident_id")
	if err != nil {
		log.Fatalf("checando coluna incident_id: %v", err)
	}
	if hasCol {
		log.Printf("coluna alert_events.incident_id existe")
		if *execute {
			if _, err := pool.Exec(ctx, "ALTER TABLE alert_events DROP COLUMN IF EXISTS incident_id"); err != nil {
				log.Fatalf("  erro ao remover coluna: %v", err)
			}
			log.Printf("  REMOVIDA: alert_events.incident_id")
		}
	} else {
		log.Printf("coluna alert_events.incident_id ausente (nada a fazer)")
	}

	log.Println("== concluído ==")
}

// tableState devolve se a tabela existe e sua contagem de linhas (0 se ausente).
func tableState(ctx context.Context, pool *pgxpool.Pool, table string) (bool, int64, error) {
	var reg *string
	if err := pool.QueryRow(ctx, "SELECT to_regclass($1)::text", "public."+table).Scan(&reg); err != nil {
		return false, 0, err
	}
	if reg == nil {
		return false, 0, nil
	}
	var count int64
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
		return false, 0, err
	}
	return true, count, nil
}

func columnExists(ctx context.Context, pool *pgxpool.Pool, table, column string) (bool, error) {
	var n int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name=$1 AND column_name=$2`,
		table, column).Scan(&n)
	return n > 0, err
}

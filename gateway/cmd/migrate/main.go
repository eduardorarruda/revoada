// Comando migrate: aplica (ou reverte) as migrations do ClickHouse.
//
//	go run ./cmd/migrate          # aplica todas as pendentes (up)
//	go run ./cmd/migrate -down    # reverte a última
//	go run ./cmd/migrate -dir X   # diretório de migrations (default deploy/migrations/clickhouse)
package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/eduardorarruda/revoada/gateway/internal/chhttp"
	"github.com/eduardorarruda/revoada/gateway/internal/config"
	"github.com/eduardorarruda/revoada/gateway/internal/migrate"
)

func main() {
	dir := flag.String("dir", "deploy/migrations/clickhouse", "diretório das migrations")
	down := flag.Bool("down", false, "reverte a última migration")
	flag.Parse()

	addr, user, pass, db := config.ClickHouse()
	ch := chhttp.New(chhttp.Config{Addr: addr, User: user, Pass: pass, DB: db})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Espera o ClickHouse aceitar conexões, com retry. Num recreate do container o CH
	// pode reportar "healthy" (HTTP em localhost) antes de aceitar conexões na
	// interface de rede onde o migrate conecta — uma tentativa única falhava com
	// "connection refused" e derrubava o deploy (o gateway depende do migrate concluir).
	if err := waitForClickHouse(ctx, ch, addr); err != nil {
		log.Fatalf("clickhouse indisponível em %s após retries: %v", addr, err)
	}

	r := migrate.New(ch, *dir)

	if *down {
		v, err := r.Down(ctx)
		if err != nil {
			log.Fatalf("erro no down: %v", err)
		}
		if v == "" {
			log.Println("nada a reverter")
		} else {
			log.Printf("revertida: %s", v)
		}
		return
	}

	newly, err := r.Up(ctx)
	if err != nil {
		log.Fatalf("erro no up: %v", err)
	}
	if len(newly) == 0 {
		log.Println("nada a aplicar (já está tudo migrado)")
		return
	}
	for _, v := range newly {
		log.Printf("aplicada: %s", v)
	}
}

// waitForClickHouse tenta o Ping repetidamente (a cada 2s) até obter sucesso ou o
// contexto expirar. Devolve nil no 1º sucesso; o último erro se o ctx acabar antes.
func waitForClickHouse(ctx context.Context, ch interface {
	Ping(context.Context) error
}, addr string) error {
	tk := time.NewTicker(2 * time.Second)
	defer tk.Stop()
	var lastErr error
	for {
		if err := ch.Ping(ctx); err == nil {
			return nil
		} else {
			lastErr = err
			log.Printf("aguardando clickhouse em %s: %v", addr, err)
		}
		select {
		case <-ctx.Done():
			return lastErr
		case <-tk.C:
		}
	}
}

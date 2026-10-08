// Comando gateway: serviço de ingestão do Revoada.
// Recebe telemetria via OTLP (HTTP/gRPC), grava inventário no PostgreSQL e publica
// métricas no NATS (escritas no ClickHouse pelo writer embutido). Expõe health/readiness.
//
// Configuração (env, prefixo REVOADA_):
//
//	REVOADA_HTTP_ADDR      porta HTTP do gateway            (default :8090)
//	REVOADA_CH_ADDR        endereço HTTP do ClickHouse      (default http://127.0.0.1:8123)
//	REVOADA_CH_USER/…      credenciais do ClickHouse        (default revoada/revoada/revoada)
//	REVOADA_PG_DSN         DSN do PostgreSQL                (default postgres://revoada:revoada@127.0.0.1:5433/revoada)
//	REVOADA_NATS_URL       URL de cliente do NATS           (default nats://127.0.0.1:4222)
//	REVOADA_NATS_MON_URL   endpoint de monitoramento do NATS (default http://127.0.0.1:8222/healthz)
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/eduardorarruda/revoada/gateway/internal/chbatch"
	"github.com/eduardorarruda/revoada/gateway/internal/chhttp"
	"github.com/eduardorarruda/revoada/gateway/internal/config"
	"github.com/eduardorarruda/revoada/gateway/internal/health"
	"github.com/eduardorarruda/revoada/gateway/internal/ingest"
	"github.com/eduardorarruda/revoada/gateway/internal/model"
	"github.com/eduardorarruda/revoada/gateway/internal/otlp"
	"github.com/eduardorarruda/revoada/gateway/internal/pg"
	"github.com/eduardorarruda/revoada/gateway/internal/queue"
	"github.com/eduardorarruda/revoada/gateway/internal/scrape"
	"github.com/eduardorarruda/revoada/gateway/internal/server"
	"github.com/eduardorarruda/revoada/gateway/internal/writer"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "faz GET no próprio /healthz e sai 0/1 (HEALTHCHECK do container)")
	flag.Parse()

	httpAddr, natsMon := config.Gateway()

	if *healthcheck {
		os.Exit(selfHealthcheck(httpAddr))
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	chAddr, chUser, chPass, chDB := config.ClickHouse()
	ch := chhttp.New(chhttp.Config{Addr: chAddr, User: chUser, Pass: chPass, DB: chDB})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// PostgreSQL (metadados: agentes + inventário).
	store, err := pg.Connect(ctx, config.Postgres())
	if err != nil {
		log.Error("conectando PostgreSQL", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	// NATS JetStream (fila de ingestão).
	q, err := queue.Connect(ctx, config.NATS())
	if err != nil {
		log.Error("conectando NATS", "err", err)
		os.Exit(1)
	}
	defer q.Close()

	// Writer: consome o stream e escreve no ClickHouse (goroutine).
	wr := writer.New(ch)
	go func() {
		if err := q.ConsumeMetrics(ctx, "writer", wr.Handle); err != nil && ctx.Err() == nil {
			log.Error("consumer do writer encerrou", "err", err)
		}
	}()

	// Readiness: ClickHouse + PostgreSQL + NATS (monitoramento).
	checkers := []health.Checker{
		health.CheckerFunc{Label: "clickhouse", Fn: ch.Ping},
		health.CheckerFunc{Label: "postgres", Fn: store.Ping},
		health.HTTPReady("nats", natsMon),
	}

	// OTLP: receiver HTTP (/v1/metrics, /v1/logs) no mesmo listener + gRPC em :4317.
	// Logs vão direto ao ClickHouse (o lote OTLP já é agregado); métricas passam pelo NATS.
	otlpRc := otlp.NewReceiver(log, store, q)
	otlpRc.SetHostBinding(otlp.ParseHostBindMode(config.HostBinding()))
	otlpRc.SetSinks(ch, ch)
	otlpRc.SetHostSink(store) // popula o inventário `hosts` a partir do OTLP (agente v2)

	// Ingestão ASSÍNCRONA de logs/traces: os handlers enfileiram e respondem rápido;
	// um worker acumula e faz flush em lote no ClickHouse (menos "parts", sem bloquear
	// o handler até o timeout do CH). Métricas seguem pelo NATS (já duráveis).
	batchMaxRows, batchInterval, batchQueue := config.CHBatch()
	logBatch := chbatch.New(chbatch.Config[model.LogRecord]{
		Name: "logs", Insert: ch.InsertLogs,
		MaxRows: batchMaxRows, Interval: batchInterval, QueueLen: batchQueue, Log: log,
	})
	spanBatch := chbatch.New(chbatch.Config[model.Span]{
		Name: "spans", Insert: ch.InsertSpans,
		MaxRows: batchMaxRows, Interval: batchInterval, QueueLen: batchQueue, Log: log,
	})
	// Chamadas de IA: uma linha normalizada por span de IA (genai_spans) e, se o
	// operador ligou, o conteúdo (genai_conteudo). Batchers próprios: um retry de
	// genai_spans não pode duplicar os spans, e vice-versa.
	genaiBatch := chbatch.New(chbatch.Config[model.GenAISpan]{
		Name: "genai_spans", Insert: ch.InsertGenAISpans,
		MaxRows: batchMaxRows, Interval: batchInterval, QueueLen: batchQueue, Log: log,
	})
	conteudoBatch := chbatch.New(chbatch.Config[model.GenAIConteudo]{
		Name: "genai_conteudo", Insert: ch.InsertGenAIConteudo,
		MaxRows: batchMaxRows, Interval: batchInterval, QueueLen: batchQueue, Log: log,
	})
	logBatch.Start(ctx)
	spanBatch.Start(ctx)
	genaiBatch.Start(ctx)
	conteudoBatch.Start(ctx)
	// Stop drena e faz flush do que restou (registrado após store/NATS: roda antes deles).
	defer logBatch.Stop()
	defer spanBatch.Stop()
	defer genaiBatch.Stop()
	defer conteudoBatch.Stop()
	otlpRc.SetLogBatcher(logBatch)
	otlpRc.SetSpanBatcher(spanBatch)
	modoConteudo := otlp.ParseConteudoIA(config.GenAIConteudo())
	otlpRc.SetGenAI(ch, genaiBatch, conteudoBatch, modoConteudo)
	log.Info("chamadas de IA", "conteudo", string(modoConteudo))

	// Rate limiting por chave (serverkey/IP) nos endpoints públicos de ingestão.
	rps, burst := config.RateLimit()
	rl := server.NewRateLimiter(rps, burst)
	// Sem as redes confiáveis, o balde por IP vê o IP do NGINX em toda requisição e a
	// frota inteira divide um balde só — o oposto do que a mudança para IP pretendia.
	rl.SetTrustedProxies(config.TrustedProxies())
	go rl.Run(ctx)

	// Teto GLOBAL de requisições de ingestão em voo (503 + Retry-After ao estourar).
	// O rate limit é POR IP e não sabe quanto o processo INTEIRO está segurando: sem
	// este teto, sob saturação o gateway respondia 200 e ia ficando lento até o POST
	// estourar o timeout de 10 s do agente, que re-bufferava e mandava de novo. Ver
	// server.InFlight.
	gate := server.NewInFlight(config.MaxInFlight())

	grpcAddr := config.OTLP()
	go func() {
		// O listener OTLP/gRPC leva o MESMO freio e o MESMO teto das rotas HTTP: até
		// esta versão ele subia sem controle nenhum e era a porta dos fundos da ingestão
		// (medido: 6.000 requisições passaram, zero recusas, contra 3.590 recusas no HTTP
		// com a mesma carga e a mesma chave inválida).
		if err := otlpRc.ServeGRPCAll(ctx, grpcAddr, rl, gate); err != nil && ctx.Err() == nil {
			log.Error("OTLP/gRPC encerrou", "err", err)
		}
	}()

	// Scraper Prometheus (alvos no PostgreSQL).
	go scrape.New(log, store, q, 8).Run(ctx)

	srv := server.New(log, checkers...)
	srv.Handle("GET /metrics", wr.PrometheusHandler())
	probeH := ingest.NewProbeResult(log, store)
	mountIngest(srv, ingestHandlers{
		Metrics:          otlpRc.HTTPHandler(),
		Logs:             otlpRc.LogsHTTPHandler(),
		Traces:           otlpRc.TracesHTTPHandler(),
		JSONLogs:         otlpRc.JSONLogsHandler(),
		Discovery:        ingest.NewDiscovery(log, store),
		ProbeResult:      probeH,
		ProbeAssignments: http.HandlerFunc(probeH.Assignments),
	}, rl, gate)

	if err := srv.Run(ctx, httpAddr); err != nil {
		log.Error("gateway encerrou com erro", "err", err)
		os.Exit(1)
	}
	log.Info("gateway encerrado")
}

// ingestHandlers reúne TODAS as rotas públicas de ingestão num lugar só.
//
// POR QUE uma struct e não sete chamadas soltas de srv.Handle: a rota
// GET /probe/assignments estava registrada sem o rl.Wrap — a única rota de ingestão sem
// freio em camada alguma. Medido: 3.000 GETs com X-Revoada-Key rotativa passaram a
// 5.170 req/s, 401 nas 3.000 e ZERO 429, e as 3.000 foram consultar o PostgreSQL (o
// cache de autenticação cresceu 3.000 entradas), que é o mesmo banco onde ficam as
// sessões do painel e o cofre de credenciais SSH. Ninguém "decidiu" deixar a rota de
// fora: ela só foi registrada numa linha diferente das outras. Com a tabela única e o
// mountIngest, esquecer o embrulho deixou de ser possível — e o teste
// TestTodaRotaDeIngestaoTemFreio percorre esta mesma tabela.
type ingestHandlers struct {
	Metrics          http.Handler
	Logs             http.Handler
	Traces           http.Handler
	JSONLogs         http.Handler
	Discovery        http.Handler
	ProbeResult      http.Handler
	ProbeAssignments http.Handler
}

// routes devolve (padrão, handler) de cada rota pública de ingestão.
func (h ingestHandlers) routes() []struct {
	Pattern string
	H       http.Handler
} {
	return []struct {
		Pattern string
		H       http.Handler
	}{
		{"POST /v1/metrics", h.Metrics},
		{"POST /v1/logs", h.Logs},
		{"POST /v1/traces", h.Traces},
		{"POST /ingest/logs", h.JSONLogs},
		{"POST /ingest/discovery", h.Discovery},
		{"POST /ingest/probe-result", h.ProbeResult},
		{"GET /probe/assignments", h.ProbeAssignments},
	}
}

// mounter é o que mountIngest precisa do servidor (permite montar num ServeMux no teste).
type mounter interface {
	Handle(pattern string, h http.Handler)
}

// mountIngest registra todas as rotas de ingestão SEMPRE com os dois controles: o rate
// limit por IP de origem (429 + Retry-After) e o teto global de requisições em voo
// (503 + Retry-After). Nenhuma rota de ingestão pode ser montada por fora daqui.
func mountIngest(srv mounter, hs ingestHandlers, rl *server.RateLimiter, gate *server.InFlight) {
	for _, rt := range hs.routes() {
		srv.Handle(rt.Pattern, rl.Wrap(gate.Wrap(rt.H)))
	}
}

// selfHealthcheck faz um GET em /healthz na porta local e devolve 0 (ok) ou 1 (falha).
func selfHealthcheck(httpAddr string) int {
	host := httpAddr
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + host + "/healthz")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return 0
	}
	return 1
}

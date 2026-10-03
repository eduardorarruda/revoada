// Package config centraliza a leitura de configuração por env vars (prefixo REVOADA_).
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Env lê uma env var com valor padrão.
func Env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// EnvDuration lê uma env var como time.Duration (ex.: "30s", "1h"); usa def se
// ausente ou inválida.
func EnvDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// EnvInt lê uma env var como inteiro positivo; usa def se ausente ou inválida.
func EnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// EnvInt64 lê uma env var como int64 positivo; usa def se ausente ou inválida.
func EnvInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// HostBinding devolve o modo de amarração de host (anti-spoofing) do ambiente:
// REVOADA_HOST_BINDING ∈ {off (default), normalize, strict}. Off = passthrough.
func HostBinding() string { return Env("REVOADA_HOST_BINDING", "off") }

// ClickHouse devolve a configuração do ClickHouse a partir do ambiente.
// Vars: REVOADA_CH_ADDR, REVOADA_CH_USER, REVOADA_CH_PASSWORD, REVOADA_CH_DB.
func ClickHouse() (addr, user, pass, db string) {
	return Env("REVOADA_CH_ADDR", "http://127.0.0.1:8123"),
		Env("REVOADA_CH_USER", "revoada"),
		Env("REVOADA_CH_PASSWORD", "revoada"),
		Env("REVOADA_CH_DB", "revoada")
}

// Gateway devolve a configuração do serviço gateway.
// Vars: REVOADA_HTTP_ADDR (porta HTTP), REVOADA_NATS_MON_URL (endpoint de monitoramento do NATS).
func Gateway() (httpAddr, natsMonURL string) {
	return Env("REVOADA_HTTP_ADDR", ":8090"),
		Env("REVOADA_NATS_MON_URL", "http://127.0.0.1:8222/healthz")
}

// Postgres devolve o DSN do PostgreSQL. Var: REVOADA_PG_DSN.
func Postgres() string {
	return Env("REVOADA_PG_DSN", "postgres://revoada:revoada@127.0.0.1:5433/revoada?sslmode=disable")
}

// NATS devolve a URL de cliente do NATS. Var: REVOADA_NATS_URL.
func NATS() string {
	return Env("REVOADA_NATS_URL", "nats://127.0.0.1:4222")
}

// OTLP devolve os endereços dos receivers OTLP. Vars: REVOADA_OTLP_GRPC_ADDR, REVOADA_OTLP_HTTP_PATH.
// O OTLP/HTTP é servido no mesmo listener HTTP do gateway (rota /v1/metrics).
func OTLP() (grpcAddr string) {
	return Env("REVOADA_OTLP_GRPC_ADDR", ":4317")
}

// AuthCacheTTL é o TTL do cache de autenticação de serverkey. Var: REVOADA_AUTH_CACHE_TTL.
func AuthCacheTTL() time.Duration {
	return EnvDuration("REVOADA_AUTH_CACHE_TTL", 30*time.Second)
}

// PGMaxConns é o teto de conexões do pool pgx. Var: REVOADA_PG_MAX_CONNS.
func PGMaxConns() int {
	return EnvInt("REVOADA_PG_MAX_CONNS", 20)
}

// NATSMaxBytes é o teto de armazenamento do stream de ingestão. Var: REVOADA_NATS_MAX_BYTES.
func NATSMaxBytes() int64 {
	return EnvInt64("REVOADA_NATS_MAX_BYTES", 2<<30) // 2 GiB
}

// NATSMaxMsgBytes é o teto (bytes serializados) de UMA mensagem publicada no NATS.
// Lotes maiores são fragmentados. Var: REVOADA_NATS_MAX_MSG_BYTES.
func NATSMaxMsgBytes() int {
	return EnvInt("REVOADA_NATS_MAX_MSG_BYTES", 900<<10) // ~900 KiB
}

// NATSDedupWindow é a janela de deduplicação do stream (Nats-Msg-Id). Publicar o MESMO
// lote duas vezes dentro dela grava UMA. Var: REVOADA_NATS_DEDUP_WINDOW.
//
// 2 min cobre o caso real: o agente recebe 503 (ou timeout) e reenvia no tick seguinte
// (15 s) ou ao drenar o WAL logo após um restart. Janela maior custa memória de índice
// no servidor NATS sem cobrir nenhum cenário novo.
func NATSDedupWindow() time.Duration {
	return EnvDuration("REVOADA_NATS_DEDUP_WINDOW", 2*time.Minute)
}

// NATSPublishTimeout é o prazo do publish de um lote no NATS. Var: REVOADA_NATS_PUBLISH_TIMEOUT.
//
// Precisa ser bem MENOR que o timeout de POST do agente (10 s): com o NATS fora, o
// handler tem de devolver 503 rápido em vez de segurar a requisição (e a memória do
// corpo dela) até o agente desistir sozinho.
func NATSPublishTimeout() time.Duration {
	return EnvDuration("REVOADA_NATS_PUBLISH_TIMEOUT", 5*time.Second)
}

// MaxInFlight é o teto GLOBAL de requisições de ingestão em voo. Var: REVOADA_MAX_INFLIGHT.
//
// POR QUE existe: não havia teto nenhum. Sob saturação o gateway aceitava tudo com 200 e
// ia ficando lento até o POST estourar o timeout de 10 s do agente — que re-bufferava e
// mandava de novo, engrossando a fila que já estava afogada. Cada requisição em voo pode
// segurar até 16 MiB de corpo descomprimido (maxOTLPBody), então o teto também é o que
// separa "lento" de "OOM": 128 × 16 MiB é o pior caso teórico; o caso real (lote de
// ~100 KiB) fica na casa de 13 MiB.
func MaxInFlight() int { return EnvInt("REVOADA_MAX_INFLIGHT", 128) }

// RateLimit devolve a taxa (req/s) e o burst do rate limiter de ingestão.
// Vars: REVOADA_RATE_LIMIT_RPS, REVOADA_RATE_LIMIT_BURST.
func RateLimit() (rps, burst int) {
	return EnvInt("REVOADA_RATE_LIMIT_RPS", 500), EnvInt("REVOADA_RATE_LIMIT_BURST", 2000)
}

// TrustedProxies são as redes cujo X-Forwarded-For o rate limit pode acreditar.
//
// Sem isto o balde por IP é uma armadilha em produção: o gateway só recebe tráfego
// pelo nginx (`proxy_pass http://gateway:8090`), então TODA requisição chega com o
// IP do proxy — e a frota inteira passa a dividir um balde único de 500 rps. Um
// único host drenando o WAL depois de uma queda passaria a devolver 429 para todos
// os outros agentes. O default cobre as faixas privadas onde a rede do Docker vive;
// confiar nelas é seguro porque o gateway não tem porta publicada em produção.
//
// Lista vazia = não confiar em cabeçalho nenhum (o comportamento certo quando o
// gateway está exposto direto, onde X-Forwarded-For é forjável pelo cliente).
func TrustedProxies() []string {
	return strings.Split(Env("REVOADA_TRUSTED_PROXIES", "10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,127.0.0.1/32,::1/128"), ",")
}

// CHBatch devolve os parâmetros do batcher de logs/spans para o ClickHouse:
// linhas por flush, intervalo de flush e capacidade da fila (lotes pendentes).
// Vars: REVOADA_CH_BATCH_MAX_ROWS, REVOADA_CH_BATCH_INTERVAL, REVOADA_CH_BATCH_QUEUE.
func CHBatch() (maxRows int, interval time.Duration, queue int) {
	return EnvInt("REVOADA_CH_BATCH_MAX_ROWS", 5000),
		EnvDuration("REVOADA_CH_BATCH_INTERVAL", time.Second),
		EnvInt("REVOADA_CH_BATCH_QUEUE", 256)
}

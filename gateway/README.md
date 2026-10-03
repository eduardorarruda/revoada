# gateway

Serviço Go de ingestão do **Revoada** (codinome interno *Revoada*; identificadores
técnicos seguem `revoada-*`). Recebe telemetria via **OTLP** (métricas, logs e traces) por
HTTP e gRPC, autentica cada remetente pela chave de ingestão (`X-Revoada-Key`, tabela `agents`),
grava o inventário de hosts no PostgreSQL, publica métricas no **NATS JetStream** (escritas
no ClickHouse por um writer embutido) e faz flush em lote de logs/spans no ClickHouse.
Também raspa alvos Prometheus e recebe descoberta de serviços e resultados de sonda dos agentes.

## Binários
- `cmd/gateway` — servidor de ingestão (OTLP HTTP + gRPC, scrape, discovery, sondas, health).
- `cmd/migrate` — aplica/reverte as migrations do ClickHouse (`-down` reverte a última;
  `-dir` aponta o diretório, ex.: `../deploy/migrations/clickhouse`).

## Endpoints
| Método | Rota | Auth | Descrição |
|---|---|---|---|
| GET | `/healthz` | — | Liveness — o processo está de pé (200). |
| GET | `/readyz`  | — | Readiness — checa ClickHouse, PostgreSQL e NATS. 200 se todos ok; 503 com detalhe por dependência. |
| GET | `/metrics` | — | Métricas Prometheus do próprio gateway. |
| POST | `/v1/metrics` | `X-Revoada-Key` | OTLP/HTTP de métricas (protobuf ou JSON; `Content-Encoding: gzip` suportado). Enfileiradas no NATS. |
| POST | `/v1/logs` | `X-Revoada-Key` | OTLP/HTTP de logs. Ingestão assíncrona (batcher → ClickHouse). |
| POST | `/v1/traces` | `X-Revoada-Key` | OTLP/HTTP de traces (protobuf ou JSON via ponte pdata — IDs em hex conforme o spec OTLP/JSON). |
| POST | `/ingest/logs` | `X-Revoada-Key` | Logs em JSON simples (agente/coletores leves). |
| POST | `/ingest/discovery` | `X-Revoada-Key` | Descoberta de serviços/containers do host. |
| POST | `/ingest/probe-result` | `X-Revoada-Key` | Resultado de sonda de site (agente em modo sonda). |
| GET | `/probe/assignments` | `X-Revoada-Key` | URLs que uma sonda deve testar. |

OTLP/gRPC (métricas, logs e traces) escuta em `:4317`; a chave vai no metadata `x-revoada-key`.
Os endpoints públicos de ingestão têm rate limit por chave/IP.

## Configuração (env vars, prefixo `REVOADA_`)
| Variável | Default | Descrição |
|---|---|---|
| `REVOADA_HTTP_ADDR` | `:8090` | Porta HTTP do gateway (OTLP/HTTP + ingest + health). |
| `REVOADA_OTLP_GRPC_ADDR` | `:4317` | Porta do receiver OTLP/gRPC. |
| `REVOADA_CH_ADDR` | `http://127.0.0.1:8123` | Endereço HTTP do ClickHouse. |
| `REVOADA_CH_USER` / `REVOADA_CH_PASSWORD` / `REVOADA_CH_DB` | `revoada` / `revoada` / `revoada` | Credenciais e database do ClickHouse. |
| `REVOADA_PG_DSN` | `postgres://revoada:revoada@127.0.0.1:5433/revoada?sslmode=disable` | DSN do PostgreSQL (agentes + inventário). |
| `REVOADA_NATS_URL` | `nats://127.0.0.1:4222` | URL de cliente do NATS (fila de métricas). |
| `REVOADA_NATS_MON_URL` | `http://127.0.0.1:8222/healthz` | Endpoint de monitoramento do NATS (readiness). |

### Amostragem de traces
`REVOADA_TRACE_SAMPLE` (fração dos traces normais, ex.: `0.2`) e `REVOADA_TRACE_SAMPLE_SLOW_MS`
(limiar em ms acima do qual o trace é sempre guardado). Traces com erro são sempre mantidos.

### Robustez / hardening (todos com default no código)
`REVOADA_RATE_LIMIT_RPS` (500) · `REVOADA_RATE_LIMIT_BURST` (2000) · `REVOADA_AUTH_CACHE_TTL` (30s) ·
`REVOADA_CH_BATCH_MAX_ROWS` (5000) · `REVOADA_CH_BATCH_INTERVAL` (1s) · `REVOADA_CH_BATCH_QUEUE` (256) ·
`REVOADA_NATS_MAX_BYTES` (2 GiB) · `REVOADA_NATS_MAX_MSG_BYTES` (~900 KiB) · `REVOADA_PG_MAX_CONNS` (20).

## Rodando localmente
```bash
# com o stack de dados de pé (deploy/docker-compose.dev.yml)
go run ./cmd/migrate -dir ../deploy/migrations/clickhouse   # aplica o schema do ClickHouse
go run ./cmd/gateway                                        # sobe o servidor em :8090 (+ :4317)
curl -s localhost:8090/readyz
```

## Healthcheck em produção
A imagem final é distroless (sem shell/curl). O próprio binário faz o self-check:
`gateway -healthcheck` faz um GET em `/healthz` e sai 0/1 — é o comando do `HEALTHCHECK` do container.

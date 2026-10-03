# Runbook — ambiente de desenvolvimento

Como subir o Revoada do zero e rodar as verificações. Escrito para quem nunca viu o projeto.

## Pré-requisitos
- Docker + Docker Compose
- Go 1.25+ no PATH
- `python3` e `curl` (usados pelo teste de integração)

## 1. Subir o stack de dados
```bash
make dev-up
docker compose -f deploy/docker-compose.dev.yml ps   # todos healthy
```
Sobe ClickHouse (8123/9000), PostgreSQL (5433→5432), Redis (6379), NATS (4222/8222).

## 2. Aplicar o schema do ClickHouse
```bash
make migrate
```
Cria `metrics`, `metrics_1m`/`metrics_1h` (downsampling via materialized view), `events`.
Idempotente — rodar de novo não faz nada.

## 3. Subir o gateway
```bash
cd gateway && go run ./cmd/gateway
curl -s localhost:8090/readyz    # {"ready":true,...}
```
Config por env `REVOADA_*` (ver gateway/README.md). O gateway cria as tabelas do Postgres
(`agents`, `hosts`) na primeira conexão.

## 4. Subir o painel e inscrever um agente
Com o gateway de pé, suba o painel (`server/`, ver [server/README.md](../server/README.md)),
crie o primeiro usuário e gere uma chave de ingestão em **Servidores › Adicionar servidor**.
O agente (`agent/`, binário `revoada-agent`) é apontado para o gateway com essa chave:
```bash
cd agent && go run ./cmd/revoada-agent -config ./agent.yaml   # agent.yaml: gateway + chave
```

Ver os dados:
```bash
docker exec revoada-clickhouse clickhouse-client --user revoada --password ##### \
  --query "SELECT metric, count() FROM revoada.metrics GROUP BY metric ORDER BY metric"
docker exec revoada-postgres psql -U revoada -d revoada -c "SELECT * FROM hosts;"
```

## Derrubar
```bash
make dev-down            # mantém os volumes (dados preservados)
docker compose -f deploy/docker-compose.dev.yml down -v   # apaga tudo
```

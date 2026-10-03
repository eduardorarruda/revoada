# Runbook — Disaster Recovery (RTO alvo: 30 min)

Recuperação total do Revoada após perda do servidor/volumes. Escrito para quem **não**
construiu o sistema. Pressupõe que os backups em MinIO/S3 estão acessíveis.

## O que é feito backup
| Dado | Origem | Destino | Retenção |
|---|---|---|---|
| Telemetria (métricas/eventos) | ClickHouse (database `revoada`) | `s3://backups/clickhouse/<DIA>` | 14 diários + 3 mensais |
| Metadados (agentes, hosts, alvos) | PostgreSQL (`revoada`) | `s3://backups/postgres/<DIA>.dump` | idem |

O sucesso/falha de cada backup vira a métrica `revoada.backup.success` (1/0) e
`revoada.backup.duration_seconds` (dá para alertar sobre ela como qualquer outra métrica).

## Agendamento (produção)
Não há scheduler embutido (design Compose, ADR 005). Agende via cron/systemd no host:
```cron
# /etc/cron.d/revoada-backup  — todo dia às 02:30
30 2 * * *  root  cd /opt/revoada && bash deploy/backup/backup.sh >> /var/log/revoada-backup.log 2>&1
```
`backup.sh` é idempotente por dia (re-run substitui o backup do dia).

## Backup manual
```bash
make backup            # ou: bash deploy/backup/backup.sh
```

## Teste de recuperação (rodar periodicamente!)
```bash
make restore-drill     # restaura em ambiente ISOLADO e compara contagens com a origem
```
Deve terminar com `RESTORE-DRILL OK`. Tabela de alta rotatividade (`metrics`) admite
divergência mínima (writes em voo, pois o backup é um snapshot pontual de um banco vivo);
tabelas estáveis (events/agents/hosts) batem exatamente.

## Recuperação TOTAL (servidor perdido) — passo a passo

**Meta: 30 min.** Pré-requisito: acesso aos backups (MinIO/S3) e ao repositório.

1. **Provisione o host** e instale Docker + Compose.
2. **Clone o repositório** e suba a infraestrutura vazia:
   ```bash
   git clone <repo> /opt/revoada && cd /opt/revoada
   docker compose -f deploy/docker-compose.dev.yml up -d clickhouse postgres redis nats minio
   ```
3. **Recrie o schema** do ClickHouse:
   ```bash
   cd gateway && go run ./cmd/migrate -dir ../deploy/migrations/clickhouse && cd ..
   ```
   (O schema do Postgres é recriado automaticamente pelo gateway ao subir.)
4. **Restaure o ClickHouse** (escolha o DIA do último backup bom):
   ```bash
   DAY=2026-07-13
   docker exec revoada-clickhouse clickhouse-client --user revoada --password ##### --query \
     "RESTORE DATABASE revoada FROM S3('http://minio:9000/backups/clickhouse/$DAY','#####','#####')"
   ```
   > Se o database `revoada` já existir vazio, use `RESTORE ... AS revoada` num database limpo ou
   > `DROP DATABASE revoada` antes. Para validar sem sobrescrever, restaure como `revoada_restored`.
5. **Restaure o PostgreSQL**:
   ```bash
   docker exec revoada-minio mc cp local/backups/postgres/$DAY.dump /tmp/pg.dump
   docker cp revoada-minio:/tmp/pg.dump /tmp/pg.dump && docker cp /tmp/pg.dump revoada-postgres:/tmp/pg.dump
   docker exec revoada-postgres pg_restore -U revoada -d revoada --clean --if-exists /tmp/pg.dump
   ```
6. **Suba o gateway** e valide:
   ```bash
   docker compose -f deploy/docker-compose.dev.yml up -d gateway
   curl -s localhost:8090/readyz         # ready:true
   ```
7. **Reaponte os agentes** (se o IP/DNS do gateway mudou) e confirme ingestão no próximo ciclo.

## Verificação pós-recuperação
- `/readyz` = 200 com clickhouse/postgres/nats ok.
- Contagem de `hosts` e `agents` bate com o esperado.
- Um dashboard de host mostra dados históricos (até o momento do último backup).
- Novos POSTs de agente aparecem em `metrics`.

## Revoada: chave mestra, CA dos agentes e migrations

**Ordem de recuperação do painel**

1. Suba o Postgres e restaure o dump mais recente (`restore-drill.sh` mostra o passo a passo).
2. Restaure a **chave mestra** no volume de dados *antes* de subir o painel:
   `gpg --decrypt revoada-chave-AAAA-MM-DD.tar.gz.gpg | tar -xz -C /var/lib/revoada`
   (veja `deploy/backup/backup-chave.sh`). Confira `chmod 600 chave-mestra.json`.
3. Suba o painel: ele aplica só as migrations que faltarem (`revoada-painel migracoes listar` mostra o estado).
4. Os agentes reconectam sozinhos: a CA dos agentes está no banco (cifrada pela chave mestra), então os
   certificados deles continuam válidos. Tarefas que estavam rodando são reconciliadas na reconexão.

**Se a chave mestra se perdeu (sem backup)**: o painel sobe com uma chave nova, mas tudo que estava cifrado
fica ilegível. Consequências e conserto:
- 2FA dos usuários: um administrador redefine o 2FA de cada um (Usuários & Acessos → Redefinir 2FA).
- Credenciais dos bancos: recadastre a senha de cada conexão.
- CA dos agentes: apague as linhas `agentes-ca` e `tarefas-ed25519` de `painel_chaves`, suba o painel (gera
  CA nova) e reinscreva os agentes com tokens novos.

**Voltar uma versão do painel**: rode `revoada-painel migracoes desfazer <última versão compatível>` com o
binário NOVO (é ele que conhece os `.down.sql`), depois instale o binário antigo. Faça backup antes.

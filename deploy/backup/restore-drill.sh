#!/usr/bin/env bash
# Restore-drill: restaura o backup mais recente num ambiente ISOLADO e valida contagens
# contra a origem. Não toca nos dados de produção (restaura em revoada_restored / db temporário).
# Uso: bash deploy/backup/restore-drill.sh [YYYY-MM-DD]
set -euo pipefail

DAY="${1:-$(date +%F)}"
CH="docker exec revoada-clickhouse clickhouse-client --user revoada --password revoada --query"
MC="docker exec revoada-minio mc"
MINIO_INT="http://minio:9000"
U="minioadmin"; P="minioadmin"; BUCKET="backups"

fail=0
check() { if [ "$2" = "$3" ]; then echo "  ok   $1 (origem=$2 restaurado=$3)"; else echo "  FAIL $1 (origem=$2 restaurado=$3)"; fail=1; fi; }

# checkApprox: para tabelas de alta rotatividade (metrics), o backup é um snapshot pontual
# e a origem VIVA cresce entre o backup e esta comparação. Aceita divergência < 0.5%
# desde que o restaurado seja não-vazio e próximo — o que prova que a restauração é íntegra.
checkApprox() {
  local label="$1" src="$2" res="$3"
  if [ "$res" -le 0 ]; then echo "  FAIL $label (restaurado vazio)"; fail=1; return; fi
  local diff=$(( src > res ? src - res : res - src ))
  local tol=$(( src / 200 + 50 )) # 0.5% + folga fixa para writes em voo
  if [ "$diff" -le "$tol" ]; then
    echo "  ok   $label (origem=$src restaurado=$res, Δ=$diff ≤ $tol — writes em voo)"
  else
    echo "  FAIL $label (origem=$src restaurado=$res, Δ=$diff > $tol)"; fail=1
  fi
}

echo "==> ClickHouse: restaura revoada -> revoada_restored ($DAY)"
$CH "DROP DATABASE IF EXISTS revoada_restored" >/dev/null
$CH "RESTORE DATABASE revoada AS revoada_restored FROM S3('$MINIO_INT/$BUCKET/clickhouse/$DAY', '$U', '$P')" 2>&1 | tail -1

src_metrics=$($CH "SELECT count() FROM revoada.metrics")
res_metrics=$($CH "SELECT count() FROM revoada_restored.metrics")
checkApprox "linhas em metrics" "$src_metrics" "$res_metrics"

src_events=$($CH "SELECT count() FROM revoada.events")
res_events=$($CH "SELECT count() FROM revoada_restored.events")
check "linhas em events" "$src_events" "$res_events"

echo "==> PostgreSQL: restaura em banco temporário revoada_restore"
$MC alias set local "$MINIO_INT" "$U" "$P" >/dev/null 2>&1 || true
$MC cp "local/$BUCKET/postgres/$DAY.dump" /tmp/pg_restore.dump >/dev/null 2>&1
docker cp revoada-minio:/tmp/pg_restore.dump /tmp/pg_restore.dump >/dev/null
docker cp /tmp/pg_restore.dump revoada-postgres:/tmp/pg_restore.dump >/dev/null
docker exec revoada-postgres psql -U revoada -d postgres -c "DROP DATABASE IF EXISTS revoada_restore;" >/dev/null 2>&1
docker exec revoada-postgres psql -U revoada -d postgres -c "CREATE DATABASE revoada_restore;" >/dev/null 2>&1
docker exec revoada-postgres pg_restore -U revoada -d revoada_restore /tmp/pg_restore.dump >/dev/null 2>&1 || true

src_agents=$(docker exec revoada-postgres psql -U revoada -d revoada -tAc "SELECT count(*) FROM agents")
res_agents=$(docker exec revoada-postgres psql -U revoada -d revoada_restore -tAc "SELECT count(*) FROM agents")
check "linhas em agents" "$src_agents" "$res_agents"

src_hosts=$(docker exec revoada-postgres psql -U revoada -d revoada -tAc "SELECT count(*) FROM hosts")
res_hosts=$(docker exec revoada-postgres psql -U revoada -d revoada_restore -tAc "SELECT count(*) FROM hosts")
check "linhas em hosts" "$src_hosts" "$res_hosts"

# limpeza dos artefatos do drill
$CH "DROP DATABASE IF EXISTS revoada_restored" >/dev/null
docker exec revoada-postgres psql -U revoada -d postgres -c "DROP DATABASE IF EXISTS revoada_restore;" >/dev/null 2>&1
rm -f /tmp/pg_restore.dump

if [ "$fail" = "0" ]; then echo "RESTORE-DRILL OK — dados batem com a origem"; else echo "RESTORE-DRILL FALHOU"; exit 1; fi

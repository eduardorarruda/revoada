#!/usr/bin/env bash
# Backup diário: ClickHouse (BACKUP -> S3/MinIO) + PostgreSQL (pg_dump -> MinIO).
# Retenção: 14 diários + 3 mensais. Emite métrica revoada.backup.* via OTLP (Fase 4 alerta).
# Uso: bash deploy/backup/backup.sh [YYYY-MM-DD]  (data opcional para testes/idempotência)
set -euo pipefail

DAY="${1:-$(date +%F)}"
CH="docker exec revoada-clickhouse clickhouse-client --user revoada --password revoada --query"
MC="docker exec revoada-minio mc"
MINIO_INT="http://minio:9000"
MINIO_USER="minioadmin"; MINIO_PASS="minioadmin"
BUCKET="backups"
GATEWAY="${REVOADA_GATEWAY:-http://127.0.0.1:8090}"
OTLP_KEY="${REVOADA_BACKUP_KEY:-BACKUP-AGENT}"

start=$(date +%s)
ok=1

# --- garante o bucket ---
$MC alias set local "$MINIO_INT" "$MINIO_USER" "$MINIO_PASS" >/dev/null 2>&1 || true
$MC mb --ignore-existing "local/$BUCKET" >/dev/null 2>&1 || true

echo "==> [1/3] ClickHouse BACKUP -> MinIO ($DAY)"
CH_PATH="$MINIO_INT/$BUCKET/clickhouse/$DAY"
# 1 backup por dia: remove o do dia se já existir (torna o re-run idempotente —
# o ClickHouse recusa BACKUP para um destino S3 que já existe).
$MC rm -r --force "local/$BUCKET/clickhouse/$DAY" >/dev/null 2>&1 || true
# BACKUP nativo do ClickHouse direto para o S3/MinIO.
if $CH "BACKUP DATABASE revoada TO S3('$CH_PATH', '$MINIO_USER', '$MINIO_PASS')" 2>&1 | grep -q BACKUP_CREATED; then
  echo "  ClickHouse: BACKUP_CREATED"
else
  echo "  ClickHouse: FALHOU"; ok=0
fi

echo "==> [2/3] PostgreSQL pg_dump -> MinIO"
if docker exec revoada-postgres pg_dump -U revoada -d revoada -Fc > /tmp/revoada_pg_$DAY.dump 2>/dev/null; then
  docker cp /tmp/revoada_pg_$DAY.dump revoada-minio:/tmp/pg.dump >/dev/null
  $MC cp /tmp/pg.dump "local/$BUCKET/postgres/$DAY.dump" >/dev/null 2>&1 || ok=0
  rm -f /tmp/revoada_pg_$DAY.dump
else
  ok=0
fi

echo "==> [3/3] retenção (14 diários + 3 mensais)"
# mantém os últimos 14 dias; dias que são dia-1 do mês contam como mensais (mantém 3).
prune() {
  local prefix="$1"
  mapfile -t all < <($MC ls "local/$BUCKET/$prefix/" 2>/dev/null | awk '{print $NF}' | sed 's#/$##' | sort)
  local keep_daily=14 keep_monthly=3
  local dailies=() monthlies=()
  for name in "${all[@]}"; do
    if [[ "$name" == *-01* || "$name" == *-01 ]]; then monthlies+=("$name"); else dailies+=("$name"); fi
  done
  # remove diários além dos 14 mais recentes
  local n=${#dailies[@]}
  for ((i=0; i<n-keep_daily; i++)); do
    $MC rm -r --force "local/$BUCKET/$prefix/${dailies[$i]}" >/dev/null 2>&1 || true
  done
  local m=${#monthlies[@]}
  for ((i=0; i<m-keep_monthly; i++)); do
    $MC rm -r --force "local/$BUCKET/$prefix/${monthlies[$i]}" >/dev/null 2>&1 || true
  done
}
prune clickhouse || true
prune postgres || true

dur=$(( $(date +%s) - start ))
echo "==> backup ok=$ok em ${dur}s"

# --- métrica revoada.backup.* via OTLP/HTTP (best-effort) ---
now_ns=$(( $(date +%s) * 1000000000 ))
curl -s -o /dev/null -X POST -H 'Content-Type: application/json' -H "X-Revoada-Key: $OTLP_KEY" \
  --data "{\"resourceMetrics\":[{\"scopeMetrics\":[{\"metrics\":[
    {\"name\":\"revoada.backup.success\",\"gauge\":{\"dataPoints\":[{\"timeUnixNano\":\"$now_ns\",\"asInt\":\"$ok\"}]}},
    {\"name\":\"revoada.backup.duration_seconds\",\"gauge\":{\"dataPoints\":[{\"timeUnixNano\":\"$now_ns\",\"asDouble\":$dur}]}}
  ]}]}]}" "$GATEWAY/v1/metrics" 2>/dev/null || true

[ "$ok" = "1" ] || { echo "BACKUP FALHOU"; exit 1; }
echo "BACKUP OK"

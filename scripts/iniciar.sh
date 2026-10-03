#!/usr/bin/env bash
# Sobe o Revoada com Docker: gera o .env (segredos aleatórios) na primeira vez e
# faz `docker compose up -d --build`. Rode da raiz do repositório.
set -euo pipefail
cd "$(dirname "$0")/.."

command -v docker >/dev/null || { echo "Instale o Docker: https://docs.docker.com/get-docker/" >&2; exit 1; }
docker compose version >/dev/null 2>&1 || { echo "Falta o plugin Docker Compose v2." >&2; exit 1; }

segredo() { openssl rand -hex "$1" 2>/dev/null || head -c "$1" /dev/urandom | od -An -tx1 | tr -d ' \n'; }

if [[ ! -f .env ]]; then
  umask 077
  sed -e "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=$(segredo 24)|" \
      -e "s|^CLICKHOUSE_PASSWORD=.*|CLICKHOUSE_PASSWORD=$(segredo 24)|" \
      -e "s|^REVOADA_JWT_SECRET=.*|REVOADA_JWT_SECRET=$(segredo 32)|" \
      .env.example > .env
  echo "→ .env criado com segredos aleatórios (guarde-o; não versione)."
fi

docker compose up -d --build

porta=$(grep -E '^REVOADA_PORTA=' .env | cut -d= -f2); porta=${porta:-8080}
echo "→ esperando o painel responder…"
for _ in $(seq 1 90); do
  if curl -fsS "http://localhost:${porta}/healthz" >/dev/null 2>&1; then
    echo ""
    echo "  Revoada no ar: http://localhost:${porta}"
    echo "  No primeiro acesso, crie o administrador (com 2FA)."
    exit 0
  fi
  sleep 2
done
echo "O painel não respondeu ainda. Veja: docker compose logs painel" >&2
exit 1

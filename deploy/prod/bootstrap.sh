#!/usr/bin/env bash
# ============================================================================
# Bootstrap ÚNICO do servidor de produção do Revoada (203.0.113.10).
# Roda como root. Cria dirs, gera segredos e instala o runner self-hosted do
# GitHub Actions. Depois disto, todo deploy é automático ao commit na main.
#
# Uso:
#   RUNNER_TOKEN=<token de Settings→Actions→Runners→New self-hosted runner> \
#   bash bootstrap.sh
#
# Idempotente: não sobrescreve o .env nem reconfigura um runner já existente.
# ============================================================================
set -euo pipefail

REPO_URL="https://github.com/eduardorarruda/revoada"
DEPLOY_DIR="/opt/revoada"
RUNNER_USER="revoada-runner"
RUNNER_DIR="/opt/actions-runner"
RUNNER_VERSION="2.319.1"   # ajuste para a versão atual se necessário

echo "==> 1/5 Diretórios"
mkdir -p "$DEPLOY_DIR/dist"

echo "==> 2/5 Segredos (.env)"
if [[ -f "$DEPLOY_DIR/.env" ]]; then
  echo "    .env já existe — preservando."
else
  umask 077
  cat > "$DEPLOY_DIR/.env" <<EOF
POSTGRES_PASSWORD=$(openssl rand -hex 24)
CLICKHOUSE_PASSWORD=$(openssl rand -hex 24)
REVOADA_JWT_SECRET=$(openssl rand -base64 48)
EOF
  echo "    .env gerado com segredos aleatórios."
fi

echo "==> 3/5 Usuário do runner (não-root, no grupo docker)"
if ! id "$RUNNER_USER" &>/dev/null; then
  useradd -m -s /bin/bash "$RUNNER_USER"
fi
usermod -aG docker "$RUNNER_USER"
chown -R "$RUNNER_USER":"$RUNNER_USER" "$DEPLOY_DIR"

echo "==> 4/5 Runner do GitHub Actions"
if [[ -f "$RUNNER_DIR/.runner" ]]; then
  echo "    runner já configurado — pulando."
else
  if [[ -z "${RUNNER_TOKEN:-}" ]]; then
    echo "ERRO: defina RUNNER_TOKEN (Settings → Actions → Runners → New self-hosted runner)." >&2
    exit 1
  fi
  mkdir -p "$RUNNER_DIR"; chown "$RUNNER_USER":"$RUNNER_USER" "$RUNNER_DIR"
  sudo -u "$RUNNER_USER" bash -c "
    cd '$RUNNER_DIR'
    curl -sSL -o runner.tar.gz https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz
    tar xzf runner.tar.gz && rm runner.tar.gz
    ./config.sh --unattended --url '$REPO_URL' --token '$RUNNER_TOKEN' \
      --name revoada-srv --labels revoada --replace
  "
  ./"$RUNNER_DIR"/svc.sh install "$RUNNER_USER" 2>/dev/null || (cd "$RUNNER_DIR" && ./svc.sh install "$RUNNER_USER")
  (cd "$RUNNER_DIR" && ./svc.sh start)
  echo "    runner instalado como serviço (label: revoada)."
fi

echo "==> 5/5 Rede externa do Traefik"
docker network inspect root_default >/dev/null 2>&1 && echo "    root_default OK" || echo "    ATENÇÃO: rede root_default não encontrada — confira o compose do Traefik."

cat <<EOF

==> Pronto. Próximos passos:
  1. Aponte o DNS: A revoada.exemplo.com.br -> 203.0.113.10
  2. Faça um push na main (ou rode o workflow 'Deploy Revoada' manualmente).
     O runner buildar e sobe o stack; o Traefik emite o certificado no 1º acesso HTTPS.
  3. Acesse https://revoada.exemplo.com.br e crie o 1º usuário (invite-only = vira admin).
EOF

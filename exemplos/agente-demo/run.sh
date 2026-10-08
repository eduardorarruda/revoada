#!/usr/bin/env bash
# Roda o agente de demonstração. Cria um venv na primeira vez (em $VENV, padrão .venv)
# e instala as versões fixadas em requirements.txt.
#
#   REVOADA_KEY=<chave> ./run.sh
#   REVOADA_GATEWAY=https://painel.exemplo.com REVOADA_KEY=<chave> RODADAS=20 ./run.sh
set -euo pipefail
cd "$(dirname "$0")"

: "${REVOADA_KEY:?defina REVOADA_KEY com a chave de ingestão (Infraestrutura → Adicionar servidor)}"
VENV="${VENV:-.venv}"

if [ ! -x "$VENV/bin/python" ]; then
  python3 -m venv "$VENV"
  "$VENV/bin/pip" install -q --disable-pip-version-check -r requirements.txt
fi

exec "$VENV/bin/python" agente.py

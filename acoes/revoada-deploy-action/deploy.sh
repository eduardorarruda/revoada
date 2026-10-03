#!/usr/bin/env bash
# Revoada Deploy: pede o deploy ao painel e acompanha até o fim.
# Funciona nos runners Linux, macOS e Windows (bash + curl + jq vêm instalados).
set -euo pipefail

painel="${REVOADA_PAINEL%/}"
: "${REVOADA_TOKEN:?informe o token de deploy}"
saida="${GITHUB_OUTPUT:-/dev/null}"
resumo="${GITHUB_STEP_SUMMARY:-/dev/null}"
commit="${GITHUB_SHA:-}"
repo="${GITHUB_REPOSITORY:-}"

corpo=$(jq -n --arg agente "$REVOADA_AGENTE" --arg aplicacao "$REVOADA_APLICACAO" --arg versao "$REVOADA_VERSAO" \
  --arg ambiente "${REVOADA_AMBIENTE:-}" --arg commit "$commit" \
  '{agente: $agente, aplicacao: $aplicacao, versao: $versao} + (if $ambiente != "" then {ambiente: $ambiente} else {} end)
   + (if $commit != "" then {commit: $commit} else {} end)')

# http MÉTODO CAMINHO [CORPO] → resposta em $RESPOSTA. Não roda em subshell: assim o
# ::error:: chega ao log do job e o exit encerra a Action de verdade.
RESPOSTA=""
http() {
  local metodo="$1" caminho="$2" dados="${3:-}" arq codigo
  arq=$(mktemp)
  codigo=$(curl -sS -o "$arq" -w '%{http_code}' -X "$metodo" "$painel$caminho" \
    -H "Authorization: Bearer $REVOADA_TOKEN" -H "Content-Type: application/json" \
    -H "X-Revoada-Repositorio: $repo" ${dados:+--data "$dados"}) || { echo "::error::painel inacessível em $painel"; exit 1; }
  if [[ "$codigo" -ge 300 ]]; then
    echo "::error::o painel respondeu $codigo: $(cat "$arq")"
    exit 1
  fi
  RESPOSTA=$(cat "$arq")
  rm -f "$arq"
}

http POST /api/deploys/acao "$corpo"
id=$(jq -r .id <<<"$RESPOSTA")
echo "deploy-id=$id" >>"$saida"
echo "Deploy $id: $REVOADA_APLICACAO $REVOADA_VERSAO em $REVOADA_AGENTE"

if [[ "${REVOADA_AGUARDAR:-true}" != "true" ]]; then
  exit 0
fi

limite=$(( $(date +%s) + ${REVOADA_TIMEOUT_MIN:-15} * 60 ))
estado=""
while (( $(date +%s) < limite )); do
  http GET "/api/deploys/acao/$id"
  t=$RESPOSTA
  estado=$(jq -r .estado <<<"$t")
  case "$estado" in
    sucesso|falha|cancelada|recusada) break ;;
  esac
  echo "… $estado"
  sleep 5
done
echo "estado=$estado" >>"$saida"

{
  echo "### Revoada Deploy — $REVOADA_APLICACAO \`$REVOADA_VERSAO\`"
  echo "- Servidor: \`$REVOADA_AGENTE\` · Estado: **$estado**"
  if [[ "$(jq -r '.resumo.revertido // false' <<<"$t")" == "true" ]]; then
    echo "- O health check falhou e o agente **reverteu** para \`$(jq -r '.resumo.anterior // "-"' <<<"$t")\`."
  fi
  echo '<details><summary>Saída</summary>'
  echo; echo '```'; jq -r '.resumo.saida // [] | .[]' <<<"$t"; echo '```'; echo '</details>'
} >>"$resumo"

case "$estado" in
  sucesso) echo "Deploy concluído: $REVOADA_APLICACAO $REVOADA_VERSAO no ar." ;;
  "") echo "::error::o deploy não terminou em ${REVOADA_TIMEOUT_MIN:-15} min (id $id)"; exit 1 ;;
  *) echo "::error::deploy $estado: $(jq -r '.erro // ""' <<<"$t")"; exit 1 ;;
esac

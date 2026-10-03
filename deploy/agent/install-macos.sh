#!/bin/sh
# Instalador do agente do Revoada para macOS (launchd). Rode como root.
#
#   sudo sh install-macos.sh --key <CHAVE> --gateway <URL> --agent-url <PREFIXO>
#
# O caminho normal é NÃO rodar isto à mão: o painel gera um arquivo .command com
# a chave já embutida (Infraestrutura → Instalar agente → macOS), e um duplo
# clique no Finder faz o resto.
#
# Opções:
#   --key        CHAVE de ingestão deste Mac (obrigatória, salvo com --enroll-token)
#   --enroll-token  TOKEN de inscrição do instalador UNIVERSAL: em vez da chave pronta,
#                o script pede ao painel a chave DESTA máquina. Exige --panel.
#   --panel      URL do painel (para a inscrição), ex: https://painel.exemplo
#   --gateway    URL do painel/gateway, ex: https://painel.exemplo (obrigatória)
#   --agent-url  PREFIXO da URL do binário — o script acrescenta "-arm64" ou
#                "-amd64" conforme o processador do Mac. Ex.:
#                https://painel.exemplo/revoada-agent-darwin
#   --probe      marca este Mac como sonda multi-região
#   --uninstall  remove o agente (daemon, binário, config e estado)
#
# Limites: só --mem-soft (GOMEMLIMIT, MB) e --max-procs (GOMAXPROCS) têm efeito
# aqui. O launchd não tem equivalente ao MemoryMax/CPUQuota do systemd, então as
# flags da cerca dura são aceitas e ignoradas — assim o painel pode gerar UMA
# linha de argumentos que serve para Linux e macOS.
set -eu

KEY=""
ENROLL_TOKEN=""
PANEL=""
GATEWAY=""
AGENT_URL=""
PROBE="false"
UNINSTALL="false"
MEM_SOFT="220"
MAX_PROCS="2"

LABEL="digital.revoada.painel.agent"
PLIST="/Library/LaunchDaemons/${LABEL}.plist"
BIN="/usr/local/bin/revoada-agent"
CFG_DIR="/etc/revoada"
CFG="${CFG_DIR}/agent.yaml"
STATE="/usr/local/var/revoada-agent"
LOG_DIR="/usr/local/var/log"

while [ $# -gt 0 ]; do
  case "$1" in
    --key) KEY="$2"; shift 2 ;;
    --enroll-token) ENROLL_TOKEN="$2"; shift 2 ;;
    --panel) PANEL="$2"; shift 2 ;;
    --gateway) GATEWAY="$2"; shift 2 ;;
    --agent-url) AGENT_URL="$2"; shift 2 ;;
    --probe) PROBE="true"; shift ;;
    --uninstall) UNINSTALL="true"; shift ;;
    --mem-soft) MEM_SOFT="$2"; shift 2 ;;
    --max-procs) MAX_PROCS="$2"; shift 2 ;;
    # Cerca dura do systemd: não existe no launchd. Aceita e ignora (ver acima).
    --mem-max|--mem-high|--cpu-quota|--nice|--tasks-max) shift 2 ;;
    --cron) shift ;;
    *) echo "opção desconhecida: $1" >&2; exit 2 ;;
  esac
done

[ "$(id -u)" = "0" ] || { echo "rode como root (sudo)" >&2; exit 1; }

if [ "$UNINSTALL" = "true" ]; then
  echo "==> desinstalando o agente revoada-agent"
  launchctl bootout "system/${LABEL}" 2>/dev/null || launchctl unload -w "$PLIST" 2>/dev/null || true
  rm -f "$PLIST" "$BIN"
  rm -rf "$CFG_DIR" "$STATE"
  echo "==> agente removido. UNINSTALL_OK"
  exit 0
fi

if [ -z "$KEY" ]; then
  [ -n "$ENROLL_TOKEN" ] || { echo "--key obrigatória (ou --enroll-token, no instalador universal)" >&2; exit 1; }
  [ -n "$PANEL" ] || { echo "--panel obrigatória junto com --enroll-token" >&2; exit 1; }
fi
[ -n "$GATEWAY" ] || { echo "--gateway obrigatória" >&2; exit 1; }
[ -n "$AGENT_URL" ] || { echo "--agent-url obrigatória" >&2; exit 1; }

# Apple Silicon (arm64) e Intel (x86_64) precisam de binários diferentes; um não
# roda no outro sem o Rosetta. O prefixo vem do painel, o sufixo sai do uname.
case "$(uname -m)" in
  arm64) ARCH="arm64" ;;
  x86_64) ARCH="amd64" ;;
  *) echo "processador $(uname -m) não suportado pelo agente" >&2; exit 1 ;;
esac
URL="${AGENT_URL}-${ARCH}"

echo "==> baixando o agente ($ARCH) de $URL"
# Valida que veio um Mach-O de verdade e não o index.html do painel (acontece se
# o binário ainda não foi publicado e o servidor web responde com o SPA). Mesma
# defesa do install.sh do Linux, com os magics do Mach-O.
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
baixou="false"
tentativa=1
espera=2
while [ "$tentativa" -le 3 ]; do
  if curl -fsSL "$URL" -o "$tmp" && [ -s "$tmp" ]; then
    magic="$(head -c4 "$tmp" | od -An -tx1 | tr -d ' \n')"
    # cffaedfe/cefaedfe = Mach-O (64/32 bits), cafebabe = binário universal.
    case "$magic" in
      cffaedfe|cefaedfe|cafebabe|feedfacf) baixou="true"; break ;;
    esac
    echo "aviso: o download não é um executável do macOS (tentativa $tentativa/3)" >&2
  else
    echo "aviso: download falhou (tentativa $tentativa/3)" >&2
  fi
  [ "$tentativa" -lt 3 ] && sleep "$espera" && espera=$((espera * 2))
  tentativa=$((tentativa + 1))
done
[ "$baixou" = "true" ] || { echo "erro: não consegui obter o agente em $URL" >&2; exit 1; }

echo "==> instalando o binário em $BIN"
mkdir -p "$(dirname "$BIN")"
chmod 0755 "$tmp"
mv -f "$tmp" "$BIN"
trap - EXIT
# Sem assinatura da Apple, a quarentena bloqueia um binário baixado. O agente é
# um daemon do sistema instalado pelo administrador, não um app da internet.
xattr -d com.apple.quarantine "$BIN" 2>/dev/null || true

mkdir -p "$CFG_DIR" "$STATE/buffer" "$LOG_DIR"
HOST="$(hostname -s)"

# Instalador universal: pede ao painel a chave DESTE Mac agora, com o binário já
# instalado (é ele que fala com o painel). Ver o mesmo trecho no install.sh.
if [ -z "$KEY" ]; then
  echo "==> pedindo ao painel a chave deste Mac ($HOST)"
  KEY="$("$BIN" enroll -token "$ENROLL_TOKEN" -panel "$PANEL" -hostname "$HOST")" || {
    echo "erro: a inscrição no painel falhou — o agente NÃO foi configurado" >&2
    exit 1
  }
  [ -n "$KEY" ] || { echo "erro: o painel não devolveu uma chave" >&2; exit 1; }
fi

# Marcador do hostname REAL, no mesmo formato do install.sh do Linux.
echo "REVOADA_HOSTNAME=$HOST"
echo "==> escrevendo $CFG"
cat > "$CFG" <<EOF
gateway_url: ${GATEWAY}
key: ${KEY}
hostname: ${HOST}
interval_seconds: 15
buffer_dir: ${STATE}/buffer
probe: ${PROBE}
collect_containers: false
# Auto-atualização DESLIGADA no macOS, e isto é uma limitação declarada, não um
# esquecimento. A troca de binário no Linux depende de um promotor que roda como
# root no ExecStartPre da unit systemd; o launchd não tem equivalente, e o agente
# (não-root) não consegue sobrescrever um binário instalado com dono root. Ligar
# a auto-atualização aqui só faria cada Mac baixar um binário que ninguém iria
# promover. Atualização no macOS = rodar este instalador de novo.
# A versão em uso continua visível no painel (o agente a reporta no inventário),
# então dá para saber quais Macs estão para trás. Ver docs/auto-atualizacao-agentes.md.
auto_update: false
resources:
  memory_limit_mb: ${MEM_SOFT}
  max_procs: ${MAX_PROCS}
EOF
chmod 0600 "$CFG"

echo "==> instalando o daemon do launchd ($LABEL)"
# Roda como root: no macOS criar um usuário de sistema exige dscl e deixa rastro
# difícil de remover; e o daemon precisa ler métricas do sistema todo de qualquer
# forma. KeepAlive religa o agente se ele cair; RunAtLoad sobe junto com o Mac.
cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>${LABEL}</string>
  <key>ProgramArguments</key>
  <array>
    <string>${BIN}</string>
    <string>-config</string>
    <string>${CFG}</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>${LOG_DIR}/revoada-agent.log</string>
  <key>StandardErrorPath</key><string>${LOG_DIR}/revoada-agent.log</string>
</dict>
</plist>
EOF
chmod 0644 "$PLIST"
chown root:wheel "$PLIST"

# bootout+bootstrap é a forma moderna (macOS 10.11+); load -w cobre os antigos.
launchctl bootout "system/${LABEL}" 2>/dev/null || true
if ! launchctl bootstrap system "$PLIST" 2>/dev/null; then
  launchctl load -w "$PLIST"
fi

echo "==> diagnóstico"
"$BIN" -config "$CFG" doctor || true

echo "==> pronto. Este Mac aparece como \"$HOST\" no painel em até um minuto."
echo "    Logs: tail -f ${LOG_DIR}/revoada-agent.log"
echo "    Desinstalar: sudo sh $0 --uninstall"

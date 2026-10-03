#!/bin/sh
# Instalador de 1 linha do agente do Revoada (revoada-agent, Linux, systemd). Rode como root.
#
#   curl -fsSL https://<host>/install.sh | sh -s -- --key <CHAVE> --gateway <URL>
#
# Opções:
#   --key      CHAVE de ingestão deste servidor (obrigatória, salvo com --enroll-token)
#   --enroll-token  TOKEN de inscrição do instalador UNIVERSAL — o mesmo arquivo serve
#              para vários servidores: em vez de trazer a chave pronta, o script pede ao
#              painel a chave DESTA máquina na hora de instalar. Exige --panel.
#   --panel    URL do painel (para a inscrição), ex: https://painel.exemplo
#   --gateway  URL do gateway, ex: http://gateway:8090 (obrigatória)
#   --agent-url  URL do binário revoada-agent (default: variável REVOADA_AGENT_URL)
#   --probe    marca este host como sonda multi-região
#   --cron     usa cron (revoada-agent once) em vez de systemd — servidores sem systemd
#              (fallback automático quando systemctl não existe)
#   --no-auto-update  instala com a auto-atualização DESLIGADA neste host. Por padrão ela
#              fica ligada: o agente pergunta ao painel de hora em hora se há versão nova,
#              baixa, verifica o SHA-256 e o systemd promove o binário no reinício seguinte.
#              Ver docs/auto-atualizacao-agentes.md.
#
# Limites de recurso (a "cerca dura" — todos com default seguro; edite se precisar):
#   --mem-max    MemoryMax da unit systemd (OOM se estourar). Default 256M
#   --mem-high   MemoryHigh (pressão de GC/reclaim antes do teto). Default 200M
#   --cpu-quota  CPUQuota (% de UM núcleo). Default 40%
#   --nice       Nice do processo (prioridade de CPU; maior = mais educado). Default 10
#   --tasks-max  TasksMax (teto de threads/goroutines-thread). Default 128
#   --mem-soft   GOMEMLIMIT do agente em MB (soft; GC agressivo perto do teto). Default 220
#   --max-procs  GOMAXPROCS do agente (núcleos em paralelo; 0=auto). Default 2
set -eu

KEY=""
ENROLL_TOKEN=""
PANEL=""
GATEWAY=""
AGENT_URL="${REVOADA_AGENT_URL:-}"
PROBE="false"
CRON="false"
UNINSTALL="false"
AUTO_UPDATE="true"
# Diretório onde o agente (não-root) estaga o binário novo e de onde o promotor
# (root, via ExecStartPre) o retira. O caminho é literal dos DOIS lados de
# propósito: se cada um deduzisse o seu, uma mudança de default de um lado
# deixaria o outro procurando num diretório que ninguém escreve — e a
# atualização falharia em silêncio.
UPDATE_DIR="/var/lib/revoada-agent/update"
PROMOTOR="/usr/local/lib/revoada/promote-update.sh"
# Defaults da cerca de recurso: generosos vs. o consumo real (pico observado ~14 MB /
# <1% de CPU), então nunca afetam operação normal — mas garantem o teto no pior caso.
MEM_MAX="256M"
MEM_HIGH="200M"
CPU_QUOTA="40%"
NICE="10"
TASKS_MAX="128"
MEM_SOFT="220"
MAX_PROCS="2"

while [ $# -gt 0 ]; do
  case "$1" in
    --key) KEY="$2"; shift 2 ;;
    --enroll-token) ENROLL_TOKEN="$2"; shift 2 ;;
    --panel) PANEL="$2"; shift 2 ;;
    --gateway) GATEWAY="$2"; shift 2 ;;
    --agent-url) AGENT_URL="$2"; shift 2 ;;
    --probe) PROBE="true"; shift ;;
    --cron) CRON="true"; shift ;;
    --no-auto-update) AUTO_UPDATE="false"; shift ;;
    --uninstall) UNINSTALL="true"; shift ;;
    --mem-max) MEM_MAX="$2"; shift 2 ;;
    --mem-high) MEM_HIGH="$2"; shift 2 ;;
    --cpu-quota) CPU_QUOTA="$2"; shift 2 ;;
    --nice) NICE="$2"; shift 2 ;;
    --tasks-max) TASKS_MAX="$2"; shift 2 ;;
    --mem-soft) MEM_SOFT="$2"; shift 2 ;;
    --max-procs) MAX_PROCS="$2"; shift 2 ;;
    *) echo "opção desconhecida: $1" >&2; exit 2 ;;
  esac
done

[ "$(id -u)" = "0" ] || { echo "rode como root" >&2; exit 1; }

# Modo desinstalação: remove serviço, binário, config, estado e o usuário revoada.
# Autossuficiente e idempotente — não precisa de --key/--gateway/--agent-url.
if [ "$UNINSTALL" = "true" ]; then
  echo "==> desinstalando o agente revoada-agent"
  systemctl disable --now revoada-agent 2>/dev/null || true
  rm -f /etc/systemd/system/revoada-agent.service
  systemctl daemon-reload 2>/dev/null || true
  systemctl reset-failed revoada-agent 2>/dev/null || true
  # A marca é lida ANTES de /var/lib sair; sem ela, o usuário (e o crontab dele)
  # não são nossos para apagar.
  NOSSO_REVOADA="false"
  # `if` e não `[ … ] && …`: sob `set -e`, um E-lógico que falha derruba o script —
  # e aqui a falha é o caso NORMAL (marca ausente).
  if [ -f /var/lib/revoada-agent/usuario-criado-pelo-instalador ]; then NOSSO_REVOADA="true"; fi
  if [ "$NOSSO_REVOADA" = "true" ]; then crontab -u revoada -r 2>/dev/null || true; fi
  rm -f /usr/local/bin/revoada-agent /usr/local/bin/revoada-agent.bak-*
  # O promotor da auto-atualização e o binário estagiado saem junto. Deixá-los
  # para trás faria uma reinstalação futura encontrar um binário "novo" antigo
  # esperando no estágio e promovê-lo por cima do que acabou de ser instalado.
  rm -f "$PROMOTOR"
  rmdir /usr/local/lib/revoada 2>/dev/null || true
  rm -rf /etc/revoada /var/lib/revoada-agent
  if [ "$NOSSO_REVOADA" = "true" ] && id revoada >/dev/null 2>&1; then userdel revoada 2>/dev/null || true; fi
  echo "==> agente removido. UNINSTALL_OK"
  exit 0
fi

if [ -z "$KEY" ]; then
  [ -n "$ENROLL_TOKEN" ] || { echo "--key obrigatória (ou --enroll-token, no instalador universal)" >&2; exit 1; }
  [ -n "$PANEL" ] || { echo "--panel obrigatória junto com --enroll-token" >&2; exit 1; }
fi
[ -n "$GATEWAY" ] || { echo "--gateway obrigatória" >&2; exit 1; }

# Endereço do painel para a auto-atualização. O instalador universal já traz
# --panel; o instalador por servidor nem sempre traz, mas SEMPRE traz --agent-url,
# que é o binário servido pelo próprio painel (https://painel/revoada-agent).
# Derivar dali é o que evita o pior desfecho silencioso: um host instalado sem
# panel_url nunca pergunta por versão nova e fica para trás sem nada indicar isso.
if [ -z "$PANEL" ] && [ -n "$AGENT_URL" ]; then
  PANEL="$(echo "$AGENT_URL" | sed -e 's|/revoada-agent.*$||' -e 's|/*$||')"
fi
# Sem endereço de painel não há a quem perguntar: melhor desligar explicitamente
# do que deixar o agente avisando de hora em hora que não sabe com quem falar.
if [ -z "$PANEL" ]; then
  AUTO_UPDATE="false"
fi

echo "==> criando usuário de sistema 'revoada' (não-root)"
# REVOADA_CRIADO_AQUI decide, na desinstalação, se o usuário pode ser apagado. `revoada` é
# o nome de uma ferramenta clássica de performance no Linux e existe em máquina
# antiga: reutilizamos a conta quando ela já está lá, e apagá-la depois — junto com
# o crontab INTEIRO dela — destruiria uma conta que o painel não criou. Agora isso é
# disparável por um clique remoto (auto-desinstalação), então a guarda importa mais.
REVOADA_CRIADO_AQUI="false"
if ! id revoada >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin revoada
  REVOADA_CRIADO_AQUI="true"
fi

echo "==> instalando o binário em /usr/local/bin/revoada-agent"
# Valida que o arquivo baixado é um binário ELF de verdade — não o index.html do SPA.
# Contexto do bug: se o binário sumir do dist (janela do deploy) e o nginx tiver
# fallback de SPA, a URL do agente devolve 200 com HTML; o `curl -f` "sucede", o
# chmod passa e sobe um "agente" que na verdade é HTML. Aqui checamos os 4 bytes
# mágicos do ELF (0x7f 'E' 'L' 'F') E rejeitamos explicitamente conteúdo HTML.
is_elf_binary() {
  # $1 = caminho do arquivo. Retorna 0 se começa com o magic ELF, 1 caso contrário.
  [ -s "$1" ] || return 1
  magic="$(head -c4 "$1" | od -An -tx1 2>/dev/null | tr -d ' \n')"
  [ "$magic" = "7f454c46" ]
}
looks_like_html() {
  # Heurística barata p/ mensagem clara: primeiros bytes do index do SPA.
  head -c 512 "$1" 2>/dev/null | tr 'A-Z' 'a-z' | grep -qE '<!doctype|<html'
}

install_from_url() {
  # Baixa para arquivo temporário, valida como ELF e só então promove ao destino.
  # RETRY com backoff (2/4/8s) cobre a janela do deploy (--delete no dist) e blips
  # de rede — tanto para HTTP não-2xx quanto para conteúdo inválido (HTML).
  tmp="$(mktemp)"
  # limpa o temporário em qualquer saída desta função/erro do script
  trap 'rm -f "$tmp"' EXIT
  attempt=1
  sleep_s=2
  while [ "$attempt" -le 3 ]; do
    if curl -fsSL "$AGENT_URL" -o "$tmp"; then
      if is_elf_binary "$tmp"; then
        # sucesso: promove atômico (mv no mesmo fs) e ajusta permissão
        chmod 0755 "$tmp"
        mv -f "$tmp" /usr/local/bin/revoada-agent
        trap - EXIT
        rm -f "$tmp" 2>/dev/null || true
        return 0
      fi
      if looks_like_html "$tmp"; then
        echo "aviso: a URL do agente retornou HTML em vez do binário — dist provavelmente sem o binário; tente novamente em instantes (tentativa $attempt/3)" >&2
      else
        echo "aviso: conteúdo baixado não é um binário ELF válido (tentativa $attempt/3)" >&2
      fi
    else
      echo "aviso: download do binário falhou (HTTP não-2xx ou rede) (tentativa $attempt/3)" >&2
    fi
    if [ "$attempt" -lt 3 ]; then
      sleep "$sleep_s"
      sleep_s=$((sleep_s * 2))
    fi
    attempt=$((attempt + 1))
  done
  echo "erro: não consegui obter um binário do agente válido em $AGENT_URL após 3 tentativas" >&2
  return 1
}

if [ -n "$AGENT_URL" ]; then
  install_from_url
elif [ -f "./revoada-agent" ]; then
  cp ./revoada-agent /usr/local/bin/revoada-agent
  chmod 0755 /usr/local/bin/revoada-agent
else
  echo "sem --agent-url nem ./revoada-agent local para instalar" >&2
  exit 1
fi

# ── Provisionamento "turn-key": liga tudo que o host suporta ──────────────────
# Um servidor adicionado pelo painel já sobe com TODAS as funcionalidades ativas:
# métricas por container Docker + coleta de logs do host (journald/docker/syslog/
# dmesg). Cada fonte só é habilitada se o host tiver o grupo/capacidade exigido —
# em máquinas sem docker/journald o agente simplesmente não coleta aquela fonte
# (degrada com elegância, sem falhar). O grupo `docker` é equivalente a root na
# prática; é concedido a pedido, e só quando o Docker está instalado.
if [ "$CRON" != "true" ] && command -v systemctl >/dev/null 2>&1; then
  USE_SYSTEMD="true"
else
  USE_SYSTEMD="false"
fi

# ── A troca do binário só é possível onde existe o promotor root ─────────────
# O prefixo `+` de ExecStartPre (rodar aquela linha como root, furando
# ProtectSystem=strict) só existe a partir do systemd 231. Em systemd anterior —
# CentOS 7 traz o 219 — o `+` é lido como parte do CAMINHO do executável, a unit
# fica inválida e o serviço NÃO SOBE. Instalar a linha às cegas transformaria a
# auto-atualização numa parada de coleta em toda máquina antiga da frota.
# Por isso: detectamos, e onde não dá o agente fica em modo "só estaga" —
# ele baixa e verifica, e a troca acontece na próxima execução deste instalador.
SYSTEMD_VER=0
if [ "$USE_SYSTEMD" = "true" ]; then
  SYSTEMD_VER="$(systemctl --version 2>/dev/null | head -n1 | sed -n 's/^systemd \([0-9][0-9]*\).*/\1/p')"
  case "$SYSTEMD_VER" in ''|*[!0-9]*) SYSTEMD_VER=0 ;; esac
fi
if [ "$USE_SYSTEMD" = "true" ] && [ "$SYSTEMD_VER" -ge 231 ] && [ "$AUTO_UPDATE" = "true" ]; then
  UPDATE_APPLY="systemd"
else
  UPDATE_APPLY="stage"
fi

SUP_GROUPS=""
add_sup_group() { getent group "$1" >/dev/null 2>&1 && SUP_GROUPS="${SUP_GROUPS:+$SUP_GROUPS }$1"; }

COL_JOURNALD="false"; COL_DOCKER="false"; COL_SYSLOG="false"; COL_DMESG="false"
if getent group systemd-journal >/dev/null 2>&1; then add_sup_group systemd-journal; COL_JOURNALD="true"; fi
if getent group docker           >/dev/null 2>&1; then add_sup_group docker;          COL_DOCKER="true";   fi
if getent group adm              >/dev/null 2>&1; then add_sup_group adm;             COL_SYSLOG="true";   fi
# dmesg (/dev/kmsg): sob systemd concedemos CAP_SYSLOG na unit; no modo cron só
# funciona se o kernel não restringe o dmesg (kernel.dmesg_restrict=0).
if [ "$USE_SYSTEMD" = "true" ]; then
  COL_DMESG="true"
elif [ "$(cat /proc/sys/kernel/dmesg_restrict 2>/dev/null || echo 1)" = "0" ]; then
  COL_DMESG="true"
fi

echo "==> coletores habilitados: containers=true journald=$COL_JOURNALD docker_logs=$COL_DOCKER syslog=$COL_SYSLOG dmesg=$COL_DMESG"

mkdir -p /etc/revoada
HOST="$(hostname)"

# Instalador universal: a chave deste servidor ainda não existe. Pedimos agora, com
# o binário já instalado — é ele que fala com o painel (assim o script não precisa
# interpretar JSON com sed, e a mesma implementação serve Linux, macOS e Windows).
# Sem chave não há instalação: falhar aqui é melhor que deixar um agente mudo.
if [ -z "$KEY" ]; then
  echo "==> pedindo ao painel a chave deste servidor ($HOST)"
  KEY="$(/usr/local/bin/revoada-agent enroll -token "$ENROLL_TOKEN" -panel "$PANEL" -hostname "$HOST")" || {
    echo "erro: a inscrição no painel falhou — o agente NÃO foi configurado" >&2
    exit 1
  }
  [ -n "$KEY" ] || { echo "erro: o painel não devolveu uma chave" >&2; exit 1; }
fi

# Marcador do hostname REAL (o mesmo que vai no agent.yaml e que o agente reporta).
# O orquestrador de provisionamento lê esta linha do stdout do install para casar o
# alvo com a linha de inventário `hosts` — fonte autoritativa, imune a SSH instável.
echo "REVOADA_HOSTNAME=$HOST"
echo "==> escrevendo /etc/revoada/agent.yaml"
cat > /etc/revoada/agent.yaml <<EOF
gateway_url: ${GATEWAY}
key: ${KEY}
hostname: ${HOST}
interval_seconds: 15
buffer_dir: /var/lib/revoada-agent/buffer
probe: ${PROBE}
collect_containers: true
collect_journald: ${COL_JOURNALD}
collect_docker_logs: ${COL_DOCKER}
collect_syslog: ${COL_SYSLOG}
collect_dmesg: ${COL_DMESG}
panel_url: ${PANEL}
auto_update: ${AUTO_UPDATE}
update_dir: ${UPDATE_DIR}
update_apply: ${UPDATE_APPLY}
resources:
  memory_limit_mb: ${MEM_SOFT}
  max_procs: ${MAX_PROCS}
EOF
chmod 0640 /etc/revoada/agent.yaml
chown root:revoada /etc/revoada/agent.yaml

echo "==> preparando /var/lib/revoada-agent"
mkdir -p /var/lib/revoada-agent/buffer
# A marca sobrevive ao processo do instalador: quem desinstala é outro programa
# (o promotor root, meses depois) e precisa da mesma resposta.
if [ "$REVOADA_CRIADO_AQUI" = "true" ]; then
  : > /var/lib/revoada-agent/usuario-criado-pelo-instalador
fi
# Diretório de estágio da auto-atualização. Precisa ser do revoada: é o agente
# (não-root) quem escreve o binário baixado aqui. O promotor roda como root e
# consegue ler de qualquer jeito.
mkdir -p "$UPDATE_DIR"
# ── O estágio da auto-atualização é ZERADO em toda instalação ────────────────
# Este script ACABOU de instalar o binário autoritativo em /usr/local/bin. Tudo
# que estiver no estágio é, por definição, mais velho que ele — e o promotor
# root, no ExecStartPre do `systemctl restart` que fecha este script, promoveria
# esse arquivo POR CIMA do que acabamos de instalar.
#
# Não é hipótese: medido em container. Instalou-se 0.9.0, semeou-se um 0.8.0 no
# estágio (o resíduo de uma tentativa anterior de auto-atualização), reexecutou-se
# o install.sh — e o serviço subiu em 0.8.0. Como o botão "Atualizar agente" do
# painel É este script rodando por SSH e terminando em `systemctl restart`, o
# efeito seria REBAIXAR o host em silêncio: o instalador relata sucesso, o binário
# certo chega ao disco, e o promotor o troca de volta. O único sintoma visível é a
# coluna de versão do painel continuar atrasada — exatamente o sintoma que o
# operador clicou em "Atualizar" para resolver.
#
# Vão junto `promovido`/`tentativas`: com eles no disco e sem a marca `saudavel`
# (que o agente novo ainda não escreveu), o promotor conta os starts e no 3º
# RESTAURA o `.bak-anterior` — outro caminho para o mesmo rebaixamento silencioso.
# O backup sai também, pelo mesmo motivo: ele é de uma versão anterior a esta
# instalação, e o promotor cria um novo na próxima promoção de verdade.
# O `--uninstall` já fazia essa limpeza (ver acima) e pelo mesmo motivo.
#
# O `desinstalar` entra nesta lista pelo mesmo motivo, e o preço dele é o maior de
# todos: uma ordem que ficou pendurada (máquina desligada antes do restart) faria o
# promotor, no `systemctl restart` que fecha ESTE script, destruir o agente que
# acabou de ser instalado — e o instalador relataria sucesso.
rm -f "$UPDATE_DIR"/revoada-agent.novo* \
      "$UPDATE_DIR"/controle.json \
      "$UPDATE_DIR"/desinstalar \
      "$UPDATE_DIR"/promovido "$UPDATE_DIR"/tentativas "$UPDATE_DIR"/saudavel
rm -f /usr/local/bin/revoada-agent.bak-anterior
chown -R revoada:revoada /var/lib/revoada-agent

# purga_cron_do_agente escreve em $1 o crontab do revoada SEM as linhas de coleta que
# esta instalação cria, preservando qualquer outra entrada do usuário. Precisa rodar
# nos DOIS modos: um host instalado antes com --cron e reinstalado com systemd
# ficava com o serviço E o cron ativos ao mesmo tempo, mandando o dobro de pontos
# para o painel — e ninguém percebia, porque os dois funcionam.
# Devolve 0 se conseguiu LER o crontab (mesmo vazio), 1 se a leitura falhou.
# A distinção é crítica: com `|| true` no pipeline, "não consegui ler" e "não tem
# crontab" viravam o mesmo arquivo vazio — e no ramo systemd o arquivo vazio dispara
# `crontab -r`. Ou seja, falha de LEITURA (spool sem permissão, SELinux, disco cheio,
# wrapper de crontab do painel de hospedagem) virava EXCLUSÃO do crontab de um
# usuário cujo conteúdo o script nunca chegou a ver. Num script que roda como root em
# servidor de cliente, esse é o pior desfecho possível.
# Devolve TRÊS estados, porque confundir dois deles já apagou crontab de cliente:
#   0 = li o crontab; o conteúdo (sem as linhas do agente) está em $1
#   1 = o usuário NÃO tem crontab — caso normal de máquina nova; é seguro escrever
#   2 = NÃO consegui ler; o chamador não pode escrever nada por cima
# O `crontab -l` sai != 0 nos casos 1 e 2 igualmente; o que os separa é a mensagem
# de erro. Sem essa separação, ou o instalador apaga o crontab de quem ele não
# conseguiu ler, ou avisa de um problema inexistente em toda instalação limpa.
purga_cron_do_agente() {
  erro="$(mktemp)"
  if atual="$(crontab -u revoada -l 2>"$erro")"; then
    printf '%s\n' "$atual" | grep -v 'revoada-agent .*\(once\|discover\)' > "$1" || true
    rm -f "$erro"
    return 0
  fi
  : > "$1"
  if grep -qiE 'no crontab for|nenhum crontab' "$erro"; then
    rm -f "$erro"
    return 1
  fi
  rm -f "$erro"
  return 2
}

if [ "$USE_SYSTEMD" != "true" ]; then
  echo "==> configurando cron (modo sem systemd) — métricas a cada 1 min, descoberta a cada 15 min"
  # No modo cron o agente roda como revoada SEM sandbox de unit; para os coletores
  # de log/containers funcionarem, o próprio usuário precisa dos grupos.
  if [ -n "$SUP_GROUPS" ]; then
    echo "==> adicionando revoada aos grupos: $SUP_GROUPS"
    usermod -aG "$(echo "$SUP_GROUPS" | tr ' ' ',')" revoada 2>/dev/null || true
  fi
  # Simétrico à purga de cron do ramo systemd: um host instalado com systemd e
  # reinstalado com --cron ficaria com o serviço E o cron coletando juntos.
  systemctl disable --now revoada-agent 2>/dev/null || true
  cronfile="$(mktemp)"
  # remove entradas antigas do revoada-agent e reescreve (idempotente); roda como usuário revoada
  # O `|| rc_cron=$?` não é estilo: sem ele, o `set -e` do topo mata o script
  # AQUI, mudo, em toda máquina nova. purga_cron_do_agente devolve 1 quando o
  # usuário simplesmente não tem crontab — que é o caso normal de um host limpo —
  # e 2 quando não conseguiu ler. Como chamada solta, os dois status derrubavam o
  # shell antes de qualquer um dos tratamentos abaixo rodar: o host ficava com
  # binário, chave e config, SEM agendamento nenhum, e o instalador saía sem
  # imprimir o motivo. Colocar a chamada numa lista AND-OR a isenta do `set -e` e
  # devolve os três estados a quem sabe o que fazer com eles.
  rc_cron=0
  purga_cron_do_agente "$cronfile" || rc_cron=$?
  if [ "$rc_cron" -eq 2 ]; then
    # Não conseguimos ler o crontab. Escrever agora significaria substituir o que
    # havia lá — e o que havia lá é do CLIENTE. Parar aqui deixa o host com binário
    # e chave mas sem agendamento; é ruim, e ainda assim muito melhor do que apagar
    # o backup noturno de alguém. Por isso as duas linhas saem impressas.
    rm -f "$cronfile"
    echo "erro: não consegui ler o crontab do usuário revoada, e não vou escrever por cima." >&2
    echo "      O agente está instalado; falta só agendá-lo. Adicione à mão:" >&2
    echo "        * * * * * /usr/local/bin/revoada-agent -config /etc/revoada/agent.yaml once >/dev/null 2>&1" >&2
    echo "        */15 * * * * /usr/local/bin/revoada-agent -config /etc/revoada/agent.yaml discover >/dev/null 2>&1" >&2
    exit 1
  fi
  echo "* * * * * /usr/local/bin/revoada-agent -config /etc/revoada/agent.yaml once >/dev/null 2>&1" >> "$cronfile"
  echo "*/15 * * * * /usr/local/bin/revoada-agent -config /etc/revoada/agent.yaml discover >/dev/null 2>&1" >> "$cronfile"
  # Sem o `||`, o `set -e` abortaria mudo e o operador acharia que instalou.
  crontab -u revoada "$cronfile" || {
    rm -f "$cronfile"
    echo "erro: não consegui instalar o cron do agente; o host não vai coletar." >&2
    exit 1
  }
  rm -f "$cronfile"
else
  echo "==> instalando a unit systemd"
  # Purga o cron de uma instalação anterior em modo --cron. Sem isto, o host fica
  # com o serviço E o cron coletando ao mesmo tempo e manda o dobro de pontos.
  cronfile="$(mktemp)"
  if purga_cron_do_agente "$cronfile"; then
    # Só mexe no crontab quando conseguiu LER o que havia lá.
    if [ -s "$cronfile" ]; then
      # Limpeza incidental não pode derrubar a instalação: sem o `||`, o `set -e`
      # mataria o script AQUI, antes de escrever a unit e de habilitar o serviço —
      # deixando o host com binário e chave, mas sem coletar nada.
      crontab -u revoada "$cronfile" || echo "aviso: não consegui reescrever o cron do revoada (o serviço systemd segue normalmente)" >&2
    else
      crontab -u revoada -r 2>/dev/null || true
    fi
  else
    echo "aviso: não consegui ler o cron do revoada; não vou mexer nele. Se este host já foi instalado com --cron, remova as linhas do revoada-agent à mão para não coletar em dobro" >&2
  fi
  rm -f "$cronfile"
  # Escreve a unit inline (não depende de baixar um arquivo — evita pegar o
  # index.html do SPA por engano se o .service não estiver no dist).
  #
  # Provisionamento turn-key: a sandbox é afrouxada SÓ o necessário para as fontes
  # detectadas acima (SupplementaryGroups p/ docker/journald/adm; CAP_SYSLOG p/
  # dmesg). Em host sem docker/journald, as linhas correspondentes ficam vazias.
  if [ "$COL_DMESG" = "true" ]; then
    CAP_BOUND="CapabilityBoundingSet=CAP_SYSLOG"
    CAP_AMBIENT="AmbientCapabilities=CAP_SYSLOG"
  else
    CAP_BOUND="CapabilityBoundingSet="
    CAP_AMBIENT="AmbientCapabilities="
  fi
  if [ -n "$SUP_GROUPS" ]; then
    SUP_LINE="SupplementaryGroups=$SUP_GROUPS"
  else
    SUP_LINE=""
  fi

  # ── Promotor do binário auto-atualizado (roda como root, no ExecStartPre) ──
  # CÓPIA OPERACIONAL de deploy/agent/promote-update.sh — mude os dois juntos.
  # A duplicação é a mesma da unit systemd logo abaixo, e pelo mesmo motivo:
  # este instalador não pode depender de baixar arquivos soltos do dist (se o
  # nginx tiver fallback de SPA, um arquivo ausente vira index.html com 200).
  #
  # O agente roda como revoada e o binário é de root: ele NÃO consegue trocar o
  # próprio binário — e não deve, senão comprometer o agente passa a valer virar
  # root. O agente baixa e VERIFICA (SHA-256 + o binário roda e diz a versão
  # certa) em $UPDATE_DIR; quem move para /usr/local/bin é este script.
  if [ "$UPDATE_APPLY" = "systemd" ]; then
    echo "==> instalando o promotor da auto-atualização em $PROMOTOR"
    mkdir -p "$(dirname "$PROMOTOR")"
    cat > "$PROMOTOR" <<'PROMOTE'
#!/bin/sh
# Promotor do binário do agente do Revoada. Roda como root, via
# ExecStartPre=-+ da unit, antes de cada start. Canônico em
# deploy/agent/promote-update.sh (com a justificativa completa).
set -eu
DIR="/var/lib/revoada-agent/update"
BIN="/usr/local/bin/revoada-agent"
NOVO="$DIR/revoada-agent.novo"
META="$NOVO.meta"
BACKUP="$BIN.bak-anterior"
MAX_TENTATIVAS=3

log() { echo "revoada-agent[promote]: $*" >&2; }

# Desinstalação pedida pelo painel (ver deploy/agent/promote-update.sh). O agente
# roda como revoada e não consegue se remover; deixa o bilhete e o root faz o resto,
# destacado, para não travar o start que está acontecendo agora.
DESINSTALAR="$DIR/desinstalar"
if [ -f "$DESINSTALAR" ] && [ ! -L "$DESINSTALAR" ]; then
  log "pedido de desinstalação encontrado; removendo o agente desta máquina"
  # O bilhete NÃO é apagado aqui. Apagá-lo antes do trabalho abria a janela pior de
  # todas: a máquina cai no meio da remoção (queda de energia, reboot do operador) e
  # volta com a unit ainda instalada, o binário no lugar e NENHUM pedido no disco —
  # agente vivo, ordem perdida, e o painel já tinha encerrado o caso. Deixando-o
  # onde está, o próximo start tenta de novo; quem o leva embora é o
  # `rm -rf /var/lib/revoada-agent` do próprio trabalho, ou seja, só quando a
  # remoção de fato chegou até o fim.
  # A marca do instalador é lida ANTES de /var/lib sair. Sem ela, o usuário `revoada`
  # já existia nesta máquina e não é nosso para apagar — nem o crontab dele.
  NOSSO_REVOADA="false"
  # `if` e não `[ … ] && …`: sob `set -e`, um E-lógico que falha derruba o script —
  # e aqui a falha é o caso NORMAL (marca ausente).
  if [ -f /var/lib/revoada-agent/usuario-criado-pelo-instalador ]; then NOSSO_REVOADA="true"; fi
  setsid /bin/sh -c '
    sleep 2
    systemctl disable --now revoada-agent 2>/dev/null || true
    rm -f /etc/systemd/system/revoada-agent.service
    systemctl daemon-reload 2>/dev/null || true
    systemctl reset-failed revoada-agent 2>/dev/null || true
    if [ "$1" = "true" ]; then crontab -u revoada -r 2>/dev/null || true; fi
    rm -f /usr/local/bin/revoada-agent /usr/local/bin/revoada-agent.bak-*
    rm -rf /etc/revoada /var/lib/revoada-agent /usr/local/lib/revoada
    if [ "$1" = "true" ] && id revoada >/dev/null 2>&1; then userdel revoada 2>/dev/null || true; fi
    logger -t revoada-agent "agente desinstalado a pedido do painel" 2>/dev/null || true
  ' sh "$NOSSO_REVOADA" >/dev/null 2>&1 &
  exit 0
fi

# Sem ferramenta de hash NÃO promovemos: instalar um binário sem conseguir
# verificá-lo é pior do que ficar na versão antiga.
sha256_de() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" 2>/dev/null | cut -d' ' -f1
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$1" 2>/dev/null | sed 's/.*= *//'
  fi
}
# Mesma checagem do instalador: um "binário" que é o index.html do SPA (janela
# de deploy + fallback do nginx) passa por arquivo comum e não executa.
eh_elf() {
  [ -s "$1" ] || return 1
  [ "$(head -c4 "$1" | od -An -tx1 2>/dev/null | tr -d ' \n')" = "7f454c46" ]
}
valor_meta() { grep "^$1=" "$META" 2>/dev/null | head -n1 | cut -d= -f2- | tr -d ' \r'; }

if [ -f "$NOVO" ] && [ -f "$META" ]; then
  # O diretório de estágio é gravável pelo agente; um link simbólico faria este
  # script copiar outro arquivo do sistema para /usr/local/bin com modo 0755.
  if [ -L "$NOVO" ]; then
    log "o binário estagiado é um link simbólico; recusando e descartando"
    rm -f "$NOVO" "$META"; exit 0
  fi
  esperado="$(valor_meta sha256)"; versao="$(valor_meta versao)"
  obtido="$(sha256_de "$NOVO")"
  if [ -z "$obtido" ]; then
    log "sem sha256sum nem openssl: não consigo verificar o binário, não vou promovê-lo"; exit 0
  fi
  # Segunda verificação do mesmo checksum que o agente já conferiu: é aqui que o
  # arquivo deixa de ser dado e vira o executável do serviço.
  if [ "$obtido" != "$esperado" ]; then
    log "SHA-256 do binário estagiado não confere; descartando"
    rm -f "$NOVO" "$META"; exit 0
  fi
  if ! eh_elf "$NOVO"; then
    log "o arquivo estagiado não é um binário ELF; descartando"
    rm -f "$NOVO" "$META"; exit 0
  fi
  # Cópia de segurança: uma versão que sobe e estoura viraria laço infinito com
  # Restart=always, e sair dele exigiria o SSH que isto veio eliminar.
  if [ -f "$BIN" ]; then
    cp -f "$BIN" "$BACKUP" 2>/dev/null || log "aviso: não consegui guardar a cópia de segurança"
  fi
  # Troca atômica: escrever direto sobre $BIN deixaria uma janela com o arquivo
  # pela metade — justo a janela em que o systemd vai executá-lo.
  tmp="$BIN.novo.$$"
  if cp -f "$NOVO" "$tmp" && chmod 0755 "$tmp" && chown root:root "$tmp" 2>/dev/null && mv -f "$tmp" "$BIN"; then
    rm -f "$NOVO" "$META"
    printf '%s\n' "$versao" > "$DIR/promovido"
    rm -f "$DIR/saudavel"
    printf '0\n' > "$DIR/tentativas"
    log "binário promovido para a versão ${versao:-desconhecida}"
  else
    rm -f "$tmp"
    log "não consegui instalar o binário estagiado; o serviço segue na versão atual"
  fi
  exit 0
fi

# Promovemos e o agente nunca se declarou são? Desfaz depois de N starts.
if [ -f "$DIR/promovido" ] && [ ! -f "$DIR/saudavel" ]; then
  n="$(cat "$DIR/tentativas" 2>/dev/null || echo 0)"
  case "$n" in ''|*[!0-9]*) n=0 ;; esac
  n=$((n + 1))
  printf '%s\n' "$n" > "$DIR/tentativas"
  if [ "$n" -ge "$MAX_TENTATIVAS" ] && [ -f "$BACKUP" ]; then
    log "a versão promovida não se sustentou em $n starts; voltando ao binário anterior"
    tmp="$BIN.anterior.$$"
    if cp -f "$BACKUP" "$tmp" && chmod 0755 "$tmp" && mv -f "$tmp" "$BIN"; then
      rm -f "$DIR/promovido" "$DIR/tentativas"
      log "binário anterior restaurado"
    else
      rm -f "$tmp"; log "não consegui restaurar o binário anterior"
    fi
  fi
fi
exit 0
PROMOTE
    chmod 0755 "$PROMOTOR"
    chown root:root "$PROMOTOR" 2>/dev/null || true
    # Os dois prefixos são obrigatórios: `+` roda como root furando
    # ProtectSystem=strict (systemd >= 231), `-` faz uma falha do promotor ser
    # ignorada — sem ele, um defeito aqui impediria o serviço de subir, e a
    # atualização derrubaria a coleta.
    EXEC_PRE="ExecStartPre=-+$PROMOTOR"
  else
    # systemd < 231 ou auto-atualização desligada: nenhuma linha de promoção.
    # Escrevê-la num systemd antigo tornaria a unit INVÁLIDA (o `+` viraria parte
    # do caminho) e o serviço não subiria — a atualização viraria uma queda.
    EXEC_PRE=""
    rm -f "$PROMOTOR" 2>/dev/null || true
    if [ "$AUTO_UPDATE" = "true" ] && [ "$SYSTEMD_VER" -gt 0 ] && [ "$SYSTEMD_VER" -lt 231 ]; then
      echo "==> systemd $SYSTEMD_VER não suporta ExecStartPre=+ (precisa de 231+): a auto-atualização vai apenas ESTAGIAR o binário; rode este instalador de novo para aplicá-lo"
    fi
  fi

  cat > /etc/systemd/system/revoada-agent.service <<UNIT
[Unit]
Description=Revoada agent (coleta de métricas do host)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=revoada
Group=revoada
${EXEC_PRE}
ExecStart=/usr/local/bin/revoada-agent -config /etc/revoada/agent.yaml
Restart=always
RestartSec=5
# Cerca dura de recurso (defesa em profundidade): o kernel/systemd limita o agente
# independentemente do código. Valores editáveis via flags do install (ou editando
# esta unit + daemon-reload). Generosos vs. o consumo real, então invisíveis em
# operação normal — só agem num pico anômalo, protegendo os serviços do host.
MemoryMax=${MEM_MAX}
MemoryHigh=${MEM_HIGH}
CPUQuota=${CPU_QUOTA}
TasksMax=${TASKS_MAX}
Nice=${NICE}
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
${CAP_BOUND}
${CAP_AMBIENT}
${SUP_LINE}
StateDirectory=revoada-agent
ReadWritePaths=/var/lib/revoada-agent

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable revoada-agent
  # `enable --now` só faz START, e start numa unit já ativa é no-op: o binário novo
  # ficava no disco enquanto o processo velho seguia rodando do inode antigo. Como
  # este script É o caminho de atualização da frota (o botão "Atualizar agente"
  # reexecuta ele por SSH), a atualização era relatada como bem-sucedida e não
  # acontecia — um host podia ficar meses numa versão que o painel dizia ter trocado.
  # `restart` sobe se estiver parado e reinicia se estiver rodando.
  systemctl restart revoada-agent
fi

echo "==> diagnóstico"
/usr/local/bin/revoada-agent -config /etc/revoada/agent.yaml doctor || true

echo "==> pronto. Logs: journalctl -u revoada-agent -f"

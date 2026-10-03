#!/bin/sh
# Promotor do binário do agente do Revoada.
#
# Roda como ROOT, pelo systemd, em ExecStartPre — antes de cada start do serviço:
#
#     ExecStartPre=-+/usr/local/lib/revoada/promote-update.sh
#
# O prefixo `+` faz o systemd rodar esta linha com privilégio total, ignorando
# User=revoada e ProtectSystem=strict; sem ele, /usr é somente-leitura e nada aqui
# funcionaria. O prefixo `-` faz uma falha daqui ser ignorada: se este script
# quebrar, o serviço sobe do mesmo jeito com o binário que já está instalado. A
# coleta nunca pode parar por causa da atualização.
#
# POR QUE ESTE SCRIPT EXISTE
#
# O agente roda como `revoada` (não-root) e o binário vive em /usr/local/bin, de
# root. O agente, portanto, NÃO consegue trocar o próprio binário — e não deve:
# no dia em que conseguir, comprometer o agente passa a valer virar root. Então o
# agente só BAIXA e VERIFICA (SHA-256 + o binário roda e diz a versão certa), e
# quem move para /usr/local/bin é este script. O único componente privilegiado do
# caminho continua sendo o systemd, que já era o único componente privilegiado do
# serviço.
#
# POR QUE EM /bin/sh E NÃO NUM SUBCOMANDO DO PRÓPRIO AGENTE
#
# Duas razões, ambas concretas:
#
#   1. Ovo e galinha. Se a promoção fosse `revoada-agent promote`, quem a
#      executaria seria o binário ANTIGO — e todo agente já instalado hoje não
#      conhece esse subcomando. Pior: o main do agente cai no laço de coleta ao
#      receber um argumento desconhecido, e um ExecStartPre que nunca retorna
#      deixa a unit presa em "activating" para sempre. O host pararia de coletar.
#   2. Menos código rodando como root. Isto aqui são ~30 linhas de shell sem
#      dependência; o binário do agente tem rede, YAML, Docker e journald dentro.
#
# CONTRATO COM O AGENTE (agent/internal/selfupdate — mudar aqui exige mudar lá):
#
#   $DIR/revoada-agent.novo        binário verificado, escrito pelo agente
#   $DIR/revoada-agent.novo.meta   "sha256=<hex>" e "versao=<x.y.z>"
#   $DIR/saudavel                       o agente se declara são depois de 2 min de pé
#   $DIR/promovido                      escrito aqui; some quando o agente fica são
#   $DIR/tentativas                     contador de starts sem saúde; some junto
set -eu

DIR="/var/lib/revoada-agent/update"
BIN="/usr/local/bin/revoada-agent"
NOVO="$DIR/revoada-agent.novo"
META="$NOVO.meta"
BACKUP="$BIN.bak-anterior"
# Quantos starts sem o agente se declarar são antes de desfazer a promoção.
# Com RestartSec=5, três starts são ~15 s: rápido o bastante para não deixar o
# host cego, e folgado o bastante para não desfazer por causa de um reboot.
MAX_TENTATIVAS=3

log() { echo "revoada-agent[promote]: $*" >&2; }

# ── DESINSTALAÇÃO PEDIDA PELO PAINEL ────────────────────────────────────────
# O agente roda como `revoada` e não consegue parar o próprio serviço nem apagar
# arquivos de root. Quando o painel manda o servidor ser removido, o agente deixa
# este bilhete no diretório de estágio (que lhe pertence) e pede o reinício; a
# remoção acontece AQUI, no único componente que já era privilegiado.
#
# Vem ANTES da promoção de binário de propósito: quem vai ser removido não precisa
# de versão nova, e promover primeiro seria trabalho jogado fora.
#
# O trabalho roda DESTACADO (setsid, em segundo plano). Um `systemctl disable --now`
# disparado de dentro do ExecStartPre da própria unit para o serviço que o systemd
# está tentando iniciar — e o systemd fica esperando por quem já não vai voltar. Em
# segundo plano, este script sai limpo, o start segue, e a remoção acontece logo
# atrás.
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

# sha256_de imprime o SHA-256 do arquivo $1, ou nada se não houver ferramenta.
# Sem ferramenta de hash NÃO promovemos: instalar um binário sem conseguir
# verificá-lo é pior do que ficar na versão antiga.
sha256_de() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" 2>/dev/null | cut -d' ' -f1
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$1" 2>/dev/null | sed 's/.*= *//'
  fi
}

# eh_elf confere os 4 bytes mágicos do ELF. É a mesma checagem do install.sh e
# pelo mesmo motivo: um "binário" que na verdade é o index.html do SPA (janela de
# deploy + fallback de SPA no nginx) passa por arquivo comum e não executa.
eh_elf() {
  [ -s "$1" ] || return 1
  [ "$(head -c4 "$1" | od -An -tx1 2>/dev/null | tr -d ' \n')" = "7f454c46" ]
}

valor_meta() { grep "^$1=" "$META" 2>/dev/null | head -n1 | cut -d= -f2- | tr -d ' \r'; }

# ─── 1. Promoção: existe binário novo verificado esperando? ──────────────────
if [ -f "$NOVO" ] && [ -f "$META" ]; then
  # Nunca seguir link simbólico. O diretório de estágio é gravável pelo agente;
  # um link apontando para outro arquivo do sistema faria este script copiá-lo
  # para /usr/local/bin com permissão 0755 — leitura para todo mundo de um
  # arquivo que talvez só root pudesse ler.
  if [ -L "$NOVO" ]; then
    log "o binário estagiado é um link simbólico; recusando e descartando"
    rm -f "$NOVO" "$META"
    exit 0
  fi

  esperado="$(valor_meta sha256)"
  versao="$(valor_meta versao)"
  obtido="$(sha256_de "$NOVO")"

  if [ -z "$obtido" ]; then
    log "sem sha256sum nem openssl neste host: não consigo verificar o binário, não vou promovê-lo"
    exit 0
  fi
  # Segunda verificação do MESMO checksum que o agente já conferiu. Não é
  # redundância inútil: entre a verificação do agente e este start houve uma
  # troca de processo e um reinício, e é aqui que o arquivo deixa de ser dado
  # para virar o executável do serviço. Divergiu, descarta — nunca instala.
  if [ "$obtido" != "$esperado" ]; then
    log "SHA-256 do binário estagiado não confere (esperado $esperado, obtido $obtido); descartando"
    rm -f "$NOVO" "$META"
    exit 0
  fi
  if ! eh_elf "$NOVO"; then
    log "o arquivo estagiado não é um binário ELF; descartando"
    rm -f "$NOVO" "$META"
    exit 0
  fi

  # Guarda o binário atual para poder desfazer. Uma versão que sobe e estoura
  # transformaria Restart=always num laço infinito; sem cópia de segurança, sair
  # desse laço exige justamente o SSH que a auto-atualização veio eliminar.
  if [ -f "$BIN" ]; then
    cp -f "$BIN" "$BACKUP" 2>/dev/null || log "aviso: não consegui guardar a cópia de segurança do binário atual"
  fi

  # Troca atômica: copia para um temporário NO MESMO sistema de arquivos e
  # renomeia. Escrever direto sobre $BIN deixaria uma janela em que o arquivo
  # está pela metade — e é justo a janela em que o systemd vai executá-lo.
  tmp="$BIN.novo.$$"
  if cp -f "$NOVO" "$tmp" && chmod 0755 "$tmp" && chown root:root "$tmp" 2>/dev/null && mv -f "$tmp" "$BIN"; then
    rm -f "$NOVO" "$META"
    printf '%s\n' "$versao" > "$DIR/promovido"
    # A marca de saúde é do binário ANTERIOR; deixá-la faria o passo 2 achar que
    # esta promoção já foi validada e nunca desfazer uma versão ruim.
    rm -f "$DIR/saudavel"
    printf '0\n' > "$DIR/tentativas"
    log "binário promovido para a versão ${versao:-desconhecida}"
  else
    rm -f "$tmp"
    log "não consegui instalar o binário estagiado; o serviço segue na versão atual"
  fi
  exit 0
fi

# ─── 2. Desfazer: promovemos e o agente nunca se declarou são? ───────────────
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
      rm -f "$tmp"
      log "não consegui restaurar o binário anterior"
    fi
  fi
fi

exit 0

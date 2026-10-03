#!/bin/sh
# Prova do install.sh e do promotor da auto-atualização.
#
# ATENÇÃO: este script cria usuários, escreve em /usr/local/bin, /etc e
# /var/lib e mexe no crontab. Ele existe para rodar num CONTAINER DESCARTÁVEL.
# NUNCA rode na máquina de trabalho nem em produção.
#
#   cd <raiz do repo>
#   make agent-linux      # precisa de dist/revoada-agent
#   docker run --rm -v "$PWD:/repo:ro" debian:12 sh -c '
#     apt-get update -qq && apt-get install -y -qq curl cron python3 &&
#     (cd /repo/dist && python3 -m http.server 8000 --bind 127.0.0.1 &) && sleep 2 &&
#     cp /repo/deploy/agent/prova-instalacao.sh /tmp/p.sh && sh /tmp/p.sh'
#
# O install.sh roda como root em servidor de cliente. Toda mudança nele passa
# por aqui antes de ir para o dist.
set -eu

[ -f /.dockerenv ] || [ -f /run/.containerenv ] || {
  echo "recusando rodar fora de container: este script mexe em /usr/local/bin, /etc e no crontab" >&2
  exit 1
}

falhas=0
ok()   { echo "  OK   $*"; }
falha(){ echo "  FALHA $*"; falhas=$((falhas+1)); }

echo "=============================================================="
echo " 1. systemd MODERNO (252): unit precisa ganhar o ExecStartPre=-+"
echo "=============================================================="
# systemctl de mentira: o container não tem systemd de verdade rodando, então
# fingimos a versão e engolimos daemon-reload/enable/restart. É o suficiente para
# provar o que interessa aqui, que é o CONTEÚDO gerado da unit.
mkdir -p /fake
cat > /fake/systemctl <<'EOS'
#!/bin/sh
case "$1" in
  --version) echo "systemd 252 (252.19-1~deb12u1)"; echo "+PAM +AUDIT"; exit 0 ;;
esac
echo "systemctl(fake) $*" >> /tmp/systemctl.log
exit 0
EOS
chmod +x /fake/systemctl
export PATH="/fake:$PATH"

sh /repo/deploy/agent/install.sh --key CHAVE-TESTE --gateway http://gw:8090 \
  --agent-url http://127.0.0.1:8000/revoada-agent --panel https://painel.exemplo >/tmp/out1.log 2>&1 || {
  echo "install.sh saiu != 0:"; cat /tmp/out1.log; exit 1; }

grep -q 'ExecStartPre=-+/usr/local/lib/revoada/promote-update.sh' /etc/systemd/system/revoada-agent.service \
  && ok "unit tem ExecStartPre=-+ com os dois prefixos" \
  || falha "unit SEM a linha de promoção"
[ -x /usr/local/lib/revoada/promote-update.sh ] \
  && ok "promotor instalado e executável" || falha "promotor ausente"
grep -q 'update_apply: systemd' /etc/revoada/agent.yaml \
  && ok "agent.yaml em modo systemd" || falha "agent.yaml sem update_apply: systemd"
grep -q 'panel_url: https://painel.exemplo' /etc/revoada/agent.yaml \
  && ok "panel_url gravado" || falha "panel_url ausente"
grep -q 'auto_update: true' /etc/revoada/agent.yaml \
  && ok "auto_update ligado por padrão" || falha "auto_update não ficou ligado"
[ -d /var/lib/revoada-agent/update ] \
  && ok "diretório de estágio criado" || falha "diretório de estágio ausente"
[ "$(stat -c %U /var/lib/revoada-agent/update)" = "revoada" ] \
  && ok "estágio pertence ao revoada (é ele, não-root, quem escreve ali)" \
  || falha "estágio não pertence ao revoada: $(stat -c %U /var/lib/revoada-agent/update)"
# O endurecimento não pode ter sido perdido no caminho.
for d in NoNewPrivileges=true ProtectSystem=strict MemoryMax= CPUQuota= Nice= TasksMax=; do
  grep -q "$d" /etc/systemd/system/revoada-agent.service \
    && ok "endurecimento preservado: $d" || falha "PERDEU $d da unit"
done

echo
echo "=============================================================="
echo " 2. Derivação do panel_url a partir da --agent-url"
echo "=============================================================="
sh /repo/deploy/agent/install.sh --key K --gateway http://gw:8090 \
  --agent-url http://127.0.0.1:8000/revoada-agent >/tmp/out2.log 2>&1 || {
  echo "falhou:"; cat /tmp/out2.log; exit 1; }
grep -q 'panel_url: http://127.0.0.1:8000$' /etc/revoada/agent.yaml \
  && ok "panel_url derivado da --agent-url" \
  || falha "derivação errada: $(grep panel_url /etc/revoada/agent.yaml)"

echo
echo "=============================================================="
echo " 3. --no-auto-update: nada de promotor, nada de ExecStartPre"
echo "=============================================================="
sh /repo/deploy/agent/install.sh --key K --gateway http://gw:8090 --no-auto-update \
  --agent-url http://127.0.0.1:8000/revoada-agent --panel https://painel.exemplo >/tmp/out3.log 2>&1
grep -q 'auto_update: false' /etc/revoada/agent.yaml \
  && ok "auto_update desligado no yaml" || falha "auto_update seguiu ligado"
grep -q 'ExecStartPre' /etc/systemd/system/revoada-agent.service \
  && falha "unit ficou com ExecStartPre mesmo com auto-update desligado" \
  || ok "unit sem linha de promoção"
[ -f /usr/local/lib/revoada/promote-update.sh ] \
  && falha "promotor sobrou no disco" || ok "promotor removido"

echo
echo "=============================================================="
echo " 4. systemd ANTIGO (219, CentOS 7): a unit NÃO pode levar o '+'"
echo "=============================================================="
# Este é o caso que transformaria a atualização numa queda: no systemd < 231 o
# '+' é lido como parte do caminho, a unit fica inválida e o serviço não sobe.
cat > /fake/systemctl <<'EOS'
#!/bin/sh
case "$1" in --version) echo "systemd 219"; exit 0 ;; esac
exit 0
EOS
chmod +x /fake/systemctl
sh /repo/deploy/agent/install.sh --key K --gateway http://gw:8090 \
  --agent-url http://127.0.0.1:8000/revoada-agent --panel https://painel.exemplo >/tmp/out4.log 2>&1
grep -q 'ExecStartPre' /etc/systemd/system/revoada-agent.service \
  && falha "escreveu ExecStartPre num systemd 219 — a unit fica inválida e o host para de coletar" \
  || ok "systemd 219 não recebeu a linha de promoção"
grep -q 'update_apply: stage' /etc/revoada/agent.yaml \
  && ok "agente cai em modo 'só estagia'" || falha "modo errado no systemd antigo"
grep -q 'não suporta ExecStartPre' /tmp/out4.log \
  && ok "avisou o operador sobre a limitação" || falha "não avisou nada"

echo
echo "=============================================================="
echo " 5. Modo cron (sem systemd): não promete o que não cumpre"
echo "=============================================================="
rm -f /fake/systemctl
sh /repo/deploy/agent/install.sh --key K --gateway http://gw:8090 --cron \
  --agent-url http://127.0.0.1:8000/revoada-agent --panel https://painel.exemplo >/tmp/out5.log 2>&1 || {
  echo "falhou:"; cat /tmp/out5.log; exit 1; }
grep -q 'update_apply: stage' /etc/revoada/agent.yaml \
  && ok "cron fica em modo 'só estagia'" || falha "cron prometeu promoção"
crontab -u revoada -l 2>/dev/null | grep -q 'revoada-agent .* once' \
  && ok "cron de coleta instalado" || falha "cron de coleta ausente"

# 5b — HOST LIMPO: o usuário revoada ainda não tem crontab nenhum. Este é o caso
# normal de toda máquina nova, e era onde o `set -e` matava o instalador mudo,
# logo depois de adicionar os grupos: o host ficava com binário, chave e config,
# e SEM agendamento nenhum — coletando nada, sem ninguém saber.
crontab -u revoada -r 2>/dev/null || true
sh /repo/deploy/agent/install.sh --key K --gateway http://gw:8090 --cron \
  --agent-url http://127.0.0.1:8000/revoada-agent --panel https://painel.exemplo >/tmp/out5b.log 2>&1 \
  && ok "5b instalação por cron em host sem crontab saiu 0" \
  || falha "5b instalação por cron morreu em host limpo"
crontab -u revoada -l 2>/dev/null | grep -q 'revoada-agent .* once' \
  && ok "5b agendou a coleta no host limpo" || falha "5b host limpo ficou SEM agendamento"

echo
echo "=============================================================="
echo " 6. CICATRIZ: falha de LEITURA do crontab não pode APAGAR nada"
echo "=============================================================="
# O bug real: com '|| true' no pipeline, "não consegui ler" e "não tem crontab"
# viravam o mesmo arquivo vazio, e o ramo systemd disparava 'crontab -r'. Num
# script que roda como root em servidor de cliente, apagar o crontab de alguém
# cujo conteúdo nunca se chegou a ver é o pior desfecho possível.
crontab -u revoada -l > /tmp/cron-antes.txt 2>/dev/null || : > /tmp/cron-antes.txt
echo "17 3 * * * /usr/local/bin/backup-noturno.sh" >> /tmp/cron-antes.txt
crontab -u revoada /tmp/cron-antes.txt
cat > /fake/systemctl <<'EOS'
#!/bin/sh
case "$1" in --version) echo "systemd 252"; exit 0 ;; esac
exit 0
EOS
chmod +x /fake/systemctl
# crontab que FALHA AO LER (mas não diz "no crontab for"): o estado 2.
cat > /fake/crontab <<'EOS'
#!/bin/sh
for a in "$@"; do
  if [ "$a" = "-l" ]; then echo "crontab: erro de permissao no spool" >&2; exit 1; fi
done
echo "crontab(fake) ESCREVEU $*" >> /tmp/crontab-escritas.log
exit 0
EOS
chmod +x /fake/crontab
: > /tmp/crontab-escritas.log
export PATH="/fake:$PATH"
sh /repo/deploy/agent/install.sh --key K --gateway http://gw:8090 \
  --agent-url http://127.0.0.1:8000/revoada-agent --panel https://painel.exemplo >/tmp/out6.log 2>&1 || true
if grep -q 'ESCREVEU' /tmp/crontab-escritas.log; then
  falha "escreveu/apagou o crontab que não conseguiu ler: $(cat /tmp/crontab-escritas.log)"
else
  ok "não tocou no crontab ilegível"
fi
grep -q 'não consegui ler o cron' /tmp/out6.log \
  && ok "avisou o operador" || falha "não avisou sobre o crontab ilegível"
rm -f /fake/crontab
# E o crontab de verdade continua lá.
crontab -u revoada -l 2>/dev/null | grep -q 'backup-noturno' \
  && ok "o crontab do cliente sobreviveu" || falha "PERDEU o crontab do cliente"

echo
echo "=============================================================="
echo " 7. systemctl restart continua sendo chamado (regressão antiga)"
echo "=============================================================="
: > /tmp/systemctl.log
cat > /fake/systemctl <<'EOS'
#!/bin/sh
case "$1" in --version) echo "systemd 252"; exit 0 ;; esac
echo "$*" >> /tmp/systemctl.log
exit 0
EOS
chmod +x /fake/systemctl
sh /repo/deploy/agent/install.sh --key K --gateway http://gw:8090 \
  --agent-url http://127.0.0.1:8000/revoada-agent --panel https://painel.exemplo >/tmp/out7.log 2>&1 || true
grep -q '^restart revoada-agent' /tmp/systemctl.log \
  && ok "systemctl restart chamado (sem ele o binário novo fica no disco e o processo velho segue)" \
  || falha "PERDEU o systemctl restart"

echo
echo "=============================================================="
echo " 8. Idempotência: instalar duas vezes seguidas não quebra"
echo "=============================================================="
sh /repo/deploy/agent/install.sh --key K --gateway http://gw:8090 \
  --agent-url http://127.0.0.1:8000/revoada-agent --panel https://painel.exemplo >/tmp/out8.log 2>&1 \
  && ok "segunda instalação saiu 0" || falha "segunda instalação falhou"

echo

echo
echo "=============================================================="
echo " 9. PROMOTOR (root): o que ele instala e o que ele RECUSA"
echo "=============================================================="
# Reinstala em modo systemd para ter o promotor no disco.
cat > /fake/systemctl <<'EOS'
#!/bin/sh
case "$1" in --version) echo "systemd 252"; exit 0 ;; esac
exit 0
EOS
chmod +x /fake/systemctl
sh /repo/deploy/agent/install.sh --key K --gateway http://gw:8090 \
  --agent-url http://127.0.0.1:8000/revoada-agent --panel https://painel.exemplo >/tmp/out9.log 2>&1

PROM=/usr/local/lib/revoada/promote-update.sh
DIR=/var/lib/revoada-agent/update
BIN=/usr/local/bin/revoada-agent
# "Binário novo" = um ELF diferente do instalado. /bin/true serve: é ELF de
# verdade e distinguível por cmp.
NOVOBIN=/tmp/novo-elf
cp /bin/true "$NOVOBIN"

estagiar() { # $1=conteudo  $2=sha  $3=versao
  cp "$1" "$DIR/revoada-agent.novo"
  printf 'sha256=%s\nversao=%s\n' "$2" "$3" > "$DIR/revoada-agent.novo.meta"
}
limpar() { rm -f "$DIR"/revoada-agent.novo* "$DIR"/promovido "$DIR"/tentativas "$DIR"/saudavel "$BIN".bak-anterior; }

# 9a — caminho feliz
limpar
cp /repo/dist/revoada-agent "$BIN"
SHA_NOVO="$(sha256sum "$NOVOBIN" | cut -d' ' -f1)"
estagiar "$NOVOBIN" "$SHA_NOVO" "0.9.0"
"$PROM" 2>/tmp/prom9a.log || true
cmp -s "$BIN" "$NOVOBIN" && ok "9a promoveu o binário verificado" || falha "9a NÃO promoveu"
[ -f "$BIN.bak-anterior" ] && ok "9a guardou cópia de segurança" || falha "9a sem cópia de segurança"
[ ! -f "$DIR/revoada-agent.novo" ] && ok "9a limpou o estágio" || falha "9a deixou lixo no estágio"
[ "$(cat "$DIR/promovido")" = "0.9.0" ] && ok "9a registrou a versão promovida" || falha "9a sem registro"
[ "$(cat "$DIR/tentativas")" = "0" ] && ok "9a zerou o contador de tentativas" || falha "9a contador errado"

# 9b — checksum divergente NUNCA é instalado
limpar
cp /repo/dist/revoada-agent "$BIN"
estagiar "$NOVOBIN" "0000000000000000000000000000000000000000000000000000000000000000" "9.9.9"
"$PROM" 2>/tmp/prom9b.log || true
cmp -s "$BIN" /repo/dist/revoada-agent && ok "9b binário NÃO foi trocado (checksum divergente)" || falha "9b INSTALOU binário com checksum errado"
[ ! -f "$DIR/revoada-agent.novo" ] && ok "9b descartou o estágio" || falha "9b manteve o estágio ruim"
grep -q 'não confere' /tmp/prom9b.log && ok "9b registrou o motivo" || falha "9b não registrou nada"

# 9c — link simbólico é recusado (não copiar arquivo do sistema para /usr/local/bin)
limpar
cp /repo/dist/revoada-agent "$BIN"
ln -sf /etc/shadow "$DIR/revoada-agent.novo"
printf 'sha256=x\nversao=9.9.9\n' > "$DIR/revoada-agent.novo.meta"
"$PROM" 2>/tmp/prom9c.log || true
cmp -s "$BIN" /repo/dist/revoada-agent && ok "9c recusou o link simbólico" || falha "9c seguiu um link simbólico"
grep -q 'link simbólico' /tmp/prom9c.log && ok "9c registrou o motivo" || falha "9c silencioso"

# 9d — arquivo íntegro que não é ELF
limpar
cp /repo/dist/revoada-agent "$BIN"
printf '<!doctype html><html>o SPA respondeu no lugar do binario</html>' > /tmp/naoelf
estagiar /tmp/naoelf "$(sha256sum /tmp/naoelf | cut -d' ' -f1)" "9.9.9"
"$PROM" 2>/tmp/prom9d.log || true
cmp -s "$BIN" /repo/dist/revoada-agent && ok "9d recusou HTML com checksum correto" || falha "9d instalou HTML"
grep -q 'ELF' /tmp/prom9d.log && ok "9d registrou o motivo" || falha "9d silencioso"

# 9e — desfazimento: promovido e o agente nunca ficou são
limpar
cp /repo/dist/revoada-agent "$BIN.bak-anterior"
cp "$NOVOBIN" "$BIN"
echo "0.9.0" > "$DIR/promovido"
"$PROM" 2>/dev/null || true; "$PROM" 2>/dev/null || true
cmp -s "$BIN" "$NOVOBIN" && ok "9e não desfez cedo demais (2 starts)" || falha "9e desfez antes da hora"
"$PROM" 2>/tmp/prom9e.log || true
cmp -s "$BIN" /repo/dist/revoada-agent && ok "9e desfez a promoção no 3º start sem saúde" || falha "9e NÃO desfez"
[ ! -f "$DIR/promovido" ] && ok "9e limpou o registro de promoção" || falha "9e deixou o registro"

# 9f — com marca de saúde, não desfaz nada
limpar
cp /repo/dist/revoada-agent "$BIN.bak-anterior"
cp "$NOVOBIN" "$BIN"
echo "0.9.0" > "$DIR/promovido"
echo "0.9.0" > "$DIR/saudavel"
"$PROM" 2>/dev/null || true; "$PROM" 2>/dev/null || true; "$PROM" 2>/dev/null || true; "$PROM" 2>/dev/null || true
cmp -s "$BIN" "$NOVOBIN" && ok "9f binário são foi mantido" || falha "9f desfez uma promoção sadia"

# 9g — sem nada estagiado, o promotor é inerte (é o caso de todo start normal)
limpar
cp /repo/dist/revoada-agent "$BIN"
"$PROM" 2>/dev/null || true
cmp -s "$BIN" /repo/dist/revoada-agent && ok "9g start comum não mexe em nada" || falha "9g mexeu no binário sem motivo"

echo
echo "=============================================================="
echo " 10. CICATRIZ: o install.sh não pode ser REBAIXADO pelo estágio"
echo "=============================================================="
# O bug: um `revoada-agent.novo` + `.meta` deixado por uma tentativa anterior
# de auto-atualização sobrevivia à reinstalação, e o ExecStartPre do `systemctl
# restart` que fecha o install.sh o promovia POR CIMA do binário recém-instalado.
# Como o botão "Atualizar agente" do painel É este script por SSH, o efeito era
# rebaixar o host em silêncio — com o painel relatando atualização bem-sucedida.
limpar
cp /repo/dist/revoada-agent "$BIN"
# Resíduo de uma tentativa anterior: um binário DIFERENTE (o "0.8.0") já verificado
# e com o .meta que faz o promotor aceitá-lo.
estagiar "$NOVOBIN" "$(sha256sum "$NOVOBIN" | cut -d' ' -f1)" "0.8.0"
sh /repo/deploy/agent/install.sh --key K --gateway http://gw:8090 \
  --agent-url http://127.0.0.1:8000/revoada-agent --panel https://painel.exemplo >/tmp/out10.log 2>&1 \
  && ok "10 reinstalação com estágio sujo saiu 0" || falha "10 reinstalação falhou"
[ ! -f "$DIR/revoada-agent.novo" ] && [ ! -f "$DIR/revoada-agent.novo.meta" ] \
  && ok "10 install.sh limpou o binário estagiado" \
  || falha "10 estágio sobreviveu à reinstalação — o promotor vai rebaixar o host"
# A prova que importa: o ExecStartPre do restart (aqui simulado chamando o promotor)
# NÃO pode trocar o binário que o install.sh acabou de pôr no lugar.
"$PROM" 2>/tmp/prom10.log || true
cmp -s "$BIN" /repo/dist/revoada-agent \
  && ok "10 binário instalado sobreviveu ao promotor (sem rebaixamento)" \
  || falha "10 REBAIXOU: o promotor trocou o binário recém-instalado pelo do estágio"

# 10b — o outro caminho do mesmo rebaixamento: `promovido` sem `saudavel` faz o
# promotor restaurar o `.bak-anterior` no 3º start. Depois de uma reinstalação
# esses marcadores são de outra vida e precisam sair junto.
limpar
cp /repo/dist/revoada-agent "$BIN"
echo "0.8.0" > "$DIR/promovido"
printf '2\n' > "$DIR/tentativas"
cp "$NOVOBIN" "$BIN.bak-anterior"
sh /repo/deploy/agent/install.sh --key K --gateway http://gw:8090 \
  --agent-url http://127.0.0.1:8000/revoada-agent --panel https://painel.exemplo >/tmp/out10b.log 2>&1 || true
[ ! -f "$DIR/promovido" ] && [ ! -f "$DIR/tentativas" ] \
  && ok "10b marcadores de promoção antiga removidos" \
  || falha "10b sobrou promovido/tentativas — o promotor vai desfazer a instalação nova"
"$PROM" 2>/dev/null || true; "$PROM" 2>/dev/null || true; "$PROM" 2>/dev/null || true
cmp -s "$BIN" /repo/dist/revoada-agent \
  && ok "10b nenhum desfazimento após reinstalar" \
  || falha "10b o promotor restaurou um binário anterior por cima da instalação nova"

echo
echo "=============================================================="
echo " RESULTADO FINAL: $falhas falha(s)"
echo "=============================================================="
exit $falhas

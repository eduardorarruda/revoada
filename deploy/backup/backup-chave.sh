#!/usr/bin/env bash
# Backup da CHAVE MESTRA do cofre (item 15 da lista de segurança).
#
# Por que é separado do backup do banco: o Postgres guarda os segredos CIFRADOS
# (credenciais de bancos, 2FA, CA dos agentes); a chave que os abre mora no volume
# de dados do painel, FORA do banco. Backup do banco sem a chave = segredos perdidos;
# banco e chave no mesmo lugar = quem leva um leva tudo. Guarde este arquivo em OUTRO
# lugar (cofre de senhas, outro bucket, outra conta), com outra senha.
#
# Uso: REVOADA_BACKUP_SENHA='frase-longa' bash deploy/backup/backup-chave.sh /destino
# Restaurar: gpg --decrypt revoada-chave-AAAA-MM-DD.tar.gz.gpg | tar -xz -C /var/lib/revoada
set -euo pipefail

DESTINO="${1:?informe o diretório de destino}"
SENHA="${REVOADA_BACKUP_SENHA:?defina REVOADA_BACKUP_SENHA (frase longa, guardada fora do servidor)}"
VOLUME="${REVOADA_VOLUME_DADOS:-revoada_revoada_dados}"
DIA="$(date +%F)"
ARQ="$DESTINO/revoada-chave-$DIA.tar.gz.gpg"

mkdir -p "$DESTINO"
umask 077
# Lê o volume por um container descartável (a imagem do painel é distroless, sem tar).
docker run --rm -v "$VOLUME:/dados:ro" alpine:3.20 tar -czf - -C /dados chave-mestra.json \
  | gpg --batch --yes --pinentry-mode loopback --passphrase-fd 3 --symmetric --cipher-algo AES256 -o "$ARQ" 3<<<"$SENHA"

echo "chave mestra salva (cifrada com AES-256) em $ARQ"
echo "teste de restauração:"
gpg --batch --pinentry-mode loopback --passphrase-fd 3 --decrypt "$ARQ" 3<<<"$SENHA" | tar -tzf - >/dev/null && echo "  ok — o arquivo abre com a senha informada"

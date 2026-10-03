# Validação de ponta a ponta (`tests/validacao`)

Prova, contra uma stack **viva**, que o número que aparece na tela é a medida real
da máquina e que o checklist de sites bate no endpoint certo. Nada é simulado do
lado do Revoada: o script usa as **mesmas rotas que o front chama** e compara com
uma verdade independente.

| Rotina | O que a tela usa | Verdade de referência |
|---|---|---|
| RAM / swap | `GET /api/host-metrics` (cartões) e `POST /api/query` (gráficos) | `/proc/meminfo` no mesmo instante; coluna `used` do `free -b` |
| CPU | idem | deltas de `/proc/stat` no MESMO intervalo da amostra + carga conhecida (N laços ocupados em K núcleos) |
| Disco | idem | `statvfs` de cada montagem; conjunto de montagens de `/proc/self/mounts` (1 por dispositivo, sem dupla contagem de subvolume btrfs/bind); arquivo de 2 GiB com `fallocate` |
| Rede / processos / uptime / load | idem (`agg=rate` nos gráficos de rede) | `/proc/net/dev` (interfaces com `/sys/class/net/X/device`), PIDs em `/proc`, `/proc/uptime`, `/proc/loadavg` |
| Logs | `GET /api/logs/search` e `/api/logs/histogram` (tela Logs) | linhas escritas pelo próprio script num arquivo seguido pelo agente |
| Sites | `POST/GET /api/site-checks`, `/api/site-checks/{id}/history` (tela Websites) | registro de cada requisição num servidor HTTP/HTTPS de teste local |
| Jornadas | `POST/GET /api/journeys` | idem (ordem dos passos, método, formulário, cookies) |
| Alertas | canal webhook temporário (`/api/notify/channels`) | POSTs recebidos pelo servidor de teste |

Cada verificação sai com **esperado / observado / tolerância / veredito**
(✅ ok, ❌ falhou, ⚠️ inconclusivo — por exemplo, a máquina já estava a 100% de CPU
e a subida da carga conhecida não cabe). A tolerância é sempre explicada: nas
métricas de host ela inclui a variação da própria verdade na janela de incerteza
do instante da medida (o `ts` da API tem resolução de 1 s e é carimbado no fim do
ciclo do agente).

## Requisitos

- Python 3.9+ (**só biblioteca padrão**; nada de `pip install`).
- Rodar **no mesmo host** do agente validado (a verdade vem do kernel local).
- `openssl` no PATH para os casos TLS (sem ele, esses casos são pulados).
- Um usuário **admin** do painel (as rotas de criação de check/canal exigem admin).
- O diretório passado em `--dir-logs` tem de estar coberto por `log_paths` do agente
  (ex.: `log_paths: [/var/log/minhaapp/*.log]` → `--dir-logs /var/log/minhaapp`).

## Credenciais (nunca no repositório)

Em ordem de preferência:

1. variáveis de ambiente `RV_EMAIL`, `RV_SENHA` e, com 2FA ligado, `RV_TOTP`
   (o segredo base32 do TOTP);
2. `--senha-arquivo` / `--totp-arquivo`;
3. `--dev <dir>`: o layout do ambiente de desenvolvimento (`<dir>/senha-admin.txt`,
   `<dir>/totp-admin.txt`; os logs de teste vão para `<dir>/logs`).

## Como rodar

```sh
# stack de desenvolvimento (painel em 127.0.0.1:8091)
python3 tests/validacao/validar.py --dev /caminho/do/dev \
    --relatorio /tmp/revoada-validacao/relatorio

# outra stack, credenciais por ambiente
RV_EMAIL=admin@empresa RV_SENHA=... RV_TOTP=... \
python3 tests/validacao/validar.py --painel https://painel.exemplo \
    --dir-logs /var/log/minhaapp --relatorio /tmp/val/relatorio

# só um grupo
python3 tests/validacao/validar.py --dev DIR --somente logs
```

Opções úteis:

| Opção | Padrão | Para quê |
|---|---|---|
| `--somente host,logs,sites` | todos | rodar só alguns grupos (rodam em paralelo) |
| `--duracao-host N` | 330 | segundos amostrando; ≥ 300 cobre o cartão de rede (média de 5 min) |
| `--duracao-sites N` | 230 | segundos observando os checks (≥ 3 sondagens no tier crítico) |
| `--sem-carga` | — | não gerar carga de CPU |
| `--sem-disco` | — | não escrever o arquivo de 2 GiB |
| `--dir-disco DIR` | `~/.cache/revoada-validacao` | onde escrever o arquivo de 2 GiB (tem de ser um disco EXIBIDO pelo painel; tmpfs não serve) |
| `--host NOME` | hostname local | hostname do agente no inventário |
| `--sonda LOCAL` | — | se um agente com `probe: true` / `probe_location: LOCAL` estiver rodando, valida também a sonda remota |

A execução dura ~6 min. Saída: tabela no terminal + `<relatorio>.json` e
`<relatorio>.md`. Código de saída 1 se alguma verificação falhar.

## O que o script altera (e desfaz)

- cria checks `val-<hora>-*`, jornadas e um canal webhook `val-<hora>-webhook` e
  **apaga tudo** no fim (inclusive em erro);
- escreve arquivos `validacao-<hora>-*.log` em `--dir-logs` e os remove no fim;
- escreve e apaga um arquivo de 2 GiB em `--dir-disco`;
- gera carga de CPU por ~40 s (`núcleos/2` processos);
- sobe um servidor HTTP/HTTPS de teste só em `127.0.0.1` (portas efêmeras) durante a
  execução; os certificados de teste são gerados no diretório do relatório e
  apagados no fim.

Os checks enviam alertas para **todos** os canais habilitados do painel (é assim
que o produto funciona). Rode numa stack de desenvolvimento/homologação ou avise
quem recebe os alertas.

## Arquivos

- `validar.py` — CLI e orquestração;
- `cliente.py` — sessão admin (login + TOTP) e chamadas às rotas do front;
- `verdade.py` — leitura independente de `/proc`, `/sys` e `statvfs`;
- `grupo_host.py`, `grupo_logs.py`, `grupo_sites.py` — as verificações;
- `servidor_teste.py` — alvo HTTP/HTTPS que registra cada requisição;
- `relatorio.py` — tabela, JSON e Markdown.

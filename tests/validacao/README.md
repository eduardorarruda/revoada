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
| Agentes de IA | `GET /api/ia/resumo`, `/api/ia/execucoes`, `/api/ia/execucoes/{trace}` (e `/conteudo`), `GET /api/traces/{trace}`, `POST /api/query` (`llm.chamadas`), `POST/DELETE /api/ia/precos`, `POST /api/ia/purge`, `POST /mcp` | spans OTLP montados pelo próprio script com tokens, erros e ferramentas conhecidos; um preço criado na rodada e uma troca de preço (custo esperado = conta exata, feita em Python); um leitor e um operador descartáveis (o que cada papel pode e não pode); dois tokens MCP descartáveis (escopo `leitura` × `ia_conteudo`) |

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
- Grupo `ia`: o admin precisa de **2FA ativo** e do segredo TOTP (`RV_TOTP`/`--totp-arquivo`).
  Ler conteúdo de IA, criar/apagar usuários e criar/revogar tokens MCP são permissões
  **críticas** (`server/internal/auth/papeis.go`): o script chama `POST /api/auth/reautenticar`
  com um código novo antes delas (cada reautenticação vale 5 min e gasta um passo TOTP; quando
  o último código ainda é o da janela atual, o script espera a próxima, até 30 s).
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

# agentes de IA: precisa de uma chave de ingestão (X-Revoada-Key) e do gateway
RV_CHAVE=... python3 tests/validacao/validar.py --dev DIR --somente ia --gateway http://127.0.0.1:8090

# idem, numa stack onde não se pode criar usuário nem token MCP (pula permissões e MCP)
RV_CHAVE=... python3 tests/validacao/validar.py --dev DIR --somente ia --ia-sem-descartaveis
```

Opções úteis:

| Opção | Padrão | Para quê |
|---|---|---|
| `--somente host,logs,sites,ia` | todos | rodar só alguns grupos (rodam em paralelo) |
| `--duracao-host N` | 330 | segundos amostrando; ≥ 300 cobre o cartão de rede (média de 5 min) |
| `--duracao-sites N` | 230 | segundos observando os checks (≥ 3 sondagens no tier crítico) |
| `--sem-carga` | — | não gerar carga de CPU |
| `--sem-disco` | — | não escrever o arquivo de 2 GiB |
| `--dir-disco DIR` | `~/.cache/revoada-validacao` | onde escrever o arquivo de 2 GiB (tem de ser um disco EXIBIDO pelo painel; tmpfs não serve) |
| `--host NOME` | hostname local | hostname do agente no inventário |
| `--sonda LOCAL` | — | se um agente com `probe: true` / `probe_location: LOCAL` estiver rodando, valida também a sonda remota |
| `--gateway URL` | `RV_GATEWAY` ou `http://127.0.0.1:8090` | grupo `ia`: para onde vão os spans OTLP |
| `--chave-ingestao K` | `RV_CHAVE` | grupo `ia`: chave de ingestão de um servidor (`X-Revoada-Key`) |
| `--duracao-ia N` | 480 | segundos esperando a série `llm.*` (o runner fecha cada minuto com 5 min de atraso) |
| `--ia-sem-descartaveis` | — | grupo `ia`: não cria usuários nem tokens MCP (pula os casos de permissões e de MCP) |

A execução dura ~6 min. Saída: tabela no terminal + `<relatorio>.json` e
`<relatorio>.md`. Código de saída 1 se alguma verificação falhar.

### Grupo `ia`, caso a caso

| Caso | O que prova |
|---|---|
| resumo e custo | totais, tokens (cache incluso), custo exato pelo preço da rodada, `custo_parcial`, chamada sem preço e sem tokens contadas (nunca US$ 0) |
| replay / lista | 9 passos em ordem, loop de ferramenta (`clima ×3`), `custo_origem` por passo |
| conteúdo | prompt fora de `spans.labels`; `/conteudo` (com reautenticação) devolve 404 com a gravação desligada ou o texto **redigido** |
| amostragem | 20 traces curtos chegam todos |
| muitos rótulos | span OpenInference com 90+ chaves é aceito **e lido**: ≥ 1 passo, `chat`, modelo de `llm.model_name`, `tokens_entrada` = 10 e `tokens_saida` = 2 de `llm.token_count.*` |
| permissões | leitor e operador descartáveis com VER no servidor do trace. Leitor: 403 em `/conteudo`, `POST /api/ia/precos`, `DELETE /api/ia/precos/{id}`, `POST /api/ia/purge`; lê `/api/ia/resumo` com os mesmos números do admin. Operador: `/conteudo` dá 403 `mfa_necessario` sem 2FA, 403 `reautenticacao_necessaria` com 2FA e sem reautenticar, e 200 (gravação ligada) ou 404 (desligada — relatado qual) depois de ativar o 2FA pela API (`/api/auth/mfa/iniciar` + `confirmar`, como um usuário de verdade) e reautenticar; 403 em preços e purge |
| MCP | tokens `["leitura"]` e `["leitura","ia_conteudo"]`; MCP Streamable HTTP (`initialize` → `notifications/initialized` → `tools/list` → `tools/call`): as 4 ferramentas de IA listadas; `ia_resumo` (1 h) = `/api/ia/resumo` na mesma janela para o modelo da rodada; `ver_execucao` devolve os 9 passos; `ler_conteudo_execucao` negado com o token só `leitura` e liberado com `ia_conteudo`; token revogado passa a dar 401 |
| troca de preço | uma 2ª linha de preço do mesmo modelo, vigente desde a virada de minuto seguinte às chamadas antigas: o custo das antigas não muda, e o total do modelo = antigo × anteriores + novo × a chamada nova |
| série de alerta | `llm.chamadas` gravada pelo runner (5 min de atraso) |
| purge (último) | `alvo: trace` → o replay some (404, até 60 s) e `/api/traces/{trace}` continua com os spans; `alvo: conversa` → as 20 execuções da amostragem somem |

A vigência da troca de preço cai numa **virada de minuto** (e não em "agora − 1 min"
qualquer) porque o resumo calcula o custo por intervalo, com o preço vigente no início
de cada balde (`server/internal/ia/custo.go`, `linhaUso.Quando`); o script espera até
~1 min para isso.

O purge **nunca** roda `alvo: todo_conteudo`: ele é um `TRUNCATE TABLE genai_conteudo`
da stack inteira (`server/internal/ia/purge.go`) — apagaria o conteúdo gravado de todas
as aplicações e de outras pessoas numa stack compartilhada. Por trace e por conversa o
script só apaga o que ele mesmo enviou.

## O que o script altera (e desfaz)

- cria checks `val-<hora>-*`, jornadas e um canal webhook `val-<hora>-webhook` e
  **apaga tudo** no fim (inclusive em erro);
- grupo `ia`: cria linhas de preço para modelos `val-modelo-<hora>` e as apaga no fim;
  cria **dois usuários descartáveis** (`val-<hora>-leitor-*@validacao.invalid` e
  `val-<hora>-operador-*@validacao.invalid`, com VER só no servidor do trace) e **dois
  tokens MCP** (`val-<hora>-leitura`, `val-<hora>-ia-conteudo`, validade de 1 dia), e
  apaga os usuários / revoga os tokens no fim, mesmo em erro. As senhas, o segredo TOTP
  do operador e os valores dos tokens são gerados na hora, ficam só na memória do
  processo e **nunca** são impressos nem gravados. O cadastro exige celular: vai um número
  fictício (DDD 00), e o canal pessoal de WhatsApp que o painel cria some com o usuário.
  Se a limpeza falhar, o relatório diz o e-mail/id para apagar à mão. Use
  `--ia-sem-descartaveis` onde isso não é permitido;
- grupo `ia`: apaga (purge) as chamadas de IA que ele mesmo enviou (o trace principal e a
  conversa `amostra-<hora>`);
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
- `cliente.py` — sessão (login + TOTP, reautenticação, espera no limite de 10 tentativas/min
  por IP das rotas de autenticação) e chamadas às rotas do front; cliente MCP mínimo;
- `verdade.py` — leitura independente de `/proc`, `/sys` e `statvfs`;
- `grupo_host.py`, `grupo_logs.py`, `grupo_sites.py`, `grupo_ia.py` — as verificações;
- `servidor_teste.py` — alvo HTTP/HTTPS que registra cada requisição;
- `relatorio.py` — tabela, JSON e Markdown.

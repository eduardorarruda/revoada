# Revoada — Arquitetura

Como o Revoada é montado por dentro: componentes, fluxo de dados, protocolo painel ↔ agente,
migração de dados, deploy, segurança e as decisões que sustentam tudo isso.

---

## 1. Visão geral

O Revoada é uma plataforma **open source e self-hosted** de operações, com um **painel central** e
**vários agentes**, cada um no seu servidor. Reúne quatro módulos num produto só:

| # | Módulo | O que faz |
|---|---|---|
| 1 | **Monitoramento de servidor** | métricas, logs, traces, alertas, plantão (on-call), modo TV |
| 2 | **Monitoramento de sites** | uptime, TLS, jornadas, status page |
| 3 | **Migração de dados** | troca de banco e upgrade de versão, com dry-run, lotes, conferência e rollback (§9) |
| 4 | **Deploy de sistemas** | GitHub Action → o agente do servidor implanta, confere a saúde e reverte sozinho (§10) |

O nome descreve a arquitetura: um bando levantando voo junto — o painel coordena, os agentes
executam, e tudo é acompanhado ao vivo. Os quatro módulos ficam visíveis em toda a interface
(navegação global, paleta de comandos e atalhos contextuais, como "migrar um banco deste servidor").

## 2. Componentes

**Agente (`revoada-agent`).** Binário Go único, sem dependências externas, instalado como serviço
nativo em cada servidor. Coleta métricas do host (gopsutil), segue arquivos de log, roda sondas de
site e descobre serviços; envia tudo por OTLP ao gateway, com buffer em disco quando a rede cai.
Também abre o **canal mTLS** com o painel (§7) e executa as tarefas liberadas na configuração local:
captura de esquema, simulação/execução/reversão/verificação de migração, diagnóstico e upgrade de
Firebird e deploy. Atualiza a si mesmo conferindo o SHA-256 do artefato.

**Gateway (`gateway/`).** Serviço Go de ingestão. Recebe OTLP/HTTP (`/v1/metrics`, `/v1/logs`,
`/v1/traces`) e OTLP/gRPC, além das rotas próprias do agente (`/ingest/logs`, `/ingest/discovery`,
`/ingest/probe-result`). Autentica cada servidor pela chave do cabeçalho `X-Revoada-Key`, aplica
limite por IP/chave e teto global de requisições em voo, publica métricas no NATS JetStream e as
grava no ClickHouse pelo writer embutido; logs e spans vão ao ClickHouse em lotes assíncronos. Também
faz scrape Prometheus dos alvos cadastrados.

**Painel (`server/`, binário `revoada-painel`).** O cérebro: API HTTP, autenticação e papéis,
consultas ao ClickHouse, alertas e notificações, sondas de site, servidor MCP (§12), canal gRPC dos
agentes (§7), módulo de migração (§9) e de deploy (§10). Um executável serve API, MCP, canal e a
interface web embutida (`server/internal/webui`, via `go:embed`).

**Interface web (`web/`).** React 19 + TypeScript + Vite. Gráficos com uPlot (séries temporais) e
ECharts (demais), animações com `motion`, atualização ao vivo por WebSocket (painéis) e SSE
(tarefas). O modo Migração tem layout e rotas próprios (`web/src/modos/migracao/`).

**App desktop (`desktop/`).** Janela nativa (Wails v2) para Linux, Windows e macOS que abre o painel
da empresa. A primeira tela pede o endereço (só `https://`, ou `http://` em localhost). Não expõe
bindings de Go: nenhuma página carregada na janela chama código nativo; a única porta é
`POST /conectar`, protegida pelo nonce da execução.

**Core (`core/`).** Go puro compartilhado entre painel e agente, sem rede nem banco: schema neutro,
modelo e validação de mapeamento, transformações, sugestão heurística, plano de execução e de
verificação, regras de upgrade Firebird, cofre em envelope, selo de credenciais, TOTP, política de
senha, validadores de fronteira e aspas de identificadores por motor.

**Proto (`proto/`).** O contrato do canal painel ↔ agente (`revoada.agente.v1`, protobuf + gRPC) e
o código Go gerado, incluindo a assinatura/conferência Ed25519 das tarefas.

## 3. Fluxo de dados

```
 servidor monitorado
 ┌──────────────────────────────────────┐
 │ revoada-agent                        │
 │  coleta · logs · sondas · tarefas    │
 └───────┬──────────────────────┬───────┘
         │ OTLP (HTTP/gRPC)     │ gRPC bidirecional, mTLS (TLS 1.3)
         │ X-Revoada-Key        │ aberto pelo agente
         ▼                      ▼
 ┌────────────────┐     ┌──────────────────────────────┐      WebSocket / SSE
 │ gateway        │     │ revoada-painel               │ ───────────────────▶ navegador
 │ :8090 · :4317  │     │ API · MCP · canal · interface│                      app desktop
 └──┬─────┬────┬──┘     │ :8091 · :7443                │
    │     │    │        └───────┬──────────────┬───────┘
    │     │    │ métricas       │ consultas    │ metadados, tarefas,
    │     │    ▼                │              │ auditoria, cofre
    │     │  ┌──────┐  writer   │              │
    │     │  │ NATS │──────┐    │              │
    │     │  └──────┘      ▼    ▼              ▼
    │     │ logs/spans  ┌────────────┐   ┌────────────┐
    │     └────────────▶│ ClickHouse │   │ PostgreSQL │
    │      (em lote)    └────────────┘   └────────────┘
    │ inventário de hosts                       ▲
    └───────────────────────────────────────────┘
```

- **Telemetria:** o agente envia métricas, logs e traces por OTLP ao gateway (qualquer aplicação
  instrumentada com OTLP também pode). Métricas passam pelo stream `INGEST` do NATS JetStream
  (deduplicação por janela, descarte do mais antigo ao encher, com contador exposto) e chegam ao
  ClickHouse pelo writer. Logs e spans são acumulados e gravados em lote. O inventário de hosts vai
  ao PostgreSQL.
- **Leitura:** o painel consulta o ClickHouse (séries com downsampling, logs, traces) e o PostgreSQL
  (usuários, painéis, alertas, agentes, tarefas, migração, auditoria) e empurra deltas ao vivo para
  a interface por WebSocket; eventos de tarefa chegam à tela por SSE.
- **Controle:** tarefas, eventos, checkpoints e resultados trafegam só pelo canal mTLS (§7) e ficam
  gravados no PostgreSQL (`tarefas`, `tarefa_eventos`, `execucao_checkpoints`).
- **Dados de migração não passam pelo painel:** o agente lê da origem e grava no destino. O painel
  recebe só esquema (estrutura, nunca linhas), relatórios, contagens e somas de conferência.

**Portas padrão**

| Serviço | Porta | Variável |
|---|---|---|
| Painel (API, MCP, interface) | 8091 (publicada como 8080 no compose da raiz) | `REVOADA_SERVER_ADDR` |
| Canal dos agentes (gRPC mTLS) | 7443 | `REVOADA_CANAL_ADDR` |
| Gateway (OTLP/HTTP e ingestão) | 8090 | `REVOADA_HTTP_ADDR` |
| Gateway (OTLP/gRPC) | 4317 | `REVOADA_OTLP_GRPC_ADDR` |
| PostgreSQL · ClickHouse (HTTP) · NATS | 5432 · 8123 · 4222 (monitoramento 8222) | `REVOADA_PG_DSN` · `REVOADA_CH_ADDR` · `REVOADA_NATS_URL` |

## 4. Stack e multiplataforma

- **Back-end:** Go 1.25, organizado em módulos num `go.work` (`core`, `proto`, `agent`, `gateway`,
  `server`). **Front-end:** React 19 + TypeScript + Vite. **Agente:** binário Go.
- **Armazenamento:** PostgreSQL 16 (metadados, tarefas, auditoria, cofre), ClickHouse (métricas,
  logs, traces), NATS JetStream (fila de ingestão).
- Painel e agente compilam para **Linux, Windows e macOS**, em **amd64 e arm64**, com
  `CGO_ENABLED=0`.
- **Painel = binário único** (`revoada-painel`) com a interface embutida; distribuído também como
  imagem distroless sem root. `REVOADA_WEB_EMBUTIDA=false` desliga a interface embutida quando um
  proxy serve o `web/dist`.
- **App desktop** (Wails v2) nos três sistemas. Fica fora do `go.work` porque exige CGO e, no Linux,
  GTK/WebKit.
- **Agente = binário único**, instalável como serviço nativo (`revoada-agent servico
  instalar|remover|iniciar|parar|status`): systemd (Linux), Serviço do Windows com recuperação em
  falha, launchd (macOS). O painel também roda como serviço (`revoada-painel servico … --env
  <arquivo>`), com a configuração lida de arquivo `0600`, nunca da unit. No Linux, os instaladores em
  shell aplicam a cerca de recursos mais apertada do systemd.
- Um painel gerencia vários agentes, cada um com **identidade única** (certificado próprio).
- **Nada de caminho fixo ou separador hardcoded:** `path/filepath` e os diretórios padrão de cada SO
  (`os.UserConfigDir`, `ProgramData`, `/etc`, `~/Library/Application Support`).
- Métricas por biblioteca multiplataforma (**gopsutil**), tratando as diferenças por SO.
- Deploy adaptado a cada SO (`sh` no Linux/macOS, PowerShell/`cmd` no Windows).
- Migração funciona com bancos nos três sistemas.
- **CI com matriz** Linux/Windows/macOS e compilação cruzada amd64/arm64 (§20).

## 5. Decisões de arquitetura

| # | Decisão | Consequência |
|---|---|---|
| D1 | Painel = binário único + app desktop | um executável serve API, MCP, canal e interface; PostgreSQL + ClickHouse por trás. SQLite embutido (Go puro, sem CGO) é a direção para um modo "só migração" num único arquivo — o monitoramento continua exigindo PostgreSQL + ClickHouse pelo volume de série temporal |
| D2 | Sugestão de mapeamento = **heurística local + MCP** | sem IA a heurística já sugere; com IA, ela lê só a estrutura e grava rascunho |
| D3 | Credenciais **no painel, seladas por tarefa** | cofre em envelope; a senha vai cifrada só para o agente que executa e nunca vai para o disco dele |
| D4 | Primeiras migrações: **Firebird 2.x → 5** e **Firebird → PostgreSQL** | outros pares (ex.: PostgreSQL → MongoDB) depois |
| D5 | Canal painel ↔ agente = **gRPC sobre mTLS, aberto pelo agente** | o agente nunca abre porta; identidade dos dois lados por certificado |

Decisões de base, registradas em `docs/adr/`: ClickHouse como store único de telemetria (001),
back-end Go em monolito modular (002), NATS JetStream como fila de ingestão (003), React + uPlot
(004), Docker Compose antes de Kubernetes (005).

**Peças reaproveitadas entre módulos:** Argon2id (`server/internal/auth/crypto.go`), sessões com
refresh em cookie httpOnly, escopo por servidor (`authz`), `audit_log` encadeado, tokens de
inscrição de uso único, heartbeat de alerta, buffer em disco do agente (`agent/internal/buffer`),
auto-instalação e auto-atualização do agente, WebSocket e SSE ao vivo, sondas de site (`sitecheck`)
e notificações (e-mail, webhook, Telegram, WhatsApp).

## 6. Estrutura de pastas

```
revoada/
├── go.work                             # core, proto, agent, gateway, server
├── Dockerfile · docker-compose.yml     # imagem do painel e início rápido (stack completo)
├── proto/revoada/agente/v1/            # contrato painel ↔ agente (protobuf + gRPC) + código gerado + assinatura
├── core/                               # Go puro compartilhado (sem rede nem banco)
│   ├── migracao/
│   │   ├── esquema/                    #   schema neutro (Tabela, Coluna, TipoLogico, Chave, Índice) + tipos
│   │   ├── modelo/                     #   Mapeamento, MapTabela, MapColuna, Transformação + validação
│   │   ├── sugestao/                   #   heurística (nome, tipo, PK/FK, sinônimos pt/en)
│   │   ├── transformar/                #   transformações de lista fechada + ajuste ao tipo do destino
│   │   ├── plano/                      #   especificação da tarefa, ordem por FK, relatório, verificação
│   │   └── upgrade/                    #   regras puras do diagnóstico Firebird → 5
│   ├── seguranca/{cofre,selo,senha,totp}/
│   ├── validacao/                      #   validadores de fronteira
│   └── ident/                          #   aspas/validação de identificadores por motor (anti-injeção)
├── agent/                              # revoada-agent
│   ├── cmd/revoada-agent/              #   comandos + serviço nativo
│   └── internal/
│       ├── collect buffer otlpsend logtail probe discover selfinstall selfupdate selfuninstall config
│       ├── canal/                      #   identidade, cliente gRPC, executor de tarefas, pendentes
│       ├── migracao/{captura,copia,upgrade}/
│       └── deploy/                     #   executores compose/script por SO
├── gateway/                            # ingestão
│   ├── cmd/{gateway,migrate}/          #   serviço e migrations do ClickHouse
│   └── internal/                       #   otlp, queue (NATS), writer, chbatch, scrape, ingest, pg…
├── server/                             # o painel (revoada-painel)
│   ├── cmd/server/                     #   binário do painel, subcomandos mcp, migracoes e servico
│   └── internal/
│       ├── auth authz audit blindagem entrada segredos store (+ store/migracoes)
│       ├── canal/                      #   CA interna, inscrição, gRPC, presença, tarefas
│       ├── migracao/ mcpsrv/ deploys/
│       ├── alerting notify sitecheck journey statuspage tv live sse query chquery logs traces dashboards…
│       └── webui/                      #   interface embutida (go:embed)
├── web/src/
│   ├── modos/migracao/                 # o modo separado de migração
│   ├── motion/ styles/ components/ pages/ panels/
├── desktop/                            # app nativo (Wails v2, fora do go.work)
├── acoes/revoada-deploy-action/        # GitHub Action de deploy
├── deploy/                             # compose de dev/prod, instaladores do agente, backup, migrations do ClickHouse
└── tests/                              # integração e validação de ponta a ponta contra stack viva
```

O gateway continua um processo separado do painel (ADR 002: monolito modular em dois binários).

## 7. Protocolo painel ↔ agente

```
 Agente (qualquer servidor)                              Painel
 1. Inscrever(token, CSR P-256, chave X25519) ── TLS ──▶  valida o token de uso único
                                              ◀────────   certificado (30 dias) + CA + chave Ed25519
 2. Conectar() ═════════ gRPC bidirecional sobre mTLS (TLS 1.3) ═════════▶
      Ola {id, versão, SO, arch, capacidades, em andamento}   registra presença, reconcilia
      Batimento a cada 10 s                ──────▶   sem sinal > 35 s = offline → alerta
                                           ◀──────   Tarefa {assinada, credenciais seladas}
      EventoTarefa {seq, etapa, progresso} ──────▶   grava + repassa à tela (SSE)
                                           ◀──────   Controle {pausar | retomar | cancelar}
      Checkpoint / ResultadoTarefa         ──────▶
                                           ◀──────   Confirmacao {seq}
```

- **CA interna:** ECDSA P-256, criada na primeira subida do painel (validade de 10 anos). Emite o
  certificado do canal do painel e os certificados dos agentes.
- **Inscrição por token de uso único:** o administrador cria o token (`POST /api/agentes/tokens`,
  validade padrão de 24 h, máximo de 30 dias). O token carrega a **impressão digital da CA**; o
  agente confere a CA por ela antes de confiar. Comando: `revoada-agent inscrever -painel
  <host:7443> -token <rvd1…>`. O agente gera localmente a chave do certificado (P-256) e o par X25519
  do selo — as privadas nunca saem do servidor.
- **Quem abre a conexão é sempre o agente** (só saída): funciona atrás de NAT/firewall e o servidor
  do agente não expõe porta.
- **Mensagens** (`revoada.agente.v1`): o agente envia `Ola`, `Batimento`, `EventoTarefa`,
  `Checkpoint`, `ResultadoTarefa`, `RespostaEsquema`, `Confirmacao`, `PedidoRenovacao`; o painel
  envia `Tarefa`, `Controle`, `PedidoEsquema`, `Confirmacao`, `RespostaRenovacao`.
- **Tarefas assinadas:** `Tarefa` = id, tipo, especificação (JSON), credenciais seladas para a chave
  X25519 **daquele** agente, validade (padrão 24 h), limites, `correlacao_id` e **assinatura Ed25519
  do painel** (calculada sobre a tarefa com o campo de assinatura vazio). `PedidoEsquema` também é
  assinado.
- **Defesa em profundidade:** o agente só executa se (1) a assinatura confere, (2) o tipo está na
  **lista `canal.tarefas_permitidas` do `agent.yaml`** (vazia por padrão) e (3) a tarefa não venceu.
  Caso contrário responde `RECUSADA`. Tipos existentes: `diagnostico.eco`, `migracao.esquema`,
  `migracao.simular`, `migracao.executar`, `migracao.reverter`, `migracao.verificar`,
  `firebird.diagnostico`, `firebird.upgrade`, `firebird.descartar`, `deploy.aplicar`.
- **Entrega pelo menos uma vez + idempotência:** toda tarefa tem `id`, todo evento tem `seq`
  crescente por tarefa; repetidos são ignorados. O agente guarda o que ainda não foi confirmado e
  reenvia ao voltar.
- **Reconexão** com backoff exponencial e jitter (1 s → 60 s) e keepalive gRPC de 30 s. Ao voltar, o
  `Ola` informa as tarefas em andamento (com o último `seq`) e o painel reconcilia — nada se perde
  nem roda duas vezes.
- **Presença/batimento:** batimento a cada 10 s (com CPU, memória e tarefas ativas). Online até
  15 s sem sinal · instável até 35 s (1–2 batimentos perdidos) · offline depois disso → alerta pelas
  notificações existentes.
- **Certificados:** 30 dias; o agente pede renovação (`PedidoRenovacao` com CSR novo) quando falta
  1/3 da validade. Revogação imediata (`POST /api/agentes/{id}/revogar`): o agente revogado não
  conecta mais.
- **Limites:** a tarefa carrega `workers`, teto de CPU e janela de horário; o motor usa `workers`
  para o paralelismo (ex.: `-par` do restore Firebird).
- **Telemetria** segue por OTLP ao gateway, autenticada pela chave do servidor (`X-Revoada-Key`),
  separada da identidade do canal.

## 8. Modos de uso

| | **Local** | **Frota** |
|---|---|---|
| O que roda | painel + agente na mesma máquina | painel num servidor + N agentes |
| Interface | navegador ou app desktop em `localhost` | navegador ou app desktop apontando para o painel (ex.: `https://painel.exemplo.com.br`) |
| Caso típico | técnico migrando **um** banco | equipe migrando, monitorando e implantando em **muitos** servidores |

O início rápido (`./scripts/iniciar.sh` + `docker compose up`) sobe o stack completo numa máquina.

## 9. Migração de dados

O modo Migração tem **layout, navegação e rotas próprios** (`web/src/modos/migracao/`), acessível de
qualquer tela. A trilha é sempre **Mapear → Simular → Aprovar → Executar → Conferir**.

### 9.1 Tipos

- **Troca de banco** (`troca_de_banco`): Firebird → PostgreSQL e PostgreSQL → PostgreSQL hoje;
  outros pares depois.
- **Upgrade de versão do mesmo banco** (`upgrade_versao`): Firebird 2.x → 5 (§9.5).

### 9.2 Mapeamento personalizado

- Mapear **tabela de origem → tabela de destino** e **coluna a coluna** dentro de cada par.
- **Sugestão automática** pela heurística local (`core/migracao/sugestao`: nomes normalizados, tipos,
  PK/FK, sinônimos pt/en) e pelo **MCP** (§12); o usuário revisa e ajusta.
- **Editor visual** com diagrama e histórico de versões para criar, editar e validar os vínculos.
- Tabelas e colunas são validadas contra a **foto do esquema** capturada pelo agente; filtros são
  estruturados (coluna, operador da lista fechada, valor), nunca SQL livre.

### 9.3 Modelo de dados

```
Conexão de banco ──┐                     ┌── Esquema capturado (foto da estrutura, com hash)
                   ▼                     ▼
             Projeto de migração ──▶ Mapeamento (versionado) ──▶ tabelas ──▶ colunas (+ transformação)
                   │
                   ▼
           Execução (simular | executar | reverter | verificar) ──▶ Tarefa ──▶ Eventos · Checkpoints
```

| Tabela (painel) | Campos principais |
|---|---|
| `conexoes_banco` | id, nome, motor (`firebird`·`postgres`), endereço, banco, usuário, **credencial** (envelope: texto cifrado + DEK cifrada + versão da KEK), opções, agente_preferido_id, versao_detectada |
| `esquemas_capturados` | id, conexao_id, agente_id, **hash**, conteúdo (schema neutro, sem dados), capturado_em |
| `projetos_migracao` | id, nome, tipo (`troca_de_banco`·`upgrade_versao`), origem_id, destino_id, estado (`rascunho`·`aprovado`·`arquivado`) |
| `mapeamentos` | projeto_id + **versão**, esquema_origem_id, esquema_destino_id, origem (`heuristica`·`mcp`·`usuario`), conteúdo (tabelas → colunas), **hash**, problemas, estado (`rascunho`·`valido`·`aprovado`), aprovado_por/em |
| `execucoes_migracao` | tarefa_id, projeto_id, versão, tipo, execução, **hash_mapeamento**, relacionada_id (retomar/reverter) |
| `execucao_checkpoints` | tarefa_id, tabela, ultima_chave, linhas, seq |
| `tarefas` · `tarefa_eventos` | estado, agente, quem disparou, origem (ui/mcp/api), `correlacao_id`, resumo; eventos por `seq` |

No **destino**, o agente mantém `revoada_controle` (checkpoints e somas de conferência, gravados na
transação de cada lote) e `revoada_manifesto` (o que foi criado/trocado, para reverter).

Cada tabela mapeada tem ação (`copiar`·`ignorar`·`criar_no_destino`), ordem (topológica por FK),
coluna de lote, filtros e colunas. Transformações (lista fechada): `nenhuma`, `converter_tipo`,
`charset`, `aparar`, `valor_padrao`, `constante`, `mapa_valores`, `concatenar`, `dividir`,
`data_formato`.

```json
{
  "tabela_origem": "CLIENTES", "tabela_destino": "clientes", "acao": "copiar", "ordem": 3, "coluna_lote": "CODIGO",
  "colunas": [
    { "coluna_origem": "CODIGO", "coluna_destino": "id", "transformacao": "nenhuma", "confianca": 0.98, "origem": "heuristica" },
    { "coluna_origem": "NOME", "coluna_destino": "nome", "transformacao": "charset", "parametros": { "de": "WIN1252", "reparar": "sim" } },
    { "coluna_origem": "DT_CAD", "coluna_destino": "criado_em", "transformacao": "converter_tipo" },
    { "coluna_destino": "origem", "transformacao": "constante", "parametros": { "valor": "firebird" } }
  ]
}
```

### 9.4 Execução, dry-run, lotes e rollback

| Peça | Onde | O que faz |
|---|---|---|
| Transformações + ajuste ao destino | `core/migracao/transformar` | aplica a lista fechada linha a linha; `Ajustar` converte para o tipo lógico do destino e diz o que **não cabe** (nulo, tamanho, número, tipo, texto inválido) ou o que **perde** (casas, hora, acentos suspeitos). Mensagens nunca trazem o valor da linha |
| Especificação, relatório, manifesto | `core/migracao/plano` | o JSON da tarefa (montado só com o que está no painel), o relatório do dry-run, a ordem por FK e o manifesto de rollback |
| Motor | `agent/internal/migracao/copia` | `migracao.simular`, `migracao.executar`, `migracao.reverter`, `migracao.verificar` |
| API | `server/internal/migracao` | `POST /api/migracao/projetos/{id}/simular`, `…/executar`, `…/reverter`, `…/verificar`, `GET …/execucoes`, `GET /api/migracao/tarefas/{id}/checkpoints` |
| Tela | `web/src/modos/migracao/Execucao.tsx` | trilha, relatório, histórico, retomar/reverter, tela ao vivo |

- **Play:** o painel envia a tarefa ao agente escolhido — no servidor do painel ou no servidor do
  banco. A tarefa usa a CPU do servidor desse agente.
- **Dry-run (obrigatório antes da execução real):** só `SELECT` na origem e no destino. Lê a origem,
  aplica as transformações (o mesmo código da execução) e valida contra tipos e restrições do destino
  **sem gravar nada**: chave primária repetida dentro da migração e já existente no destino, FK de uma
  coluna contra o que a migração vai gravar no pai **e** contra o destino, conversões com perda,
  violações previstas de NOT NULL/UNIQUE. Relatório por tabela com linhas, ordem de carga e problemas;
  a linha é apontada pela chave, nunca pelo conteúdo.
- **Regras de liberação (painel):** executar exige versão **aprovada** + simulação **dessa versão e
  desse hash** sem bloqueantes + nenhuma tarefa rodando + nenhuma execução incompleta esperando
  decisão (retomar ou reverter) + nenhuma concluída não revertida.
- **Estratégias**, todas registradas no manifesto **antes** de gravar a primeira linha, na mesma
  transação do DDL:

| Situação | Carga | Reverter |
|---|---|---|
| `criar_no_destino` | `CREATE TABLE` (PK traduzida) e grava direto; FKs religadas no fim | `DROP TABLE` |
| tabela existente vazia | staging → troca | antes da troca: apaga a staging; depois: `DELETE` |
| tabela existente com dados | **cópia prévia** + staging → troca | antes: apaga a staging; depois: restaura a cópia |
| upgrade Firebird (§9.5) | banco novo; o original nunca é tocado | descartar o banco novo |

  Toda tabela que já existia passa por staging e **todas trocam juntas, numa transação, na ordem das
  FKs do destino** — as tabelas reais só mudam no fim, e uma filha nunca aponta para pai que ainda
  está na staging.
- **Lotes e retomada:** paginação por chave (`WHERE chave > última ORDER BY chave`); cada lote é uma
  transação que grava as linhas **e** o checkpoint em `revoada_controle`. Se o agente cair entre o
  commit e o aviso ao painel, a retomada lê do destino: nem duplica, nem pula. Tamanho adaptativo
  (começa em 5 000; fica entre 500 e 50 000 conforme o tempo do lote). Tabela sem coluna de lote
  recomeça do zero ao retomar (seguro: o alvo só tem linhas desta execução).
- **Reverter:** uma transação só no destino (o PostgreSQL desfaz DDL junto): filhas → pais apagando,
  pais → filhas restaurando. O manifesto fica no resumo da execução **e** em `revoada_manifesto`.
- **Charset de bancos antigos:** bancos Firebird antigos costumam ter UTF-8 gravado em coluna WIN1252
  ("JosÃ©"). O dry-run avisa (`suspeita_charset`) e a transformação `charset` com `reparar=sim`
  conserta.
- **Credenciais:** seladas por tarefa para o agente que roda (contexto `tarefa:<id>:origem|destino`,
  15 min); reverter recebe só a do destino (menor privilégio). O agente zera a senha depois de abrir
  a conexão.
- **Princípios:** o original nunca é alterado · dry-run antes de tudo · conferência depois de tudo ·
  tudo reprodutível (especificação, relatório e manifesto anexados à execução).

**Conferência em três camadas.** Nenhuma troca acontece sem que as camadas obrigatórias batam; um
erro que passa por uma é pego pela outra.

1. **Contagem + checksum da chave (obrigatória, barra a troca).** Contagem de linhas e checksum
   independente de ordem (soma dos hashes da chave), calculados ao gravar e relidos do destino. Se
   não bater, nada é trocado.
2. **Somas de conteúdo por coluna em forma canônica (obrigatória, barra a troca).** Cada coluna
   gravada tem uma soma de 256 bits independente de ordem — Σ sha256(chave canônica ‖ 0x00 ‖ valor
   canônico) — calculada sobre o valor que o motor manda gravar e de novo sobre o que o PostgreSQL
   devolve ao reler o alvo (staging ou tabela), uma vez, em sequência. A forma canônica
   (`copia/canonico.go`) modela só o que o PostgreSQL faz de legítimo ao gravar: inteiro em base 10;
   numeric exato (`big.Rat`, 12.5 = 12.50) arredondado como `numeric(p,s)`; `real` como float32;
   timestamp sem fuso pelo relógio e `timestamptz` pelo instante, em µs, com o arredondamento de
   `timestamp(p)`/`time(p)`; `char(n)` sem o preenchimento; texto byte a byte; bytea em hex;
   json/jsonb com chaves ordenadas e números exatos; uuid em minúsculas; nulo com marcador próprio.
   Truncar texto, trocar acento, deslocar fuso ou perder casas além do que a coluna prevê muda a soma.
   Coluna que recebeu DEFAULT e tipo sem forma canônica segura (interval, arrays, money, timetz…)
   ficam **de fora com o motivo** — nunca entram no OK. As somas vão em `revoada_controle` na mesma
   transação do lote, então pausar/retomar continua conferindo. Se alguma coluna não bater, a
   execução falha antes da troca e diz **quais colunas e quais chaves** divergem (até 20; nunca os
   valores).
3. **Dupla conferência pelo texto dos bancos (em `migracao.verificar`).** As camadas 1 e 2 comparam o
   valor **depois** de lido pelo driver — um erro de **leitura** (ex.: o driver montando uma DATA no
   fuso local e perdendo um dia no início do horário de verão) passaria pelas duas. Por isso a
   verificação faz uma conferência independente (`copia/texto.go`): cada banco **renderiza o valor
   como texto no próprio servidor** (Firebird `CAST(col AS VARCHAR(64))`; PostgreSQL
   `::text`/`to_char`, timestamptz como relógio no fuso da origem) e o agente compara os textos, sem
   a conversão tipada do driver. Normalização mínima: fração de segundo até a precisão comum, numeric
   com o arredondamento de `numeric(p,s)`, double com tolerância relativa de 1e-15, `char(n)` sem
   preenchimento, nulo só bate com nulo; BLOB de texto confere tamanho + primeiros 8 000 caracteres
   ("conferido em parte" além disso), binário só o tamanho. Entram só colunas sem transformação e de
   tipos comparáveis; as demais vêm listadas com o motivo. As expressões vão na mesma leitura de cada
   lote (sem dobrar idas e voltas). A tela mostra "Conferido pelos próprios bancos: N colunas" ou a
   divergência.

**Verificação sob demanda** (`migracao.verificar`, `POST …/verificar`): só leitura nos dois lados.
Relê a origem em ordem de chave, converte cada linha exatamente como a execução, busca as mesmas
chaves no destino (`IN` parametrizado, em pedaços) e compara coluna a coluna pela forma canônica,
além da conferência pelo texto. Relatório por tabela: linhas conferidas, idênticas, divergentes,
ausentes, divergências por coluna, colunas conferidas e não conferidas (com motivo) e amostra de até
20 chaves. Tabela sem chave compara as somas por coluna. Chaves são valores da origem: o painel as
esconde de quem não pode executar migração e o MCP nunca as devolve.

### 9.5 Diagnóstico e upgrade Firebird → 5

Tipos `firebird.diagnostico`, `firebird.upgrade` e `firebird.descartar`
(`agent/internal/migracao/upgrade`, regras puras em `core/migracao/upgrade`, API em
`server/internal/migracao/upgrade.go`, tela `web/src/modos/migracao/Upgrade.tsx`).

- **Projeto `upgrade_versao`:** origem = banco antigo; destino = conexão com o **servidor Firebird
  5**, cujo `banco` é o caminho do arquivo **novo** e cuja opção `diretorio_backup` é a pasta que os
  dois servidores enxergam (o `.fbk` passa por ela). FB5 com usuário SRP pede `wire_crypt=true`.
- **Diagnóstico — "o que vai quebrar?" (só leitura):** versão, ODS, dialeto, charset e tamanho;
  nomes que viraram palavras reservadas (FB3/FB4/FB5); UDFs (desligadas no FB4+) com substituto
  nativo sugerido; `CURRENT_TIMESTAMP`/`'NOW'` em procedures, triggers e views; **nulos em colunas NOT
  NULL**; **órfãos de FK**; **chaves duplicadas**; texto sem charset que não é UTF-8 (sugere
  `FIX_FSS`) e texto duplamente codificado; usuários a recriar com SRP; validação física opcional
  (`gfix -v -full -n`; no 2.x exige banco sem conexões — vira aviso); **ensaio**: backup só de
  metadados + restore num FB5. Saída: nota 0–100, risco, bloqueios, parada estimada e `fix.sql`
  sugerido (o que muda dado sai comentado).
- **Upgrade:** backup pelo serviço da versão antiga (`-g`, sem coleta de lixo: o original não é
  tocado) → restore pelo serviço do FB5 com **create** (nunca replace), `-par N` e `FIX_FSS`
  opcional → validação: contagem de cada tabela, valor de cada gerador, quantidade de objetos. Se não
  conferir, a tarefa falha e o banco novo não deve ser usado.
- **Liberação:** diagnóstico com sucesso há menos de 24 h, sem bloqueio e com ensaio ok; nenhuma
  tarefa rodando; nenhum upgrade concluído não descartado.
- **Reverter = descartar:** o driver não apaga arquivo de banco; o descarte põe o banco novo em
  **shutdown completo** (ninguém conecta por engano) e diz qual arquivo apagar.

## 10. Deploy de sistemas

```
GitHub Actions ──POST /api/deploys/acao──▶ painel ──Tarefa deploy.aplicar (assinada)──▶ agente do servidor
   (Action)    ◀──GET /api/deploys/acao/{id}──      ◀──eventos, resultado──────────────    compose | script
```

- **Action** (`acoes/revoada-deploy-action`): composite em bash + curl + jq (roda em runners Linux,
  macOS e Windows). Entradas: painel, token, agente, aplicação, versão, ambiente, aguardar, timeout.
  Espera o fim, falha o job se o deploy falhar e escreve o resumo (saída e rollback) no Job Summary.
  **Não há runner self-hosted:** quem executa é o agente do servidor.
- **Painel** (`server/internal/deploys`): `POST /api/deploys/acao` e `GET /api/deploys/acao/{id}`
  autenticados pelo token de deploy (`REVOADA_DEPLOY_TOKEN`, conta de serviço) para a Action;
  `POST /api/deploys` (permissão `rodar_deploy` + 2FA + reautenticação) e `GET /api/deploys` para a
  tela (Operar → Deploys: histórico, deploy manual, tela ao vivo).
- **Agente** (`agent/internal/deploy`, tarefa `deploy.aplicar`): o painel manda **só** aplicação e
  versão. A receita vem do `agent.yaml` do servidor (`canal.deploy`): `compose` (`docker compose
  pull` + `up -d` com `REVOADA_VERSAO`) ou `script` (`.sh` no Linux/macOS, `.ps1`/`.cmd` no Windows;
  caminho preso à pasta da aplicação). Um deploy por vez por servidor.
- **Health check** com espera configurável (padrão 60 s); se falhar, **rollback automático** para a
  versão anterior (o rollback recebe `REVOADA_VERSAO` = a que falhou e `REVOADA_VERSAO_ANTERIOR` = a
  que volta). O estado (atual/anterior) só registra versão que ficou saudável.
- Ao terminar, cada deploy vira **anotação** na linha do tempo dos gráficos do servidor e, se falhou
  ou foi revertido, **notificação** pelos canais de alerta.

## 11. Monitoramento de servidor e de sites

- **Métricas:** ingestão OTLP (HTTP e gRPC), scrape Prometheus e agente; downsampling (1 min/1 h) e
  retenção em camadas no ClickHouse; dashboards versionados (uPlot/ECharts) com atualização ao vivo.
- **Logs:** busca, histograma, padrões, contexto, live tail e métricas derivadas de busca.
- **Traces:** OTLP com tail sampling, waterfall, mapa de serviços e correlação métrica → trace → log.
- **Alertas:** regras com prévia retroativa, rotas, silêncios, plantão com escalonamento, detecção de
  flapping; notificações por e-mail, webhook, Telegram e WhatsApp.
- **Sites:** checks com tempo decomposto (DNS → TLS → TTFB), estados UP → DEGRADADO → SUSPEITO →
  DOWN, expiração de certificado, sondas multi-região, jornadas de navegador e status page pública.
- **Modo TV/kiosk:** parede de saúde, playlists e tomada de tela por alerta, com tokens somente
  leitura.
- **Integração com o canal:** presença dos agentes (§7) dispara alerta quando um agente cai;
  migrações e deploys aparecem na linha do tempo.

## 12. MCP

Servidor MCP em `server/internal/mcpsrv`, com o SDK oficial `github.com/modelcontextprotocol/go-sdk`.

- **Transportes:** Streamable HTTP em `/mcp` (respostas JSON, sessão de 30 min) e
  `revoada-painel mcp` (stdio): uma ponte que lê JSON-RPC da entrada e repassa ao `/mcp` com o token
  (`REVOADA_URL`, `REVOADA_MCP_TOKEN`) — não abre banco, passa pelas mesmas regras e pela mesma
  auditoria.
- **Tokens:** `rvm_…`, criados por administrador com 2FA + reautenticação, mostrados **uma vez**,
  guardados só como SHA-256, com validade (até 365 dias), último uso e revogação imediata. Escopos:
  `leitura` · `mapeamento` · `simulacao`. **Não existe escopo** para executar, aprovar, reverter,
  descartar ou ver credencial.
- **Ferramentas (11):** `listar_agentes`, `listar_servidores`, `alertas_ativos`, `listar_conexoes`
  (sem credencial), `ler_esquema`, `listar_projetos`, `ver_mapeamento`, `status_execucoes` (leitura) ·
  `capturar_esquema`, `gravar_rascunho` (mapeamento; grava versão nova com `origem = mcp`, nunca
  aprova) · `pedir_simulacao` (simulação). Passam pelo mesmo serviço da API.
- **Sem dado de linha:** chaves de amostra dos relatórios e chaves citadas em mensagens de erro saem
  ocultas; nenhuma ferramenta devolve linha.
- **Freio e trilha:** 120 chamadas/min por token; cada chamada (inclusive as negadas) vira linha da
  auditoria com `origem = mcp`, ferramenta, escopo e argumentos.
- **Tela:** Administração → MCP: criar/revogar tokens e copiar a configuração pronta para clientes
  HTTP e stdio.

## 13. Segurança

| Ativo | Ameaça principal |
|---|---|
| Credenciais de bancos de terceiros | vazamento do banco do painel, backup exposto, log com senha |
| Execução em servidores de produção | usuário malicioso, painel invadido, agente falso |
| Dados em trânsito na migração | interceptação, IA/terceiros recebendo dados |
| Contas do painel | força bruta, senha vazada, sessão roubada |

- **Autenticação forte:** Argon2id com sal por senha e comparação em tempo constante; senha mínima
  de 12 caracteres e bloqueio de senhas comuns; **MFA TOTP** com proteção contra reuso e 10 códigos
  de recuperação de uso único guardados em hash (obrigatório para administrador e operador, §21.1);
  **reautenticação** (janela de 5 min) para ações críticas: executar/reverter migração, ver/alterar
  credencial, deploy, gerenciar usuários.
- **Autorização por papel no back-end em toda rota** (§14), nunca só escondendo botão.
- **Segredos em repouso — cofre em envelope** (`core/seguranca/cofre`): cada segredo tem sua DEK
  (AES-256-GCM); a DEK é cifrada pela KEK (chave mestra), que **nunca fica no banco**. A DEK cifrada
  é amarrada à versão da KEK. Rotação da KEK sem decifrar dados (só reembrulha as DEKs). Provedor
  padrão: arquivo `chave-mestra.json` (0600) no diretório de dados do SO (`REVOADA_DATA_DIR`,
  `REVOADA_CHAVE_MESTRA`); a interface de provedor comporta chaveiro do SO, OpenBao/Vault Transit e
  KMS de nuvem. Credenciais são **somente escrita** na UI/API.
- **Selo por agente** (`core/seguranca/selo`): o agente gera o par X25519 na inscrição (a privada
  nunca sai). Para cada tarefa, o painel decifra a senha só em memória e a sela para a chave pública
  daquele agente (ECDH X25519 com chave efêmera → HKDF-SHA256 → XChaCha20-Poly1305), com validade
  curta e amarrada ao contexto (ex.: id da tarefa). O agente abre em memória e descarta; a senha
  nunca vai para o disco dele.
- **Canal:** CA interna ECDSA P-256; certificados de agente de **30 dias com renovação automática**;
  revogação imediata; token de inscrição de uso único com a impressão digital da CA; **TLS 1.3** +
  mTLS; tarefas assinadas com Ed25519 (§7).
- **Tokens e chaves com expiração e rotação:** sessões, certificados, tokens de agente, de deploy e
  de MCP.
- **HTTPS em toda comunicação externa:** TLS próprio (`REVOADA_TLS_CERT`/`REVOADA_TLS_CHAVE`) ou Let's
  Encrypt automático (`REVOADA_TLS_DOMINIO`), ou proxy na frente; HSTS de 1 ano.
- **Menor privilégio:** agente com usuário próprio e sandbox do systemd; lista local de tarefas
  permitidas (vazia por padrão); deploy só com receitas declaradas no servidor; recomendação de
  usuário somente leitura no banco de origem; painel em imagem distroless sem root.
- **Injeção:** valores sempre parametrizados; identificadores só se existirem na foto do esquema e
  passam pelas aspas do motor (`core/ident`); filtros estruturados; transformações de lista fechada;
  `exec.Command` com lista de argumentos, nunca shell com texto montado.
- **Força bruta e abuso:** limite por IP (login, 2FA, inscrição) e por usuário nas APIs; bloqueio
  progressivo da conta (5 erros → 1 min, dobrando até 1 h); limites também na ingestão do gateway.
- **Blindagem HTTP** (`server/internal/blindagem`): cabeçalhos de segurança, CSP da interface
  embutida sem `unsafe-inline` (scripts inline liberados pelo hash), CORS por lista permitida com
  recusa de escrita de origem estranha, `X-Request-ID`, limite de corpo e JSON estrito.
- **Auditoria completa** (§15) com encadeamento por hash e redação de segredos.
- **Cadeia de suprimentos:** `govulncheck`, `npm audit` e gitleaks no CI; Dependabot; releases com
  `SHA256SUMS`; o auto-update do agente só instala artefato com SHA-256 válido.

## 14. Papéis

| Permissão | Administrador (`admin`) | Operador (`operador`) | Leitor (`leitor`) |
|---|:-:|:-:|:-:|
| Ver painéis, execuções, relatórios | ✅ | ✅ | ✅ |
| Criar/editar mapeamentos (rascunho) | ✅ | ✅ | ❌ |
| Aprovar mapeamento, simular, executar, pausar, reverter | ✅ | ✅ | ❌ |
| Rodar deploy | ✅ | ✅ | ❌ |
| Cadastrar conexões e credenciais | ✅ | ❌ | ❌ |
| Inscrever/revogar agentes, usuários, tokens, MCP | ✅ | ❌ | ❌ |

- A matriz vive em `server/internal/auth/papeis.go` e é conferida no back-end em cada rota
  (`auth.Exige`). Papel desconhecido vira leitor (na dúvida, o menor privilégio); novos usuários
  nascem leitores.
- **MFA:** obrigatório para administrador e operador (§21.1), opcional para leitor. Ações críticas
  exigem 2FA ativo e reautenticação recente; as respostas 403 trazem códigos que a interface trata
  (`sem_permissao`, `mfa_necessario`, `reautenticacao_necessaria`).
- O **escopo por servidor** (usuário ou grupo × servidores) vale por cima dos papéis.

## 15. Observabilidade do agente e auditoria

- Logs estruturados (`slog`, JSON) no agente, no gateway e no painel, ligados pelo mesmo
  **`correlacao_id`** (tarefa → painel → agente) e pelo `X-Request-ID` das requisições.
- Histórico de execuções por tarefa: início, fim, estado, erros, quem disparou e de onde
  (ui/mcp/api); eventos com `seq`, nível, etapa e progresso.
- **Batimento** a cada 10 s: online / instável / offline, com alerta quando o agente cai ou perde a
  conexão.
- **Auditoria:** quem fez o quê, quando, em qual banco/servidor/agente, origem e resultado. Cada
  registro leva o hash do anterior e o próprio hash; `GET /api/audit/integridade` aponta exatamente
  onde a corrente foi quebrada. Senhas, tokens e códigos são redigidos antes de gravar.
- O próprio Revoada expõe `/metrics` (Prometheus, protegido por `REVOADA_METRICAS_TOKEN`),
  `/healthz` e `/readyz`.

## 16. UI/UX

- **Design próprio, sem cara de template**; software **sempre explicativo**: cada tela diz o que está
  acontecendo e por quê, em pt-BR.
- **Animações com propósito** (`motion`), em transform/opacity, respeitando
  `prefers-reduced-motion`. O **Bando** — cinco pássaros em V, o símbolo em movimento — é o
  indicador de trabalho (tela carregando, tarefa rodando no agente).
- **Identidade:** marinho profundo (`#070d18`) e azul-petróleo (`#45c6cc`) como único sinal de
  marca; verde/âmbar/vermelho reservados para estado. Tokens em camadas em
  `web/src/styles/tokens.css`; nenhuma cor fora dele.
- Telas-chave: visão geral, servidores, **modo Migração** (conexões, projetos, editor visual,
  simulação, execução ao vivo, verificação), deploys, sites, alertas, agentes (presença) e segurança
  da conta (2FA, sessões, tokens).
- Toda tela responsiva (desktop e celular).

## 17. Qualidade do back-end

- Arquitetura de pastas limpa (§6): `cmd/` + `internal/`, Go idiomático, reutilizar antes de criar;
  recursão onde faz sentido (ordenação topológica das tabelas por FK, comparação de esquemas).
- **Erros explícitos**, embrulhados com contexto, sem falha silenciosa e sem `panic` em runtime.
- **Cache** nas consultas quentes (ex.: autenticação de chave no gateway).
- **Testes nas partes críticas:** criptografia, autorização, protocolo, mapeamento, lotes/retomada,
  rollback e conferência; integração com Firebird e PostgreSQL reais; caos (rede caindo, agente morto
  no meio).

## 18. Requisitos não funcionais

| Tema | Requisito |
|---|---|
| Resiliência | tarefa sobrevive a queda de rede e reinício do agente; telemetria vai ao buffer em disco |
| Desempenho | agente ocioso leve; migração limitada por workers e lotes adaptativos |
| Portabilidade | Linux, Windows, macOS × amd64, arm64 |
| Privacidade (LGPD) | nenhum dado de linha vai para IA/terceiros; credenciais nunca em claro |
| Idioma | interface e docs em pt-BR |

## 19. Licença

O Revoada é distribuído sob a **GNU Affero General Public License v3.0 (AGPL-3.0)**. Quem
modificar o código e oferecê-lo como serviço pela rede precisa disponibilizar o código-fonte dessas
modificações.

## 20. Testes e CI

- **Local** (`Makefile`): `make dev-up` sobe ClickHouse, PostgreSQL, Redis e NATS;
  `make migrate` aplica as migrations do ClickHouse; `make test`/`vet`/`build`; `make agent` compila
  o agente para Linux, Windows e macOS; `make painel` gera o binário único com a interface embutida;
  `make desktop` gera o app Wails; `make backup` e `make restore-drill` ensaiam backup e restauração.
- **CI** (`.github/workflows/ci.yml`):
  - `go vet` + `go test -race` em `core`, `agent`, `gateway` e `server`, em **ubuntu, windows e
    macos**;
  - compilação cruzada `linux|windows|darwin × amd64|arm64`;
  - build do app desktop;
  - `golangci-lint` por módulo;
  - web: `npm run lint`, `npm test` (Vitest) e `npm run build`;
  - segurança: `govulncheck`, `npm audit` (produção) e gitleaks.
- **Release** (`.github/workflows/release.yml`): em tag `v*`, compila painel e agente para
  `linux|windows|darwin × amd64|arm64` e publica no GitHub Releases com `SHA256SUMS`.
- **Integração de migração:** testes contra Firebird e PostgreSQL reais, ativados por
  `REVOADA_TEST_FB` e `REVOADA_TEST_PG_HOST`.
- **Validação de ponta a ponta** (`tests/validacao`): prova, contra uma stack viva, que o número na
  tela é a medida real da máquina (comparando com `/proc`, `statvfs` etc.) e que as sondas de site
  batem no endpoint certo.

## 21. Decisões assumidas por padrão

Decisões tomadas sem pedido explícito, revisáveis. As decisões estruturais **D1–D5** estão em §5.

### 21.1 MFA obrigatório para administrador e operador

Administrador e operador precisam de 2FA ativo; para leitor é opcional. Implementado em
`auth.MFAObrigatorio`.

### 21.2 Porta própria para o canal dos agentes

O canal gRPC mTLS usa porta própria configurável (padrão **7443**, `REVOADA_CANAL_ADDR`; endereço
anunciado aos agentes em `REVOADA_CANAL_PUBLICO` e nomes do certificado em `REVOADA_CANAL_HOSTS`).
Compartilhar a 443 com o painel via ALPN fica para depois.

### 21.3 Nome do binário do agente

O binário do agente se chama `revoada-agent`; o do painel, `revoada-painel`.

## 22. Requisitos de segurança e operação

O projeto atende, sempre, aos 20 itens abaixo. O detalhe de cada um (como, onde no código e como
configurar) fica em [`docs/SEGURANCA.md`](SEGURANCA.md). Toda funcionalidade nova entra respeitando
esta lista (ex.: o módulo de migração usa `core/validacao`, `core/ident` e o cofre; o deploy usa só
receitas declaradas no servidor).

| # | Item | Como é atendido |
|---|---|---|
| 1 | HTTPS | TLS próprio ou Let's Encrypt; HSTS; canal dos agentes TLS 1.3 + mTLS |
| 2 | Senhas com hash | Argon2id |
| 3 | MFA | TOTP + códigos de recuperação; obrigatório para administrador e operador |
| 4 | Rate limit | por IP, por usuário e bloqueio progressivo da conta |
| 5 | Validação de inputs | JSON estrito, limite de corpo, validadores de fronteira |
| 6 | Sanitização de dados | texto sem caracteres de controle/invisíveis, redação de segredos, saída escapada |
| 7 | SQL Injection | parâmetros + identificadores da foto do esquema + filtros estruturados |
| 8 | Migrations | versionadas, com checksum e trava |
| 9 | Rollback | `.down.sql` + `revoada-painel migracoes desfazer`; rollback de migração de dados (§9.4) e de deploy (§10) |
| 10 | Controle de acesso | papéis no back-end + escopo por servidor + lista local do agente |
| 11 | Expiração de sessão | access token de 15 min / inatividade de 24 h / limite absoluto de 7 dias / sair de todos os dispositivos |
| 12 | Secrets | cofre em envelope, chave mestra fora do banco, gitleaks |
| 13 | CORS | lista permitida + recusa de escrita de origem estranha |
| 14 | Logs | JSON, `X-Request-ID`, `correlacao_id`, auditoria encadeada |
| 15 | Backups | banco + séries com retenção e ensaio de restauração; chave mestra em backup separado e cifrado |
| 16 | Criptografia | AES-256-GCM, X25519/XChaCha20-Poly1305, Ed25519, TLS |
| 17 | Dependências | govulncheck, npm audit, Dependabot |
| 18 | Menor privilégio | distroless sem root, agente com usuário próprio, leitor por padrão |
| 19 | Monitoramento | `/metrics`, saúde, presença dos agentes com alerta |
| 20 | Plano de recuperação | `docs/runbook-dr.md` (inclui chave mestra, CA e migrations) |

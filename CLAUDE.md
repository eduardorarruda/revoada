# Revoada — Contexto do Projeto (para assistentes de IA)

## O que é
Plataforma open source e self-hosted de observabilidade, migração de bancos de dados e deploy:
coleta (agentes push + OTLP + scrape), armazenamento (ClickHouse), visualização em tempo real (React),
alerting/on-call, modo TV/Kiosk, motor de migração com verificação em camadas e servidor MCP —
em um único produto que sobe com `docker compose up`.
A arquitetura está em docs/ARQUITETURA.md — consulte-a na dúvida.

## Stack (decidida — não trocar sem ADR em docs/adr/)
- Backend: Go (monolito modular; workspace `go.work` com os módulos core, proto, gateway, server, agent; `desktop/` é um módulo Wails à parte)
- Store de telemetria: ClickHouse (métricas, logs, traces, eventos)
- Metadados: PostgreSQL · Fila: NATS JetStream
- Frontend: React 19 + TypeScript + Vite; uPlot (séries temporais) + ECharts (demais)
- Deploy: Docker Compose + GitHub Actions

## Estrutura do monorepo
/agent      → agente em Go que roda no servidor monitorado (coleta, canal mTLS, migração, deploy)
/gateway    → serviço Go de ingestão (OTLP HTTP/gRPC, scrape)
/server     → painel: API, alerting, auth/papéis, canal com os agentes, MCP; serve a interface embutida
/web        → frontend React
/core       → código compartilhado (segurança, modelo de migração)
/proto      → contratos gRPC do canal painel ↔ agente
/desktop    → aplicativo desktop (Wails) que abre o painel numa janela nativa
/acoes      → GitHub Action de deploy
/deploy     → compose de dev e de produção (exemplo), migrations do ClickHouse, backup
/docs       → ARQUITETURA.md, guias, runbooks e ADRs
/tests      → validação de ponta a ponta

## Convenções
- Go: estrutura cmd/ + internal/; erros embrulhados com contexto; testes de tabela; sem panic em runtime.
- TS: strict; componentes funcionais; sem any.
- Endpoint novo do gateway entra no gateway/openapi.yaml.
- Migrations do Postgres: sempre com up e down; nunca edite uma migration já publicada (o painel confere o checksum) — crie uma nova.
- Segurança: TLS validado sempre (nunca `curl -k`); segredos via env ou cofre, nunca no código;
  senhas Argon2id; tokens revogáveis.
- UI: tokens de design em web/src/styles/tokens.css; dark-first; cor semântica só para estado;
  tabular figures em números dinâmicos; todo painel tem descrição ("explicativo por padrão").
- Idioma: UI, docs e comentários em pt-BR; nos identificadores, siga o estilo do arquivo vizinho.

## Definição de pronto
Compila sem warning · testes passam · lint passa · comportamento verificado na tela quando há UI.

## Forma de trabalhar
Ciclo de toda entrega: **pesquisar → planejar → teste primeiro → implementar → revisar → verificar**.
1. Pesquisar e reaproveitar: procurar no próprio repo antes, depois lib pronta e referência externa.
2. Planejar antes de feature grande.
3. Teste primeiro: o teste que falha vem antes do código.
4. Revisar em contexto limpo (qualidade, segurança, acessibilidade); corrigir problemas graves antes de seguir.
5. Verificar de verdade: gates de cada módulo + ver a tela rodando no navegador, desktop e mobile.

O repositório traz regras opcionais para o plugin ECC (github.com/affaan-m/ECC) em `.claude/`.
Quando ECC e este projeto divergirem, as regras deste projeto vencem. Em especial:
- Commit e push só com pedido explícito de quem conduz a sessão.
- Produção é somente leitura sem autorização (deploy, restart, ALTER/DELETE).
- Dado sensível (chaves, senhas, hosts reais, dados de clientes) nunca sobe para o repositório.
- Toda tela responsiva, dentro dos tokens de `web/src/styles/tokens.css`; UI em pt-BR e explicativa.
- A cobertura de 80% é meta para código novo, não motivo para teste de fachada.

<div align="center">

<img src="web/public/logo-mark.png" alt="" width="132">

# Revoada

**Monitoramento de servidores e migração de bancos de dados, no seu servidor.**
Métricas, logs, alertas, checagem de sites e uma migração Firebird → PostgreSQL que prova,
linha a linha, que nada se perdeu no caminho.

[![CI](https://github.com/eduardorarruda/revoada/actions/workflows/ci.yml/badge.svg)](https://github.com/eduardorarruda/revoada/actions/workflows/ci.yml)
[![Licença: AGPL-3.0](https://img.shields.io/badge/licen%C3%A7a-AGPL--3.0-0d7a73)](LICENSE)
[![Go 1.26](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![React](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=black)](web)
[![Self-hosted](https://img.shields.io/badge/self--hosted-docker%20compose-2496ED?logo=docker&logoColor=white)](#-início-rápido)

[Início rápido](#-início-rápido) ·
[Recursos](#-o-que-ele-faz) ·
[Migração verificada](#-migração-que-se-prova-certa) ·
[Arquitetura](#-arquitetura) ·
[Contribuir](CONTRIBUTING.md)

<img src="docs/imagens/inicio.png" alt="Tela inicial do Revoada: veredito da frota, indicadores e o que precisa de atenção" width="100%">

</div>

---

## ✨ Por que o Revoada

- **Tudo num lugar só.** CPU, RAM, disco, rede, containers, logs, traces, checagem de sites e
  jornadas, alertas no WhatsApp/e-mail/Telegram e status page — sem montar cinco ferramentas.
- **A resposta antes do gráfico.** A tela inicial diz em uma frase se está tudo bem — e nunca
  afirma o que não mediu: servidor mudo, métrica velha ou fonte que falhou aparecem como tal.
- **Migração de banco com prova.** Simulação obrigatória, execução em lotes com checkpoint,
  troca atômica, reversão em uma transação e **verificação em três camadas independentes**.
- **Seu servidor, seus dados.** Self-hosted, um `docker compose up`. O agente abre a conexão
  (mTLS) — o servidor monitorado não expõe porta nenhuma.
- **Seguro por padrão.** MFA obrigatório para quem administra, cofre de credenciais em
  envelope, tarefas assinadas, auditoria encadeada e redação de segredos nos logs.

## 🚀 Início rápido

Você precisa de **Docker** com o plugin **Compose v2**.

```bash
git clone https://github.com/eduardorarruda/revoada.git
cd revoada
./scripts/iniciar.sh
```

O script gera um `.env` com segredos aleatórios, compila as imagens e sobe tudo. Abra
**http://localhost:8080** e crie o administrador no primeiro acesso (com 2FA).

| Serviço | Porta | Para quê |
|---|---|---|
| Painel | `8080` | Interface, API e MCP |
| Gateway | `8090` (HTTP) · `4317` (gRPC) | Recebe métricas, logs e traces (OTLP) |
| Canal dos agentes | `7443` | Conexão mTLS iniciada pelo agente (tarefas de migração, deploy) |

> Em produção, coloque um proxy com HTTPS na frente do painel (Caddy, Traefik ou nginx),
> ajuste os endereços públicos no `.env` e ligue `REVOADA_SECURE_COOKIES=1`.
> Todas as opções estão comentadas em [`.env.example`](.env.example).

### Monitorar um servidor

1. Baixe o `revoada-agent` da [página de releases](https://github.com/eduardorarruda/revoada/releases)
   (Linux, Windows e macOS, amd64/arm64) — ou compile: `cd agent && go build ./cmd/revoada-agent`.
2. No painel, **Infraestrutura → Adicionar servidor** gera a chave de ingestão. Coloque-a no
   [`agent.yaml`](agent/config.example.yaml) junto com o endereço do gateway.
3. Instale como serviço do sistema (systemd, launchd ou Serviço do Windows):

```bash
sudo ./revoada-agent -config /etc/revoada/agent.yaml servico instalar
sudo ./revoada-agent -config /etc/revoada/agent.yaml servico iniciar
```

Para as tarefas de migração e deploy, inscreva o agente no canal: **Agentes → Inscrever
servidor** mostra o comando `revoada-agent inscrever` com um token de uso único. Hospedagem
sem systemd (cPanel)? Veja [docs/agente-cpanel.md](docs/agente-cpanel.md).

## 🧭 O que ele faz

<table>
<tr>
<td width="50%" valign="top">

**Observar**
- Métricas do host e de containers Docker, por montagem e por interface
- Dashboards versionados, tempo real por WebSocket, Explore sem escrever consulta
- Mural de Saúde com semáforo por servidor e modo TV/kiosk

**Investigar**
- Logs com busca, histograma, padrões, contexto e *live tail*
- Traces OTLP com *waterfall*, mapa de serviços e correlação métrica → trace → log
- **Agentes de IA**: custo, tokens, erros e o replay passo a passo de cada execução, a partir do OpenTelemetry que a aplicação já emite
- Redação de segredos na origem: senha, token, JWT, URL com senha, cartão de crédito

</td>
<td width="50%" valign="top">

**Reagir**
- Alertas com prévia retroativa, silêncios, plantão, escalonamento e detecção de oscilação
- Notificações por WhatsApp, e-mail, webhook e Telegram
- Checagem de sites (DNS → TLS → TTFB), jornadas de vários passos e status page pública

**Operar**
- Migração Firebird/PostgreSQL → PostgreSQL com verificação em três camadas
- Upgrade Firebird 2.x → 5 com diagnóstico, ensaio e `fix.sql` — o original nunca é alterado
- Deploy pelo agente a partir de uma GitHub Action, com health check e reversão automática
- Servidor **MCP** para agentes de IA, com tokens de escopo e auditoria

</td>
</tr>
</table>

<p align="center">
  <img src="docs/imagens/servidor.png" alt="Detalhe de um servidor: CPU, RAM, disco, rede, processos e serviços descobertos" width="49%">
  <img src="docs/imagens/logs.png" alt="Busca de logs com histograma e filtros" width="49%">
</p>
<p align="center">
  <img src="docs/imagens/mural.png" alt="Mural de Saúde com o semáforo de cada servidor" width="49%">
  <img src="docs/imagens/sites.png" alt="Checagem de sites e jornadas" width="49%">
</p>

## 🤖 Agentes de IA

Se a sua aplicação chama LLM — chatbot, agente com ferramentas, RAG —, mande os traces
OpenTelemetry dela para o gateway. O Revoada reconhece a convenção GenAI do
OpenTelemetry, o OpenLLMetry, o OpenInference e o Vercel AI SDK e mostra quanto custa, quanto demora,
onde falha e o que o agente fez em cada passo, ao lado da CPU e dos logs do mesmo
servidor. Custo é estimativa por uma tabela de preços com data (e "sem preço" quando
falta, nunca zero); prompt e resposta ficam **desligados** por padrão. O Revoada observa
IA, mas não usa: nada nele chama um modelo ([ADR 008](docs/adr/008-observar-agentes-de-ia.md)).

Guia por biblioteca (Python, Node.js, Go): [docs/instrumentacao-ia.md](docs/instrumentacao-ia.md).
Para ver a tela sem chave de API: [exemplos/agente-demo](exemplos/agente-demo).

## 🛡️ Migração que se prova certa

Migrar o banco de um cliente só é seguro se der para **provar** que o destino ficou igual à
origem. O Revoada não pede que você confie — ele mostra:

| Passo | O que é garantido |
|---|---|
| **Conexões** | Senha cifrada no cofre, selada para um agente específico; o painel nunca vê dado das tabelas |
| **Estrutura** | O agente lê só o schema (tabelas, tipos, chaves, FKs) dos dois bancos |
| **Mapeamento** | Versionado; só versão sem erro é aprovada; coluna obrigatória sem origem bloqueia |
| **Simulação** | Lê 100% da origem e aplica as transformações sem gravar nada; aponta cada linha que o destino recusaria |
| **Execução** | Lotes com checkpoint na mesma transação (pausa e retoma), staging e troca atômica no fim |
| **Verificação** | Três camadas: soma canônica de cada coluna antes da troca · comparação linha a linha · **os próprios bancos imprimem cada valor como texto e os textos são comparados** |

A terceira camada não passa pela conversão de tipos do Revoada — é ela que pega erro de
leitura, de tipo e de fuso horário. Num ERP de teste com 192 mil linhas, todas as camadas
fecharam em **192.005 linhas × 20 colunas idênticas**. Reverter desfaz tudo em uma transação.

<p align="center">
  <img src="docs/imagens/migracao-verificacao.png" alt="Tela de verificação da migração: 192.005 linhas idênticas, tabela a tabela" width="100%">
</p>

## 🏗️ Arquitetura

```mermaid
flowchart LR
  subgraph srv["Servidores monitorados"]
    A["revoada-agent<br/>métricas · logs · tarefas"]
    APP["Suas aplicações<br/>(OTLP)"]
  end
  A -- "OTLP" --> G
  APP -- "OTLP" --> G
  A <-. "canal mTLS (o agente conecta)" .-> P
  G["Gateway<br/>ingestão"] --> N[("NATS<br/>JetStream")]
  N --> CH[("ClickHouse<br/>séries · logs · traces")]
  G --> CH
  P["Painel<br/>API · interface · MCP"] --> CH
  P --> PG[("PostgreSQL<br/>metadados · cofre")]
  U(["Você / agentes de IA"]) --> P
```

| Pasta | Papel |
|---|---|
| [`server/`](server) | Painel: API, autenticação, alertas, migração, MCP e a interface embutida (binário único) |
| [`gateway/`](gateway) | Ingestão OTLP (HTTP e gRPC), scrape Prometheus, descoberta e sondas |
| [`agent/`](agent) | Agente em Go: coleta, buffer em disco, canal mTLS, migração, upgrade Firebird, deploy |
| [`web/`](web) | Interface React + Vite + TypeScript |
| [`core/`](core) | Regras puras compartilhadas: cofre, selo, schema, mapeamento, transformações, plano |
| [`proto/`](proto) | Contrato gRPC painel ↔ agente |
| [`desktop/`](desktop) | App desktop (Wails) |
| [`acoes/`](acoes) | GitHub Action [`revoada-deploy-action`](acoes/revoada-deploy-action) |
| [`deploy/`](deploy) | Compose de desenvolvimento, migrações do ClickHouse, instaladores do agente, backup |
| [`tests/validacao/`](tests/validacao) | Bateria que confere o stack rodando contra o kernel da máquina |

Mais detalhes em [docs/ARQUITETURA.md](docs/ARQUITETURA.md) e
[docs/SEGURANCA.md](docs/SEGURANCA.md).

## ✅ Como sabemos que funciona

Além dos testes de unidade e de integração (Firebird 2.5, Firebird 5 e PostgreSQL de verdade,
em UTC e no fuso de São Paulo), a [bateria de validação](tests/validacao) compara o que a tela
mostra com o que a máquina mede: RAM contra `/proc/meminfo`, CPU contra `/proc/stat` sob carga
conhecida, disco contra `statvfs`, cada linha de log marcada, e cada checagem de site contra o
log de um servidor de teste que registra exatamente o que recebeu. Resultado atual: **152 de
152** verificações.

## 🧑‍💻 Desenvolvimento

```bash
make dev-up      # PostgreSQL, ClickHouse, NATS e Redis
make migrate     # tabelas de telemetria
make test        # testes Go
cd web && npm install && npm run dev   # interface em :5173
```

O passo a passo completo está em [CONTRIBUTING.md](CONTRIBUTING.md).

## 🤝 Contribuindo

Issues e pull requests são bem-vindos — leia o [guia de contribuição](CONTRIBUTING.md) e o
[código de conduta](CODE_OF_CONDUCT.md). Vulnerabilidades: [SECURITY.md](SECURITY.md).

## 📄 Licença

[AGPL-3.0](LICENSE). Use, estude, modifique e distribua à vontade; se oferecer uma versão
modificada como serviço, compartilhe as modificações com quem usa.

<details>
<summary><b>English summary</b></summary>

Revoada is a self-hosted, open-source platform for **server monitoring** (metrics, logs,
traces, uptime checks, alerting, status pages) and **database migration** (Firebird/PostgreSQL
→ PostgreSQL, Firebird 2.x → 5 upgrades) whose migrations are verified in three independent
layers — including having both databases render every value as text and comparing them.
It also observes **AI agents** from their OpenTelemetry traces (GenAI semantic conventions, Vercel AI SDK,
OpenLLMetry, OpenInference): cost, tokens, errors and a step-by-step replay — without
calling any model itself.
Start it with `./scripts/iniciar.sh` (Docker Compose) and open `http://localhost:8080`.
The codebase and UI are in Brazilian Portuguese. Licensed under AGPL-3.0.

</details>

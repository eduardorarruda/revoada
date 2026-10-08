# Fixtures de spans de IA

Os testes do gateway (`gateway/internal/otlp/genai_test.go`) leem spans **gravados de
bibliotecas reais de instrumentação**, e não spans escritos à mão: a convenção GenAI do
OpenTelemetry ainda está em desenvolvimento e cada biblioteca a interpreta de um jeito.
Atributo inventado em teste esconde exatamente o tipo de diferença que importa
(por exemplo: o OpenLLMetry soma o cache em `input_tokens`; a API da Anthropic, não).

Nada aqui chama um provedor de verdade. `servidores.py` sobe um **LLM falso** (rotas
compatíveis com OpenAI e Anthropic, com uma chamada de ferramenta e um erro 500
simulados) e um **coletor OTLP** que grava cada `POST /v1/traces` em `out/`.

As respostas falsas carregam de propósito um número de cartão de teste e uma chave no
formato `sk-…` (sem valor real): servem de canário para a redação de conteúdo.

## Regravar

```bash
cd tests/genai-fixtures
docker run --rm -v "$PWD":/w -w /w --user "$(id -u):$(id -g)" -e HOME=/tmp \
  python:3.12-slim bash rodar.sh
cp out/*.pb ../../gateway/internal/otlp/testdata/genai/
```

Versões usadas na gravação atual: [`versoes.txt`](versoes.txt). Ao atualizar uma
biblioteca, regrave, rode `go test ./internal/otlp/...` no gateway e ajuste o
normalizador (`core/genai`) se algum nome de atributo mudou.

`dump.py` imprime os atributos de cada `.pb` em texto, para inspeção.

## Vercel AI SDK

`vercel/gravar.mjs` grava o `generateText` com ferramenta contra um LLM falso embutido
no próprio script, nas duas integrações de telemetria do SDK 7:

- `@ai-sdk/otel` → `OpenTelemetry` (semconv GenAI: chat, execute_tool, invoke_agent);
- `@ai-sdk/otel` → `LegacyOpenTelemetry` (atributos `ai.*`, dialeto `vercel-ai`).

No SDK 7, `experimental_telemetry` sozinho **não emite span nenhum**: é preciso
registrar a integração com `registerTelemetry(new OpenTelemetry())`.

```bash
cd tests/genai-fixtures/vercel
docker run --rm -v "$PWD/..":/w -w /w/vercel --user "$(id -u):$(id -g)" -e HOME=/tmp \
  node:22-slim bash -c 'mkdir -p ../out && npm install --no-audit --no-fund && node gravar.mjs && VARIANTE=legado node gravar.mjs'
```

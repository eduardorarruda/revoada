# Instrumentação de agentes de IA

Como mandar a telemetria de **aplicações que chamam LLM** — chatbots, agentes com
ferramentas, pipelines de RAG — para a tela **Investigar → Agentes de IA** do Revoada:
custo, tokens, latência, erros e o passo a passo de cada execução, ao lado da CPU, dos
logs e dos deploys do mesmo servidor.

O Revoada **observa** aplicações de IA, mas não usa IA: nenhuma parte dele chama um
modelo, guarda chave de provedor ou fica no caminho da chamada. Ele só recebe os traces
OpenTelemetry que a sua aplicação já sabe emitir. O porquê está no
[ADR 008](adr/008-observar-agentes-de-ia.md).

Este guia complementa o de [traces](instrumentacao-traces.md) (endpoint, formatos,
segurança do transporte). Quem quiser ver a tela funcionando antes de mexer na própria
aplicação pode rodar o [agente de demonstração](../exemplos/agente-demo/), que não
precisa de chave de API.

---

## 1. Início rápido

1. **Chave de ingestão.** No painel, **Infraestrutura → Adicionar servidor** gera a
   chave. É a mesma das métricas e dos traces; o host da aplicação é o `host.name` que
   ela informar (com `REVOADA_HOST_BINDING` ligado no gateway, passa a valer o host da
   chave).
2. **Instrumentar.** Em Python, com a instrumentação oficial do OpenTelemetry:

   ```bash
   pip install "openai>=2.8,<3" opentelemetry-sdk opentelemetry-exporter-otlp-proto-http \
     opentelemetry-instrumentation-openai-v2==2.4b0 opentelemetry-util-genai==0.4b0

   export OTEL_SERVICE_NAME=meu-agente
   export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://<seu-gateway>:8090/v1/traces
   export OTEL_EXPORTER_OTLP_HEADERS=X-Revoada-Key=<SEU_SERVERKEY>
   export OTEL_RESOURCE_ATTRIBUTES=host.name=meu-servidor-01
   export OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental
   export OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=SPAN_ONLY   # ou NO_CONTENT

   opentelemetry-instrument python app.py
   ```

   (Sem `opentelemetry-instrument`, configure o SDK no código — ver §3.1.)
3. **Abrir a tela.** **Investigar → Agentes de IA**: visão geral (custo, chamadas,
   erros, latência), **Execuções** com o replay passo a passo, **Ferramentas** e
   **Modelos e preços**.

O que mais confunde na primeira vez está na §8: tokens "não informado", replay sem
conteúdo e spans que aparecem em Traces mas não em Agentes de IA.

---

## 2. Como o Revoada entende os spans

- **Endpoint:** `POST http://<seu-gateway>:8090/v1/traces` (OTLP/HTTP protobuf ou
  JSON) ou gRPC na `4317`, com o header `X-Revoada-Key`. Atrás do proxy de
  `deploy/prod/`, `https://<seu-painel>/v1/traces`. Detalhes em
  [instrumentacao-traces.md](instrumentacao-traces.md).
- **Recurso:** `service.name` (ou `OTEL_SERVICE_NAME`) vira a coluna **service**; defina
  sempre — sem ele a aplicação aparece como `unknown_service`. `host.name` liga a
  aplicação ao servidor.
- **Reconhecimento:** o gateway lê quatro dialetos — a convenção GenAI atual do
  OpenTelemetry, o legado dela (OpenLLMetry antigo, `traceloop.*`), o OpenInference
  (Arize/Phoenix) e a integração antiga do Vercel AI SDK (`ai.*`) — e grava uma linha normalizada por span de IA (modelo, tokens com
  cache, agente, ferramenta, conversa, erro, custo informado). A tabela completa de
  atributos está na §9. Só ter **alguma** chave `gen_ai.*` não basta: frameworks
  propagam `gen_ai.conversation.id` para spans HTTP, que não são chamadas de IA.
- **O span continua sendo um span.** Ele vai também para a tabela de traces; o
  waterfall em **Investigar → Traces** não muda.
- **Sem amostragem:** custo é soma, e somar sobre 20% das chamadas daria um gasto cinco
  vezes menor que a fatura. Por isso todo span de IA é mantido, qualquer que seja
  `REVOADA_TRACE_SAMPLE`. Limite honesto: se spans **sem** IA do mesmo trace chegaram
  num lote anterior e foram descartados pela amostragem, o trace aparece marcado como
  parcial.
- **Prazo:** chamadas por 90 dias (`genai_spans`), agregado por minuto por 400 dias
  (`genai_1m`, é dele que saem visão geral e alertas), conteúdo por 7 dias (§4).
- **Execução** é um trace com ao menos um span de IA. O agente da execução é o do span
  `invoke_agent` mais alto; sem ele, o `service`.

---

## 3. Por biblioteca

As receitas abaixo foram medidas contra um LLM falso (`tests/genai-fixtures`), com as
versões de [`versoes.txt`](../tests/genai-fixtures/versoes.txt). A convenção GenAI do
OpenTelemetry **ainda não é estável** e as bibliotecas mudam nomes entre versões; ao
atualizar uma delas, confira em **Investigar → Traces** se os atributos continuam os
da §9.

### 3.1 Python — instrumentação oficial do OpenTelemetry (recomendada)

`opentelemetry-instrumentation-openai-v2` cria um span por chamada de modelo
(`chat gpt-4o-mini`) com provedor, modelo, tokens, motivo de término e, se pedido, o
conteúdo. Armadilhas medidas com a 2.4b0:

- **Exige `opentelemetry-util-genai==0.4b0`.** A 1.x removeu um módulo que a 2.4b0
  importa, e o programa quebra com `ImportError`.
- **Exige `openai>=2.8,<3`.** O openai 3.x trocou para `httpx2` e a instrumentação
  deixa de funcionar.
- **`OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental`** liga os nomes atuais
  da convenção (`gen_ai.provider.name`, `gen_ai.input.messages`…).
- **`OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT` agora é um enum:**
  `NO_CONTENT` · `SPAN_ONLY` · `EVENT_ONLY` · `SPAN_AND_EVENT`. O antigo `true` é
  recusado com um aviso e vira `NO_CONTENT`. Use **`SPAN_ONLY`**: `EVENT_ONLY` manda o
  conteúdo como **log** OTLP, fora do trace, e o replay do Revoada não o vê.
- **Não informa tokens de cache.** Mesmo com `cached_tokens` na resposta da OpenAI, o
  span da 2.4b0 traz só `input_tokens`/`output_tokens`. O custo estimado cobra o cache
  como entrada nova — fica **acima** do real, nunca abaixo (§5).
- **Não cria spans de agente nem de ferramenta.** Esses você cria à mão (abaixo).

Configuração no código, sem `opentelemetry-instrument`:

```python
import os
os.environ.setdefault("OTEL_SEMCONV_STABILITY_OPT_IN", "gen_ai_latest_experimental")
os.environ.setdefault("OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT", "SPAN_ONLY")

from opentelemetry import trace
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
from opentelemetry.instrumentation.openai_v2 import OpenAIInstrumentor

provedor = TracerProvider(resource=Resource.create({"service.name": "meu-agente"}))
provedor.add_span_processor(BatchSpanProcessor(OTLPSpanExporter(
    endpoint="http://<seu-gateway>:8090/v1/traces",
    headers={"X-Revoada-Key": os.environ["REVOADA_KEY"]},
)))
trace.set_tracer_provider(provedor)
OpenAIInstrumentor().instrument()
# … e no fim do processo: provedor.shutdown(), senão o último lote se perde.
```

**Spans de agente e de ferramenta.** São eles que dão nome ao agente, agrupam a
conversa e mostram cada ferramenta no replay. Os nomes seguem a convenção GenAI
(o mesmo que `tests/genai-fixtures/otel_oficial.py` grava):

```python
tracer = trace.get_tracer("meu-agente")

with tracer.start_as_current_span("invoke_agent Meteorologista") as ag:
    ag.set_attribute("gen_ai.operation.name", "invoke_agent")
    ag.set_attribute("gen_ai.provider.name", "openai")
    ag.set_attribute("gen_ai.agent.name", "Meteorologista")
    ag.set_attribute("gen_ai.agent.id", "meteorologista-v1")       # opcional
    ag.set_attribute("gen_ai.conversation.id", id_da_conversa)       # agrupa execuções

    r = cliente.chat.completions.create(model="gpt-4o-mini", messages=msgs, tools=tools)
    for chamada in r.choices[0].message.tool_calls or []:
        with tracer.start_as_current_span(f"execute_tool {chamada.function.name}") as ft:
            ft.set_attribute("gen_ai.operation.name", "execute_tool")
            ft.set_attribute("gen_ai.tool.name", chamada.function.name)
            ft.set_attribute("gen_ai.tool.call.id", chamada.id)      # liga ao pedido do modelo
            ft.set_attribute("gen_ai.tool.type", "function")
            try:
                resultado = executar(chamada)
            except Exception as e:
                ft.set_attribute("error.type", type(e).__name__)
                ft.set_status(trace.Status(trace.StatusCode.ERROR, str(e)))
                raise
```

Argumentos e resultado da ferramenta podem ir em `gen_ai.tool.call.arguments` e
`gen_ai.tool.call.result` — são tratados como conteúdo (§4). O
[agente de demonstração](../exemplos/agente-demo/agente.py) traz o laço completo, com
limite de passos e erro marcado no span.

### 3.2 Python — OpenLLMetry (Traceloop)

`opentelemetry-instrumentation-openai` e `opentelemetry-instrumentation-anthropic`
(medido na 0.62) **já emitem os nomes novos** da convenção (`gen_ai.provider.name`,
`gen_ai.usage.input_tokens`, `gen_ai.usage.cache_read.input_tokens`):

```bash
pip install "openai>=2.8,<3" anthropic opentelemetry-sdk opentelemetry-exporter-otlp-proto-http \
  opentelemetry-instrumentation-openai opentelemetry-instrumentation-anthropic
```

```python
from opentelemetry.instrumentation.openai import OpenAIInstrumentor
from opentelemetry.instrumentation.anthropic import AnthropicInstrumentor
OpenAIInstrumentor().instrument(); AnthropicInstrumentor().instrument()
```

- **Conteúdo:** controlado por `TRACELOOP_TRACE_CONTENT`. Nas versões que lemos,
  ausente equivale a `true` — para **não** mandar prompt, defina `false`.
- **Cache dentro da entrada:** o OpenLLMetry soma o cache em `input_tokens` (medido:
  Anthropic com 20 tokens novos + 100 lidos do cache + 50 gravados chega como
  `input_tokens=170`). A API crua da Anthropic informa os três separados; o Revoada
  aceita os dois jeitos (§9, "cache").
- **Agente e ferramenta:** os decoradores do `traceloop-sdk` (`@workflow`, `@agent`,
  `@tool`, `@task`) geram `traceloop.span.kind` e `traceloop.entity.name`, que o
  Revoada lê como `invoke_agent`, `execute_tool` e `chain`. Sem o SDK, crie os spans
  à mão como na §3.1.
- Versões antigas do OpenLLMetry usam os nomes legados (`gen_ai.system`,
  `gen_ai.prompt.0.content`, `llm.usage.*`); também são lidos.

### 3.3 Python — OpenInference (Arize/Phoenix), LangChain e LlamaIndex

```bash
pip install "openai>=2.8,<3" opentelemetry-sdk opentelemetry-exporter-otlp-proto-http \
  openinference-instrumentation-openai==0.1.64     # exige openai >= 2.8
```

```python
from openinference.instrumentation.openai import OpenAIInstrumentor
OpenAIInstrumentor().instrument()
```

O mesmo vale para `openinference-instrumentation-langchain` e
`openinference-instrumentation-llama-index`: o tipo do span
(`openinference.span.kind` = `LLM`, `AGENT`, `TOOL`, `CHAIN`, `RETRIEVER`, `RERANKER`,
`EMBEDDING`, `GUARDRAIL`) vira a operação do Revoada, e os spans de agente e de
ferramenta já vêm prontos. Honestidade: gravamos fixtures só da instrumentação de
OpenAI; LangChain e LlamaIndex usam o mesmo padrão de atributos, mas não foram
medidos aqui.

- **Conteúdo vem ligado por padrão** (`input.value`, `output.value`,
  `llm.input_messages.*`). Para não mandar, use a configuração de ocultação do
  OpenInference (`OPENINFERENCE_HIDE_INPUTS`, `OPENINFERENCE_HIDE_OUTPUTS` — confira os
  nomes na versão que usar).
- **Conversa:** o OpenInference não tem `gen_ai.conversation.id`; use `session.id`
  (`from openinference.instrumentation import using_session`).
- O OpenInference achata cada mensagem em várias chaves. Antes, um span com histórico
  longo estourava o teto de 64 rótulos e era recusado inteiro; agora o gateway tira as
  chaves de conteúdo antes de contar.

### 3.4 Node.js — Vercel AI SDK

Medido com `ai` 7.0 e `@ai-sdk/otel` 1.0 (fixtures em `tests/genai-fixtures/vercel`). No
SDK 7, `experimental_telemetry` sozinho **não emite span nenhum**: a ponte com o
OpenTelemetry virou um pacote à parte, registrado uma vez na inicialização.

```bash
npm install ai @ai-sdk/openai @ai-sdk/otel @opentelemetry/sdk-node @opentelemetry/exporter-trace-otlp-proto
```

```js
import { NodeSDK } from '@opentelemetry/sdk-node';
import { OTLPTraceExporter } from '@opentelemetry/exporter-trace-otlp-proto';
import { OpenTelemetry } from '@ai-sdk/otel';
import { generateText, registerTelemetry, stepCountIs, tool } from 'ai';
import { openai } from '@ai-sdk/openai';

const sdk = new NodeSDK({
  serviceName: 'meu-agente-node',
  traceExporter: new OTLPTraceExporter({
    url: 'http://<seu-gateway>:8090/v1/traces',
    headers: { 'X-Revoada-Key': process.env.REVOADA_KEY },
  }),
});
sdk.start();
registerTelemetry(new OpenTelemetry()); // spans na convenção GenAI

const { text } = await generateText({
  model: openai('gpt-4o-mini'),
  prompt: 'Como está o clima em Recife?',
  tools: { clima: tool({ /* … */ }) },
  stopWhen: stepCountIs(5),
  telemetry: { functionId: 'Meteorologista' }, // vira o nome do agente no Revoada
});

await sdk.shutdown();
```

O que chega, com a integração `OpenTelemetry`:

- `invoke_agent <modelo>` com `gen_ai.agent.name` = `functionId` — o agente da execução;
- um `chat` por chamada ao modelo (tokens, cache lido, motivo de término), um
  `execute_tool` por ferramenta (com argumentos e resultado) e um `agent_step` por
  passo do laço. Tudo aparece no replay.
- O span `invoke_agent` também traz a **soma** dos tokens da execução. O Revoada só soma
  custo nas chamadas de modelo, então essa soma não conta duas vezes.

A integração antiga, `LegacyOpenTelemetry` (atributos `ai.*`, como nas versões 4 a 6 do
SDK), também é entendida (dialeto `vercel-ai`): `ai.generateText` vira o agente
(`ai.telemetry.functionId`), `ai.*.doGenerate`/`doStream` a chamada ao modelo e
`ai.toolCall` a ferramenta. As chaves de conteúdo dela (`ai.prompt*`,
`ai.response.text`, `ai.response.toolCalls`, `ai.toolCall.args`, `ai.toolCall.result`)
saem dos rótulos como as das outras convenções. Para o texto nem sair da aplicação, use
`telemetry: { recordInputs: false, recordOutputs: false }`.

### 3.5 Go — spans manuais

Não há instrumentação automática madura para LLM em Go. Crie os spans com
`go.opentelemetry.io/otel` seguindo a convenção — é o formato que o Revoada lê melhor:

```go
tr := otel.Tracer("meu-agente")

ctx, ag := tr.Start(ctx, "invoke_agent Meteorologista")
ag.SetAttributes(
    attribute.String("gen_ai.operation.name", "invoke_agent"),
    attribute.String("gen_ai.agent.name", "Meteorologista"),
    attribute.String("gen_ai.conversation.id", conversaID),
)
defer ag.End()

ctx, ch := tr.Start(ctx, "chat gpt-4o-mini", trace.WithSpanKind(trace.SpanKindClient))
ch.SetAttributes(
    attribute.String("gen_ai.operation.name", "chat"),
    attribute.String("gen_ai.provider.name", "openai"),
    attribute.String("gen_ai.request.model", "gpt-4o-mini"),
)
resp, err := cliente.Chat(ctx, pedido)
if err != nil {
    ch.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", err)))
    ch.SetStatus(codes.Error, err.Error())
} else {
    ch.SetAttributes(
        attribute.String("gen_ai.response.model", resp.Model),
        attribute.Int64("gen_ai.usage.input_tokens", resp.Usage.PromptTokens),   // já inclui o cache
        attribute.Int64("gen_ai.usage.output_tokens", resp.Usage.CompletionTokens),
        attribute.Int64("gen_ai.usage.cache_read.input_tokens", resp.Usage.CachedTokens),
        attribute.StringSlice("gen_ai.response.finish_reasons", []string{resp.FinishReason}),
    )
}
ch.End()
```

O exporter é o de [instrumentacao-traces.md §3.5](instrumentacao-traces.md#35-go)
(`otlptracehttp` com `WithHeaders` para a `X-Revoada-Key`). Se a aplicação já sabe o
custo da chamada (um proxy como LiteLLM ou OpenRouter devolve), mande em
`gen_ai.usage.cost` (dólar): ele vence a estimativa.

---

## 4. Conteúdo (prompt e resposta) e privacidade

São duas chaves, uma em cada ponta:

| Onde | Chave | O que decide |
|---|---|---|
| Aplicação | `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT` (oficial), `TRACELOOP_TRACE_CONTENT` (OpenLLMetry), ocultação do OpenInference, `recordInputs/Outputs` (AI SDK) | se o conteúdo **sai** da aplicação |
| Gateway | `REVOADA_GENAI_CONTEUDO` | se o conteúdo que chegou é **gravado** |

`REVOADA_GENAI_CONTEUDO` (no `.env`, repassado ao serviço `gateway`):

| Valor | Efeito |
|---|---|
| `desligado` (padrão) | O conteúdo é descartado na chegada. Custo, tokens, latência, erro e passo a passo continuam completos. |
| `redigido` | Grava o conteúdo com as mesmas regras de redação dos logs: chaves de API, tokens, JWT, senhas em URL e em `chave=valor`, números de cartão viram `«redigido»`. |
| `completo` | Grava como chegou. |

Valor desconhecido (erro de digitação) cai em `desligado`. Em qualquer modo, as chaves
de conteúdo **saem dos rótulos do span** — o prompt não sobra na tabela de traces.

Quando gravado, o conteúdo:

- fica numa tabela própria (`genai_conteudo`) por **7 dias**;
- é cortado em 32 KiB por mensagem e 256 KiB por span (o corte fica marcado na tela;
  o span nunca é recusado por causa do conteúdo);
- só é lido por quem tem a permissão **ver conteúdo de IA** (admin e operador, com 2FA
  e confirmação de identidade recente, como ver credencial; o leitor vê o replay sem
  o texto), e **cada leitura entra na auditoria**;
- pode ser apagado antes do prazo por conversa, por execução ou por inteiro, na aba
  **Modelos e preços → Dados e privacidade** (admin) ou por `POST /api/ia/purge`
  (ver [api-ia.md](api-ia.md)).

**LGPD.** Prompt é dado pessoal quase sempre: nome, CPF, endereço, o problema que a
pessoa descreveu ao seu chatbot. Antes de ligar o conteúdo:

- `redigido` **não é anonimização**. Ele pega segredos e cartões; nome, CPF, e-mail,
  telefone e texto livre passam como estão.
- Tenha base legal e finalidade (depurar o agente é uma; "guardar por via das dúvidas"
  não é), e diga isso na política de privacidade do produto que usa o agente.
- Pedido de titular: apague pela conversa (`alvo: "conversa"`) — por isso vale mandar
  `gen_ai.conversation.id`.
- Com o gateway em `desligado`, o conteúdo **ainda viaja** da aplicação até o gateway
  (e a `8090` é HTTP puro). Se ele não deve sair da aplicação, desligue na aplicação
  (`NO_CONTENT`, `TRACELOOP_TRACE_CONTENT=false`…).

---

## 5. Custo e preços

O custo é **estimado** e a tela sempre diz isso: "estimado com preço de DATA".

- **Conta:** tokens × preço vigente **no momento da chamada**, em dólar por 1 milhão de
  tokens, com preço próprio para cache de leitura e de escrita quando cadastrado (sem
  ele, o cache é cobrado como entrada — a estimativa erra para cima, nunca para baixo).
- **Tabela de preços:** **Agentes de IA → Modelos e preços**. Cada linha tem provedor,
  modelo, preços e `vigente desde`. Trocar um preço é criar uma linha nova com data
  posterior: o passado continua calculado com o preço antigo.
- **Modelo com prefixo:** `gpt-4o-mini*` cobre `gpt-4o-mini-2024-07-18` (os provedores
  devolvem o nome com a data da versão). Vence a linha mais específica, e a do mesmo
  provedor antes de outra; um provedor revendedor (`azure.ai.openai`, `openai.chat`)
  usa a linha de `openai` quando não tem uma própria.
- **Tabela de referência:** o Revoada vem com uma tabela de preços públicos **datada**
  (a atual é de 2025-10), importada uma vez. Ela envelhece: a tela avisa quando passa
  de 90 dias. Confira os preços do seu contrato — desconto de volume, lote e região
  não entram.
- **Sem preço não é zero:** modelo sem linha na tabela aparece como **sem preço**, e o
  total vira "parcial". O mesmo para chamada sem tokens ("não informado").
- **Custo informado vence:** se a biblioteca ou um proxy manda o custo pronto
  (`gen_ai.usage.cost`, `llm.cost.total`, `gen_ai.cost`), ele é usado no lugar da
  estimativa.

---

## 6. Alertas

O painel grava, a cada minuto, séries comuns em `metrics` (rótulos `service`, `agente`,
`modelo`, `provedor`) — as regras de alerta normais funcionam sobre elas:

| Série | O que mede |
|---|---|
| `llm.custo_usd` | custo do minuto (informado + estimado) |
| `llm.chamadas` · `llm.erros` | chamadas de modelo e as que falharam |
| `llm.tokens.entrada` · `llm.tokens.saida` | tokens do minuto |
| `llm.latencia_p95_ms` | p95 das chamadas do minuto |
| `llm.execucao.passos_max` | maior nº de chamadas numa execução que terminou no minuto |
| `llm.sem_preco` | chamadas de modelo sem preço cadastrado |

Exemplos (campos do formulário de regra):

| Quero saber quando… | Métrica | Agregação | Janela | Condição |
|---|---|---|---|---|
| o gasto passa de US$ 5 por hora | `llm.custo_usd` | `sum` | 3600 s | `> 5` |
| o provedor começa a falhar | `llm.erros` | `sum` | 300 s | `> 10` |
| um agente entra em loop | `llm.execucao.passos_max` | `max` | 300 s | `> 15` |
| o modelo fica lento | `llm.latencia_p95_ms` | `max` | 600 s | `> 8000` |
| apareceu modelo sem preço | `llm.sem_preco` | `sum` | 3600 s | `> 0` |

Dois limites para saber:

- As séries são gravadas com **5 minutos de atraso** (para esperar spans de agentes
  longos). Um alerta de custo dispara uns 5 minutos depois do gasto, não na hora.
- Como em qualquer regra do Revoada, **cada combinação de rótulos é uma série
  avaliada sozinha**: `llm.custo_usd > 5` vale por `service`/`agente`/`modelo`, não
  pelo total do tenant. Use os filtros da regra para mirar um agente ou modelo.

---

## 7. MCP

O servidor MCP do painel expõe `ia_resumo` (custo, chamadas, erros, por modelo e por
agente), `listar_execucoes` e `ver_execucao` (o replay). O conteúdo das mensagens só
vem em `ver_execucao` para tokens com o escopo **`ia_conteudo`** — e entra na
auditoria como qualquer leitura de conteúdo.

---

## 8. Problemas comuns

| Sintoma | Causa provável | O que fazer |
|---|---|---|
| Tokens "não informado" em chamadas com streaming | A OpenAI só informa uso em stream com `stream_options={"include_usage": True}` | Passe `stream_options` na chamada; sem tokens, o custo fica "parcial" |
| Replay sem o texto das mensagens | Conteúdo desligado no gateway (padrão) **ou** na aplicação | Gateway: `REVOADA_GENAI_CONTEUDO=redigido`. Aplicação oficial: `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=SPAN_ONLY` (`true` é recusado e vira `NO_CONTENT`; `EVENT_ONLY` manda como log, fora do trace). E a permissão **ver conteúdo de IA** |
| O exporter recebe `401` | Header ausente ou chave desconhecida | `OTEL_EXPORTER_OTLP_HEADERS=X-Revoada-Key=<chave>` (sem aspas, sem espaço). `403` = chave revogada |
| Spans aparecem em Traces, mas não em Agentes de IA | O span não tem atributo de IA reconhecível (nomes de uma convenção que o Revoada não lê). No Vercel AI SDK 7, faltou `registerTelemetry(new OpenTelemetry())` | Abra o span em Traces e compare com a §9. Falta `gen_ai.operation.name`/`gen_ai.request.model`? Instrumentação antiga ou não configurada |
| Chamadas aparecem, mas sem agente nem ferramenta | A instrumentação oficial e o AI SDK não criam esses spans | Crie `invoke_agent` e `execute_tool` à mão (§3.1) |
| `ImportError`/`ModuleNotFoundError` vindo de `opentelemetry.util.genai` | `opentelemetry-util-genai` 1.x com a instrumentação 2.4b0 | `pip install opentelemetry-util-genai==0.4b0` |
| Instrumentação oficial quebra ou some depois de atualizar o `openai` | `openai` 3.x (trocou para `httpx2`) | `pip install "openai>=2.8,<3"` |
| Custo "sem preço" | Modelo fora da tabela | Cadastre em **Modelos e preços** (aceita prefixo com `*`) |
| Custo maior que a fatura | Instrumentação que não informa cache (a oficial 2.4b0), ou preço de tabela acima do seu contrato | Confira os tokens de cache no replay e os preços cadastrados |
| Serviço `unknown_service` | `service.name` não definido | `OTEL_SERVICE_NAME=meu-agente` |
| Últimas chamadas não chegam quando o processo termina | O lote em memória não foi enviado | Chame `shutdown()` do `TracerProvider` (Python) ou do `NodeSDK` antes de sair |

---

## 9. Atributos reconhecidos

Fonte da verdade: [`core/genai/dialetos.go`](../core/genai/dialetos.go),
[`genai.go`](../core/genai/genai.go) e [`conteudo.go`](../core/genai/conteudo.go). Os
atributos do recurso e do span são fundidos (os do span vencem); listas chegam como
JSON. Em cada linha, as chaves são lidas **na ordem**, e vale a primeira não vazia.

### Qual dialeto

| Dialeto | Quando |
|---|---|
| `openinference` | tem `openinference.span.kind` |
| `otel-genai-legado` | tem um sinal de IA (abaixo), não tem `gen_ai.provider.name` e tem `gen_ai.system`, `gen_ai.usage.prompt_tokens`, `traceloop.span.kind`, `llm.request.type` ou chaves `gen_ai.prompt.*` / `gen_ai.completion.*` |
| `otel-genai` | tem um sinal de IA e não é legado |
| `vercel-ai` | tem `ai.operationId` (integração antiga do Vercel AI SDK) |

Sinais de IA: `gen_ai.operation.name`, `gen_ai.provider.name`, `gen_ai.system`,
`gen_ai.request.model`, `gen_ai.response.model`, `gen_ai.agent.name`, `gen_ai.tool.name`,
`gen_ai.usage.input_tokens`, `gen_ai.usage.prompt_tokens`, `llm.request.type`,
`traceloop.span.kind`.

### Campos

| Campo | Chaves lidas |
|---|---|
| Operação | `gen_ai.operation.name` · legado: `llm.request.type` (`chat`, `completion`→`text_completion`, `embedding`→`embeddings`, `rerank`), `traceloop.span.kind` (`agent`/`workflow`→`invoke_agent`, `tool`→`execute_tool`, `task`→`chain`) · OpenInference: `openinference.span.kind` (`LLM`→`chat`, `EMBEDDING`, `AGENT`, `TOOL`, `CHAIN`, `RETRIEVER`, `RERANKER`, `GUARDRAIL`) · Vercel: `ai.operationId` (`ai.generateText`/`streamText`/`generateObject`/`streamObject`→`invoke_agent`, `*.doGenerate`/`*.doStream`→`chat`, `ai.embed*`→`embeddings`, `ai.toolCall`→`execute_tool`). Sem nenhuma: `gen_ai.tool.name` → ferramenta; `gen_ai.agent.name` sem modelo → agente; modelo → `chat` |
| Provedor | `gen_ai.provider.name`, `gen_ai.system`, `llm.provider`, `llm.system`, `ai.model.provider` (no Vercel, só a parte antes do ponto: `openai.chat` → `openai`) |
| Modelo | `gen_ai.response.model`, `gen_ai.request.model`, `llm.model_name`, `llm.response.model`, `llm.request.model`, `ai.response.model`, `ai.model.id` |
| Agente | `gen_ai.agent.name`, `agent.name`; num span de agente, `traceloop.entity.name`, (OpenInference) o nome do span ou (Vercel) `ai.telemetry.functionId` |
| Id do agente | `gen_ai.agent.id`, `agent.id` |
| Conversa | `gen_ai.conversation.id`, `session.id`, `traceloop.association.properties.conversation_id`, `traceloop.association.properties.session_id` |
| Ferramenta | `gen_ai.tool.name`, `tool.name`, `ai.toolCall.name`; num span de ferramenta, `traceloop.entity.name` ou (OpenInference) o nome do span |
| Id da chamada de ferramenta | `gen_ai.tool.call.id`, `tool_call.id`, `tool.call.id`, `ai.toolCall.id` |
| Tokens de entrada | `gen_ai.usage.input_tokens`, `gen_ai.usage.prompt_tokens`, `llm.usage.prompt_tokens`, `llm.token_count.prompt`, `ai.usage.inputTokens`, `ai.usage.promptTokens` |
| Tokens de saída | `gen_ai.usage.output_tokens`, `gen_ai.usage.completion_tokens`, `llm.usage.completion_tokens`, `llm.token_count.completion`, `ai.usage.outputTokens`, `ai.usage.completionTokens` |
| Cache lido | `gen_ai.usage.cache_read.input_tokens`, `gen_ai.usage.cache_read_input_tokens`, `llm.token_count.prompt_details.cache_read`, `ai.usage.inputTokenDetails.cacheReadTokens`, `ai.usage.cachedInputTokens` |
| Cache gravado | `gen_ai.usage.cache_creation.input_tokens`, `gen_ai.usage.cache_creation_input_tokens`, `llm.token_count.prompt_details.cache_write` |
| Custo informado (US$) | `gen_ai.usage.cost`, `llm.cost.total`, `gen_ai.cost` |
| Erro | `error.type`; sem ele e com status ERROR, `exception.type`; sem nenhum, `erro` |
| Motivos de término | `gen_ai.response.finish_reasons` (lista); `llm.finish_reason`, `gen_ai.completion.0.finish_reason`, `ai.response.finishReason` |

Regras de leitura: token negativo, fracionário ou em texto é "não informado" (nunca um
número inventado). A entrada **sempre inclui o cache**: quando o cache informado é maior
que a entrada, a biblioteca o mandou separado, e ele é somado.

### Conteúdo

Extraído para o replay (se o modo de conteúdo gravar) e **sempre retirado dos rótulos**:

| Dialeto | Chaves |
|---|---|
| `otel-genai` | `gen_ai.system_instructions`, `gen_ai.input.messages`, `gen_ai.output.messages` (listas JSON de `{role, parts}`), `gen_ai.tool.call.arguments`, `gen_ai.tool.call.result`; nas versões que usavam eventos, os span events com `gen_ai.input.messages`/`gen_ai.output.messages` e `gen_ai.system.message`, `gen_ai.user.message`, `gen_ai.assistant.message`, `gen_ai.tool.message`, `gen_ai.choice` |
| `otel-genai-legado` | `gen_ai.prompt.N.{role,content}`, `gen_ai.completion.N.{role,content}`, `llm.prompts.N.*`, `llm.completions.N.*`, `traceloop.entity.input`, `traceloop.entity.output` |
| `openinference` | `llm.input_messages.N.message.{role,content}` (e `contents.K.message_content.text`, `tool_calls.K.tool_call.function.{name,arguments}`), `llm.output_messages.N.message.*`, `input.value`, `output.value` |
| `vercel-ai` | `ai.prompt.messages`, `ai.prompt` (`{messages}` / `{prompt}` / `{system}`), `ai.response.text`, `ai.response.toolCalls`, `ai.toolCall.args`, `ai.toolCall.result` |

Retiradas dos rótulos de **todo** span (reconhecido como IA ou não), mas não gravadas:
os documentos de RAG do OpenInference (`retrieval.documents.*`, `reranker.input_documents.*`,
`reranker.output_documents.*`, `reranker.query`, `embedding.embeddings.*`),
`gen_ai.tool.definitions`, `gen_ai.prompt`,
`gen_ai.completion`, `tool.parameters`, `llm.tools.*`, `llm.prompt_template.*`,
`ai.prompt.tools`, `ai.response.object`, `ai.response.reasoning`, `ai.value(s)`,
`ai.embedding(s)`.

---

## Fontes

- Convenção GenAI do OpenTelemetry: https://opentelemetry.io/docs/specs/semconv/gen-ai/
- Instrumentação oficial (Python): https://github.com/open-telemetry/opentelemetry-python-contrib/tree/main/instrumentation-genai
- OpenLLMetry: https://github.com/traceloop/openllmetry
- OpenInference: https://github.com/Arize-ai/openinference
- Vercel AI SDK, telemetria: https://ai-sdk.dev/docs/ai-sdk-core/telemetry
- Fixtures gravadas e versões: [`tests/genai-fixtures`](../tests/genai-fixtures/README.md)

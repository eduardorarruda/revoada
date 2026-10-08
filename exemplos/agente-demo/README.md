# Agente de demonstração: Meteorologista

Um agente de IA pequeno, em Python, que manda traces para o Revoada e enche a tela
**Investigar → Agentes de IA** com algo para ver — sem chave de API e sem gastar nada.

Cada rodada é uma pergunta de clima: o modelo pede a ferramenta `clima`, a ferramenta
roda, o modelo responde. Algumas rodadas saem do caminho feliz de propósito:

| Rodada | Cidade | O que acontece | O que aparece no painel |
|---|---|---|---|
| 1, 3, 5, 6, 8 | Recife, Porto Alegre… | modelo → ferramenta → resposta | execução normal, 2 chamadas de modelo + 1 de ferramenta |
| 2 | Atlântida | a estação sempre pede "tente novamente" e o modelo repete a ferramenta até o agente desistir (limite de 6 passos) | **loop**: a mesma ferramenta 7 vezes seguidas no replay, execução com erro `LoopDeFerramentas` |
| 4 | Manaus | a ferramenta estoura o tempo | span de ferramenta com erro `TimeoutError`; o modelo responde que não conseguiu |
| 7 | Belém | a chamada usa um modelo que não existe | chamada de modelo com erro (`InternalServerError` no LLM falso, `NotFoundError` num provedor real) |

Com mais de 8 rodadas, o ciclo se repete.

Por padrão, as chamadas vão para um **LLM falso** local (`llm_falso.py`), que imita a
API da OpenAI: pede ferramenta, devolve tokens (com uma parte "em cache") e demora de
0,2 a 0,9 s. O custo aparece estimado pelo preço do `gpt-4o-mini`.

A instrumentação é a oficial do OpenTelemetry (`opentelemetry-instrumentation-openai-v2`)
para as chamadas de modelo, mais spans **manuais** de agente (`invoke_agent`) e de
ferramenta (`execute_tool`) — a oficial não cria esses dois. É o mesmo desenho
recomendado em [docs/instrumentacao-ia.md](../../docs/instrumentacao-ia.md); leia
`agente.py` como exemplo de código para a sua aplicação.

## Rodar

Você precisa de um Revoada no ar e de uma **chave de ingestão** (no painel,
**Infraestrutura → Adicionar servidor**).

**Com Docker** (não depende do Python da máquina):

```bash
cd exemplos/agente-demo
docker run --rm -it --network host -v "$PWD":/demo:ro -e VENV=/tmp/venv \
  -e REVOADA_GATEWAY=http://localhost:8090 -e REVOADA_KEY=<SUA_CHAVE> \
  python:3.12-slim bash /demo/run.sh
```

`--network host` deixa o container alcançar o gateway em `localhost` (Linux). No macOS
e no Windows, troque por `-e REVOADA_GATEWAY=http://host.docker.internal:8090`.

**Com Python na máquina** (verificado com 3.12):

```bash
cd exemplos/agente-demo
REVOADA_KEY=<SUA_CHAVE> ./run.sh
```

O `run.sh` cria um venv em `.venv` na primeira vez e instala as versões de
`requirements.txt`. (No Python 3.14 algumas dependências ainda não tinham wheel quando
este exemplo foi escrito; na dúvida, use o Docker.)

A saída esperada termina assim:

```
[8/8] Natal         normal              trace 2a6144fddc5f206137ce9b1cf76b2f33
      → Agora faz 34 °C em Natal, céu limpo.

Spans aceitos pelo gateway: 41
Abra Investigar → Agentes de IA → Execuções no painel.
```

São 41 spans em 8 execuções: 8 de agente, 20 chamadas de modelo e 13 de ferramenta.
Se o gateway recusar, o resumo diz quantos lotes falharam e o programa sai com código 1
(401/403 = chave errada ou revogada).

## Variáveis

| Variável | Padrão | Para quê |
|---|---|---|
| `REVOADA_KEY` | — (obrigatória) | Chave de ingestão, vai no header `X-Revoada-Key` |
| `REVOADA_GATEWAY` | `http://localhost:8090` | Endereço do gateway. Atrás do proxy HTTPS de produção, o do painel (`https://painel.exemplo.com`) |
| `RODADAS` | `8` | Quantas perguntas fazer |
| `INTERVALO` | `1` | Segundos entre uma rodada e outra |
| `OTEL_SERVICE_NAME` | `agente-demo` | Vira a coluna **service** |
| `HOST_NAME` | hostname da máquina | Vai em `host.name` (o servidor ao qual a aplicação é ligada) |
| `MODELO` | `gpt-4o-mini` | Modelo pedido nas chamadas |
| `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT` | `SPAN_ONLY` | `NO_CONTENT` para não mandar prompt/resposta |
| `OPENAI_API_KEY` + `OPENAI_BASE_URL` | — | As **duas** definidas: usa o provedor de verdade (gasta tokens) |
| `VENV` | `.venv` | Onde o `run.sh` cria o venv |

O provedor real exige as duas variáveis de propósito: uma `OPENAI_API_KEY` esquecida no
shell não deveria transformar uma demonstração em fatura. Para a OpenAI,
`OPENAI_BASE_URL=https://api.openai.com/v1`; serve qualquer API compatível (Azure
OpenAI, OpenRouter, vLLM, Ollama…). Com um modelo de verdade, a rodada de "loop" depende
do modelo: alguns repetem a ferramenta, outros desistem antes.

## Ver o conteúdo das mensagens no replay

O agente manda prompt e resposta no próprio span (`SPAN_ONLY`), mas o gateway os
**descarta** por padrão. Para o replay mostrar o texto, ligue no `.env` do Revoada e
reinicie o gateway:

```bash
REVOADA_GENAI_CONTEUDO=redigido
```

E é preciso a permissão **ver conteúdo de IA** (admin ou operador). Antes de ligar isso
com dados reais, leia a seção de privacidade em
[docs/instrumentacao-ia.md](../../docs/instrumentacao-ia.md#4-conteúdo-prompt-e-resposta-e-privacidade).

## Arquivos

| Arquivo | O que é |
|---|---|
| `agente.py` | O agente: exportador OTLP, spans de agente e ferramenta, laço com limite de passos |
| `llm_falso.py` | LLM falso compatível com `/v1/chat/completions` (também roda sozinho: `python llm_falso.py`) |
| `requirements.txt` | Versões verificadas — `openai` 2.x e `opentelemetry-util-genai==0.4b0` são obrigatórios |
| `run.sh` | Cria o venv e roda o agente |

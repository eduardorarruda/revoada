# Instrumentação de traces — visão geral do endpoint OTLP (Revoada)

Como enviar **traces** de qualquer aplicação para a tela **Traces** do painel. Este documento
cobre o endpoint OTLP e traz **receitas curtas** por linguagem. Para PHP em detalhe, veja o
guia dedicado [`instrumentacao-php.md`](instrumentacao-php.md).

---

## 1. O endpoint OTLP

| Item | Valor |
|------|-------|
| **Endpoint base** | `http://<seu-gateway>:8090` (o `REVOADA_GATEWAY_PUBLIC_URL`) ou, atrás de proxy HTTPS, `https://<seu-painel>` |
| **Rota** | `POST /v1/traces` (os SDKs OTLP anexam `/v1/traces` ao endpoint base) |
| **Header de auth** | `X-Revoada-Key: <SEU_SERVERKEY>` (mesma chave das métricas; tabela `agents`) |
| **Sucesso** | HTTP `200` + corpo `ExportTraceServiceResponse` protobuf |
| **Sobrecarga** | HTTP `429` (retente) |

### Como o tráfego chega no gateway

O gateway atende OTLP/HTTP (`/v1/metrics`, `/v1/logs`, `/v1/traces`) no mesmo listener HTTP,
porta **8090**, e OTLP/gRPC na **4317**. Há dois jeitos comuns de expor isso:

- **`docker-compose.yml` da raiz:** publica `8090` e `4317` direto no host
  (`REVOADA_PORTA_GATEWAY`, `REVOADA_PORTA_OTLP_GRPC`). Endpoint: `http://<seu-gateway>:8090`.
- **Atrás de proxy reverso (`deploy/prod/`):** o nginx recebe em HTTPS e faz proxy de `/v1/`
  para `gateway:8090` (porta interna, não exposta). Endpoint: `https://<seu-painel>`.
  Fonte: `deploy/prod/nginx.conf` (`location /v1/ → proxy_pass …:8090`).

### Formatos e transportes aceitos

- **OTLP/HTTP protobuf** — `Content-Type: application/x-protobuf`. **Recomendado.**
- **OTLP/JSON** — `Content-Type: application/json` (aceita `; charset=utf-8`). IDs
  `traceId`/`spanId` são lidos em **hex** corretamente (decoder OTLP oficial).
- **gzip** — `Content-Encoding: gzip` é aceito e descomprimido (limite de 16 MiB após
  descompressão). Padrão recomendado mesmo assim: **protobuf sem gzip**.
- **gRPC OTLP (`:4317`)** — o gateway aceita; use se a `4317` estiver publicada para você
  (atrás do proxy HTTP de `deploy/prod/`, ela não fica exposta).

### Atributos que a UI usa

| Atributo (resource) | Efeito na UI |
|---------------------|--------------|
| `service.name` | Coluna **Serviço** na lista e nós do service map |
| `host.name` | Filtro **Servidor** (o gateway espelha `host.name` → label `host`, que é o campo do filtro) |
| Span event `exception` | `exception.type` / `.message` / `.stacktrace` no detalhe do span |

> **Amostragem no painel:** o gateway retém 100% dos traces com **erro** OU **duração ≥ 1s**;
> os demais a uma fração (default 20%, env `REVOADA_TRACE_SAMPLE` / `REVOADA_TRACE_SAMPLE_SLOW_MS` no
> gateway). Ao testar, gere um trace com erro ou > 1s.

---

## 2. Variáveis de ambiente comuns a TODAS as linguagens

Os SDKs/agents OpenTelemetry das linguagens abaixo usam **as mesmas variáveis**:

```bash
OTEL_SERVICE_NAME=meu-sistema
OTEL_TRACES_EXPORTER=otlp
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_EXPORTER_OTLP_ENDPOINT=http://<seu-gateway>:8090
OTEL_EXPORTER_OTLP_HEADERS=X-Revoada-Key=<SEU_SERVERKEY>
OTEL_RESOURCE_ATTRIBUTES=host.name=meu-servidor-01
OTEL_PROPAGATORS=baggage,tracecontext
```

Troque `meu-sistema`, `meu-servidor-01` e `<SEU_SERVERKEY>`. O SDK anexa `/v1/traces` ao
endpoint base sozinho.

---

## 3. Receitas por linguagem

Todas usam as env vars do §2 (endpoint, `X-Revoada-Key`, `OTEL_SERVICE_NAME`, `host.name`).

| Stack | Como instrumentar | Detalhe |
|-------|-------------------|---------|
| **PHP** | Zero-code (ext C + pacotes `auto-*`) ou SDK manual | Guia completo: [`instrumentacao-php.md`](instrumentacao-php.md) |
| **Java / Spring** | OTel **Java Agent** (zero-code) | `-javaagent:opentelemetry-javaagent.jar` — ver §3.1 |
| **Node.js** | `@opentelemetry/auto-instrumentations-node` via `--require` | ver §3.2 |
| **Python** | `opentelemetry-bootstrap` + `opentelemetry-instrument <cmd>` | ver §3.3 |
| **.NET** | OpenTelemetry .NET auto-instrumentation | ver §3.4 |
| **Go** | SDK manual (sem zero-code maduro) | ver §3.5 |

### 3.1 Java / Spring Boot (ex.: um ERP em Java 17 / Spring Boot)

Zero-code com o **Java Agent** — não muda o código, só o start-up. Baixe
`opentelemetry-javaagent.jar` (releases de `open-telemetry/opentelemetry-java-instrumentation`)
e injete no processo.

```dockerfile
# Dockerfile
ADD https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/latest/download/opentelemetry-javaagent.jar /otel/opentelemetry-javaagent.jar

ENV OTEL_SERVICE_NAME=meu-erp \
    OTEL_TRACES_EXPORTER=otlp \
    OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf \
    OTEL_EXPORTER_OTLP_ENDPOINT=http://<seu-gateway>:8090 \
    OTEL_EXPORTER_OTLP_HEADERS=X-Revoada-Key=<SEU_SERVERKEY> \
    OTEL_RESOURCE_ATTRIBUTES=host.name=meu-servidor-01

ENTRYPOINT ["java", "-javaagent:/otel/opentelemetry-javaagent.jar", "-jar", "/app/app.jar"]
```

Ou, sem mexer no ENTRYPOINT, via `JAVA_TOOL_OPTIONS=-javaagent:/otel/opentelemetry-javaagent.jar`.

### 3.2 Node.js

```bash
npm install @opentelemetry/auto-instrumentations-node
```

```bash
export OTEL_SERVICE_NAME=meu-app-node
export OTEL_TRACES_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_EXPORTER_OTLP_ENDPOINT=http://<seu-gateway>:8090
export OTEL_EXPORTER_OTLP_HEADERS=X-Revoada-Key=<SEU_SERVERKEY>
export OTEL_RESOURCE_ATTRIBUTES=host.name=meu-servidor-01

node --require @opentelemetry/auto-instrumentations-node/register app.js
```

### 3.3 Python

```bash
pip install opentelemetry-distro opentelemetry-exporter-otlp
opentelemetry-bootstrap -a install     # instala instrumentações das libs detectadas
```

```bash
export OTEL_SERVICE_NAME=meu-app-python
export OTEL_TRACES_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_EXPORTER_OTLP_ENDPOINT=http://<seu-gateway>:8090
export OTEL_EXPORTER_OTLP_HEADERS=X-Revoada-Key=<SEU_SERVERKEY>
export OTEL_RESOURCE_ATTRIBUTES=host.name=meu-servidor-01

opentelemetry-instrument python app.py      # ex.: opentelemetry-instrument gunicorn ...
```

### 3.4 .NET

Instale a **OpenTelemetry .NET auto-instrumentation** (script/módulo oficial) e defina as env
vars; ela injeta o CLR profiler no processo.

```bash
# instala a auto-instrumentation (exemplo Linux; há script equivalente p/ Windows)
curl -sSfL https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation/releases/latest/download/otel-dotnet-auto-install.sh -O
sh ./otel-dotnet-auto-install.sh
. ./instrument.sh                      # exporta as vars do profiler no shell atual

export OTEL_SERVICE_NAME=meu-app-dotnet
export OTEL_TRACES_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_EXPORTER_OTLP_ENDPOINT=http://<seu-gateway>:8090
export OTEL_EXPORTER_OTLP_HEADERS=X-Revoada-Key=<SEU_SERVERKEY>
export OTEL_RESOURCE_ATTRIBUTES=host.name=meu-servidor-01

dotnet MinhaApp.dll
```

### 3.5 Go

Go **não tem zero-code maduro** — instrumente com o SDK manual (`go.opentelemetry.io/otel` +
`otlptracehttp`), configurando o exporter com o endpoint e o header `X-Revoada-Key`, ou apenas
lendo as env vars do §2. Ponto de partida:
https://opentelemetry.io/docs/languages/go/getting-started/

Trecho do exporter (o header custom vai em `WithHeaders`):

```go
exp, err := otlptracehttp.New(ctx,
    otlptracehttp.WithEndpoint("<seu-gateway>:8090"),
    otlptracehttp.WithInsecure(), // HTTP sem TLS (ver §4)
    otlptracehttp.WithHeaders(map[string]string{"X-Revoada-Key": "<SEU_SERVERKEY>"}),
)
```

---

## 4. Segurança

- A **8090 do gateway é HTTP puro (sem TLS)**: exposta direto, a `X-Revoada-Key` e os dados
  trafegam em texto claro. Em produção, ponha um proxy reverso com HTTPS na frente
  (`deploy/prod/`) ou, no mínimo, restrinja a origem/rede.
- **Nunca** comite a `X-Revoada-Key` — injete por env/secret. Use `<SEU_SERVERKEY>` como
  placeholder em qualquer exemplo versionado.

---

## Fontes

- Especificação do protocolo OTLP: https://opentelemetry.io/docs/specs/otel/protocol/
- Variáveis de ambiente do exporter: https://opentelemetry.io/docs/specs/otel/protocol/exporter/
- Java auto-instrumentation: https://opentelemetry.io/docs/zero-code/java/agent/
- Node.js: https://opentelemetry.io/docs/languages/js/getting-started/nodejs/
- Python: https://opentelemetry.io/docs/zero-code/python/
- .NET: https://opentelemetry.io/docs/zero-code/dotnet/
- Go: https://opentelemetry.io/docs/languages/go/getting-started/
- PHP: https://opentelemetry.io/docs/languages/php/

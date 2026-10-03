# Instrumentação de traces em PHP — Revoada

**Público-alvo:** desenvolvedores PHP que querem ver seus apps na tela **Traces** do painel.

Este é o guia "instale assim e funciona". A prioridade é PHP; para outras linguagens veja
[`instrumentacao-traces.md`](instrumentacao-traces.md).

---

## 1. TL;DR

Envie **traces OpenTelemetry** para o painel via **OTLP/HTTP protobuf**:

| Item | Valor |
|------|-------|
| **Endpoint base** | `http://<seu-gateway>:8090` — o endereço público do gateway (`REVOADA_GATEWAY_PUBLIC_URL`); atrás de um proxy reverso com HTTPS, `https://<seu-painel>`. O SDK acrescenta `/v1/traces` sozinho |
| **Protocolo** | `http/protobuf` (recomendado). JSON e gzip também são aceitos — ver §10 |
| **Header de auth** | `X-Revoada-Key: <SEU_SERVERKEY>` (a mesma chave usada pelo agente de métricas; vem da tabela `agents`) |
| **`service.name`** | nome do sistema — vira a coluna **Serviço** na lista de traces |
| **`host.name`** | hostname do servidor — alimenta o **filtro "Servidor"** da UI (ver §8) |

> A resposta de sucesso é HTTP **200** com um corpo protobuf vazio (contrato OTLP). Sob
> sobrecarga o gateway devolve **429** (retente). Chave inválida → 401/403.

Bloco mínimo de variáveis de ambiente (o resto do guia detalha cada cenário):

```bash
OTEL_PHP_AUTOLOAD_ENABLED=true
OTEL_SERVICE_NAME=meu-sistema
OTEL_TRACES_EXPORTER=otlp
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_EXPORTER_OTLP_ENDPOINT=http://<seu-gateway>:8090
OTEL_EXPORTER_OTLP_HEADERS=X-Revoada-Key=<SEU_SERVERKEY>
OTEL_RESOURCE_ATTRIBUTES=host.name=meu-servidor-01
```

---

## 2. Requisitos

| Caminho | PHP mínimo | Observação |
|---------|-----------|------------|
| **Zero-code (auto-instrumentação)** | **PHP ≥ 8.1** | Exige a extensão C `opentelemetry` (1.3.x exige `^8.1`) |
| **Manual (SDK, sem extensão C)** | **PHP ≥ 8.1** | SDK `open-telemetry/sdk` 1.15 exige `^8.1` |
| **Manual em versões antigas** | PHP 7.4 | Só fixando versões antigas dos pacotes no Composer (ver §7) |
| **Legado extremo** | PHP 5.x / 7.0–7.3 | Sem SDK: OTLP/JSON via curl puro (ver §7) — é fallback |

O caminho **oficial e recomendado é PHP ≥ 8.1**. Tudo abaixo assume 8.1+ salvo a seção de legado.

---

## 3. Cenário A — Framework com zero-code (Laravel, Symfony, Slim, CodeIgniter, …)

Cobertura **estável** de auto-instrumentação (jul/2026): Laravel, Symfony, Slim, CodeIgniter,
CakePHP, Yii, Magento2, PDO, MySqli, PostgreSql, Curl, Guzzle, Psr15/18, MongoDB, Doctrine.

### 3.1 Instalar a extensão C `opentelemetry`

Escolha **uma** forma:

```bash
# Docker / imagens oficiais PHP (recomendado):
install-php-extensions opentelemetry

# ou via PECL:
pecl install opentelemetry
# e habilite no php.ini:
#   extension=opentelemetry.so

# Windows: baixe a DLL pré-compilada em
#   github.com/open-telemetry/opentelemetry-php-instrumentation (releases)
# e adicione  extension=php_opentelemetry.dll  no php.ini
```

Confirme com `php -m | grep opentelemetry`.

### 3.2 Instalar os pacotes Composer

```bash
composer require \
  open-telemetry/sdk \
  open-telemetry/exporter-otlp \
  open-telemetry/opentelemetry-auto-laravel   # troque pelo do seu framework
```

Pacotes de auto-instrumentação por framework (instale o(s) que usar):

```
open-telemetry/opentelemetry-auto-laravel
open-telemetry/opentelemetry-auto-symfony
open-telemetry/opentelemetry-auto-slim
open-telemetry/opentelemetry-auto-codeigniter
open-telemetry/opentelemetry-auto-pdo        # spans de queries SQL
open-telemetry/opentelemetry-auto-curl       # spans de chamadas HTTP saída
open-telemetry/opentelemetry-auto-guzzle
```

Para produção, recomenda-se também a extensão `ext-protobuf` (serialização protobuf nativa,
bem mais rápida que a implementação pura-PHP):

```bash
install-php-extensions protobuf   # ou pecl install protobuf
```

### 3.3 Variáveis de ambiente (pronto para colar)

Defina no ambiente do processo PHP-FPM/CLI (arquivo do pool FPM, `.env` do container, etc.):

```bash
OTEL_PHP_AUTOLOAD_ENABLED=true
OTEL_SERVICE_NAME=meu-sistema
OTEL_TRACES_EXPORTER=otlp
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_EXPORTER_OTLP_ENDPOINT=http://<seu-gateway>:8090
OTEL_EXPORTER_OTLP_HEADERS=X-Revoada-Key=<SEU_SERVERKEY>
OTEL_PROPAGATORS=baggage,tracecontext
OTEL_RESOURCE_ATTRIBUTES=host.name=meu-servidor-01
OTEL_PHP_TRACES_PROCESSOR=batch
OTEL_PHP_EXCLUDED_URLS=healthz,status
```

O que cada variável faz:

| Variável | Função |
|----------|--------|
| `OTEL_PHP_AUTOLOAD_ENABLED=true` | Liga a auto-instrumentação no autoload do Composer (essencial no zero-code) |
| `OTEL_SERVICE_NAME` | Nome do serviço → coluna **Serviço** na UI |
| `OTEL_TRACES_EXPORTER=otlp` | Exporta traces via OTLP (não console) |
| `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf` | Formato do payload; casa com o gateway |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Endpoint **base**; o SDK anexa `/v1/traces` |
| `OTEL_EXPORTER_OTLP_HEADERS` | Header de auth `X-Revoada-Key` |
| `OTEL_PROPAGATORS=baggage,tracecontext` | Propaga `traceparent` W3C entre serviços (correlação) |
| `OTEL_RESOURCE_ATTRIBUTES=host.name=...` | Hostname → filtro **Servidor** da UI (ver §8) |
| `OTEL_PHP_TRACES_PROCESSOR=batch` | Envia spans em lote (menos overhead que síncrono) |
| `OTEL_PHP_EXCLUDED_URLS=healthz,status` | Não gera trace para health checks (ruído) |

Pronto. Faça uma requisição na aplicação e confira a tela **Traces** (ver §9).

---

## 4. Cenário B — PHP sem framework / procedural / WordPress

Sem framework não há um "kernel" onde o pacote auto se encaixe. A solução é carregar o
autoload do Composer **antes** de qualquer código, com `auto_prepend_file`, e ligar o
**root span automático**.

### 4.1 Passos

```bash
# 1) instale a extensão C (ver §3.1) e os pacotes base + os "auto" de PDO/Curl:
composer require \
  open-telemetry/sdk open-telemetry/exporter-otlp \
  open-telemetry/opentelemetry-auto-pdo \
  open-telemetry/opentelemetry-auto-curl
```

```ini
; 2) no php.ini OU no pool do PHP-FPM (www.conf), aponte o autoload do Composer:
auto_prepend_file = /caminho/do/app/vendor/autoload.php
```

```bash
# 3) variáveis de ambiente: as MESMAS do §3.3, MAIS:
OTEL_PHP_EXPERIMENTAL_AUTO_ROOT_SPAN=true
```

`OTEL_PHP_EXPERIMENTAL_AUTO_ROOT_SPAN=true` cria automaticamente um **span raiz por request**
(sem ele, apps procedurais não geram o span de topo). Os pacotes `auto-pdo`/`auto-curl` geram
os spans filhos (queries e chamadas HTTP).

### 4.2 Nota específica de WordPress

- WordPress **exige** o `auto_prepend_file` acima — sem ele não há traces.
- O pacote de framework é `open-telemetry/opentelemetry-auto-wordpress`, ainda **0.x**
  (beta funcional, jul/2026). Instale-o além dos `auto-pdo`/`auto-curl`:
  ```bash
  composer require open-telemetry/opentelemetry-auto-wordpress
  ```
- Defina `OTEL_SERVICE_NAME` com o nome do site e mantenha `OTEL_PHP_EXCLUDED_URLS` para
  não instrumentar `wp-cron.php`/health checks.

---

## 5. Cenário C — Instrumentação manual (sem extensão C)

Quando não dá para instalar a extensão C (host gerenciado, cPanel restrito, etc.), use o SDK
manualmente. Você controla o `TracerProvider`, cria spans no código e envia via OTLP HTTP,
injetando o header `X-Revoada-Key` diretamente no transport.

### 5.1 Instalar

```bash
composer require \
  open-telemetry/sdk \
  open-telemetry/exporter-otlp \
  php-http/guzzle7-adapter        # cliente PSR-18 (qualquer adapter PSR-18 serve)
```

O exporter suporta **`http/protobuf`** e **`http/json`**. Protobuf funciona com
`google/protobuf` puro-PHP (bom para dev); em produção instale `ext-protobuf`.

### 5.2 Exemplo completo e funcional

```php
<?php
require __DIR__ . '/vendor/autoload.php';

use OpenTelemetry\API\Trace\SpanKind;
use OpenTelemetry\API\Trace\StatusCode;
use OpenTelemetry\Contrib\Otlp\OtlpHttpTransportFactory;
use OpenTelemetry\Contrib\Otlp\SpanExporter;
use OpenTelemetry\SDK\Common\Attribute\Attributes;
use OpenTelemetry\SDK\Common\Time\ClockFactory;
use OpenTelemetry\SDK\Resource\ResourceInfo;
use OpenTelemetry\SDK\Resource\ResourceInfoFactory;
use OpenTelemetry\SDK\Trace\SpanProcessor\BatchSpanProcessor;
use OpenTelemetry\SDK\Trace\TracerProvider;
use OpenTelemetry\SemConv\ResourceAttributes;

// 1) Transport OTLP/HTTP com o header de auth custom (3º parâmetro = array de headers).
//    Use a URL COMPLETA com /v1/traces (no modo manual você monta o path).
$transport = (new OtlpHttpTransportFactory())->create(
    'http://<seu-gateway>:8090/v1/traces',
    'application/x-protobuf',                 // ou 'application/json' p/ OTLP/JSON
    ['X-Revoada-Key' => '<SEU_SERVERKEY>']       // <-- header de auth do painel
);
$exporter = new SpanExporter($transport);

// 2) Resource: service.name + host (host.name p/ inventário, host p/ filtro da UI).
$host = gethostname();
$resource = ResourceInfoFactory::defaultResource()->merge(
    ResourceInfo::create(Attributes::create([
        ResourceAttributes::SERVICE_NAME => 'meu-servico-php',
        'host.name' => $host,
        'host'      => $host,
    ]))
);

// 3) TracerProvider com BatchSpanProcessor.
$tracerProvider = TracerProvider::builder()
    ->addSpanProcessor(new BatchSpanProcessor($exporter, ClockFactory::getDefault()))
    ->setResource($resource)
    ->build();

$tracer = $tracerProvider->getTracer('meu-app-manual');

// 4) Criar e encerrar um span (com exception, se houver).
$span = $tracer->spanBuilder('processa-pedido')
    ->setSpanKind(SpanKind::KIND_SERVER)
    ->startSpan();
$scope = $span->activate();
try {
    // ... seu trabalho ...
    $span->setAttribute('pedido.id', 12345);
} catch (\Throwable $e) {
    $span->recordException($e);                                  // vira exception.* na UI
    $span->setStatus(StatusCode::STATUS_ERROR, $e->getMessage());
} finally {
    $scope->detach();
    $span->end();
}

// 5) IMPORTANTE: flush + encerramento — sem isto o lote pode não ser enviado.
$tracerProvider->shutdown();
```

> **OTLP/JSON:** troque o 2º parâmetro do `create()` para `'application/json'`. O gateway
> decodifica JSON com IDs em **hex** corretamente (usa o decoder OTLP oficial). Protobuf
> continua sendo o caminho recomendado.

---

## 6. Cenário D — PHP legado (< 8.1)

Fallback. Use só quando não há como rodar 8.1+.

### 6.1 PHP 7.4 — SDK manual com versões antigas

O caminho do §5, mas fixando versões antigas dos pacotes que ainda suportavam 7.4 no
`composer.json` (o SDK atual exige `^8.1`). Ex.:

```jsonc
// composer.json — fixar releases compatíveis com 7.4 (ajuste às últimas que aceitam 7.4)
"require": {
  "php": "7.4.*",
  "open-telemetry/sdk": "1.0.*",
  "open-telemetry/exporter-otlp": "1.0.*"
}
```

Sem extensão C (só manual). O resto (transport com `X-Revoada-Key`, TracerProvider, shutdown) é
idêntico ao §5.

### 6.2 PHP 5.x / 7.0–7.3 — OTLP/JSON via curl puro

Sem SDK. Você monta o payload **OTLP/JSON** à mão e envia no shutdown do request. O gateway
aceita OTLP/JSON e interpreta `traceId`/`spanId` como **hex** (32 e 16 chars, respectivamente).

Payload mínimo (`span.json`):

```json
{
  "resourceSpans": [{
    "resource": {
      "attributes": [
        {"key": "service.name", "value": {"stringValue": "app-legado"}},
        {"key": "host.name",   "value": {"stringValue": "srv-web-01"}},
        {"key": "host",        "value": {"stringValue": "srv-web-01"}}
      ]
    },
    "scopeSpans": [{
      "scope": {"name": "curl-manual"},
      "spans": [{
        "traceId": "5b8aa5a2d2c872e8321cf37308d69df2",
        "spanId":  "051581bf3cb55c13",
        "name": "GET /pedido",
        "kind": 2,
        "startTimeUnixNano": "1750000000000000000",
        "endTimeUnixNano":   "1750000001000000000",
        "status": {"code": 1}
      }]
    }]
  }]
}
```

- `traceId`: **32** caracteres hex. `spanId`: **16** caracteres hex.
- `kind`: 2 = SERVER, 3 = CLIENT, 1 = INTERNAL.
- `status.code`: 1 = OK, 2 = ERROR (traces com ERROR são sempre retidos — ver §7).

Enviar:

```bash
curl -sS -X POST http://<seu-gateway>:8090/v1/traces \
  -H 'Content-Type: application/json' \
  -H 'X-Revoada-Key: <SEU_SERVERKEY>' \
  --data @span.json
```

Gerando IDs válidos em PHP legado e enviando no shutdown:

```php
<?php
function revoada_hex($bytes) {          // IDs hex do tamanho certo
    if (function_exists('random_bytes')) return bin2hex(random_bytes($bytes));
    $s = '';
    for ($i = 0; $i < $bytes; $i++) $s .= sprintf('%02x', mt_rand(0, 255));
    return $s;
}

$startNs = sprintf('%.0f', microtime(true) * 1e9);
$traceId = revoada_hex(16);   // 32 hex
$spanId  = revoada_hex(8);    // 16 hex

register_shutdown_function(function () use ($traceId, $spanId, $startNs) {
    $endNs = sprintf('%.0f', microtime(true) * 1e9);
    $payload = json_encode(['resourceSpans' => [[
        'resource'   => ['attributes' => [
            ['key' => 'service.name', 'value' => ['stringValue' => 'app-legado']],
            ['key' => 'host.name',    'value' => ['stringValue' => gethostname()]],
            ['key' => 'host',         'value' => ['stringValue' => gethostname()]],
        ]],
        'scopeSpans' => [[ 'spans' => [[
            'traceId' => $traceId, 'spanId' => $spanId,
            'name' => $_SERVER['REQUEST_URI'] ?? 'cli',
            'kind' => 2,
            'startTimeUnixNano' => $startNs, 'endTimeUnixNano' => $endNs,
            'status' => ['code' => 1],
        ]]]],
    ]]]);
    $ch = curl_init('http://<seu-gateway>:8090/v1/traces');
    curl_setopt_array($ch, [
        CURLOPT_POST => true,
        CURLOPT_HTTPHEADER => ['Content-Type: application/json', 'X-Revoada-Key: <SEU_SERVERKEY>'],
        CURLOPT_POSTFIELDS => $payload,
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_TIMEOUT => 2,
    ]);
    curl_exec($ch);
    curl_close($ch);
});
```

Lib de referência de terceiros para esse padrão: `sarigue/curlmetry`.

---

## 7. Produção e PHP-FPM

O PHP-FPM não tem processo de fundo: o flush dos spans acontece **no shutdown do request**
(síncrono). Boas práticas:

- **`fastcgi_finish_request()`** logo após enviar a resposta ao usuário — assim o envio dos
  spans não adiciona latência percebida pelo cliente.
- **Processor `batch`** (`OTEL_PHP_TRACES_PROCESSOR=batch`) — agrupa spans, menos requests.
- **`ext-protobuf`** instalada — serialização nativa, muito mais rápida.
- **`OTEL_PHP_EXCLUDED_URLS=healthz,status`** — não instrumente health checks (ruído/custo).
- **Sampling do lado do cliente** (opcional): `OTEL_TRACES_SAMPLER=parentbased_traceidratio`
  + `OTEL_TRACES_SAMPLER_ARG=0.2` para não emitir 100% em apps de alto volume.
- **Collector local (sidecar), opcional:** rode um OpenTelemetry Collector na `4318` local
  encaminhando ao painel. Ele dá **retry/buffer** que um processo PHP que morre não tem, e
  desacopla o request do envio de rede.

### Amostragem no painel (lado servidor)

O gateway aplica uma amostragem própria na ingestão (independente do cliente):

- **Sempre retém 100%** de um trace que tenha **algum span com erro** OU **duração ≥ 1s**.
- Traces "normais" (rápidos, sem erro) são retidos a uma fração — **default 20%**.
- Configurável por env **no gateway** (não no cliente): `REVOADA_TRACE_SAMPLE` (0.0–1.0,
  default `0.2`) e `REVOADA_TRACE_SAMPLE_SLOW_MS` (limiar de "lento" em ms, default `1000`).

> **Ao testar:** gere um trace com **erro** (`recordException` + status ERROR) OU que demore
> **> 1s**. Esses são sempre guardados — assim você não confunde amostragem com "não funcionou".

---

## 8. Filtro "Servidor" da UI — atenção ao `host`

A tela **Traces** tem um filtro por **Servidor**. Internamente ele casa pelo label **`host`**
do span. Você tem duas opções, ambas válidas:

- Definir **`host.name`** (convenção OpenTelemetry) — o gateway **copia automaticamente**
  `host.name` para `host` quando `host` está vazio. Recomendado, porque `host.name` também
  alimenta o inventário de Infraestrutura.
- Ou definir `host` explicitamente.

Nos exemplos manuais (§5/§6) definimos **os dois** por garantia. No zero-code basta:

```bash
OTEL_RESOURCE_ATTRIBUTES=host.name=meu-servidor-01
```

Use o **mesmo hostname** que o servidor reporta nas métricas, para os traces caírem no
servidor certo.

---

## 9. Verificação — conferir no painel

1. Abra a tela **Traces**.
2. **Lista:** o trace aparece com Serviço (= `service.name`), duração total, nº de spans e
   marcador de erro. Lembre da amostragem (§7): use um trace com erro ou > 1s ao testar.
3. **Waterfall:** clique no trace para ver a cascata de spans (pai/filho por `traceparent`).
4. **Filtro Servidor:** selecione seu host — casa pelo label `host` (§8).
5. **Service map:** serviços e as arestas de chamada entre eles.
6. **Exceptions:** `$span->recordException($e)` vira, no span, `exception.type`,
   `exception.message` e `exception.stacktrace` (o gateway captura o primeiro event
   `exception`). Combine com `$span->setStatus(StatusCode::STATUS_ERROR, ...)`.

Se nada aparece: confira (a) header `X-Revoada-Key` correto; (b) endpoint do gateway (`…:8090` ou o proxy HTTPS); (c) protocolo
`http/protobuf`; (d) que houve `shutdown()`/flush; (e) gere um trace com erro/lento para furar
a amostragem.

---

## 10. Detalhes do endpoint aceitos hoje

- **Rota:** `POST /v1/traces` (no endpoint base `http://<seu-gateway>:8090`).
- **Protobuf:** `Content-Type: application/x-protobuf` — caminho padrão.
- **OTLP/JSON:** `Content-Type: application/json` (aceita `; charset=utf-8`). IDs
  `traceId`/`spanId`/`parentSpanId` são lidos em **hex** corretamente.
- **gzip:** `Content-Encoding: gzip` é aceito e descomprimido (limite de 16 MiB **após**
  descompressão). Para simplicidade, o caminho recomendado é **protobuf sem gzip**.
- **gRPC OTLP (`:4317`):** o gateway também aceita; o `docker-compose.yml` da raiz publica a
  4317 (`REVOADA_PORTA_OTLP_GRPC`). Atrás de um proxy HTTP, prefira OTLP/HTTP.

---

## 11. Segurança

- A porta **8090 do gateway é HTTP puro (sem TLS)**. Exposta direto, a chave `X-Revoada-Key`
  e os dados de trace trafegam em **texto claro** — em produção, coloque um proxy reverso com
  HTTPS na frente (como em `deploy/prod/`).
- Sem TLS: **restrinja a origem/rede** de quem envia (firewall, VPN, rede
  interna) para reduzir a exposição da chave.
- **Nunca** comite a `X-Revoada-Key` no repositório do app — injete via variável de ambiente/
  secret manager. Nos exemplos, `<SEU_SERVERKEY>` é sempre um placeholder.

---

## Fontes

- OpenTelemetry PHP: https://opentelemetry.io/docs/languages/php/
- Zero-code PHP: https://opentelemetry.io/docs/zero-code/php/
- SDK e exporters: https://github.com/open-telemetry/opentelemetry-php
- Auto-instrumentação (contrib): https://github.com/open-telemetry/opentelemetry-php-contrib
- Extensão C: https://github.com/open-telemetry/opentelemetry-php-instrumentation
- Pacotes: https://packagist.org/packages/open-telemetry/
- Variáveis de ambiente OTLP: https://opentelemetry.io/docs/specs/otel/protocol/exporter/

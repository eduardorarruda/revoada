-- Chamadas de IA (LLM, agente, ferramenta), normalizadas pelo gateway a partir dos
-- spans OTLP. Uma linha por span de IA. O span também segue para a tabela spans (o
-- waterfall continua igual), mas aqui as colunas são tipadas e a retenção é maior:
-- custo e tendência precisam de meses, e somar tokens lendo um Map de texto não escala.
--
-- Tokens Nullable de propósito: NULL é "a biblioteca não informou", e o painel mostra
-- isso como não informado, nunca como zero. tokens_entrada sempre inclui o cache.
--
-- non_replicated_deduplication_window: o batcher do gateway reenvia o MESMO bloco
-- quando um insert falha no meio do caminho, e sem deduplicação o retry contaria a
-- chamada (e o custo) duas vezes. A janela também está em genai_1m e o gateway insere
-- com deduplicate_blocks_in_dependent_materialized_views=1: medido no ClickHouse 24.8,
-- sem isso o bloco repetido some de genai_spans mas a MV o soma de novo.
CREATE TABLE IF NOT EXISTS genai_spans
(
    tenant_id            LowCardinality(String),
    ts                   DateTime64(3),
    trace_id             String,
    span_id              String,
    parent_span_id       String,
    service              LowCardinality(String),
    host                 LowCardinality(String),
    nome                 String,
    duracao_ms           Float64,
    convencao            LowCardinality(String),
    operacao             LowCardinality(String),
    provedor             LowCardinality(String),
    modelo               LowCardinality(String),
    agente               LowCardinality(String),
    agente_id            String,
    conversa_id          String,
    ferramenta           LowCardinality(String),
    chamada_id           String,
    tokens_entrada       Nullable(UInt64),
    tokens_saida         Nullable(UInt64),
    tokens_cache_leitura Nullable(UInt64),
    tokens_cache_escrita Nullable(UInt64),
    custo_informado_usd  Nullable(Float64),
    erro                 LowCardinality(String),
    motivos_fim          Array(LowCardinality(String)),
    com_conteudo         UInt8,
    INDEX idx_trace    trace_id    TYPE bloom_filter(0.01) GRANULARITY 4,
    INDEX idx_conversa conversa_id TYPE bloom_filter(0.01) GRANULARITY 4
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (tenant_id, service, operacao, ts)
TTL toDateTime(ts) + INTERVAL 90 DAY
SETTINGS index_granularity = 8192, non_replicated_deduplication_window = 1000;

-- Agregado exato por minuto. As telas de visão geral e o runner de alertas leem daqui.
-- O custo NÃO é gravado: depende da tabela de preços (PostgreSQL, com vigência), e é
-- calculado na leitura multiplicando estes tokens pelo preço vigente em cada minuto.
-- As colunas estimar_* somam só as chamadas SEM custo informado pela biblioteca, que
-- são as que precisam de estimativa (as outras entram por custo_informado_usd).
CREATE TABLE IF NOT EXISTS genai_1m
(
    tenant_id              LowCardinality(String),
    ts                     DateTime,
    service                LowCardinality(String),
    host                   LowCardinality(String),
    provedor               LowCardinality(String),
    modelo                 LowCardinality(String),
    agente                 LowCardinality(String),
    operacao               LowCardinality(String),
    chamadas               SimpleAggregateFunction(sum, UInt64),
    erros                  SimpleAggregateFunction(sum, UInt64),
    com_tokens             SimpleAggregateFunction(sum, UInt64),
    tokens_entrada         SimpleAggregateFunction(sum, UInt64),
    tokens_saida           SimpleAggregateFunction(sum, UInt64),
    tokens_cache_leitura   SimpleAggregateFunction(sum, UInt64),
    tokens_cache_escrita   SimpleAggregateFunction(sum, UInt64),
    estimar_entrada        SimpleAggregateFunction(sum, UInt64),
    estimar_saida          SimpleAggregateFunction(sum, UInt64),
    estimar_cache_leitura  SimpleAggregateFunction(sum, UInt64),
    estimar_cache_escrita  SimpleAggregateFunction(sum, UInt64),
    custo_informado_usd    SimpleAggregateFunction(sum, Float64),
    com_custo_informado    SimpleAggregateFunction(sum, UInt64),
    duracao_ms_soma        SimpleAggregateFunction(sum, Float64),
    latencia               AggregateFunction(quantilesTDigest(0.5, 0.95, 0.99), Float64)
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (tenant_id, ts, service, host, provedor, modelo, agente, operacao)
TTL ts + INTERVAL 400 DAY
SETTINGS non_replicated_deduplication_window = 1000;

-- A subconsulta renomeia as colunas de origem: os aliases de saída têm os mesmos nomes
-- (tokens_entrada…) e, sem ela, um sumIf(tokens_entrada) seria lido como agregado
-- dentro de agregado.
CREATE MATERIALIZED VIEW IF NOT EXISTS genai_1m_mv TO genai_1m AS
SELECT
    tenant_id,
    toStartOfMinute(t)                                 AS ts,
    service,
    host,
    provedor,
    modelo,
    agente,
    operacao,
    toUInt64(count())                                  AS chamadas,
    toUInt64(countIf(e != ''))                         AS erros,
    toUInt64(countIf(te IS NOT NULL OR ts_ IS NOT NULL)) AS com_tokens,
    toUInt64(sum(ifNull(te, 0)))                       AS tokens_entrada,
    toUInt64(sum(ifNull(ts_, 0)))                      AS tokens_saida,
    toUInt64(sum(ifNull(tcl, 0)))                      AS tokens_cache_leitura,
    toUInt64(sum(ifNull(tce, 0)))                      AS tokens_cache_escrita,
    toUInt64(sumIf(ifNull(te, 0), ci IS NULL))         AS estimar_entrada,
    toUInt64(sumIf(ifNull(ts_, 0), ci IS NULL))        AS estimar_saida,
    toUInt64(sumIf(ifNull(tcl, 0), ci IS NULL))        AS estimar_cache_leitura,
    toUInt64(sumIf(ifNull(tce, 0), ci IS NULL))        AS estimar_cache_escrita,
    sum(ifNull(ci, 0))                                 AS custo_informado_usd,
    toUInt64(countIf(ci IS NOT NULL))                  AS com_custo_informado,
    sum(d)                                             AS duracao_ms_soma,
    quantilesTDigestState(0.5, 0.95, 0.99)(d)          AS latencia
FROM
(
    SELECT
        tenant_id, ts AS t, service, host, provedor, modelo, agente, operacao,
        erro AS e, duracao_ms AS d, custo_informado_usd AS ci,
        tokens_entrada AS te, tokens_saida AS ts_,
        tokens_cache_leitura AS tcl, tokens_cache_escrita AS tce
    FROM genai_spans
)
GROUP BY tenant_id, ts, service, host, provedor, modelo, agente, operacao;

-- Conteúdo das chamadas (prompt, resposta, argumentos de ferramenta). Só é gravado
-- quando REVOADA_GENAI_CONTEUDO = redigido ou completo. Fica separado, comprimido e com
-- prazo curto: é o dado mais sensível do painel. Partição diária para o TTL apagar
-- partes inteiras em vez de reescrever linhas.
CREATE TABLE IF NOT EXISTS genai_conteudo
(
    tenant_id LowCardinality(String),
    ts        DateTime64(3),
    trace_id  String,
    span_id   String,
    lado      LowCardinality(String),
    papel     LowCardinality(String),
    ordem     UInt16,
    texto     String CODEC(ZSTD(3)),
    truncado  UInt8,
    redigido  UInt8
)
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (tenant_id, trace_id, span_id, lado, ordem)
TTL toDateTime(ts) + INTERVAL 7 DAY
SETTINGS index_granularity = 8192, non_replicated_deduplication_window = 1000;

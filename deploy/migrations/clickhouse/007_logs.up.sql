-- Logs (Fase 5, P5.1): store de logs com índices de skip para busca full-text.
-- ORDER BY (tenant, service, ts) agrupa por serviço no tempo; o tokenbf no body
-- acelera buscas por termo; TTL de 30 dias (ajustável por tenant no futuro).
CREATE TABLE IF NOT EXISTS logs
(
    tenant_id    LowCardinality(String),
    ts           DateTime64(3),
    service      LowCardinality(String),
    severity     LowCardinality(String),          -- TRACE|DEBUG|INFO|WARN|ERROR|FATAL
    severity_num UInt8,                            -- 1..24 (mapa OTLP)
    body         String,
    labels       Map(LowCardinality(String), String),
    trace_id     String,
    span_id      String,
    INDEX idx_body   body     TYPE tokenbf_v1(32768, 3, 0) GRANULARITY 4,
    INDEX idx_trace  trace_id TYPE bloom_filter(0.01)      GRANULARITY 4,
    INDEX idx_sev    severity_num TYPE minmax               GRANULARITY 4
)
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (tenant_id, service, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY
SETTINGS index_granularity = 8192;

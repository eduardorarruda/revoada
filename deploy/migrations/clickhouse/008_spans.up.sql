-- Traces/spans (Fase 5, P5.3). Um span por linha; o trace é reconstruído por trace_id.
CREATE TABLE IF NOT EXISTS spans
(
    tenant_id      LowCardinality(String),
    ts             DateTime64(3),                   -- início do span
    trace_id       String,
    span_id        String,
    parent_span_id String,
    service        LowCardinality(String),
    name           LowCardinality(String),
    kind           LowCardinality(String),          -- INTERNAL|SERVER|CLIENT|PRODUCER|CONSUMER
    duration_ms    Float64,
    status_code    LowCardinality(String),          -- UNSET|OK|ERROR
    status_msg     String,
    labels         Map(LowCardinality(String), String),
    INDEX idx_trace trace_id    TYPE bloom_filter(0.01) GRANULARITY 4,
    INDEX idx_dur   duration_ms TYPE minmax           GRANULARITY 4
)
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (tenant_id, service, ts)
TTL toDateTime(ts) + INTERVAL 15 DAY
SETTINGS index_granularity = 8192;

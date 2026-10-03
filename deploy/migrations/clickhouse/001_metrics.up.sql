-- Tabela bruta de métricas. Retenção curta (7 dias); o valor histórico vive nas MVs.
CREATE TABLE IF NOT EXISTS metrics
(
    tenant_id  LowCardinality(String),
    metric     LowCardinality(String),
    labels     Map(LowCardinality(String), String),
    ts         DateTime64(3),
    value      Float64
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (tenant_id, metric, ts)
TTL toDateTime(ts) + INTERVAL 7 DAY

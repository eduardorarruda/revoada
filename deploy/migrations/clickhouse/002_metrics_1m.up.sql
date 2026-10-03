-- Downsampling por minuto (retenção 90 dias). AggregatingMergeTree guarda estados parciais;
-- a consulta final usa avgMerge/min/max/sum.
CREATE TABLE IF NOT EXISTS metrics_1m
(
    tenant_id LowCardinality(String),
    metric    LowCardinality(String),
    labels    Map(LowCardinality(String), String),
    ts        DateTime,
    avg_state AggregateFunction(avg, Float64),
    min_val   SimpleAggregateFunction(min, Float64),
    max_val   SimpleAggregateFunction(max, Float64),
    cnt       SimpleAggregateFunction(sum, UInt64)
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (tenant_id, metric, labels, ts)
TTL ts + INTERVAL 90 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS metrics_1m_mv TO metrics_1m AS
SELECT
    tenant_id,
    metric,
    labels,
    toStartOfMinute(ts) AS ts,
    avgState(value)      AS avg_state,
    min(value)           AS min_val,
    max(value)           AS max_val,
    toUInt64(count())    AS cnt
FROM metrics
GROUP BY tenant_id, metric, labels, ts

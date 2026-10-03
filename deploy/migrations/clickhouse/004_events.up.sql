-- Eventos discretos: deploys, alertas, mudanças de host, snapshots de processos/conexões.
CREATE TABLE IF NOT EXISTS events
(
    tenant_id LowCardinality(String),
    kind      LowCardinality(String),
    ts        DateTime64(3),
    title     String,
    body      String,
    labels    Map(LowCardinality(String), String)
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (tenant_id, kind, ts)

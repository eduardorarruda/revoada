-- Checkpoint do migrador de histórico MySQL (append-only; progresso = max(last_id)).
CREATE TABLE IF NOT EXISTS legacy_migration_checkpoint
(
    source  String,
    last_id UInt64,
    updated DateTime DEFAULT now()
)
ENGINE = MergeTree
ORDER BY (source, updated)

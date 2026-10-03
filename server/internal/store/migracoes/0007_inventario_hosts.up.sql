-- ============================================================================
-- Inventário de hosts no schema do PAINEL (antes só o gateway criava a tabela)
-- ============================================================================
-- Sem o gateway rodando (instalação só com painel + agentes do canal, ou o binário
-- único da Etapa 9), /api/hosts e o heartbeat dos alertas davam 500 com
-- "relation hosts does not exist". A DDL é IDÊNTICA à de gateway/internal/pg/schema.sql:
-- os dois usam IF NOT EXISTS e convivem em qualquer ordem.
CREATE TABLE IF NOT EXISTS hosts (
    tenant_id     TEXT NOT NULL,
    hostname      TEXT NOT NULL,
    os            TEXT,
    kernel        TEXT,
    arch          TEXT,
    cpu_model     TEXT,
    cpu_cores     INT,
    ips           TEXT,
    agent_version TEXT,
    uptime_secs   DOUBLE PRECISION,
    last_seen     TIMESTAMPTZ,
    display_name  TEXT,
    PRIMARY KEY (tenant_id, hostname)
);

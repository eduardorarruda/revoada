-- Schema de metadados do gateway (idempotente). Espelha deploy/migrations/postgres/001.
CREATE TABLE IF NOT EXISTS agents (
    serverkey   TEXT PRIMARY KEY,
    tenant_id   TEXT NOT NULL,
    hostname    TEXT,
    revoked     BOOLEAN NOT NULL DEFAULT FALSE,
    last_seen   TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

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
    display_name  TEXT,             -- alias amigável definido pelo usuário; hostname continua a chave
    PRIMARY KEY (tenant_id, hostname)
);
-- Bases já existentes sem a coluna (idempotente; IF EXISTS para não falhar em base fresca).
ALTER TABLE IF EXISTS hosts ADD COLUMN IF NOT EXISTS display_name TEXT;

-- Alvos de scrape Prometheus.
CREATE TABLE IF NOT EXISTS scrape_targets (
    id               SERIAL PRIMARY KEY,
    tenant_id        TEXT NOT NULL,
    url              TEXT NOT NULL,
    interval_seconds INT NOT NULL DEFAULT 15,
    labels           JSONB NOT NULL DEFAULT '{}',
    enabled          BOOLEAN NOT NULL DEFAULT TRUE,
    UNIQUE (tenant_id, url)
);

-- Auto-discovery (P6.2): serviços conhecidos detectados pelo agente em cada host.
CREATE TABLE IF NOT EXISTS host_services (
    id            BIGSERIAL PRIMARY KEY,
    tenant_id     TEXT NOT NULL,
    hostname      TEXT NOT NULL,
    kind          TEXT NOT NULL,   -- mysql|postgres|redis|nginx|mongodb|clickhouse|docker
    detail        TEXT,
    source        TEXT,            -- process|port|docker
    discovered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, hostname, kind)
);

-- Sondas multi-região (P6.3): resultado por sonda para cada URL.
CREATE TABLE IF NOT EXISTS probe_results (
    tenant_id      TEXT NOT NULL,
    url            TEXT NOT NULL,
    probe_location TEXT NOT NULL,
    up             BOOLEAN NOT NULL,
    total_ms       DOUBLE PRECISION NOT NULL DEFAULT 0,
    diagnostic     TEXT,
    reported_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- status: código HTTP visto pela sonda (0 = nem houve resposta).
    -- truncated: a sonda cortou o corpo no teto de leitura, então total_ms é um PISO e
    -- a asserção de palavra-chave ficou indeterminada. Os dois vinham do agente e eram
    -- descartados em silêncio pelo gateway (o probeReq nem declarava os campos).
    status         INT NOT NULL DEFAULT 0,
    truncated      BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (tenant_id, url, probe_location)
);
-- Bases que já tinham probe_results sem as colunas (idempotente).
ALTER TABLE probe_results ADD COLUMN IF NOT EXISTS status INT NOT NULL DEFAULT 0;
ALTER TABLE probe_results ADD COLUMN IF NOT EXISTS truncated BOOLEAN NOT NULL DEFAULT FALSE;

-- Vínculo serverkey <-> localização de sonda (P6.3).
--
-- POR QUE existe: /ingest/probe-result aceitava `probe_location` LIVRE de qualquer
-- serverkey válida. Provado por exploração: com UMA ÚNICA chave foram forjados dois
-- votos (`caos-fake-tokyo` e `caos-fake-berlim`) e um check de ORIGEM ÚNICA, que a
-- sonda central via no ar, virou `DOWN — down em 2 sonda(s)`. O voto de sonda é um
-- VOTO: quem tem uma chave não pode ter dois.
--
-- O vínculo é 1:1 nos dois sentidos: uma chave reporta por UMA localização (PK) e uma
-- localização pertence a UMA chave (UNIQUE) — senão a chave A poderia sequestrar o
-- nome da sonda B. É "confia no primeiro uso": a primeira vez que uma chave reporta,
-- o par fica registrado. Trocar de nome é possível quando o vínculo anterior está
-- PARADO (ver probeRebindAfter em pg.go), que é o caso legítimo do operador que
-- mudou probe_location no config e reiniciou a sonda.
CREATE TABLE IF NOT EXISTS probe_agents (
    tenant_id      TEXT NOT NULL,
    serverkey      TEXT NOT NULL,
    probe_location TEXT NOT NULL,
    bound_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_report    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, serverkey),
    UNIQUE (tenant_id, probe_location)
);

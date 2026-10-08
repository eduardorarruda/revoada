-- ============================================================================
-- Agentes de IA (ADR 008): tabela de preços dos modelos
-- ============================================================================
-- Preço em dólar por 1 milhão de tokens, com VIGÊNCIA: uma troca de preço é uma linha
-- nova com vigente_desde posterior, e o passado continua calculado pelo preço que valia
-- na época. Editar a linha antiga reescreveria o custo de meses já fechados.
-- modelo aceita prefixo com * no fim (gpt-4o-mini* cobre gpt-4o-mini-2024-07-18).
-- cache_* NULL = cobra como entrada (estimativa por cima, nunca por baixo).
CREATE TABLE IF NOT EXISTS llm_precos (
    id                   BIGSERIAL PRIMARY KEY,
    tenant_id            TEXT NOT NULL DEFAULT 'default',
    provedor             TEXT NOT NULL DEFAULT '',
    modelo               TEXT NOT NULL CHECK (modelo <> ''),
    entrada_por_1m       NUMERIC(14,6) NOT NULL CHECK (entrada_por_1m >= 0),
    saida_por_1m         NUMERIC(14,6) NOT NULL CHECK (saida_por_1m >= 0),
    cache_leitura_por_1m NUMERIC(14,6) CHECK (cache_leitura_por_1m >= 0),
    cache_escrita_por_1m NUMERIC(14,6) CHECK (cache_escrita_por_1m >= 0),
    moeda                TEXT NOT NULL DEFAULT 'USD',
    vigente_desde        TIMESTAMPTZ NOT NULL,
    origem               TEXT NOT NULL DEFAULT 'manual',
    criado_por           TEXT NOT NULL DEFAULT '',
    criado_em            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, provedor, modelo, vigente_desde)
);

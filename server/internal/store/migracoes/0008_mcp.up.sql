-- ============================================================================
-- MCP (SPEC §12, Etapa 8): tokens próprios, com escopo, para agentes de IA
-- ============================================================================
-- O token só aparece uma vez (na criação); aqui fica o SHA-256 dele. Escopos:
-- leitura (listar/ler), mapeamento (capturar schema, gravar rascunho) e simulacao
-- (pedir dry-run). Não existe escopo para executar, reverter ou ver credencial.
CREATE TABLE IF NOT EXISTS mcp_tokens (
    id         TEXT PRIMARY KEY,
    nome       TEXT NOT NULL,
    hash       TEXT NOT NULL UNIQUE,
    escopos    TEXT[] NOT NULL DEFAULT '{leitura}',
    criado_por TEXT NOT NULL DEFAULT '',
    criado_em  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expira_em  TIMESTAMPTZ NOT NULL,
    ultimo_uso TIMESTAMPTZ,
    revogado   BOOLEAN NOT NULL DEFAULT false
);

-- ============================================================================
-- Canal painel ↔ agente (SPEC §7, Etapa 2)
-- ============================================================================

-- Chaves do próprio painel (CA interna dos agentes, chave Ed25519 que assina as
-- tarefas). Sempre cifradas pelo cofre: o JSON é um cofre.Segredo.
CREATE TABLE IF NOT EXISTS painel_chaves (
    nome      TEXT PRIMARY KEY,
    segredo   JSONB NOT NULL,
    criado_em TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Tokens de inscrição de agente: USO ÚNICO e com validade (o token antigo,
-- enrollment_tokens, não vencia). Guardado só o hash.
CREATE TABLE IF NOT EXISTS agente_tokens (
    hash       TEXT PRIMARY KEY,
    rotulo     TEXT NOT NULL DEFAULT '',
    criado_por TEXT NOT NULL DEFAULT '',
    criado_em  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expira_em  TIMESTAMPTZ NOT NULL,
    usado_em   TIMESTAMPTZ,
    agente_id  TEXT
);

-- Agentes inscritos: identidade = certificado emitido pela CA interna.
CREATE TABLE IF NOT EXISTS agentes (
    id              TEXT PRIMARY KEY,
    rotulo          TEXT NOT NULL DEFAULT '',
    hostname        TEXT NOT NULL DEFAULT '',
    so              TEXT NOT NULL DEFAULT '',
    arch            TEXT NOT NULL DEFAULT '',
    versao          TEXT NOT NULL DEFAULT '',
    chave_selo      BYTEA NOT NULL,                 -- X25519 pública (credenciais seladas)
    cert_serial     TEXT NOT NULL,                  -- só o certificado vigente é aceito
    cert_valido_ate TIMESTAMPTZ NOT NULL,
    revogado        BOOLEAN NOT NULL DEFAULT FALSE,
    estado          TEXT NOT NULL DEFAULT 'nunca_conectou', -- online | instavel | offline
    capacidades     TEXT[] NOT NULL DEFAULT '{}',
    visto_em        TIMESTAMPTZ,
    criado_em       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Tarefas enviadas aos agentes e o histórico de cada execução.
CREATE TABLE IF NOT EXISTS tarefas (
    id            TEXT PRIMARY KEY,
    tipo          TEXT NOT NULL,
    agente_id     TEXT NOT NULL REFERENCES agentes(id) ON DELETE CASCADE,
    estado        TEXT NOT NULL,          -- na_fila | enviada | executando | pausada | sucesso | falha | cancelada | recusada
    especificacao JSONB NOT NULL DEFAULT '{}',
    progresso     DOUBLE PRECISION NOT NULL DEFAULT 0,
    iniciada_por  TEXT NOT NULL DEFAULT '',
    origem        TEXT NOT NULL DEFAULT 'ui',   -- ui | api | mcp | github_action
    correlacao_id TEXT NOT NULL,
    expira_em     TIMESTAMPTZ NOT NULL,
    criada_em     TIMESTAMPTZ NOT NULL DEFAULT now(),
    inicio        TIMESTAMPTZ,
    fim           TIMESTAMPTZ,
    erro          TEXT NOT NULL DEFAULT '',
    resumo        JSONB
);
CREATE INDEX IF NOT EXISTS tarefas_agente_estado_idx ON tarefas (agente_id, estado);
CREATE INDEX IF NOT EXISTS tarefas_criada_idx ON tarefas (criada_em DESC);

-- Eventos de cada tarefa (o "ao vivo" e o histórico). (tarefa_id, seq) é a chave de
-- idempotência: o agente reenvia depois de uma queda e o repetido é ignorado.
CREATE TABLE IF NOT EXISTS tarefa_eventos (
    tarefa_id TEXT NOT NULL REFERENCES tarefas(id) ON DELETE CASCADE,
    seq       BIGINT NOT NULL,
    em        TIMESTAMPTZ NOT NULL,
    etapa     TEXT NOT NULL DEFAULT '',
    nivel     TEXT NOT NULL DEFAULT 'info',
    mensagem  TEXT NOT NULL DEFAULT '',
    progresso DOUBLE PRECISION NOT NULL DEFAULT 0,
    metricas  JSONB NOT NULL DEFAULT '{}',
    PRIMARY KEY (tarefa_id, seq)
);


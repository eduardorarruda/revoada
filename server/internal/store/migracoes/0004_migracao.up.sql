-- ============================================================================
-- Migração de dados (SPEC §9, Etapa 3)
-- ============================================================================

-- Onde estão os bancos. A senha NUNCA fica em texto puro: credencial é um
-- cofre.Segredo (JSON com o texto cifrado e a DEK cifrada pela chave mestra).
CREATE TABLE IF NOT EXISTS conexoes_banco (
    id                  TEXT PRIMARY KEY,
    nome                TEXT NOT NULL,
    motor               TEXT NOT NULL,                 -- firebird | postgres
    endereco            TEXT NOT NULL,                 -- host:porta
    banco               TEXT NOT NULL,                 -- caminho do .fdb ou nome do banco
    usuario             TEXT NOT NULL,
    credencial          JSONB NOT NULL,
    opcoes              JSONB NOT NULL DEFAULT '{}',
    agente_preferido_id TEXT REFERENCES agentes(id) ON DELETE SET NULL,
    versao_detectada    TEXT NOT NULL DEFAULT '',
    criada_por          TEXT NOT NULL DEFAULT '',
    criada_em           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Fotos da estrutura (sem dados). O hash avisa quando o banco mudou.
CREATE TABLE IF NOT EXISTS esquemas_capturados (
    id           TEXT PRIMARY KEY,
    conexao_id   TEXT NOT NULL REFERENCES conexoes_banco(id) ON DELETE CASCADE,
    agente_id    TEXT NOT NULL DEFAULT '',
    hash         TEXT NOT NULL,
    conteudo     JSONB NOT NULL,
    capturado_em TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS esquemas_conexao_idx ON esquemas_capturados (conexao_id, capturado_em DESC);

CREATE TABLE IF NOT EXISTS projetos_migracao (
    id         TEXT PRIMARY KEY,
    nome       TEXT NOT NULL,
    tipo       TEXT NOT NULL,                           -- troca_de_banco | upgrade_versao
    origem_id  TEXT NOT NULL REFERENCES conexoes_banco(id),
    destino_id TEXT REFERENCES conexoes_banco(id),
    estado     TEXT NOT NULL DEFAULT 'rascunho',        -- rascunho | aprovado | arquivado
    criado_por TEXT NOT NULL DEFAULT '',
    criado_em  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Cada edição do mapeamento é uma VERSÃO nova (nada é sobrescrito). O conteúdo é o
-- documento core/migracao/modelo.Mapeamento (tabela → tabela, coluna → coluna).
CREATE TABLE IF NOT EXISTS mapeamentos (
    projeto_id        TEXT NOT NULL REFERENCES projetos_migracao(id) ON DELETE CASCADE,
    versao            INT  NOT NULL,
    esquema_origem_id TEXT REFERENCES esquemas_capturados(id),
    esquema_destino_id TEXT REFERENCES esquemas_capturados(id),
    origem            TEXT NOT NULL,                    -- heuristica | mcp | usuario
    conteudo          JSONB NOT NULL,
    hash              TEXT NOT NULL,
    problemas         JSONB NOT NULL DEFAULT '[]',
    estado            TEXT NOT NULL DEFAULT 'rascunho', -- rascunho | valido | aprovado
    criado_por        TEXT NOT NULL DEFAULT '',
    criado_em         TIMESTAMPTZ NOT NULL DEFAULT now(),
    aprovado_por      TEXT,
    aprovado_em       TIMESTAMPTZ,
    PRIMARY KEY (projeto_id, versao)
);

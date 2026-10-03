-- ============================================================================
-- Execução da migração (SPEC §9.4, Etapas 5 e 6)
-- ============================================================================

-- Liga uma tarefa do canal (simular | executar | reverter) ao projeto e à versão do
-- mapeamento. `execucao` é o id que nomeia o controle no destino (checkpoints,
-- staging, cópia prévia): numa retomada, é o da execução original.
CREATE TABLE IF NOT EXISTS execucoes_migracao (
    tarefa_id       TEXT PRIMARY KEY REFERENCES tarefas(id) ON DELETE CASCADE,
    projeto_id      TEXT NOT NULL REFERENCES projetos_migracao(id) ON DELETE CASCADE,
    versao          INT  NOT NULL,
    tipo            TEXT NOT NULL,                  -- simular | executar | reverter
    execucao        TEXT NOT NULL,
    hash_mapeamento TEXT NOT NULL,
    relacionada_id  TEXT,                           -- reverter/retomar: a execução de origem
    criada_em       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS execucoes_projeto_idx ON execucoes_migracao (projeto_id, criada_em DESC);
CREATE INDEX IF NOT EXISTS execucoes_execucao_idx ON execucoes_migracao (execucao);

-- Último lote confirmado de cada tabela (cópia do que o agente grava no destino, para
-- a tela e para o painel saber de onde a retomada continua).
CREATE TABLE IF NOT EXISTS execucao_checkpoints (
    tarefa_id     TEXT NOT NULL REFERENCES tarefas(id) ON DELETE CASCADE,
    tabela        TEXT NOT NULL,
    ultima_chave  TEXT NOT NULL DEFAULT '',
    linhas        BIGINT NOT NULL DEFAULT 0,
    seq           BIGINT NOT NULL DEFAULT 0,
    atualizado_em TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tarefa_id, tabela)
);

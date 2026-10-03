-- ============================================================================
-- Segurança base (SPEC §13, Etapa 1)
-- ============================================================================

-- 2FA (TOTP). O segredo NUNCA fica em texto puro: é um cofre.Segredo (JSON com o
-- texto cifrado e a chave de dados cifrada pela chave mestra, que mora fora do banco).
-- mfa_ultimo_passo impede reusar o mesmo código (replay). Os códigos de recuperação
-- são guardados só como sha256 e cada um vale uma vez.
ALTER TABLE users ADD COLUMN IF NOT EXISTS mfa_ativo        BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE users ADD COLUMN IF NOT EXISTS mfa_segredo      JSONB;
ALTER TABLE users ADD COLUMN IF NOT EXISTS mfa_ultimo_passo BIGINT  NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS mfa_recuperacao  TEXT[]  NOT NULL DEFAULT '{}';

-- Força bruta por conta: falhas seguidas bloqueiam o login por tempo crescente
-- (1 min, 2, 4… até 1 h). Complementa o limite por IP que já existe.
ALTER TABLE users ADD COLUMN IF NOT EXISTS falhas_login  INT NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS bloqueado_ate TIMESTAMPTZ;

-- A sessão lembra se foi aberta com 2FA, para o refresh não "perder" o segundo fator.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS mfa BOOLEAN NOT NULL DEFAULT FALSE;

-- Auditoria encadeada: cada linha guarda o hash da anterior e o próprio hash
-- (sha256 dos campos). Apagar ou editar uma linha quebra a corrente e a verificação
-- aponta exatamente onde. Linhas antigas (sem hash) ficam fora da verificação.
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS origem        TEXT NOT NULL DEFAULT 'ui';  -- ui | api | mcp | agente
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS correlacao_id TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS hash_anterior TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS hash          TEXT NOT NULL DEFAULT '';

CREATE OR REPLACE FUNCTION revoada_audit_hash(a audit_log) RETURNS TEXT AS $$
    SELECT encode(sha256(convert_to(concat_ws('|',
        a.hash_anterior, a.id::text, coalesce(a.actor_id::text, ''), a.actor_name, a.actor_role,
        a.method, a.path, a.resource, a.target, a.status::text, a.payload::text, a.ip,
        a.origem, a.correlacao_id,
        to_char(a.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US')
    ), 'UTF8')), 'hex')
$$ LANGUAGE sql IMMUTABLE;


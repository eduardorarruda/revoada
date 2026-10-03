-- Desfaz a segurança base: 2FA, bloqueio de login, marca de 2FA na sessão e a
-- corrente de hashes da auditoria. Os dados dessas colunas são perdidos.
DROP FUNCTION IF EXISTS revoada_audit_hash(audit_log);
ALTER TABLE audit_log DROP COLUMN IF EXISTS hash, DROP COLUMN IF EXISTS hash_anterior,
                      DROP COLUMN IF EXISTS correlacao_id, DROP COLUMN IF EXISTS origem;
ALTER TABLE sessions  DROP COLUMN IF EXISTS mfa;
ALTER TABLE users     DROP COLUMN IF EXISTS bloqueado_ate, DROP COLUMN IF EXISTS falhas_login,
                      DROP COLUMN IF EXISTS mfa_recuperacao, DROP COLUMN IF EXISTS mfa_ultimo_passo,
                      DROP COLUMN IF EXISTS mfa_segredo, DROP COLUMN IF EXISTS mfa_ativo;

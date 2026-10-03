-- Expiração de sessão (item 11): além da inatividade (validade do refresh), toda
-- sessão tem um limite ABSOLUTO contado do login. A rotação do refresh cria uma
-- linha nova; familia_inicio carrega a hora do login original.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS familia_inicio TIMESTAMPTZ NOT NULL DEFAULT now();

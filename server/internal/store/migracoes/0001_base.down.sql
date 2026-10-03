-- A base (monitoramento herdado: usuários, dashboards, alertas, sites, auditoria…)
-- NÃO se desfaz por migration: apagar isto é apagar o produto. Para voltar no tempo,
-- restaure o backup (docs/runbook-dr.md).
DO $$ BEGIN RAISE EXCEPTION 'a migration 0001 (base) não tem rollback; use o backup'; END $$;

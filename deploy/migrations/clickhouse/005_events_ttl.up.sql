-- Eventos (snapshots de processos ~19KB/POST) não precisam viver para sempre.
-- TTL de 90 dias evita crescimento ilimitado da tabela events.
ALTER TABLE events MODIFY TTL toDateTime(ts) + INTERVAL 90 DAY

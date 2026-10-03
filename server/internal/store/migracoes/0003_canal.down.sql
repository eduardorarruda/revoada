-- Desfaz o canal com os agentes: identidades, tokens, tarefas e eventos. Os agentes
-- precisarão ser inscritos de novo.
DROP TABLE IF EXISTS tarefa_eventos;
DROP TABLE IF EXISTS tarefas;
DROP TABLE IF EXISTS agentes;
DROP TABLE IF EXISTS agente_tokens;
DROP TABLE IF EXISTS painel_chaves;

-- Desfaz o módulo de migração: conexões, fotos de schema, projetos e mapeamentos.
DROP TABLE IF EXISTS mapeamentos;
DROP TABLE IF EXISTS projetos_migracao;
DROP TABLE IF EXISTS esquemas_capturados;
DROP TABLE IF EXISTS conexoes_banco;

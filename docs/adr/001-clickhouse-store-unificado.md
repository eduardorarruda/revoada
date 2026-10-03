# ADR 001 — ClickHouse como store unificado de telemetria

- **Status:** aceito
- **Referência:** [ARQUITETURA.md §5](../ARQUITETURA.md) (decisões de arquitetura)

## Contexto
Precisamos guardar métricas, logs, traces e eventos com alta cardinalidade, retenção longa
e consultas analíticas rápidas, num produto self-hosted que sobe com compose.

## Decisão
Usar **ClickHouse** como único store das quatro pilastras de telemetria, com downsampling
por materialized views e TTL em camadas.

## Alternativas consideradas
- Prometheus + Loki + Tempo separados — três stores, três linguagens, correlação frágil.
- TimescaleDB — bom para métricas, fraco para logs/traces em escala e full-text.
- Stack Elastic — custo de operação e RAM altos para o porte departamental.

## Consequências
- (+) Correlação métrica↔log↔trace por join no mesmo engine; um só backup.
- (+) Compressão e custo de storage excelentes.
- (−) Menos "plug-and-play" que Prometheus para alerting — exige camada própria (Fase 4).
- Exige disciplina de schema e MVs (ADR referenciado pelas migrations do P1.1).

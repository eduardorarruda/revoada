# ADR 003 — NATS JetStream como fila de ingestão

- **Status:** aceito
- **Referência:** [ARQUITETURA.md §5](../ARQUITETURA.md) (decisões de arquitetura)

## Contexto
A escrita no ClickHouse precisa ser em lote e resiliente: se o store cair, os dados dos agentes
não podem se perder. Precisamos desacoplar recepção de gravação.

## Decisão
Usar **NATS JetStream** como buffer durável entre o receiver (gateway) e o writer (ClickHouse):
o receiver publica, o writer consome, agrega em lotes e só dá ack após inserção confirmada.

## Alternativas consideradas
- Kafka — robusto, porém pesado demais para o porte e a operação departamental.
- Redis Streams — já temos Redis, mas durabilidade/replay inferiores ao JetStream.
- Fila em memória — perda de dados em queda; inaceitável.

## Consequências
- (+) Backpressure e replay: derrubar o ClickHouse não perde dados (P1.4).
- (+) Leve; JetStream habilita persistência com um flag.
- (−) Mais um serviço no compose para operar e monitorar.

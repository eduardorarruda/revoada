# ADR 002 — Backend em Go, monolito modular

- **Status:** aceito
- **Referência:** [ARQUITETURA.md §5](../ARQUITETURA.md) (decisões de arquitetura)

## Contexto
Precisamos de ingestão de alto throughput, baixo consumo de memória e um binário simples de
operar num único servidor, sem a complexidade de microsserviços cedo demais.

## Decisão
Backend em **Go 1.23+** organizado como **monolito modular**: dois binários (gateway de ingestão
e server de API) com módulos internos (query, alerting, scheduler, auth). Sem microsserviços agora.

## Alternativas consideradas
- Node/TypeScript no backend — pior throughput de ingestão e GC menos previsível.
- Microsserviços desde já — complexidade operacional desnecessária no porte atual.
- Rust — curva e velocidade de entrega piores para o time.

## Consequências
- (+) Deploy trivial (binário estático + distroless), start rápido, footprint baixo.
- (+) Fronteiras de módulo já preparam extração futura se a escala exigir.
- (−) Um repositório precisa de disciplina para não virar espaguete — daí as convenções do CLAUDE.md.

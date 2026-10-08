# ADR 007 — Sem IA no escopo

- **Status:** aceito — esclarecido pelo [ADR 008](008-observar-agentes-de-ia.md): observar aplicações de IA dos usuários é escopo; usar IA dentro do Revoada continua fora.
- **Referência:** decisão de produto (ver [ARQUITETURA.md §5](../ARQUITETURA.md))

## Contexto
Uma versão anterior da análise propunha IA (detecção de anomalias, root-cause, assistente) como
"grande diferencial". Numa avaliação honesta para o porte departamental da Revoada, o custo de
construir e manter isso não se justifica frente ao valor entregue.

## Decisão
**Remover IA do escopo.** Nenhum prompt do plano constrói recursos de IA. Thresholds e regras
determinísticas (Fase 4) cobrem as necessidades reais de alerta.

## Alternativas consideradas
- Anomaly detection estatística — valor marginal sobre thresholds bem calibrados no porte atual.
- Assistente/LLM embarcado — custo, privacidade dos dados e manutenção desproporcionais.

## Consequências
- (+) Escopo enxuto, focado no que dá valor comprovado; menos superfície para manter.
- (+) Sem dependência de modelos/serviços externos nem exposição de telemetria.
- (−) Se surgir demanda real e mensurável, reabrir com um novo ADR — não é porta fechada para sempre.

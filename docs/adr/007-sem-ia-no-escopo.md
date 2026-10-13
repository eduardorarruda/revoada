# ADR 007 — Sem IA no escopo

- **Status:** aceito — esclarecido pelo [ADR 008](008-observar-agentes-de-ia.md): observar aplicações de IA dos usuários é escopo; usar IA dentro do Revoada continua fora.
- **Referência:** decisão de produto (ver [ARQUITETURA.md §5](../ARQUITETURA.md))

## Contexto
Uma versão anterior da análise propunha IA (detecção de anomalias, causa raiz, assistente) como
diferencial. Para o porte departamental a que o Revoada se destina (uma instalação self-hosted
por equipe), o custo de construir e manter isso não se justifica frente ao valor entregue.

## Decisão
**Remover IA do escopo.** O Revoada não embute IA: não tem detecção de anomalia, análise de
causa raiz nem assistente. Thresholds e regras determinísticas (`server/internal/alerting`)
cobrem as necessidades reais de alerta. Observar aplicações de IA dos usuários é outra coisa e
está no [ADR 008](008-observar-agentes-de-ia.md).

## Alternativas consideradas
- Anomaly detection estatística — valor marginal sobre thresholds bem calibrados no porte atual.
- Assistente/LLM embarcado — custo, privacidade dos dados e manutenção desproporcionais.

## Consequências
- (+) Escopo enxuto, focado no que dá valor comprovado; menos superfície para manter.
- (+) Sem dependência de modelos/serviços externos nem exposição de telemetria.
- (−) Se surgir demanda real e mensurável, reabrir com um novo ADR — não é porta fechada para sempre.

# ADR 005 — Docker Compose antes de Kubernetes

- **Status:** aceito
- **Referência:** [ARQUITETURA.md §5](../ARQUITETURA.md) (decisões de arquitetura)

## Contexto
O alvo inicial é departamental/single-host. Adotar Kubernetes cedo custaria complexidade
operacional sem retorno proporcional.

## Decisão
Empacotar e operar tudo com **Docker Compose** (dev e prod) e deploy push-to-deploy via SSH.
K8s/GitOps ficam para a Fase 7, **só quando o Compose limitar de fato**.

## Alternativas consideradas
- Kubernetes desde já — overkill para um servidor; exige cluster, ingress, operadores.
- Nomad/Swarm — menos padrão de mercado para o time.

## Consequências
- (+) `docker compose up` entrega o produto inteiro; barreira de entrada mínima.
- (+) Caminho de migração para K8s preservado (imagens já prontas no GHCR).
- (−) Escala horizontal e HA exigem trabalho manual até a Fase 7.

# ADR 005 — Docker Compose antes de Kubernetes

- **Status:** aceito
- **Referência:** [ARQUITETURA.md §5](../ARQUITETURA.md) (decisões de arquitetura)

## Contexto
O alvo inicial é departamental/single-host. Adotar Kubernetes cedo custaria complexidade
operacional sem retorno proporcional.

## Decisão
Empacotar e operar tudo com **Docker Compose** (dev e prod). No exemplo de produção, o deploy
sai de um push: um runner self-hosted do GitHub Actions no próprio host compila e sobe o stack
(`deploy/prod/bootstrap.sh`). K8s/GitOps só entram **quando o Compose limitar de fato**.

## Alternativas consideradas
- Kubernetes desde já — overkill para um servidor; exige cluster, ingress, operadores.
- Nomad/Swarm — menos padrão de mercado para o time.

## Consequências
- (+) `docker compose up` entrega o produto inteiro; barreira de entrada mínima.
- (+) Caminho de migração para K8s preservado: cada serviço já roda em contêiner próprio.
- (−) Escala horizontal e HA exigem trabalho manual enquanto o deploy for Compose.

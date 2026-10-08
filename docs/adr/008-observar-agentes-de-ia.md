# ADR 008 — Observar agentes de IA, sem embarcar IA

- **Status:** aceito
- **Data:** 2026-10-07
- **Referência:** [ADR 007](007-sem-ia-no-escopo.md) · [docs/instrumentacao-ia.md](../instrumentacao-ia.md)

## Contexto
O ADR 007 tirou IA do escopo: nada de detecção de anomalia, root cause ou assistente
embarcado, por custo, privacidade e manutenção. Ele deixou a porta aberta para "demanda
real e mensurável".

A demanda apareceu do outro lado. Quem usa o Revoada passou a rodar **agentes de IA em
produção** — chatbots, automações com ferramentas, pipelines que chamam LLM — e precisa
saber o que já sabe sobre o resto da infraestrutura: quanto custa, quanto demora, onde
falha, o que o agente fez em cada passo. Essas aplicações já emitem essa telemetria por
OpenTelemetry (convenção GenAI, OpenLLMetry, OpenInference), e o gateway do Revoada já
recebe OTLP. Hoje esses spans chegam e se perdem: atributos de lista viram vazio, o
span do OpenInference estoura o teto de 64 rótulos e é recusado, a amostragem de 20%
torna qualquer soma de custo falsa, e o prompt fica em texto puro em `spans.labels`.

## Decisão
**O Revoada observa aplicações de IA, mas não usa IA.** O gateway reconhece os spans de
chamadas de LLM, agente e ferramenta, normaliza os três dialetos num formato só e grava
custo, tokens, latência, erro e — se o operador ligar — o conteúdo redigido. Nenhuma
parte do Revoada chama um modelo, guarda chave de provedor ou manda telemetria para fora.

## Alternativas consideradas
- **Manter fora do escopo** — o recurso continua existindo na prática (os spans já
  chegam), só que quebrado e vazando prompt nos rótulos. Não é neutro.
- **Ler tudo de `spans.labels`** — custo e tendência precisam de meses e a tabela de
  spans guarda 15 dias; somar tokens convertendo texto de um `Map` em toda consulta não
  escala. Por isso há tabelas próprias (`genai_spans`, `genai_1m`, `genai_conteudo`).
- **Replay que reexecuta a chamada (playground)** — exigiria chave de API e chamada ao
  provedor, exatamente o que o ADR 007 recusa. O replay é visual, e o botão "Copiar como
  requisição" entrega o JSON para a pessoa rodar onde quiser.
- **Avaliação de qualidade (LLM-as-judge)** — exigiria chamar um modelo. Fora.
- **Proxy de LLM no caminho da chamada** — o Revoada passaria a ser ponto de falha das
  aplicações do cliente. Fora: ele só recebe telemetria.

## Consequências
- (+) Custo, latência, erro e passo a passo de agentes ao lado de CPU, logs e deploys do
  mesmo host — a correlação que ferramentas dedicadas a LLM não têm.
- (+) Corrige de quebra três perdas que afetavam todo trace (lista virando vazio, span
  recusado pelo teto de rótulos, prompt nos rótulos).
- (−) A convenção GenAI do OpenTelemetry ainda não é estável. O normalizador
  (`core/genai`) é testado contra spans gravados de bibliotecas reais
  (`tests/genai-fixtures`) e precisa ser revisto quando elas mudarem.
- (−) Custo é **estimativa** pela tabela de preços (com vigência); a tela sempre diz a
  data do preço usado e mostra "sem preço" em vez de inventar zero.
- (−) Conteúdo de prompt é dado pessoal. Fica desligado por padrão, com redação, prazo
  de 7 dias, permissão própria e leitura auditada.
- O ADR 007 continua valendo para tudo que **usa** IA dentro do Revoada.

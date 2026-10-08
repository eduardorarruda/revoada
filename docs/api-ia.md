# API de agentes de IA

Rotas do painel para a tela **Agentes de IA** e para o MCP. Todas exigem login e
respeitam o escopo de servidores do usuário (quem só vê o servidor `web-01` só vê as
chamadas de IA que chegaram com a chave dele). Datas em milissegundos Unix (`*_ms`);
dinheiro em dólar (`*_usd`); `null` significa **não informado / sem preço** — nunca zero.

Parâmetros comuns de consulta: `from`, `to` (RFC 3339 ou epoch; padrão: última 1 h),
`service`, `agente`, `modelo`, `host`.

## Conceitos

- **Chamada de modelo**: operação `chat`, `text_completion`, `generate_content` ou
  `embeddings`. É a que consome tokens e custa dinheiro.
- **Execução**: um trace que contém ao menos um span de IA. O agente da execução é o do
  span `invoke_agent` mais alto; sem ele, o `service`.
- **Custo**: `custo_origem` diz de onde veio — `informado` (a biblioteca mandou),
  `estimado` (tokens × tabela de preços, com `preco_data`), `sem_preco` (há tokens mas
  o modelo não tem preço cadastrado) ou `sem_tokens` (a biblioteca não informou tokens).

## `GET /api/ia/resumo`

```jsonc
{
  "janela": { "from_ms": 0, "to_ms": 0, "passo_s": 60 },
  "totais": {
    "chamadas": 3214,              // chamadas de modelo
    "erros": 58,
    "taxa_erro": 0.018,
    "execucoes": 412,
    "ferramentas_chamadas": 901,
    "ferramentas_erros": 12,
    "tokens_entrada": 1840000,
    "tokens_saida": 220000,
    "tokens_cache_leitura": 900000,
    "sem_tokens": 14,              // chamadas de modelo sem tokens informados
    "custo_usd": 12.4,             // informado + estimado; null se nada pôde ser calculado
    "custo_informado_usd": 0.0,
    "custo_parcial": true,         // true quando houve chamada sem preço ou sem tokens
    "chamadas_sem_preco": 3,
    "latencia_p50_ms": 820, "latencia_p95_ms": 4200, "latencia_p99_ms": 7100
  },
  "serie": [ { "ts_ms": 0, "custo_usd": 0.12, "chamadas": 40, "erros": 1 } ],
  "por_modelo": [
    { "provedor": "openai", "modelo": "gpt-4o-mini-2024-07-18", "chamadas": 3000, "erros": 50,
      "tokens_entrada": 0, "tokens_saida": 0, "custo_usd": 1.2, "sem_preco": false,
      "latencia_p95_ms": 3900,
      "preco": { "entrada_por_1m": 0.15, "saida_por_1m": 0.6, "vigente_desde": "2025-10-01T00:00:00Z", "origem": "referencia-2025-10" } }
  ],
  "por_agente": [ { "agente": "Atendente", "service": "bot", "chamadas": 0, "erros": 0, "custo_usd": 0.0, "tokens_entrada": 0, "tokens_saida": 0 } ],
  "ferramentas": [ { "ferramenta": "buscar_pedido", "chamadas": 0, "erros": 0, "latencia_p95_ms": 0 } ],
  "precos": { "referencia": "2025-10", "dias_desde_atualizacao": 370, "desatualizada": true }
}
```

## `GET /api/ia/execucoes`

Filtros extras: `status=erro`, `custo_min` (dólar), `conversa_id`, `limit` (≤ 500).

```jsonc
{ "execucoes": [ {
  "trace_id": "4bf9…", "inicio_ms": 0, "duracao_ms": 5400, "service": "bot", "host": "web-01",
  "agente": "Atendente", "conversa_id": "c-123",
  "chamadas_modelo": 3, "chamadas_ferramenta": 2, "erros": 0,
  "tokens_entrada": 4200, "tokens_saida": 300, "custo_usd": 0.0021, "custo_parcial": false,
  "modelos": ["gpt-4o-mini-2024-07-18"], "status": "ok"     // "ok" | "erro"
} ] }
```

## `GET /api/ia/execucoes/{trace_id}`

O replay. `passos` vem em ordem de início; `profundidade` é o nível na árvore de spans
de IA (0 = raiz). `repeticoes` aponta a mesma ferramenta chamada várias vezes seguidas
(sinal de agente em loop).

```jsonc
{
  "trace_id": "4bf9…", "agente": "Atendente", "service": "bot", "host": "web-01",
  "conversa_id": "c-123", "inicio_ms": 0, "duracao_ms": 5400,
  "totais": { "chamadas_modelo": 3, "chamadas_ferramenta": 2, "erros": 0,
              "tokens_entrada": 4200, "tokens_saida": 300, "custo_usd": 0.0021, "custo_parcial": false },
  "passos": [ {
    "span_id": "a1", "parent_span_id": "", "ts_ms": 0, "duracao_ms": 900, "profundidade": 1,
    "operacao": "chat", "nome": "chat gpt-4o-mini", "provedor": "openai", "modelo": "gpt-4o-mini",
    "agente": "", "ferramenta": "", "chamada_id": "",
    "tokens_entrada": 120, "tokens_saida": 30, "tokens_cache_leitura": 100,
    "custo_usd": 0.00003, "custo_origem": "estimado", "preco_data": "2025-10-01",
    "erro": "", "motivos_fim": ["tool_calls"], "com_conteudo": true
  } ],
  "repeticoes": [ { "ferramenta": "buscar_pedido", "vezes": 6, "primeiro_span_id": "b7" } ],
  "conteudo": { "disponivel": true, "pode_ver": true }
}
```

## `GET /api/ia/execucoes/{trace_id}/conteudo`

Exige a permissão **ver conteúdo de IA** (admin e operador), que é crítica: 2FA e
reautenticação recente (403 com `codigo: "reautenticacao_necessaria"` até confirmar a
identidade). Cada leitura entra na trilha de auditoria. 403 para leitor; 404 se não
houver conteúdo gravado.

```jsonc
{ "mensagens": [ { "span_id": "a1", "lado": "entrada", "papel": "user", "ordem": 0,
                   "texto": "…", "truncado": false, "redigido": true } ] }
```

## `GET /api/ia/ferramentas`

```jsonc
{ "ferramentas": [ { "ferramenta": "buscar_pedido", "chamadas": 120, "erros": 6, "taxa_erro": 0.05,
    "latencia_p50_ms": 80, "latencia_p95_ms": 410,
    "erros_frequentes": [ { "erro": "TimeoutError", "vezes": 5 } ] } ] }
```

## Preços

- `GET /api/ia/precos` — `{ "precos": [Preco], "modelos_sem_preco": [{ "provedor", "modelo", "chamadas" }], "referencia": { … } }`
- `POST /api/ia/precos` (admin) — cria uma linha (uma troca de preço é uma linha nova
  com `vigente_desde` posterior; o passado continua calculado com o preço antigo).
- `DELETE /api/ia/precos/{id}` (admin).

```jsonc
// Preco
{ "id": 1, "provedor": "openai", "modelo": "gpt-4o-mini*", "entrada_por_1m": 0.15, "saida_por_1m": 0.6,
  "cache_leitura_por_1m": 0.075, "cache_escrita_por_1m": null, "moeda": "USD",
  "vigente_desde": "2025-10-01T00:00:00Z", "origem": "referencia-2025-10" }
```

`modelo` aceita `*` no fim como prefixo (`gpt-4o-mini*` cobre `gpt-4o-mini-2024-07-18`).

## `POST /api/ia/purge` (admin, com 2FA e reautenticação recente)

Apaga chamadas e conteúdo. Corpo: `{ "alvo": "conversa" | "trace" | "todo_conteudo", "valor": "…", "frase": "…" }`.
`todo_conteudo` exige a frase `APAGAR TODO O CONTEÚDO DE IA`.

O que some e o que fica:

- `trace` / `conversa`: as linhas de `genai_spans` e o conteúdo daquelas execuções. Elas
  saem da lista, do replay e de toda conta feita sobre as linhas (filtro por agente ou
  conversa, custo por agente, ferramentas). O **agregado por minuto** (`genai_1m`, base da
  visão geral sem filtro e da tabela de modelos) continua contando aquelas chamadas: ele
  só guarda somas por minuto e modelo, sem texto, trace ou id de conversa — não é dado
  pessoal, e apagar parte de uma soma não é possível sem reconstruí-la.
- `todo_conteudo`: só o texto gravado; custo, tokens e passos continuam.
- O span genérico do trace (`spans`, tela Traces) não é tocado — ele já não guarda o
  conteúdo de IA, que o gateway retira dos rótulos.

As remoções do ClickHouse são assíncronas: a resposta é 202 e as linhas somem em segundos.

## Séries para alertas

O painel grava a cada minuto em `metrics` (rótulos `service`, `agente`, `modelo`,
`provedor`), com 5 minutos de atraso para esperar spans de agentes longos:

| Série | O que mede |
|---|---|
| `llm.custo_usd` | custo do minuto (informado + estimado) |
| `llm.chamadas` · `llm.erros` | chamadas de modelo e as que falharam |
| `llm.tokens.entrada` · `llm.tokens.saida` | tokens do minuto |
| `llm.latencia_p95_ms` | p95 das chamadas do minuto |
| `llm.execucao.passos_max` | maior nº de chamadas numa execução que terminou no minuto |
| `llm.sem_preco` | chamadas de modelo sem preço cadastrado |

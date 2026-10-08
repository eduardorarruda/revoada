// Dados de exemplo das telas de IA, só para os testes (nenhuma tela importa isto).
// Seguem os exemplos de docs/api-ia.md.
import type { IaExecucao, IaExecucaoDetalhe, IaPasso, IaResumo, IaTotais } from "../../api.ia";

export function totais(over: Partial<IaTotais> = {}): IaTotais {
  return {
    chamadas: 3214,
    erros: 58,
    taxa_erro: 0.018,
    execucoes: 412,
    ferramentas_chamadas: 901,
    ferramentas_erros: 12,
    tokens_entrada: 1_840_000,
    tokens_saida: 220_000,
    tokens_cache_leitura: 900_000,
    sem_tokens: 14,
    custo_usd: 12.4,
    custo_informado_usd: 0,
    custo_parcial: true,
    chamadas_sem_preco: 3,
    latencia_p50_ms: 820,
    latencia_p95_ms: 4200,
    latencia_p99_ms: 7100,
    ...over,
  };
}

export function resumo(over: Partial<IaResumo> = {}): IaResumo {
  return {
    janela: { from_ms: 0, to_ms: 3_600_000, passo_s: 60 },
    totais: totais(),
    serie: [
      { ts_ms: 0, custo_usd: 0.12, chamadas: 40, erros: 1 },
      { ts_ms: 60_000, custo_usd: null, chamadas: 2, erros: 0 },
    ],
    por_modelo: [
      {
        provedor: "openai",
        modelo: "gpt-4o-mini-2024-07-18",
        chamadas: 3000,
        erros: 50,
        tokens_entrada: 1_800_000,
        tokens_saida: 200_000,
        custo_usd: 1.2,
        sem_preco: false,
        latencia_p95_ms: 3900,
        preco: { entrada_por_1m: 0.15, saida_por_1m: 0.6, vigente_desde: "2025-10-01T00:00:00Z", origem: "referencia-2025-10" },
      },
      {
        provedor: "acme",
        modelo: "acme-large",
        chamadas: 3,
        erros: 0,
        tokens_entrada: 900,
        tokens_saida: 100,
        custo_usd: null,
        sem_preco: true,
        latencia_p95_ms: null,
        preco: null,
      },
    ],
    por_agente: [
      { agente: "Atendente", service: "bot", chamadas: 2000, erros: 10, custo_usd: 9.1, tokens_entrada: 1, tokens_saida: 1 },
      { agente: "Resumidor", service: "jobs", chamadas: 900, erros: 0, custo_usd: 3.3, tokens_entrada: 1, tokens_saida: 1 },
    ],
    ferramentas: [
      { ferramenta: "buscar_pedido", chamadas: 120, erros: 6, latencia_p95_ms: 410 },
      { ferramenta: "clima", chamadas: 30, erros: 0, latencia_p95_ms: null },
    ],
    precos: { referencia: "2025-10", dias_desde_atualizacao: 370, desatualizada: true },
    ...over,
  };
}

export function execucao(over: Partial<IaExecucao> = {}): IaExecucao {
  return {
    trace_id: "4bf92f3577b34da6",
    inicio_ms: Date.UTC(2026, 9, 7, 13, 0, 0),
    duracao_ms: 5400,
    service: "bot",
    host: "web-01",
    agente: "Atendente",
    conversa_id: "c-123",
    chamadas_modelo: 3,
    chamadas_ferramenta: 2,
    erros: 0,
    tokens_entrada: 4200,
    tokens_saida: 300,
    custo_usd: 0.0021,
    custo_parcial: false,
    modelos: ["gpt-4o-mini-2024-07-18"],
    status: "ok",
    ...over,
  };
}

export function passo(over: Partial<IaPasso> = {}): IaPasso {
  return {
    span_id: "a1",
    parent_span_id: "",
    ts_ms: 0,
    duracao_ms: 900,
    profundidade: 1,
    operacao: "chat",
    nome: "chat gpt-4o-mini",
    provedor: "openai",
    modelo: "gpt-4o-mini",
    agente: "",
    ferramenta: "",
    chamada_id: "",
    tokens_entrada: 120,
    tokens_saida: 30,
    tokens_cache_leitura: 100,
    custo_usd: 0.00003,
    custo_origem: "estimado",
    preco_data: "2025-10-01",
    erro: "",
    motivos_fim: ["tool_calls"],
    com_conteudo: true,
    ...over,
  };
}

const ferramenta = (span_id: string, ts_ms: number, over: Partial<IaPasso> = {}): IaPasso =>
  passo({
    span_id,
    ts_ms,
    duracao_ms: 200,
    profundidade: 2,
    operacao: "execute_tool",
    nome: "execute_tool clima",
    provedor: "",
    modelo: "",
    ferramenta: "clima",
    tokens_entrada: null,
    tokens_saida: null,
    tokens_cache_leitura: null,
    custo_usd: null,
    custo_origem: "",
    preco_data: "",
    motivos_fim: [],
    com_conteudo: false,
    ...over,
  });

/** Agente → modelo → clima ×3 (com modelo entre elas) → modelo com erro. */
export function detalhe(over: Partial<IaExecucaoDetalhe> = {}): IaExecucaoDetalhe {
  return {
    trace_id: "4bf92f3577b34da6",
    agente: "Atendente",
    service: "bot",
    host: "web-01",
    conversa_id: "c-123",
    inicio_ms: 1000,
    duracao_ms: 5400,
    totais: {
      chamadas_modelo: 3,
      chamadas_ferramenta: 3,
      erros: 1,
      tokens_entrada: 4200,
      tokens_saida: 300,
      custo_usd: null,
      custo_parcial: true,
    },
    passos: [
      passo({ span_id: "r0", ts_ms: 1000, duracao_ms: 5400, profundidade: 0, operacao: "invoke_agent", nome: "invoke_agent Atendente", agente: "Atendente", modelo: "", provedor: "", tokens_entrada: null, tokens_saida: null, tokens_cache_leitura: null, custo_usd: null, custo_origem: "", preco_data: "", motivos_fim: [], com_conteudo: false }),
      passo({ span_id: "a1", ts_ms: 1100 }),
      ferramenta("b7", 2000),
      passo({ span_id: "a2", ts_ms: 2300, custo_usd: null, custo_origem: "sem_preco", preco_data: "" }),
      ferramenta("b8", 2600),
      ferramenta("b9", 2900),
      passo({ span_id: "a3", ts_ms: 3200, erro: "RateLimitError: 429", custo_usd: null, custo_origem: "sem_tokens", tokens_entrada: null, tokens_saida: null, tokens_cache_leitura: null }),
    ],
    repeticoes: [{ ferramenta: "clima", vezes: 3, primeiro_span_id: "b7" }],
    conteudo: { disponivel: true, pode_ver: true },
    ...over,
  };
}

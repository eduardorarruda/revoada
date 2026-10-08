import { beforeEach, describe, expect, it, vi } from "vitest";

// O cliente de IA é fino: o que importa travar é o CAMINHO e o CORPO que ele monta,
// porque é ali que front e back combinam o contrato (docs/api-ia.md).
const api = vi.fn();
vi.mock("./api", () => ({ api: (...a: unknown[]) => api(...a) }));

const ia = await import("./api.ia");

beforeEach(() => {
  api.mockReset();
  api.mockResolvedValue({});
});

describe("queryIa", () => {
  it("omite parâmetros vazios e codifica os demais", () => {
    expect(ia.queryIa({ agente: "Atendente Fiscal", modelo: "", host: undefined, custo_min: 0.5 })).toBe(
      "?agente=Atendente+Fiscal&custo_min=0.5",
    );
  });

  it("sem filtro, sem interrogação", () => {
    expect(ia.queryIa({})).toBe("");
  });
});

describe("rotas de leitura", () => {
  it("resumo e ferramentas levam a janela na query", async () => {
    await ia.getIaResumo({ from: "2026-10-07T10:00:00.000Z", to: "2026-10-07T11:00:00.000Z" });
    await ia.listIaFerramentas({ agente: "bot" });
    expect(api).toHaveBeenNthCalledWith(1, "/api/ia/resumo?from=2026-10-07T10%3A00%3A00.000Z&to=2026-10-07T11%3A00%3A00.000Z");
    expect(api).toHaveBeenNthCalledWith(2, "/api/ia/ferramentas?agente=bot");
  });

  it("execuções aceitam os filtros extras do contrato", async () => {
    await ia.listIaExecucoes({ status: "erro", custo_min: 0.01, limit: 200 });
    expect(api).toHaveBeenCalledWith("/api/ia/execucoes?status=erro&custo_min=0.01&limit=200");
  });

  it("replay e conteúdo escapam o trace_id", async () => {
    await ia.getIaExecucao("ab/cd");
    await ia.getIaConteudo("ab/cd");
    expect(api).toHaveBeenNthCalledWith(1, "/api/ia/execucoes/ab%2Fcd");
    expect(api).toHaveBeenNthCalledWith(2, "/api/ia/execucoes/ab%2Fcd/conteudo");
  });
});

describe("rotas de escrita", () => {
  it("criar preço faz POST com a linha inteira", async () => {
    const novo = {
      provedor: "openai",
      modelo: "gpt-4o-mini*",
      entrada_por_1m: 0.15,
      saida_por_1m: 0.6,
      cache_leitura_por_1m: null,
      cache_escrita_por_1m: null,
      moeda: "USD",
      vigente_desde: "2025-10-01T00:00:00.000Z",
    };
    await ia.criarIaPreco(novo);
    const [caminho, init] = api.mock.calls[0] as [string, RequestInit];
    expect(caminho).toBe("/api/ia/precos");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual(novo);
  });

  it("apagar preço é DELETE pelo id", async () => {
    await ia.apagarIaPreco(7);
    expect(api).toHaveBeenCalledWith("/api/ia/precos/7", { method: "DELETE" });
  });

  it("purge de todo o conteúdo manda a frase exigida", async () => {
    await ia.purgeIa({ alvo: "todo_conteudo", frase: ia.FRASE_PURGE_TODO_CONTEUDO });
    const [caminho, init] = api.mock.calls[0] as [string, RequestInit];
    expect(caminho).toBe("/api/ia/purge");
    expect(JSON.parse(String(init.body))).toEqual({ alvo: "todo_conteudo", frase: "APAGAR TODO O CONTEÚDO DE IA" });
  });
});

import { describe, it, expect } from "vitest";
import { aggLabel, aggSuffix, panelFootnote, smoothingWarning, sourceLabel, unitForAgg } from "./resolution";

describe("agregação escrita em português", () => {
  it("traduz os códigos oferecidos no Explore", () => {
    expect(aggLabel("avg")).toBe("média");
    expect(aggLabel("max")).toBe("máximo");
    expect(aggLabel("min")).toBe("mínimo");
    expect(aggLabel("sum")).toBe("soma");
    expect(aggLabel("last")).toBe("último valor");
  });

  it("sem agregação informada assume média (o default do backend)", () => {
    expect(aggLabel(undefined)).toBe("média");
    expect(aggLabel(null)).toBe("média");
  });

  it("agregação desconhecida não some nem quebra a tela", () => {
    expect(aggLabel("p99")).toBe("p99");
  });

  it("o sufixo do título marca só o que engana — média fica implícita", () => {
    expect(aggSuffix("avg")).toBe("");
    expect(aggSuffix(undefined)).toBe("");
    expect(aggSuffix("max")).toBe(", máximo");
    expect(aggSuffix("last")).toBe(", último valor");
  });
});

describe("resolução efetiva vinda do campo `table` da Query API", () => {
  it("nomeia a fonte de cada tabela", () => {
    expect(sourceLabel("metrics")).toBe("dado bruto");
    expect(sourceLabel("metrics_1m")).toBe("resumo de 1 min");
    expect(sourceLabel("metrics_1h")).toBe("resumo de 1 h");
  });

  it("tabela ausente/desconhecida simplesmente não gera texto", () => {
    expect(sourceLabel(undefined)).toBe("");
    expect(sourceLabel("outra_coisa")).toBe("");
  });

  it("avisa o que se perde em cada rollup; dado bruto não perde nada", () => {
    expect(smoothingWarning("metrics")).toBe("");
    expect(smoothingWarning("metrics_1m")).toContain("1 min");
    expect(smoothingWarning("metrics_1h")).toContain("1 h");
  });
});

describe("nota de rodapé do painel", () => {
  it("junta agregação, passo e fonte", () => {
    expect(panelFootnote({ agg: "max", table: "metrics_1m", step: 300 })).toBe(
      "Máximo a cada 5 min · fonte: resumo de 1 min, picos com menos de 1 min ficam suavizados",
    );
  });

  it("dado bruto não recebe aviso de suavização", () => {
    expect(panelFootnote({ agg: "avg", table: "metrics", step: 60 })).toBe("Média a cada 1 min · fonte: dado bruto");
  });

  it("janela de 30 dias avisa que o pico de 1 min sumiu", () => {
    const nota = panelFootnote({ agg: "avg", table: "metrics_1h", step: 3600 });
    expect(nota).toContain("resumo de 1 h");
    expect(nota).toContain("suavizados");
  });

  it("sem table e sem step ainda diz a agregação", () => {
    expect(panelFootnote({ agg: "sum" })).toBe("Soma por ponto");
  });
});

describe("unitForAgg — a unidade depende da agregação, não só da métrica", () => {
  it("rate sobre bytes vira bytes por segundo", () => {
    // Sem isto o painel de rede mostraria "12,3 MB" onde o valor significa
    // "12,3 MB/s" — a diferença entre tráfego trivial e link saturado.
    expect(unitForAgg("bytes", "rate")).toBe("bytes_per_s");
  });

  it("as outras agregações não mexem na unidade", () => {
    for (const agg of ["avg", "max", "min", "sum", "last", undefined, null]) {
      expect(unitForAgg("bytes", agg)).toBe("bytes");
      expect(unitForAgg("percent", agg)).toBe("percent");
    }
  });

  it("rate sobre unidade que não é bytes fica como está", () => {
    // Não existe "percent por segundo"; inventar unidade seria pior que manter.
    expect(unitForAgg("percent", "rate")).toBe("percent");
    expect(unitForAgg("none", "rate")).toBe("none");
  });

  it("a taxa tem rótulo em português no rodapé do painel", () => {
    expect(aggLabel("rate")).toBe("taxa");
  });
});

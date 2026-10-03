import { describe, it, expect } from "vitest";
import {
  inferScope,
  filterValue,
  setFilter,
  isContainerMetric,
  DEFAULT_CONTAINER_METRIC,
} from "./dashboardScope";

// Fase F — o escopo do painel é uma camada amigável sobre `filters`/`metric`.
// Estes helpers puros são a ponte entre a UI de escopo e o `query.filters` salvo.

describe("inferScope", () => {
  it("sem filtro de container → servidor inteiro (padrão)", () => {
    expect(inferScope([])).toBe("server");
    expect(inferScope([{ label: "host", value: "srv-01" }])).toBe("server");
  });

  it("com filtro de container preenchido → container específico", () => {
    expect(inferScope([{ label: "container", value: "nginx" }])).toBe("container");
  });

  it("filtro de container vazio não conta como container", () => {
    expect(inferScope([{ label: "container", value: "" }])).toBe("server");
  });
});

describe("setFilter / filterValue", () => {
  it("define um filtro por rótulo preservando os demais", () => {
    const rows = setFilter([{ label: "host", value: "srv-01" }], "container", "nginx");
    expect(filterValue(rows, "host")).toBe("srv-01");
    expect(filterValue(rows, "container")).toBe("nginx");
  });

  it("valor vazio remove o filtro (sem duplicar)", () => {
    const rows = setFilter([{ label: "container", value: "nginx" }], "container", "");
    expect(rows.find((r) => r.label === "container")).toBeUndefined();
  });

  it("substitui o valor existente em vez de duplicar a linha", () => {
    const rows = setFilter([{ label: "host", value: "srv-01" }], "host", "srv-02");
    expect(rows).toHaveLength(1);
    expect(filterValue(rows, "host")).toBe("srv-02");
  });
});

describe("isContainerMetric", () => {
  it("reconhece métricas de container", () => {
    expect(isContainerMetric(DEFAULT_CONTAINER_METRIC)).toBe(true);
    expect(isContainerMetric("container.memory.utilization")).toBe(true);
    expect(isContainerMetric("system.cpu.utilization")).toBe(false);
  });
});

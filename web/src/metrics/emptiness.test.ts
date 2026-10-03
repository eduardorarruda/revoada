import { describe, it, expect } from "vitest";
import { hasAnyValue, isEmptyResult, panelStateFor } from "./emptiness";

describe("estado vazio do painel", () => {
  it("resposta SEM séries é vazia, mesmo com eixo do tempo cheio", () => {
    // Caso medido: /api/tv/query para host inexistente devolveu 5 baldes em `ts`
    // e `series: []`. Pelo critério antigo (`ts.length`) isso virava "ok".
    expect(isEmptyResult([])).toBe(true);
    expect(panelStateFor([])).toBe("empty");
  });

  it("grade densa só de nulos é vazia", () => {
    expect(isEmptyResult([{ values: [null, null, null] }])).toBe(true);
  });

  it("um único ponto real já basta para não ser vazio", () => {
    expect(isEmptyResult([{ values: [null, null, 0] }])).toBe(false);
    expect(panelStateFor([{ values: [null, 42] }])).toBe("ok");
  });

  it("zero é valor legítimo — não conta como ausência", () => {
    expect(hasAnyValue([{ values: [0] }])).toBe(true);
  });

  it("NaN e Infinity não são medida", () => {
    expect(hasAnyValue([{ values: [NaN, Infinity, -Infinity] }])).toBe(false);
  });

  it("basta uma série entre várias ter valor", () => {
    expect(hasAnyValue([{ values: [null] }, { values: [] }, { values: [7] }])).toBe(true);
  });

  it("tolera undefined/null vindos da API", () => {
    expect(isEmptyResult(undefined)).toBe(true);
    expect(isEmptyResult(null)).toBe(true);
    expect(isEmptyResult([{ values: undefined }])).toBe(true);
  });
});

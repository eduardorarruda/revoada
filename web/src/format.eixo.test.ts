import { describe, expect, it } from "vitest";
import { rotulosEixo } from "./format";

// Eixo de CPU entre 99% e 100% saía "100% 100% 100% 99%": o formato compacto não
// tem casas decimais e rótulos iguais em alturas diferentes mentem sobre a escala.
describe("rótulos do eixo", () => {
  it("ganha casas decimais só quando o compacto repetiria rótulos", () => {
    expect(rotulosEixo([99.4, 99.6, 99.8, 100], "percent")).toEqual(["99,4%", "99,6%", "99,8%", "100%"]);
    expect(rotulosEixo([0, 25, 50, 75, 100], "percent")).toEqual(["0%", "25%", "50%", "75%", "100%"]);
  });

  it("vai até duas casas se uma ainda repetir", () => {
    expect(new Set(rotulosEixo([99.91, 99.94, 99.97, 100], "percent")).size).toBe(4);
  });
});

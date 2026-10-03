import { describe, expect, it } from "vitest";
import { mapaParaTexto, textoParaMapa } from "./Parametros";

describe("mapa de valores", () => {
  it("lê uma linha por par e ignora linhas sem '='", () => {
    expect(textoParaMapa("S=true\n N = false \nlixo\n*=?")).toEqual({ S: "true", N: "false", "*": "?" });
  });

  it("mantém '=' dentro do valor", () => {
    expect(textoParaMapa("a=b=c")).toEqual({ a: "b=c" });
  });

  it("ida e volta", () => {
    const m = { S: "true", N: "false" };
    expect(textoParaMapa(mapaParaTexto(m))).toEqual(m);
  });
});

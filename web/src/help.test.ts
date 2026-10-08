import { help } from "./help";

describe("Guia", () => {
  it("Agentes de IA vem logo depois de Instrumentação, como no menu Investigar", () => {
    const telas = Object.keys(help.pages);
    expect(telas.indexOf("ia")).toBe(telas.indexOf("instrumentacao") + 1);
  });

  it("o glossário explica os termos de IA que a tela usa", () => {
    expect(Object.keys(help.concepts)).toEqual(expect.arrayContaining(["token", "execucao-ia", "custo-estimado"]));
  });
});

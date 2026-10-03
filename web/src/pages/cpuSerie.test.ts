import { cpuSerieDoHost } from "./cpuSerie";

const SOMA = "system.cpu.utilization.total";
const LEGADA = "system.cpu.utilization";

describe("qual série de CPU o gráfico pede por servidor", () => {
  it("0.8.1 em diante pede a soma, que só ele publica", () => {
    for (const v of ["0.8.1", "0.8.2", "0.9.0", "0.10.0", "1.0.0", "v0.8.1"]) {
      expect(cpuSerieDoHost(v)).toBe(SOMA);
    }
  });

  // A frota real em 10/08/2026: quatro hosts em 0.7.0. Pedir a soma para eles
  // desenharia um painel VAZIO — a série não existe. Neles a legada já é a soma.
  it("anterior à 0.8.1 pede a legada, que nesses agentes já inclui o roubo", () => {
    for (const v of ["0.8.0", "0.7.0", "0.1.0", "0.0.1"]) {
      expect(cpuSerieDoHost(v)).toBe(LEGADA);
    }
  });

  // Versão desconhecida cai no comportamento que a frota inteira tinha antes desta
  // mudança: no pior caso o gráfico fica como estava, nunca em branco.
  it("versão ausente ou ilegível cai na legada, nunca em gráfico vazio", () => {
    for (const v of [undefined, null, "", "   ", "latest", "0.8", "0.8.1-dirty", "a.b.c", "-1.0.0"]) {
      expect(cpuSerieDoHost(v)).toBe(LEGADA);
    }
  });
});

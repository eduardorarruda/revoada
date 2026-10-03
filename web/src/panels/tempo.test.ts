import { instante, rotulosTempo } from "./tempo";

// 24/09/2026 16:30 em Brasília = 19:30 UTC.
const T = Date.UTC(2026, 8, 24, 19, 30) / 1000;

describe("rótulos de tempo dos gráficos", () => {
  it("hora em 24h, no fuso de Brasília, com a data só quando o dia vira", () => {
    const splits = [T, T + 3600, T + 7 * 3600, T + 8 * 3600];
    expect(rotulosTempo(splits, 8 * 3600)).toEqual(["16:30\n24/09", "17:30", "23:30", "00:30\n25/09"]);
  });

  it("zoom abaixo de 1 min entre tiques mostra os segundos", () => {
    expect(rotulosTempo([T, T + 10, T + 20], 30)).toEqual(["16:30:00\n24/09", "16:30:10", "16:30:20"]);
  });

  it("janela longa mostra só a data", () => {
    expect(rotulosTempo([T, T + 86400], 7 * 86400)).toEqual(["24/09", "25/09"]);
  });

  it("nunca sai no formato americano", () => {
    const [r] = rotulosTempo([T], 3600);
    expect(r).not.toMatch(/am|pm|\/26/i);
  });

  it("instante completo para a legenda", () => {
    expect(instante(T + 5)).toBe("24/09, 16:30:05");
    expect(instante(null)).toBe("—");
  });
});

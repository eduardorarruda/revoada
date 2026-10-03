import { describe, it, expect } from "vitest";
import {
  APP_TIME_ZONE,
  fmtRelAbs,
  formatDate,
  formatDateTime,
  formatTime,
  formatUptimePercent,
} from "./format";

// Instante escolhido de propósito para CRUZAR a meia-noite: 00:30 UTC do dia 07 é
// 21:30 do dia 06 em Brasília. Qualquer formatador que use o fuso do navegador (ou
// UTC) falha aqui, independentemente da máquina onde o teste roda — é exatamente o
// erro que aparecia num kiosk/VM deixado em UTC.
const CRUZA_MEIA_NOITE = "2026-08-07T00:30:00Z";
// Meio-dia UTC = 09:00 em Brasília.
const MEIO_DIA_UTC = "2026-08-07T12:00:00Z";

describe("fuso horário fixo da operação", () => {
  it("é o de Brasília, não o do navegador", () => {
    expect(APP_TIME_ZONE).toBe("America/Sao_Paulo");
  });

  // O separador entre data e hora ("06/08/2026, 21:30") é escolha do ICU e varia
  // entre versões do Node; o que importa (e o que o defeito quebrava) é a CONVERSÃO.
  it("data e hora saem em horário de Brasília, mesmo quando isso muda o DIA", () => {
    const s = formatDateTime(CRUZA_MEIA_NOITE);
    expect(s).toContain("06/08/2026");
    expect(s).toContain("21:30:00");
    expect(s).not.toContain("07/08/2026"); // seria o dia em UTC
  });

  it("hora isolada também converte", () => {
    expect(formatTime(MEIO_DIA_UTC)).toBe("09:00:00");
    expect(formatTime(MEIO_DIA_UTC, { seconds: false })).toBe("09:00");
  });

  it("data isolada usa o dia de Brasília", () => {
    expect(formatDate(CRUZA_MEIA_NOITE)).toBe("06/08/2026");
    expect(formatDate(MEIO_DIA_UTC)).toBe("07/08/2026");
  });

  it("sem segundos quando pedido", () => {
    const s = formatDateTime(CRUZA_MEIA_NOITE, { seconds: false });
    expect(s).toContain("06/08/2026");
    expect(s).toContain("21:30");
    expect(s).not.toContain("21:30:00");
  });

  it("o tooltip diz QUAL fuso é — horário sem fuso é horário ambíguo", () => {
    const { abs } = fmtRelAbs(CRUZA_MEIA_NOITE);
    expect(abs).toContain("06/08/2026");
    expect(abs).toContain("21:30:00");
    expect(abs).toContain("horário de Brasília");
  });

  it("valor ausente ou inválido vira travessão, nunca 'Invalid Date'", () => {
    for (const f of [formatDateTime, formatTime, formatDate]) {
      expect(f(null)).toBe("—");
      expect(f(undefined)).toBe("—");
      expect(f("")).toBe("—");
      expect(f("não é data")).toBe("—");
    }
  });
});

describe("uptime nunca arredonda para cima", () => {
  it("um site que caiu não pode exibir 100%", () => {
    expect(formatUptimePercent(99.996)).toBe("99,99%");
    expect(formatUptimePercent(99.999999)).toBe("99,99%");
  });

  it("só quem ficou o período inteiro no ar mostra 100,00%", () => {
    expect(formatUptimePercent(100)).toBe("100,00%");
  });

  it("sempre 2 casas, com vírgula decimal (pt-BR)", () => {
    expect(formatUptimePercent(99.5)).toBe("99,50%");
    expect(formatUptimePercent(0)).toBe("0,00%");
    expect(formatUptimePercent(99.99)).toBe("99,99%"); // erro de ponto flutuante absorvido
  });

  it("sem dado vira travessão", () => {
    expect(formatUptimePercent(null)).toBe("—");
    expect(formatUptimePercent(undefined)).toBe("—");
    expect(formatUptimePercent(Number.NaN)).toBe("—");
  });
});

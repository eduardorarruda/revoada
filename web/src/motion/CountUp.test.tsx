import { separar, montar } from "./CountUp";

// O contador anima o número de dentro do texto já formatado. Se ele errar o formato
// brasileiro, o KPI mostra "47.2%" no meio da animação e "47,2%" no fim — o número
// pisca de formato na frente de quem lê.
describe("CountUp — formato pt-BR", () => {
  it.each([
    ["47,2%", 47.2, "", "%", 1],
    ["1/3", 1, "", "/3", 0],
    ["1.284 req/s", 1284, "", " req/s", 0],
    ["há 3 min", 3, "há ", " min", 0],
    ["-12,50 ms", -12.5, "", " ms", 2],
  ])("separa %s", (texto, numero, antes, depois, casas) => {
    const p = separar(texto)!;
    expect(p.numero).toBeCloseTo(numero);
    expect(p.antes).toBe(antes);
    expect(p.depois).toBe(depois);
    expect(p.casas).toBe(casas);
  });

  it("remonta no meio da animação com as mesmas casas e o mesmo milhar", () => {
    expect(montar(separar("47,2%")!, 12.345)).toBe("12,3%");
    expect(montar(separar("1.284 req/s")!, 1003.7)).toBe("1.004 req/s");
    expect(montar(separar("1/3")!, 0.6)).toBe("1/3");
  });

  it("texto sem número não é animado", () => {
    expect(separar("—")).toBeNull();
    expect(separar("sem dados")).toBeNull();
  });
});

import { act, render } from "@testing-library/react";
import { CountUp } from "./CountUp";

// Na TV, um coletor que some por um minuto e volta fazia o número correr de 0 de
// novo: parece um defeito de dado para quem olha a parede de longe.
describe("CountUp — dado que some e volta", () => {
  it("volta a partir do último valor medido, não do zero", () => {
    vi.useFakeTimers({ toFake: ["requestAnimationFrame", "cancelAnimationFrame", "performance"] });
    try {
      const { container, rerender } = render(<CountUp texto="47,2%" />);
      act(() => void vi.advanceTimersByTime(2000));
      expect(container.textContent).toBe("47,2%");

      rerender(<CountUp texto="—" />);
      act(() => void vi.advanceTimersByTime(50));
      expect(container.textContent).toBe("—");

      rerender(<CountUp texto="50,0%" />);
      act(() => void vi.advanceTimersByTime(20));
      const n = Number(container.textContent!.replace("%", "").replace(",", "."));
      expect(n).toBeGreaterThanOrEqual(47);
    } finally {
      vi.useRealTimers();
    }
  });
});

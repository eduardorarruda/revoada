import { fireEvent, render, screen } from "@testing-library/react";
import { acharJanela, intervaloDaJanela, JanelaTempo } from "./JanelaTempo";

describe("JanelaTempo", () => {
  it("marca a janela ativa e avisa a troca", () => {
    const onChange = vi.fn();
    render(<JanelaTempo valor="1h" onChange={onChange} />);
    expect(screen.getByRole("button", { name: "1 h" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "24 h" })).toHaveAttribute("aria-pressed", "false");
    fireEvent.click(screen.getByRole("button", { name: "24 h" }));
    expect(onChange).toHaveBeenCalledWith("24h");
  });

  it("id desconhecido cai na janela padrão (1 h)", () => {
    expect(acharJanela("abc").id).toBe("1h");
    expect(acharJanela(null).id).toBe("1h");
    expect(acharJanela("7d").frase).toBe("Últimos 7 dias");
  });

  it("intervalo termina agora e começa N segundos antes, em RFC 3339", () => {
    const agora = Date.UTC(2026, 9, 7, 12, 0, 0);
    expect(intervaloDaJanela(acharJanela("15m"), agora)).toEqual({
      from: "2026-10-07T11:45:00.000Z",
      to: "2026-10-07T12:00:00.000Z",
    });
  });
});

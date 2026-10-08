import { fireEvent, render, screen } from "@testing-library/react";
import { acharJanela, intervaloDaJanela, janelaParaAmpliar, JanelaTempo } from "./JanelaTempo";

/** Simula a tela estreita (o jsdom não tem matchMedia: sem ele, é desktop). */
function telaEstreita(estreita: boolean) {
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    writable: true,
    value: (q: string) => ({
      matches: estreita && q.includes("max-width"),
      media: q,
      addEventListener: () => {},
      removeEventListener: () => {},
    }),
  });
}

afterEach(() => {
  Reflect.deleteProperty(window, "matchMedia");
});

describe("JanelaTempo", () => {
  it("marca a janela ativa e avisa a troca", () => {
    const onChange = vi.fn();
    render(<JanelaTempo valor="1h" onChange={onChange} />);
    expect(screen.getByRole("button", { name: "1 h" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "24 h" })).toHaveAttribute("aria-pressed", "false");
    fireEvent.click(screen.getByRole("button", { name: "24 h" }));
    expect(onChange).toHaveBeenCalledWith("24h");
  });

  it("desktop: controle segmentado, sem botão primário, com a pílula só na opção ativa", () => {
    render(<JanelaTempo valor="6h" onChange={() => {}} />);
    const grupo = screen.getByRole("group", { name: "Janela de tempo" });
    expect(grupo).toHaveClass("janela-seg");
    const ativa = screen.getByRole("button", { name: "6 h" });
    expect(ativa).toHaveClass("janela-seg__opcao--ativa");
    expect(ativa).not.toHaveClass("btn--primary");
    expect(grupo.querySelectorAll(".janela-seg__pilula")).toHaveLength(1);
    expect(ativa.querySelector(".janela-seg__pilula")).not.toBeNull();
  });

  it("celular: vira um <select> nativo com a frase de cada janela", () => {
    telaEstreita(true);
    const onChange = vi.fn();
    render(<JanelaTempo valor="1h" onChange={onChange} />);
    expect(screen.queryByRole("group")).not.toBeInTheDocument();
    const select = screen.getByRole("combobox", { name: "Janela de tempo" });
    expect(select).toHaveValue("1h");
    expect(screen.getByRole("option", { name: "Últimos 7 dias" })).toBeInTheDocument();
    fireEvent.change(select, { target: { value: "7d" } });
    expect(onChange).toHaveBeenCalledWith("7d");
  });

  it("janela para ampliar só quando é maior que a atual", () => {
    expect(janelaParaAmpliar(acharJanela("1h"), "24h")?.id).toBe("24h");
    expect(janelaParaAmpliar(acharJanela("24h"), "24h")).toBeNull();
    expect(janelaParaAmpliar(acharJanela("30d"), "7d")).toBeNull();
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

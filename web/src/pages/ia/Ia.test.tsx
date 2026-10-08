import { fireEvent, render, screen } from "@testing-library/react";

// A casca só decide QUAL vista mostrar e carrega a janela de tempo pela URL; as
// vistas são trocadas por marcadores que exibem o que receberam.
vi.mock("./VisaoGeral", () => ({ VisaoGeral: ({ janela }: { janela: { id: string } }) => <p>visão {janela.id}</p> }));
vi.mock("./Execucoes", () => ({
  Execucoes: ({ params }: { params: URLSearchParams }) => <p>execuções agente={params.get("agente")}</p>,
}));
vi.mock("./Replay", () => ({ Replay: ({ traceId }: { traceId: string }) => <p>replay {traceId}</p> }));
vi.mock("./Ferramentas", () => ({ Ferramentas: () => <p>ferramentas</p> }));
vi.mock("./Precos", () => ({ Precos: () => <p>preços</p> }));

const { PaginaIa } = await import("./Ia");

afterEach(() => {
  window.location.hash = "";
});

describe("casca de Agentes de IA", () => {
  it("abas levam a janela e marcam a vista atual", () => {
    render(<PaginaIa rota="/ia" query="janela=24h" />);
    expect(screen.getByText("visão 24h")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Visão geral" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Ferramentas" })).toHaveAttribute("href", "#/ia/ferramentas?janela=24h");
  });

  it("o replay fica sob a aba Execuções e não mostra seletor de janela", () => {
    render(<PaginaIa rota="/ia/execucoes/abc123" query="" />);
    expect(screen.getByText("replay abc123")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Execuções" })).toHaveAttribute("aria-current", "page");
    expect(screen.queryByRole("group", { name: "Janela de tempo" })).not.toBeInTheDocument();
  });

  it("trocar a janela grava na URL, preservando os filtros", () => {
    render(<PaginaIa rota="/ia/execucoes" query="agente=Atendente" />);
    expect(screen.getByText("execuções agente=Atendente")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "7 dias" }));
    expect(window.location.hash).toBe("#/ia/execucoes?agente=Atendente&janela=7d");
  });
});

describe("filtros vindos da URL", () => {
  it("um link que troca só o filtro remonta Execuções com o filtro novo", () => {
    const { rerender } = render(<PaginaIa rota="/ia/execucoes" query="agente=Atendente" />);
    rerender(<PaginaIa rota="/ia/execucoes" query="agente=Resumidor" />);
    expect(screen.getByText("execuções agente=Resumidor")).toBeInTheDocument();
  });
});

describe("menu lateral", () => {
  it("'Agentes de IA' está em Investigar, para todos, e acende em qualquer #/ia/*", async () => {
    const { NAV } = await import("../../components/AppShell");
    const grupo = NAV.find((g) => g.label === "Investigar");
    const item = grupo?.items.find((i) => i.href === "#/ia");
    expect(item?.label).toBe("Agentes de IA");
    expect(item?.adminOnly).toBeFalsy();
    expect(item?.match?.("/ia/execucoes/abc")).toBe(true);
    expect(item?.match?.("/iaxyz")).toBe(false);
  });
});

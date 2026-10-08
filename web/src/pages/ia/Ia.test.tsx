import { fireEvent, render, screen, within } from "@testing-library/react";

// A casca só decide QUAL vista mostrar e carrega a janela de tempo pela URL; as
// vistas são trocadas por marcadores que exibem o que receberam. A Visão geral
// falsa também conta à casca o estado de carga, como a de verdade faz.
const cargaFalsa = { carregando: false, dados: null as object | null, atualizadoEm: null as number | null };
vi.mock("./VisaoGeral", async () => {
  const { useSinalizarCarga } = await vi.importActual<typeof import("./comum")>("./comum");
  return {
    VisaoGeral: ({ janela }: { janela: { id: string } }) => {
      useSinalizarCarga(cargaFalsa);
      return <p>visão {janela.id}</p>;
    },
  };
});
vi.mock("./Execucoes", () => ({
  Execucoes: ({ params }: { params: URLSearchParams }) => <p>execuções agente={params.get("agente")}</p>,
}));
vi.mock("./Replay", () => ({ Replay: ({ traceId }: { traceId: string }) => <p>replay {traceId}</p> }));
vi.mock("./Ferramentas", () => ({ Ferramentas: () => <p>ferramentas</p> }));
vi.mock("./Precos", () => ({ Precos: () => <p>preços</p> }));

const { PaginaIa } = await import("./Ia");

afterEach(() => {
  window.location.hash = "";
  Object.assign(cargaFalsa, { carregando: false, dados: null, atualizadoEm: null });
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

describe("cabeçalho, abas e recarga", () => {
  it("trocar de aba não remonta o cabeçalho: só a vista troca, e o sublinhado acompanha a aba", () => {
    const { rerender } = render(<PaginaIa rota="/ia" query="" />);
    const titulo = screen.getByRole("heading", { level: 1, name: "Agentes de IA" });
    rerender(<PaginaIa rota="/ia/ferramentas" query="" />);
    expect(screen.getByRole("heading", { level: 1, name: "Agentes de IA" })).toBe(titulo);
    expect(screen.getByText("ferramentas")).toBeInTheDocument();
    const indicadores = document.querySelectorAll(".ia-aba__indicador");
    expect(indicadores).toHaveLength(1);
    expect(screen.getByRole("link", { name: "Ferramentas" })).toContainElement(indicadores[0] as HTMLElement);
  });

  it("a janela de tempo e o Atualizar moram nas ações do cabeçalho", () => {
    render(<PaginaIa rota="/ia" query="" />);
    const cabecalho = document.querySelector(".page-header") as HTMLElement;
    expect(within(cabecalho).getByRole("group", { name: "Janela de tempo" })).toBeInTheDocument();
    expect(within(cabecalho).getByRole("button", { name: "Atualizar agora" })).toBeInTheDocument();
    // A opção ativa não é um botão primário (destaque cheio é para a ação da tela).
    expect(within(cabecalho).getByRole("button", { name: "1 h" })).not.toHaveClass("btn--primary");
  });

  it("aba com rótulo curto no celular mantém o nome completo para o leitor de tela", () => {
    render(<PaginaIa rota="/ia/precos" query="" />);
    const aba = screen.getByRole("link", { name: "Modelos e preços" });
    expect(within(aba).getByText("Preços")).toHaveClass("ia-aba__curto");
  });

  it("recarga com dado na tela: vista esmaecida (aria-busy), botão girando e selo de frescor", () => {
    Object.assign(cargaFalsa, { carregando: true, dados: {}, atualizadoEm: Date.now() });
    render(<PaginaIa rota="/ia" query="" />);
    expect(document.querySelector(".ia-vista")).toHaveAttribute("aria-busy", "true");
    expect(screen.getByRole("button", { name: "Atualizar agora" })).toHaveAttribute("aria-busy", "true");
    expect(screen.getByText(/atualizado agora/)).toBeInTheDocument();
  });

  it("primeira carga (sem dado) não esmaece a vista", () => {
    Object.assign(cargaFalsa, { carregando: true, dados: null, atualizadoEm: null });
    render(<PaginaIa rota="/ia" query="" />);
    expect(document.querySelector(".ia-vista")).toHaveAttribute("aria-busy", "false");
    expect(screen.queryByText(/atualizado/)).not.toBeInTheDocument();
  });

  it("a ajuda traz as perguntas frequentes num bloco próprio", () => {
    render(<PaginaIa rota="/ia" query="" />);
    fireEvent.click(screen.getByRole("button", { name: "Ajuda desta página" }));
    const ajuda = screen.getByRole("dialog", { name: "Agentes de IA" });
    expect(within(ajuda).getByText("Perguntas frequentes")).toBeInTheDocument();
    expect(ajuda.querySelector(".help-faq .help-faq__q")).toHaveTextContent(/parcial/);
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
    // Ícone próprio: o Bot é do MCP.
    const mcp = NAV.flatMap((g) => g.items).find((i) => i.href === "#/mcp");
    expect(item?.icon).not.toBe(mcp?.icon);
  });
});

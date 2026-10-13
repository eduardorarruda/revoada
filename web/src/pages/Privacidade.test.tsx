import { render, screen } from "@testing-library/react";

const buscarPrivacidade = vi.fn();
vi.mock("../api.privacidade", async (original) => ({
  ...(await original<typeof import("../api.privacidade")>()),
  buscarPrivacidade: () => buscarPrivacidade(),
}));

const { Privacidade } = await import("./Privacidade");

beforeEach(() => {
  buscarPrivacidade.mockReset();
});

describe("Política de privacidade", () => {
  it("mostra o controlador, o contato e os prazos configurados nesta instalação", async () => {
    buscarPrivacidade.mockResolvedValue({
      controlador: "Empresa Exemplo Ltda.",
      contato: "privacidade@exemplo.com.br",
      retencao_dias: 60,
      auditoria_dias: 400,
      sessao_inatividade_horas: 8,
      sessao_max_horas: 48,
    });
    render(<Privacidade />);
    expect(await screen.findByText("Empresa Exemplo Ltda.")).toBeInTheDocument();
    expect(screen.getByText("privacidade@exemplo.com.br")).toBeInTheDocument();
    expect(screen.getAllByText(/400 dias/).length).toBeGreaterThan(0);
    expect(screen.getByText("60 dias")).toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("sem configuração: fala do administrador, sem inventar canal", async () => {
    buscarPrivacidade.mockResolvedValue({
      controlador: "",
      contato: "",
      retencao_dias: 120,
      auditoria_dias: 365,
      sessao_inatividade_horas: 24,
      sessao_max_horas: 168,
    });
    render(<Privacidade />);
    expect(await screen.findByText(/fale com o administrador do painel/)).toBeInTheDocument();
    expect(screen.getByText("o administrador desta instalação")).toBeInTheDocument();
  });

  it("se a consulta falhar, avisa que os prazos são os padrões", async () => {
    buscarPrivacidade.mockRejectedValue(new Error("503"));
    render(<Privacidade />);
    expect(await screen.findByRole("status")).toHaveTextContent("os prazos abaixo são os padrões do Revoada");
    expect(screen.getAllByText(/365 dias/).length).toBeGreaterThan(0);
  });

  it("tem um título de página e seções com títulos, para leitor de tela", () => {
    buscarPrivacidade.mockReturnValue(new Promise(() => {}));
    render(<Privacidade />);
    expect(screen.getByRole("heading", { level: 1, name: "Política de privacidade" })).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 2 }).length).toBeGreaterThanOrEqual(8);
    expect(screen.getByRole("link", { name: /Voltar ao painel/ })).toHaveAttribute("href", "#/");
  });
});

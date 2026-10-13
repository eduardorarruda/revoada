import { fireEvent, render, within } from "@testing-library/react";

// Só o necessário da API para o shell montar sem backend nem sessão.
vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api")>()),
  getMe: () => null,
  getRole: () => "admin",
  getAccessToken: () => null,
  logout: () => Promise.resolve(),
  sondarConexao: () => Promise.resolve(),
  listHosts: () => Promise.resolve([]),
}));

const { AppShell } = await import("./AppShell");

describe("AppShell — menu do usuário", () => {
  // role="menu" promete navegação por setas que o painel não tem; com dois itens,
  // link e botão comuns (Tab) num grupo rotulado é o padrão correto.
  it("abre um grupo rotulado com link e botão comuns, sem roles de menu ARIA", () => {
    const { container } = render(
      <AppShell route="/">
        <p>conteúdo</p>
      </AppShell>,
    );
    // Consultas por papel ficam restritas ao menu: no shell inteiro (sidebar com
    // dezenas de itens) o getByRole do jsdom leva segundos.
    const area = within(container.querySelector(".usermenu") as HTMLElement);
    const gatilho = area.getByRole("button", { name: /^Conta:/ });
    expect(gatilho).not.toHaveAttribute("aria-haspopup");
    fireEvent.click(gatilho);

    const grupo = area.getByRole("group", { name: "Conta" });
    expect(gatilho).toHaveAttribute("aria-expanded", "true");
    expect(gatilho).toHaveAttribute("aria-controls", grupo.id);
    expect(container.querySelector('[role="menu"], [role="menuitem"]')).toBeNull();
    expect(area.getByRole("link", { name: /Segurança da conta/ })).toBeInTheDocument();
    // A política de privacidade (LGPD) fica a um clique também para quem já entrou.
    expect(area.getByRole("link", { name: /Política de privacidade/ })).toHaveAttribute("href", "#/privacidade");
    expect(area.getByRole("button", { name: /Sair/ })).toBeInTheDocument();
  }, 15_000);
});

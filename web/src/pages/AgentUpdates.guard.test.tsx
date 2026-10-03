import { render, screen, waitFor } from "@testing-library/react";
import { filtrarNav, NAV } from "../components/AppShell";
import { ToastProvider } from "../components";

// A tela de Atualização dos agentes decide quando o programa que roda DENTRO dos
// servidores do cliente troca de versão. A guarda de admin dela existe em três
// camadas independentes, e este arquivo prova as duas que vivem no navegador:
//
//   1. o item do menu é `adminOnly` e o filtro do menu o esconde de não-admin;
//   2. a própria página recusa quem chega pela URL direta (#/admin/agent-updates),
//      com explicação em vez de erro seco.
//
// (A terceira é o servidor: as rotas /api/agent/update-policy e
// /api/agents/update-hold estão sob `admin()` em server/internal/httpapi. Sem ela as
// outras duas seriam enfeite — mas ela é testada no Go.)
//
// Por que testar isto: as três camadas são "código que não faz nada" no dia a dia.
// É exatamente o tipo de linha que alguém apaga numa refatoração sem perceber, e a
// falha só aparece quando um usuário comum encontra o botão que para a frota.

const isAdmin = vi.fn(() => true);
const getAgentUpdatePolicy = vi.fn();
const listAgents = vi.fn();
const listHosts = vi.fn();

vi.mock("../api", () => ({
  isAdmin: () => isAdmin(),
  getAgentUpdatePolicy: () => getAgentUpdatePolicy(),
  listAgents: () => listAgents(),
  listHosts: () => listHosts(),
  setAgentUpdatePolicy: vi.fn(),
  setAgentUpdateHold: vi.fn(),
}));

const { AgentUpdates } = await import("./AgentUpdates");

const ROTA = "#/admin/agent-updates";

beforeEach(() => {
  isAdmin.mockReturnValue(true);
  getAgentUpdatePolicy.mockResolvedValue({
    off: false,
    pin: "",
    motivo: "",
    updated_at: "0001-01-01T00:00:00Z",
    updated_by: "",
  });
  listAgents.mockResolvedValue({ agents: [] });
  listHosts.mockResolvedValue({ hosts: [] });
});

function renderizar() {
  return render(
    <ToastProvider>
      <AgentUpdates />
    </ToastProvider>,
  );
}

describe("guarda de administrador da tela de Atualização dos agentes", () => {
  it("o item de menu é admin-only (camada 1)", () => {
    const item = NAV.flatMap((g) => g.items).find((i) => i.href === ROTA);
    expect(item, `o item ${ROTA} sumiu do menu`).toBeDefined();
    expect(item?.adminOnly, `${ROTA} precisa ser adminOnly`).toBe(true);
  });

  it("o filtro do menu esconde a tela de quem não é admin, e a mostra ao admin", () => {
    const hrefs = (admin: boolean) =>
      filtrarNav(NAV, admin, "").flatMap((g) => g.items.map((i) => i.href));
    expect(hrefs(false)).not.toContain(ROTA);
    expect(hrefs(true)).toContain(ROTA);
    // O filtro não pode "esconder" simplesmente devolvendo nada: as telas comuns
    // continuam lá para quem não é admin.
    expect(hrefs(false)).toContain("#/hosts");
  });

  it("mesmo buscando pelo nome, o menu não revela a tela a quem não é admin", () => {
    const achados = filtrarNav(NAV, false, "atualização").flatMap((g) => g.items.map((i) => i.href));
    expect(achados).not.toContain(ROTA);
  });

  it("a página recusa quem não é admin e chega pela URL direta (camada 2)", async () => {
    isAdmin.mockReturnValue(false);
    renderizar();
    expect(screen.getByText(/exclusiva de administradores/i)).toBeInTheDocument();
    // Nenhum controle de freio pode ser renderizado: nem o formulário da frota, nem
    // a tabela de servidores.
    expect(screen.queryByRole("button", { name: /salvar política/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /segurar/i })).not.toBeInTheDocument();
    // E a recusa é ANTES da API: negar acesso não pode custar uma consulta.
    expect(getAgentUpdatePolicy).not.toHaveBeenCalled();
    expect(listAgents).not.toHaveBeenCalled();
    expect(listHosts).not.toHaveBeenCalled();
  });

  it("o admin continua vendo os controles (a guarda não barra quem pode)", async () => {
    renderizar();
    await waitFor(() => expect(screen.getByRole("checkbox")).toBeInTheDocument());
    expect(screen.getByRole("button", { name: /salvar política/i })).toBeInTheDocument();
    expect(listHosts).toHaveBeenCalled();
  });
});

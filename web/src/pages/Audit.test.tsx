import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import type { AuditEntry } from "../api";

// A tela consulta a API e checa o papel do usuário — as duas coisas são mockadas
// para o teste rodar sem backend nem sessão.
const listAudit = vi.fn();
const isAdmin = vi.fn(() => true);
vi.mock("../api", () => ({
  listAudit: (...a: unknown[]) => listAudit(...a),
  isAdmin: () => isAdmin(),
}));

const { Audit } = await import("./Audit");

const entries: AuditEntry[] = [
  {
    id: 2,
    actor_id: 1,
    actor_name: "ana",
    actor_role: "admin",
    method: "PUT",
    path: "/api/notify/channels/3",
    resource: "notify",
    target: "channels/3",
    status: 200,
    payload: { name: "WhatsApp Plantão", config: { apikey: "••• redigido •••", base_url: "https://evo" } },
    ip: "203.0.113.9",
    created_at: "2026-07-28T14:30:00Z",
  },
  {
    id: 1,
    actor_name: "maria",
    actor_role: "user",
    method: "DELETE",
    path: "/api/site-checks/9",
    resource: "site-checks",
    target: "9",
    status: 403,
    payload: {},
    ip: "10.0.0.4",
    created_at: "2026-07-28T13:00:00Z",
  },
];

beforeEach(() => {
  listAudit.mockReset();
  isAdmin.mockReturnValue(true);
  listAudit.mockResolvedValue({ entries, total: 2, resources: ["notify", "site-checks"] });
});

describe("tela de Auditoria", () => {
  it("lista as alterações traduzindo ação e recurso para linguagem do usuário", async () => {
    render(<Audit />);
    await waitFor(() => expect(screen.getByText("ana")).toBeInTheDocument());
    // Escopado ao corpo da tabela: os mesmos rótulos existem de propósito no
    // seletor de filtro, e não é isso que este teste está verificando.
    const tbody = within(document.querySelector("tbody") as HTMLElement);

    // Método HTTP não pode vazar para as linhas — o usuário lê a intenção.
    expect(tbody.getByText("Alterou")).toBeInTheDocument();
    expect(tbody.getByText("Removeu")).toBeInTheDocument();
    expect(tbody.queryByText("PUT")).not.toBeInTheDocument();

    // Recurso traduzido, não o identificador cru da rota.
    expect(tbody.getByText("Canais de alerta")).toBeInTheDocument();
    expect(tbody.getByText("Websites")).toBeInTheDocument();

    // Ação recusada aparece como falha, com o código.
    expect(tbody.getByText(/falhou \(403\)/)).toBeInTheDocument();
  });

  it("expande a linha e mostra o conteúdo enviado sem tabela aninhada", async () => {
    render(<Audit />);
    await waitFor(() => expect(screen.getByText("ana")).toBeInTheDocument());

    const tbody = document.querySelector("tbody") as HTMLElement;
    fireEvent.click(within(tbody).getAllByRole("button")[0]);

    await waitFor(() => expect(screen.getByText("WhatsApp Plantão")).toBeInTheDocument());
    // O segredo continua redigido na exibição.
    expect(screen.getByText(/redigido/)).toBeInTheDocument();
    // Uma <table> dentro da linha expandida herdaria o CSS .dtable do pai
    // (zebra/hover/bordas) e quebraria o visual — o detalhe usa <dl>.
    expect(document.querySelectorAll("table").length).toBe(1);
    expect(document.querySelector("dl")).not.toBeNull();
  });

  it("usa o container padrão das telas (margem lateral e largura máxima)", async () => {
    const { container } = render(<Audit />);
    await waitFor(() => expect(screen.getByText("ana")).toBeInTheDocument());
    // Sem .page a tela cola no menu e na borda: o shell não aplica padding, cada
    // página traz o seu. Vale também para a versão de acesso negado.
    expect(container.firstElementChild).toHaveClass("page");

    isAdmin.mockReturnValue(false);
    const negado = render(<Audit />);
    expect(negado.container.firstElementChild).toHaveClass("page");
  });

  it("explica em vez de dar erro quando quem abre não é administrador", () => {
    isAdmin.mockReturnValue(false);
    render(<Audit />);
    expect(screen.getByText(/exclusiva de administradores/i)).toBeInTheDocument();
    expect(listAudit).not.toHaveBeenCalled();
  });

  it("volta para a primeira página ao trocar um filtro", async () => {
    listAudit.mockResolvedValue({ entries, total: 250, resources: ["notify"] });
    render(<Audit />);
    await waitFor(() => expect(screen.getByText("ana")).toBeInTheDocument());

    fireEvent.click(screen.getByText(/próximas/));
    await waitFor(() => expect(listAudit).toHaveBeenCalledWith(expect.objectContaining({ offset: 100 })));

    fireEvent.change(screen.getByPlaceholderText("nome do usuário"), { target: { value: "maria" } });
    await waitFor(() =>
      expect(listAudit).toHaveBeenCalledWith(expect.objectContaining({ actor: "maria", offset: 0 })),
    );
  });
});

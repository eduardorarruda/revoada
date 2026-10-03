import { render, screen, waitFor, within } from "@testing-library/react";
import { ToastProvider } from "../components";
import type { AgentKey, HostDetail } from "../api";

// A tela lista SERVIDORES e resolve servidor → chave. Este arquivo prova o que cada
// linha diz nos quatro casos que existem de verdade na frota, com payloads
// TRANSCRITOS da API de desenvolvimento (GET /api/hosts e GET /api/agents, servidos
// pelo binário de server/cmd/server em 10/08/2026) — não inventados a partir dos
// tipos. O risco que isto cobre: a tela antiga mostrava o apelido da chave e o
// operador acreditava estar olhando para servidores.

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

// --- payloads reais (recortados) ---

const HOSTS: HostDetail[] = [
  // Agente 0.7.0 que RELATOU o próprio hostname: amarração exata, com freio ligado.
  base("dev-agent-host", "0.7.0"),
  // Agente 0.7.0 sem relato de hostname: só o apelido da chave amarra.
  base("agente-medicao", "0.7.0"),
  // Servidor com agente mais novo e nenhum relato: a chave existe, mas ele nunca
  // perguntou ao painel.
  base("notebook-dev", "0.8.0"),
  // Servidor do inventário sem chave nenhuma (o caso da frota de produção inteira).
  base("faseC-host", "0.7.0"),
  // Host que nem versão informou.
  base("gw-load-0", ""),
];

function base(hostname: string, agent_version: string): HostDetail {
  return {
    hostname,
    display_name: "",
    os: "linux",
    kernel: "7.1.3-2-cachyos",
    arch: "x86_64",
    cpu_model: "",
    cpu_cores: 4,
    ips: "",
    agent_version,
    uptime_secs: 0,
    last_seen: "2026-08-09T22:58:42.834897-03:00",
    up: false,
  };
}

const AGENTES: AgentKey[] = [
  {
    id: "0f13bfd65fece4f8",
    serverkey: "dev-…46",
    tenant_id: "default",
    hostname: "notebook-dev",
    revoked: false,
    last_seen: "2026-08-09T22:57:57.934412-03:00",
    created_at: "2026-08-09T10:53:47.172059-03:00",
    update_hold: false,
  },
  {
    id: "02c905b0fa580984",
    serverkey: "loca…01",
    tenant_id: "default",
    hostname: "agente-medicao",
    revoked: false,
    last_seen: "2026-08-09T17:45:38.353625-03:00",
    created_at: "2026-07-20T20:58:11.980461-03:00",
    update_hold: false,
    update_report: { versao_atual: "0.7.0", os: "linux", arch: "amd64", estado: "em_dia", quando: "2026-08-10T10:00:00Z" },
  },
  {
    id: "eb723b6046ffc46b",
    serverkey: "devk…01",
    tenant_id: "default",
    hostname: "dev-agent-host",
    revoked: false,
    last_seen: "2026-08-09T12:01:39.155108-03:00",
    created_at: "2026-07-20T17:16:26.377243-03:00",
    update_hold: true,
    update_hold_reason: "teste de amarração servidor→chave",
    update_report: {
      versao_atual: "0.7.0",
      os: "linux",
      arch: "amd64",
      hostname: "dev-agent-host",
      estado: "erro_download",
      erro: "sem espaco",
      quando: "2026-08-10T11:00:00Z",
    },
  },
  {
    id: "11267cb6ac3995b3",
    serverkey: "REVO…EY",
    tenant_id: "default",
    hostname: "x",
    revoked: true,
    last_seen: null,
    created_at: "2026-07-13T22:02:22.523354-03:00",
    update_hold: false,
  },
];

beforeEach(() => {
  isAdmin.mockReturnValue(true);
  getAgentUpdatePolicy.mockResolvedValue({
    off: false,
    pin: "",
    motivo: "",
    updated_at: "0001-01-01T00:00:00Z",
    updated_by: "",
  });
  listAgents.mockResolvedValue({ agents: AGENTES });
  listHosts.mockResolvedValue({ hosts: HOSTS });
});

async function abrir() {
  render(
    <ToastProvider>
      <AgentUpdates />
    </ToastProvider>,
  );
  await waitFor(() => expect(screen.getByRole("link", { name: /dev-agent-host/ })).toBeInTheDocument());
}

// linha devolve o <tr> do servidor pedido — as duas tabelas da tela usam os mesmos
// rótulos de coluna, então toda asserção precisa ser escopada.
function linha(hostname: string): HTMLElement {
  const link = screen.getByRole("link", { name: new RegExp(hostname) });
  return link.closest("tr") as HTMLElement;
}

describe("tela de atualização, listando servidores", () => {
  it("mostra um servidor por linha, com a versão que ele está rodando de verdade", async () => {
    await abrir();
    // O apelido da chave ("x") NÃO é servidor e não pode virar linha da tabela de
    // servidores — era exatamente esse o defeito da tela antiga.
    for (const h of ["dev-agent-host", "agente-medicao", "notebook-dev", "faseC-host", "gw-load-0"]) {
      expect(screen.getByRole("link", { name: new RegExp(h) })).toBeInTheDocument();
    }
    expect(within(linha("notebook-dev")).getByText("0.8.0")).toBeInTheDocument();
    // Versão vem do inventário: o host que não informou versão mostra travessão, não
    // um zero inventado.
    expect(within(linha("gw-load-0")).getByText("—")).toBeInTheDocument();
  });

  it("amarração confirmada pelo agente: mostra o relato, o freio e o botão de soltar", async () => {
    await abrir();
    const tr = within(linha("dev-agent-host"));
    expect(tr.getByText("falhou ao baixar")).toBeInTheDocument();
    expect(tr.getByText("sem espaco")).toBeInTheDocument(); // o erro cru do agente
    expect(tr.getByText("segurado")).toBeInTheDocument();
    expect(tr.getByText("teste de amarração servidor→chave")).toBeInTheDocument();
    expect(tr.getByText("confirmado pelo agente")).toBeInTheDocument();
    expect(tr.getByRole("button", { name: "Soltar" })).toBeEnabled();
  });

  it("amarração pelo apelido da chave é oferecida, mas identificada como tal", async () => {
    await abrir();
    const tr = within(linha("agente-medicao"));
    expect(tr.getByText("em dia")).toBeInTheDocument();
    expect(tr.getByText("pelo nome da chave")).toBeInTheDocument();
    expect(tr.getByRole("button", { name: "Segurar" })).toBeEnabled();
  });

  it("servidor com chave mas sem nenhuma consulta diz que ele NÃO pergunta ao painel", async () => {
    await abrir();
    // O caso da frota 0.7.0 em produção. "sem relato" seria verdade e inútil; o que
    // o operador precisa saber é que esperar não resolve.
    const tr = within(linha("notebook-dev"));
    expect(tr.getByText("não pergunta ao painel")).toBeInTheDocument();
  });

  it("servidor sem chave identificada não ganha botão de freio — e diz por quê", async () => {
    await abrir();
    const tr = within(linha("faseC-host"));
    expect(tr.getByText("não dá para saber")).toBeInTheDocument();
    expect(tr.getAllByText("sem chave identificada").length).toBeGreaterThan(0);
    // Nada de botão: um freio que pode cair na máquina errada é pior que nenhum.
    expect(tr.queryByRole("button")).not.toBeInTheDocument();
    expect(tr.getByText("sem chave para segurar")).toBeInTheDocument();
  });

  it("as chaves que sobraram continuam alcançáveis, numa lista à parte", async () => {
    await abrir();
    // A chave "x" (revogada) não é servidor nenhum; some da tabela de servidores mas
    // não do painel — senão o operador perderia a única forma de segurá-la.
    const card = screen.getByText("Chaves sem servidor identificado").closest("section, div") as HTMLElement;
    expect(within(card).getByText("x")).toBeInTheDocument();
    expect(within(card).getByText(/chave revogada/i)).toBeInTheDocument();
  });
});

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// O contrato da API mudou por causa de um vazamento: GET /api/agents devolvia a
// serverkey COMPLETA de toda a frota, então uma sessão de admin comprometida colhia
// numa requisição a credencial de ingestão de todos os servidores. Agora a listagem
// vem mascarada, as rotas de escrita falam por `id` público e a chave em claro só
// sai por um POST auditado, uma por vez.
//
// Estes testes travam o lado do cliente disso: se alguém voltar a mandar a
// serverkey nas rotas de escrita, ou fizer a tela revelar chaves sozinha, quebra
// aqui — não em produção.

let api: typeof import("./api");

interface Chamada {
  url: string;
  method: string;
  body: unknown;
}
let chamadas: Chamada[];
/** url → resposta que o servidor de mentira deve dar. */
let respostas: Record<string, { status: number; body: unknown }>;

function fakeFetch(url: string, init?: RequestInit): Promise<Response> {
  const method = init?.method ?? "GET";
  chamadas.push({
    url,
    method,
    body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
  });
  // A rota casa pelo caminho, ignorando query string.
  const caminho = url.split("?")[0];
  const r = respostas[caminho] ?? { status: 200, body: {} };
  return Promise.resolve(
    new Response(r.status === 204 ? null : JSON.stringify(r.body), {
      status: r.status,
      headers: { "Content-Type": "application/json" },
    }),
  );
}

beforeEach(async () => {
  vi.resetModules();
  chamadas = [];
  respostas = {};
  vi.stubGlobal("fetch", vi.fn(fakeFetch));
  api = await import("./api");
});

afterEach(() => {
  vi.unstubAllGlobals();
});

/** Uma linha da listagem, como o backend a devolve hoje: chave MASCARADA. */
const LINHA = {
  id: "0f13bfd65fece4f8",
  serverkey: "dev-…46",
  tenant_id: "default",
  hostname: "web-01",
  revoked: false,
  last_seen: null,
  created_at: "2026-08-01T10:00:00Z",
  update_hold: false,
};

describe("chaves de agente: a listagem não é mais um cofre aberto", () => {
  it("revogar e excluir mandam o id público, nunca a serverkey", async () => {
    respostas["/api/agents/revoke"] = { status: 204, body: null };
    respostas["/api/agents/delete"] = { status: 204, body: null };

    await api.setAgentRevoked(LINHA.id, true);
    await api.deleteAgent(LINHA.id);

    const corpos = chamadas.map((c) => c.body as Record<string, unknown>);
    expect(corpos[0]).toEqual({ id: LINHA.id, revoked: true });
    expect(corpos[1]).toEqual({ id: LINHA.id });
    for (const c of corpos) expect(c).not.toHaveProperty("serverkey");
  });

  it("listar NÃO revela chave nenhuma — a revelação é um pedido separado e auditável", async () => {
    respostas["/api/agents"] = { status: 200, body: { agents: [LINHA] } };
    const r = await api.listAgents();

    expect(r.agents[0].serverkey).toBe("dev-…46");
    // Se carregar a lista disparasse reveal, roubar a frota voltaria a ser um clique.
    expect(chamadas.some((c) => c.url === "/api/agents/reveal")).toBe(false);
  });

  it("revelar uma chave é POST (passa pela trilha de auditoria) e leva só um id", async () => {
    respostas["/api/agents/reveal"] = { status: 200, body: { id: LINHA.id, serverkey: "dev-local-abc123" } };

    const r = await api.revealAgentKey(LINHA.id);

    expect(r.serverkey).toBe("dev-local-abc123");
    const c = chamadas.at(-1)!;
    expect(c.method).toBe("POST"); // GET não seria auditado
    expect(c.body).toEqual({ id: LINHA.id });
  });

  it("o instalador de uma chave existente vai por agent_id — a chave não passa pelo navegador", async () => {
    respostas["/api/agent-installer"] = { status: 200, body: {} };
    // downloadAgentInstaller lida com blob/headers; aqui basta o request montado.
    await api.downloadAgentInstaller({ os: "linux", agent_id: LINHA.id }).catch(() => undefined);

    const c = chamadas.find((x) => x.url === "/api/agent-installer")!;
    expect(c.body).toEqual({ os: "linux", agent_id: LINHA.id });
    expect(c.body).not.toHaveProperty("serverkey");
  });
});

describe("freio da auto-atualização", () => {
  it("grava a política global por PUT, com motivo", async () => {
    respostas["/api/agent/update-policy"] = { status: 204, body: null };
    await api.setAgentUpdatePolicy({ off: true, pin: "0.9.1", motivo: "0.9.2 quebrou disco no Ubuntu 20.04" });

    const c = chamadas.at(-1)!;
    expect(c.method).toBe("PUT");
    expect(c.body).toEqual({ off: true, pin: "0.9.1", motivo: "0.9.2 quebrou disco no Ubuntu 20.04" });
  });

  it("a mensagem de pin inválido do backend chega inteira ao usuário", async () => {
    // Quem escreveu a regra escreveu a explicação ("use o formato 0.9.1"): traduzir
    // de novo no front só criaria uma segunda versão da verdade.
    respostas["/api/agent/update-policy"] = {
      status: 400,
      body: "versão fixada inválida: use o formato 0.9.1",
    };
    await expect(api.setAgentUpdatePolicy({ off: false, pin: "abc", motivo: "x" })).rejects.toThrow(
      /use o formato 0\.9\.1/,
    );
  });

  it("segurar um servidor identifica o host pelo id público", async () => {
    respostas["/api/agents/update-hold"] = { status: 204, body: null };
    await api.setAgentUpdateHold(LINHA.id, true, "banco de produção");

    const c = chamadas.at(-1)!;
    expect(c.method).toBe("POST");
    expect(c.body).toEqual({ id: LINHA.id, hold: true, motivo: "banco de produção" });
  });
});

describe("expurgo de logs travado", () => {
  it("a pré-visualização é POST e NÃO recua para GET — o recorte carrega o próprio segredo", async () => {
    // O recuo existia enquanto a rota POST não estava publicada. Mantê-lo faria um
    // 405 acidental reabrir em silêncio o caminho que escreve o segredo procurado na
    // query string, e daí no log de acesso do servidor web.
    respostas["/api/logs/purge/preview"] = { status: 405, body: "method not allowed" };

    await expect(
      api.purgeLogsPreview({ host: "web-01", to: "2026-01-01T00:00:00Z", body_like: "token-vazado" }),
    ).rejects.toThrow();

    expect(chamadas).toHaveLength(1);
    expect(chamadas[0].method).toBe("POST");
    expect(chamadas.some((c) => c.url.includes("body_like="))).toBe(false);
  });

  it("lista as limpezas travadas e sabe cancelar uma", async () => {
    respostas["/api/logs/purge/status"] = {
      status: 200,
      body: {
        stuck: [
          { mutation_id: "m-42", parts_remaining: 3, fail_reason: "NOT_ENOUGH_SPACE", created_at: "2026-08-01 10:00:00" },
        ],
      },
    };
    respostas["/api/logs/purge/cancel"] = { status: 200, body: { cancelled: "m-42" } };

    const travadas = await api.purgeLogsStuck();
    expect(travadas).toHaveLength(1);
    expect(travadas[0].mutation_id).toBe("m-42");

    await api.purgeLogsCancel("m-42");
    expect(chamadas.at(-1)!.body).toEqual({ mutation_id: "m-42" });
  });

  it("sem nenhuma travada, a lista é vazia (e a caixa não aparece na tela)", async () => {
    respostas["/api/logs/purge/status"] = { status: 200, body: { stuck: [] } };
    expect(await api.purgeLogsStuck()).toEqual([]);
  });
});

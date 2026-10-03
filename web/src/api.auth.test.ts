import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Depois de um tempo parado, o painel caía na tela de login sozinho — e um F5
// devolvia a pessoa para dentro, provando que a sessão nunca tinha morrido.
//
// A causa: o refresh token é de USO ÚNICO (o servidor revoga o antigo ao emitir o
// novo). O access token vence em 15 minutos; passado esse tempo, as várias telas em
// polling levam 401 na mesma leva e cada uma disparava a SUA renovação. A primeira
// rotacionava o cookie, as outras chegavam com o token já revogado, levavam 401 do
// próprio /api/auth/refresh e derrubavam a sessão. O F5 escapava porque no boot
// existe uma renovação só.
//
// O servidor de mentira abaixo reproduz exatamente isso: cada refresh só aceita o
// token vigente. Contra o código antigo, o primeiro teste falha.

let api: typeof import("./api");

/** Estado do servidor de mentira. */
interface Fake {
  /** O que o navegador guarda no cookie httpOnly e manda em cada renovação. */
  cookie: string;
  /** Tokens de refresh já gastos — o servidor revoga o antigo ao emitir o novo. */
  gastos: Set<string>;
  access: string;
  refreshes: number;
  /** Quando > 0, o /api/auth/refresh responde este status em vez de renovar. */
  statusRefresh: number;
}

let fake: Fake;

function resposta(status: number, body: unknown, headers: Record<string, string> = {}) {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ...headers },
  });
}

function servidorFalso(url: string, init?: RequestInit): Promise<Response> {
  if (url === "/api/auth/login") {
    fake.access = "access-1";
    fake.cookie = "refresh-1";
    return Promise.resolve(resposta(200, { role: "admin" }, { "X-Access-Token": fake.access }));
  }
  if (url === "/api/auth/refresh") {
    fake.refreshes++;
    if (fake.statusRefresh) return Promise.resolve(resposta(fake.statusRefresh, {}));
    // O cookie que o navegador manda é o que ele tem AGORA — e o novo só chega
    // quando a resposta volta. Por isso a ida e volta tem latência: sem ela, o
    // teste não reproduz a corrida que derrubava a sessão de verdade.
    const enviado = fake.cookie;
    return new Promise((resolve) =>
      setTimeout(() => {
        if (fake.gastos.has(enviado)) {
          resolve(resposta(401, {})); // uso único: este já foi trocado
          return;
        }
        fake.gastos.add(enviado);
        fake.cookie = `refresh-${fake.refreshes + 1}`;
        fake.access = `access-${fake.refreshes + 1}`;
        resolve(resposta(200, { role: "admin" }, { "X-Access-Token": fake.access }));
      }, 5),
    );
  }
  // Rota autenticada qualquer: só aceita o access token atual.
  const auth = new Headers(init?.headers).get("Authorization");
  if (auth !== `Bearer ${fake.access}`) return Promise.resolve(resposta(401, {}));
  return Promise.resolve(resposta(200, { ok: true }));
}

beforeEach(async () => {
  vi.resetModules();
  fake = { cookie: "refresh-1", gastos: new Set(), access: "access-1", refreshes: 0, statusRefresh: 0 };
  vi.stubGlobal("fetch", vi.fn((url: string, init?: RequestInit) => servidorFalso(url, init)));
  api = await import("./api");
  await api.login("ana", "secreta");
});

afterEach(() => {
  vi.unstubAllGlobals();
});

/** Simula os 15 minutos parado: o access token em memória deixa de valer. */
function accessTokenVenceu() {
  fake.access = "access-vencido-do-lado-do-servidor";
}

describe("renovação de sessão", () => {
  it("várias chamadas levando 401 juntas renovam UMA vez e nenhuma cai no login", async () => {
    const perdeuSessao = vi.fn();
    window.addEventListener("revoada:auth-lost", perdeuSessao);
    accessTokenVenceu();

    // Cinco telas em polling acordando no mesmo instante, como acontece de verdade.
    const respostas = await Promise.all([
      api.api("/api/hosts"),
      api.api("/api/alerts"),
      api.api("/api/notify/channels"),
      api.api("/api/websites"),
      api.api("/api/audit"),
    ]);

    expect(respostas).toHaveLength(5);
    expect(fake.refreshes).toBe(1); // e não uma por chamada
    expect(perdeuSessao).not.toHaveBeenCalled();
    expect(api.isAuthenticated()).toBe(true);
    window.removeEventListener("revoada:auth-lost", perdeuSessao);
  });

  it("servidor reiniciando (502) não expulsa quem está logado", async () => {
    // Acontece a cada deploy: a chamada falha, mas a sessão continua de pé e a
    // tela tenta de novo no ciclo seguinte.
    const perdeuSessao = vi.fn();
    window.addEventListener("revoada:auth-lost", perdeuSessao);
    accessTokenVenceu();
    fake.statusRefresh = 502;

    await expect(api.api("/api/hosts")).rejects.toThrow(/renovar a sessão/);
    expect(perdeuSessao).not.toHaveBeenCalled();
    expect(api.isAuthenticated()).toBe(true);
    window.removeEventListener("revoada:auth-lost", perdeuSessao);
  });

  it("sessão realmente expirada (401 no refresh) volta para o login", async () => {
    // A contrapartida: quando é para cair no login, tem que cair — senão o polling
    // fica martelando 401 numa tela vazia.
    const perdeuSessao = vi.fn();
    window.addEventListener("revoada:auth-lost", perdeuSessao);
    accessTokenVenceu();
    fake.statusRefresh = 401;

    await expect(api.api("/api/hosts")).rejects.toThrow(/sessão expirada/);
    expect(perdeuSessao).toHaveBeenCalledTimes(1);
    expect(api.isAuthenticated()).toBe(false);
    window.removeEventListener("revoada:auth-lost", perdeuSessao);
  });
});

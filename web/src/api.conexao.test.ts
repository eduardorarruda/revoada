// A ligação entre `api()` e o selo de conexão. O módulo conexao.ts tem os seus
// próprios testes; aqui provamos a outra metade — que o cliente da API de fato
// avisa, e avisa a coisa certa, em cada desfecho de uma chamada real.
//
// Sem este teste, alguém "simplifica" o try/catch do api() e o selo volta a ser
// o enfeite que era: parado em "ao vivo" para sempre.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

let api: typeof import("./api");
let conexao: typeof import("./conexao");

/** Resposta que o fetch de mentira deve dar na próxima chamada. */
let proxima: { status: number } | { erroDeRede: true };

function fakeFetch(): Promise<Response> {
  if ("erroDeRede" in proxima) return Promise.reject(new TypeError("Failed to fetch"));
  const { status } = proxima;
  return Promise.resolve(
    new Response(status === 204 ? null : JSON.stringify({ ok: true }), {
      status,
      headers: { "Content-Type": "application/json" },
    }),
  );
}

beforeEach(async () => {
  vi.resetModules();
  vi.stubGlobal("fetch", vi.fn(fakeFetch));
  api = await import("./api");
  conexao = await import("./conexao");
  conexao.resetarConexaoParaTeste();
});

afterEach(() => vi.unstubAllGlobals());

describe("api() alimenta o estado de conexão", () => {
  it("chamada que volta bem marca 'no ar' e carimba a hora", async () => {
    proxima = { status: 200 };
    await api.api("/api/hosts");
    expect(conexao.estadoConexao()).toBe("no-ar");
    expect(conexao.idadeUltimoSucesso()).toBe(0);
  });

  it("fetch que nem sai (rede caída) derruba para 'reconectando'", async () => {
    proxima = { erroDeRede: true };
    await expect(api.api("/api/hosts")).rejects.toThrow();
    expect(conexao.estadoConexao()).toBe("reconectando");
  });

  it("502 do proxy durante um deploy derruba para 'reconectando'", async () => {
    proxima = { status: 502 };
    await expect(api.api("/api/hosts")).rejects.toThrow();
    expect(conexao.estadoConexao()).toBe("reconectando");
  });

  it("404 NÃO derruba: o painel respondeu, só não achou a coisa", async () => {
    proxima = { status: 404 };
    await expect(api.api("/api/coisa-que-nao-existe")).rejects.toThrow();
    expect(conexao.estadoConexao()).toBe("no-ar");
  });

  it("volta a 'no ar' assim que uma chamada passa de novo", async () => {
    proxima = { erroDeRede: true };
    await expect(api.api("/api/hosts")).rejects.toThrow();
    proxima = { status: 200 };
    await api.api("/api/hosts");
    expect(conexao.estadoConexao()).toBe("no-ar");
  });
});

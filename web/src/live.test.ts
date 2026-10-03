import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

// O LiveClient só precisa do token; o resto de api.ts não entra no teste.
vi.mock("./api", () => ({ getAccessToken: () => "token-de-teste" }));

const { LiveClient } = await import("./live");

// WebSocket falso: registra as instâncias abertas e o que foi enviado, sem rede.
class FakeWS {
  static instances: FakeWS[] = [];
  static readonly OPEN = 1;
  readyState = 0;
  fechado = false;
  enviados: string[] = [];
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(public url: string) {
    FakeWS.instances.push(this);
  }
  abrir() {
    this.readyState = FakeWS.OPEN;
    this.onopen?.();
  }
  send(s: string) {
    this.enviados.push(s);
  }
  close() {
    this.fechado = true;
    this.readyState = 3;
    this.onclose?.();
  }
}

function setVisibilidade(estado: DocumentVisibilityState) {
  Object.defineProperty(document, "visibilityState", { value: estado, configurable: true });
  document.dispatchEvent(new Event("visibilitychange"));
}

const originalWS = globalThis.WebSocket;

beforeEach(() => {
  FakeWS.instances = [];
  vi.useFakeTimers();
  Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  (globalThis as unknown as { WebSocket: unknown }).WebSocket = FakeWS;
});

afterEach(() => {
  vi.useRealTimers();
  (globalThis as unknown as { WebSocket: unknown }).WebSocket = originalWS;
});

// Por que estes testes existem: o servidor RECONSULTA cada painel assinado a cada 1 s.
// Uma aba de dashboard esquecida em segundo plano sustentava 420 consultas/min
// (7 painéis) no ClickHouse 24/7 — 7,4% de um núcleo por aba, medido. O poll REST já
// pausava com a aba escondida; o WebSocket, que é o caminho mais caro, não.
describe("LiveClient e a visibilidade da aba", () => {
  it("conecta normalmente com a aba visível", () => {
    const c = new LiveClient();
    c.connect();
    expect(FakeWS.instances).toHaveLength(1);
    c.close();
  });

  it("não abre socket nenhum quando a aba já nasce escondida", () => {
    Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true });
    const c = new LiveClient();
    c.connect();
    expect(FakeWS.instances).toHaveLength(0);
    c.close();
  });

  it("esconder a aba derruba o socket e NÃO reagenda reconexão", () => {
    const c = new LiveClient();
    c.connect();
    const ws = FakeWS.instances[0];
    ws.abrir();

    setVisibilidade("hidden");
    expect(ws.fechado).toBe(true);

    // O backoff de reconexão não pode ressuscitar a assinatura pelas costas.
    vi.advanceTimersByTime(60000);
    expect(FakeWS.instances).toHaveLength(1);
    c.close();
  });

  it("ao voltar a aba, reconecta e RE-ASSINA os painéis", () => {
    const c = new LiveClient();
    c.connect();
    FakeWS.instances[0].abrir();
    c.setSubscriptions([{ panelId: 7, metric: "system.cpu.utilization" }]);

    setVisibilidade("hidden");
    setVisibilidade("visible");

    expect(FakeWS.instances).toHaveLength(2);
    const novo = FakeWS.instances[1];
    novo.abrir();
    expect(novo.enviados).toHaveLength(1);
    expect(JSON.parse(novo.enviados[0])).toEqual({
      type: "subscribe",
      panels: [{ panelId: 7, metric: "system.cpu.utilization" }],
    });
    c.close();
  });

  it("uma queda real (não pausa) continua reconectando com backoff", () => {
    const c = new LiveClient();
    c.connect();
    FakeWS.instances[0].abrir();
    FakeWS.instances[0].close(); // queda do servidor, aba ainda visível

    vi.advanceTimersByTime(500);
    expect(FakeWS.instances).toHaveLength(2);
    c.close();
  });

  it("close() solta o listener de visibilidade (não reabre depois de desmontado)", () => {
    const c = new LiveClient();
    c.connect();
    c.close();
    setVisibilidade("hidden");
    setVisibilidade("visible");
    expect(FakeWS.instances).toHaveLength(1);
  });
});

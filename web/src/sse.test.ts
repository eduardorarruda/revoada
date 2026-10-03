import { describe, it, expect } from "vitest";
import { parseSSEBlock, drainSSE } from "./sse";

// Fase G — o onboarding SSH consome um POST text/event-stream lendo o corpo em
// streaming (fetch + ReadableStream). Estes helpers puros fazem o corte/parse dos
// eventos `data: {...}\n\n`; testá-los garante que um passo não se perde nem se
// duplica quando o evento chega partido entre dois chunks da rede.

type Step = { step: string; status: string; detail: string };

describe("parseSSEBlock", () => {
  it("extrai o JSON de uma linha data:", () => {
    const e = parseSSEBlock<Step>('data: {"step":"ssh","status":"ok","detail":"conectado"}');
    expect(e).toEqual({ step: "ssh", status: "ok", detail: "conectado" });
  });

  it("junta múltiplas linhas data: (spec SSE)", () => {
    const e = parseSSEBlock<{ a: number }>('data: {"a":\ndata: 1}');
    expect(e).toEqual({ a: 1 });
  });

  it("ignora comentários/heartbeats e blocos sem data:", () => {
    expect(parseSSEBlock(": keep-alive")).toBeNull();
    expect(parseSSEBlock("event: ping")).toBeNull();
  });

  it("ignora o marcador [DONE] e blocos vazios", () => {
    expect(parseSSEBlock("data: [DONE]")).toBeNull();
    expect(parseSSEBlock("")).toBeNull();
  });

  it("ignora JSON malformado em vez de lançar", () => {
    expect(parseSSEBlock("data: {não é json")).toBeNull();
  });

  it("tolera terminação CRLF numa linha data:", () => {
    const e = parseSSEBlock<Step>('data: {"step":"service","status":"ok","detail":"ativo"}\r');
    expect(e).toEqual({ step: "service", status: "ok", detail: "ativo" });
  });
});

describe("drainSSE", () => {
  it("corta um único evento completo e não deixa resto", () => {
    const { events, rest } = drainSSE<Step>("", 'data: {"step":"ssh","status":"ok","detail":"ok"}\n\n');
    expect(events).toHaveLength(1);
    expect(events[0].step).toBe("ssh");
    expect(rest).toBe("");
  });

  it("corta vários eventos vindos no mesmo chunk", () => {
    const chunk =
      'data: {"step":"ssh","status":"ok","detail":"a"}\n\n' +
      'data: {"step":"serverkey","status":"ok","detail":"b"}\n\n';
    const { events, rest } = drainSSE<Step>("", chunk);
    expect(events.map((e) => e.step)).toEqual(["ssh", "serverkey"]);
    expect(rest).toBe("");
  });

  it("segura o evento partido entre dois chunks e o entrega no seguinte", () => {
    const first = drainSSE<Step>("", 'data: {"step":"install","sta');
    expect(first.events).toHaveLength(0);
    const second = drainSSE<Step>(first.rest, 'tus":"ok","detail":"agente"}\n\n');
    expect(second.events).toHaveLength(1);
    expect(second.events[0].status).toBe("ok");
    expect(second.rest).toBe("");
  });

  it("normaliza separador CRLF entre eventos", () => {
    const { events } = drainSSE<Step>("", 'data: {"step":"ssh","status":"erro","detail":"x"}\r\n\r\n');
    expect(events).toHaveLength(1);
    expect(events[0].status).toBe("erro");
  });
});

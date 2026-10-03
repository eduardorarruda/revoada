import { describe, expect, it } from "vitest";
import { TRACE_PARTIAL_LABEL, flag, spansSaoParciais, type TraceSpan } from "../api";

// O gateway marca com revoada.trace_partial='1' os spans de um trace que PERDEU spans
// antes de a decisão de amostragem virar "manter". A tela precisa dizer isso: num
// trace parcial a raiz exibida pode ser só o span mais antigo que sobrou, e a
// duração é um PISO. Até aqui a marca existia no banco e não virava aviso nenhum —
// a tela prometia exatamente o contrário ("os problemas e os lentos você sempre
// vê").

function span(p: Partial<TraceSpan>): TraceSpan {
  return {
    start_ms: 0,
    duration_ms: 10,
    service: "svc",
    name: "GET /",
    kind: "SERVER",
    span_id: "a",
    parent_span_id: "",
    status_code: "UNSET",
    status_msg: "",
    labels: {},
    ...p,
  };
}

describe("detecção de trace parcial na cascata", () => {
  it("um único span marcado já torna o trace parcial", () => {
    // O rótulo vai nos spans que passam pelo gateway depois da virada da decisão;
    // os que chegaram antes podem não tê-lo. Exigir unanimidade esconderia o aviso
    // justamente nos traces mais fragmentados.
    const spans = [span({ span_id: "a" }), span({ span_id: "b", labels: { [TRACE_PARTIAL_LABEL]: "1" } })];
    expect(spansSaoParciais(spans)).toBe(true);
  });

  it("trace inteiro não é marcado como parcial", () => {
    expect(spansSaoParciais([span({ labels: { host: "web-01" } }), span({ span_id: "b" })])).toBe(false);
  });

  it("span sem labels não quebra nem inventa parcialidade", () => {
    const semLabels = { ...span({}), labels: undefined } as unknown as TraceSpan;
    expect(spansSaoParciais([semLabels])).toBe(false);
  });

  it("só o valor '1' conta — o gateway nunca emite outro", () => {
    expect(spansSaoParciais([span({ labels: { [TRACE_PARTIAL_LABEL]: "0" } })])).toBe(false);
  });
});

describe("leitura do campo `partial` da lista de traces", () => {
  // O ClickHouse devolve booleano como 1/0 e, dependendo do formato, como string.
  it("aceita as formas que o ClickHouse pode devolver", () => {
    expect(flag(1)).toBe(true);
    expect(flag("1")).toBe(true);
    expect(flag(true)).toBe(true);
    expect(flag("true")).toBe(true);
  });

  it("campo AUSENTE nunca vira 'parcial'", () => {
    // Enquanto /api/traces/search não agregar o rótulo, a coluna precisa ficar
    // calada: marcar todo trace como parcial seria trocar uma mentira por outra.
    expect(flag(undefined)).toBe(false);
    expect(flag(null)).toBe(false);
    expect(flag(0)).toBe(false);
    expect(flag("0")).toBe(false);
  });
});

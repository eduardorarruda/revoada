import { describe, it, expect } from "vitest";
import { displayValue, healthFreshness, lastPoint, panelFreshness, pointFreshness, staleAfterSeconds, NO_POINT } from "./lastPoint";

// Relógio fixo: 07/08/2026 12:00:00Z em segundos.
const AGORA_S = 1786104000;
const AGORA_MS = AGORA_S * 1000;

// serie monta ts/values alinhados terminando "agora", com passo de 60s.
function serie(values: (number | null)[], step = 60): { ts: number[]; values: (number | null)[] } {
  const ts = values.map((_, i) => AGORA_S - (values.length - 1 - i) * step);
  return { ts, values };
}

describe("último ponto da série", () => {
  it("devolve o valor E o instante dele", () => {
    const s = serie([1, 2, 3]);
    expect(lastPoint(s.ts, s.values)).toEqual({ value: 3, ts: AGORA_S });
  });

  it("pula os nulos do fim, mas o carimbo é o do ponto encontrado (não o de agora)", () => {
    const s = serie([10, 20, null, null]);
    expect(lastPoint(s.ts, s.values)).toEqual({ value: 20, ts: AGORA_S - 120 });
  });

  it("série inteiramente vazia ou nula não vira zero", () => {
    expect(lastPoint([], [])).toEqual(NO_POINT);
    expect(lastPoint(serie([null, null]).ts, [null, null])).toEqual(NO_POINT);
    expect(lastPoint(undefined, undefined)).toEqual(NO_POINT);
  });

  it("zero legítimo continua sendo zero (não é 'sem dado')", () => {
    const s = serie([5, 0]);
    expect(lastPoint(s.ts, s.values).value).toBe(0);
  });

  it("ignora NaN/Infinity vindos do backend", () => {
    const s = serie([7, Number.NaN]);
    expect(lastPoint(s.ts, s.values).value).toBe(7);
  });
});

describe("janela de validade do ponto (~3× o passo)", () => {
  // São TRÊS passos, não dois: o servidor passou a descartar o balde que ainda
  // está aberto, então o ponto mais novo já nasce com até um passo de idade. Com
  // dois passos de orçamento, um único minuto sem ingestão (restart do gateway,
  // hiccup de rede) apagava o número do painel e escrevia "sem dado" — pior que o
  // problema que a janela de validade existe para resolver.
  it("três passos, com piso de 2 minutos", () => {
    expect(staleAfterSeconds(60)).toBe(180);
    expect(staleAfterSeconds(300)).toBe(900);
    expect(staleAfterSeconds(3600)).toBe(10800);
    expect(staleAfterSeconds(10)).toBe(120); // piso: 3×10s seria curto demais
    expect(staleAfterSeconds(undefined)).toBe(180); // sem passo informado, assume 60s
  });

  it("um balde perdido não apaga o número, mas seis horas apagam", () => {
    const umBaldePerdido = { value: 42, ts: AGORA_S - 150 }; // 2,5 passos de 60s
    expect(pointFreshness(umBaldePerdido, 60, AGORA_MS).state).toBe("fresh");
    const morto = { value: 42, ts: AGORA_S - 6 * 3600 };
    expect(pointFreshness(morto, 60, AGORA_MS).state).toBe("stale");
  });
});

describe("frescor do ponto — o defeito do 'valor de 6 h atrás ao vivo'", () => {
  it("ponto recente é fresco", () => {
    const p = { value: 42, ts: AGORA_S - 30 };
    expect(pointFreshness(p, 60, AGORA_MS).state).toBe("fresh");
  });

  it("host morto há 6 h numa janela de 24 h NÃO é fresco", () => {
    const p = { value: 42, ts: AGORA_S - 6 * 3600 };
    const f = pointFreshness(p, 60, AGORA_MS);
    expect(f.state).toBe("stale");
    expect(f.ageSeconds).toBe(6 * 3600);
  });

  it("janela longa tolera mais: com passo de 1 h, 90 min ainda é fresco", () => {
    const p = { value: 42, ts: AGORA_S - 90 * 60 };
    expect(pointFreshness(p, 3600, AGORA_MS).state).toBe("fresh");
    expect(pointFreshness(p, 60, AGORA_MS).state).toBe("stale");
  });

  it("sem ponto é 'nunca recebeu', não 'velho'", () => {
    expect(pointFreshness(NO_POINT, 60, AGORA_MS).state).toBe("nodata");
    expect(pointFreshness({ value: 5, ts: null }, 60, AGORA_MS).state).toBe("nodata");
  });
});

describe("valor exibido pelo painel", () => {
  it("mostra o número só quando ele é atual", () => {
    expect(displayValue({ value: 42, ts: AGORA_S - 30 }, 60, AGORA_MS)).toBe(42);
  });

  it("valor velho vira null (a tela mostra '—' e a idade), nunca o número cru", () => {
    expect(displayValue({ value: 42, ts: AGORA_S - 6 * 3600 }, 60, AGORA_MS)).toBeNull();
  });

  it("sem dado vira null — jamais 0", () => {
    expect(displayValue(NO_POINT, 60, AGORA_MS)).toBeNull();
    expect(displayValue(NO_POINT, 60, AGORA_MS)).not.toBe(0);
  });

  it("zero recente continua sendo exibido como zero", () => {
    expect(displayValue({ value: 0, ts: AGORA_S - 10 }, 60, AGORA_MS)).toBe(0);
  });
});

// O defeito: /api/host-metrics e /api/health-wall devolviam `{pct, state}` e nada
// mais. A validade era constante do servidor (300 s / 120 s) e não chegava na tela.
// Quando o coletor de CPU cala e RAM/disco seguem, o host permanece "no ar" e a
// célula de CPU repetia 90,7% em vermelho por minutos, sem dizer de quando era.
describe("frescor do ladrilho de saúde (cpu/ram/disco)", () => {
  const passo = 15; // cadência real do agente

  it("amostra recente é fresca", () => {
    expect(healthFreshness({ pct: 42, state: "ok", ts: AGORA_S - 10, step_seconds: passo }, AGORA_MS).state).toBe("fresh");
  });

  it("valor de 4 min atrás com passo de 15 s NÃO é atual — e sai com a idade", () => {
    const f = healthFreshness({ pct: 90.7, state: "crit", ts: AGORA_S - 240, step_seconds: passo }, AGORA_MS);
    expect(f.state).toBe("stale");
    expect(f.ageSeconds).toBe(240);
  });

  it("piso de 2 min: um ciclo perdido não apaga o número", () => {
    expect(healthFreshness({ pct: 42, state: "ok", ts: AGORA_S - 60, step_seconds: passo }, AGORA_MS).state).toBe("fresh");
  });

  it("nodata continua sendo 'nunca medido', não 'velho'", () => {
    expect(healthFreshness({ pct: 0, state: "nodata" }, AGORA_MS).state).toBe("nodata");
    expect(healthFreshness(null, AGORA_MS).state).toBe("nodata");
  });

  it("backend antigo (sem ts) não apaga a tela — segue como fresco", () => {
    expect(healthFreshness({ pct: 42, state: "ok" }, AGORA_MS).state).toBe("fresh");
  });
});

describe("selo de frescor do painel — pior entre conexão e dado", () => {
  const recente = { value: 42, ts: AGORA_S - 20 };
  const velho = { value: 42, ts: AGORA_S - 6 * 3600 };

  it("conexão boa e dado recente = ao vivo", () => {
    const f = panelFreshness({ connected: true, messageAgeSeconds: 2, point: recente, step: 60, now: AGORA_MS });
    expect(f.status).toBe("live");
  });

  it("WebSocket perfeito com dado de 6 h atrás NÃO é 'ao vivo' — o defeito da bolinha verde", () => {
    const f = panelFreshness({ connected: true, messageAgeSeconds: 1, point: velho, step: 60, now: AGORA_MS });
    expect(f.status).toBe("stale");
    expect(f.ageSeconds).toBe(6 * 3600); // a idade mostrada é a do DADO, não a da mensagem
  });

  it("mensagem do WebSocket parada também deixa o selo velho, mesmo com dado recente", () => {
    const f = panelFreshness({ connected: true, messageAgeSeconds: 45, point: recente, step: 60, now: AGORA_MS });
    expect(f.status).toBe("stale");
    expect(f.ageSeconds).toBe(45);
  });

  it("desconectado vence tudo", () => {
    const f = panelFreshness({ connected: false, messageAgeSeconds: 1, point: recente, step: 60, now: AGORA_MS });
    expect(f.status).toBe("reconnecting");
  });

  it("sem ponto nenhum não finge estar ao vivo", () => {
    const f = panelFreshness({ connected: true, messageAgeSeconds: 1, point: NO_POINT, step: 60, now: AGORA_MS });
    expect(f.status).toBe("live"); // conexão viva; a AUSÊNCIA de valor é o painel que mostra ("—")
    expect(displayValue(NO_POINT, 60, AGORA_MS)).toBeNull();
  });
});

import { estadoDaTV, estadoDoMural, haQuanto, tendencia } from "./estado";

const base = { criticals: [], warnings: [], deploys: [] };
const alerta = { rule: "r", severity: "critical", since: "", value: 1, labels: {}, diagnosis: "", oncall: "" };

describe("estado geral da TV", () => {
  it("sem resposta ainda não afirma nada", () => {
    expect(estadoDaTV(null)).toEqual({ tom: "neutro", texto: "Conectando…" });
  });
  it("sem crítico nem aviso: em ordem", () => {
    expect(estadoDaTV(base)).toEqual({ tom: "ok", texto: "Tudo em ordem" });
  });
  it("crítico vence aviso, e o número concorda", () => {
    expect(estadoDaTV({ ...base, criticals: [alerta, alerta], warnings: [alerta] })).toEqual({ tom: "crit", texto: "2 críticos" });
    expect(estadoDaTV({ ...base, warnings: [alerta] })).toEqual({ tom: "warn", texto: "1 aviso" });
  });
});

// A seta ao lado do número grande: só aponta quando a mudança é de verdade.
describe("tendência do bloco", () => {
  it("sobe, desce ou fica estável (variação menor que 2% do valor)", () => {
    expect(tendencia([10, 12, 20])).toBe("sobe");
    expect(tendencia([20, null, 10])).toBe("desce");
    expect(tendencia([50, 50.5])).toBe("estavel");
  });
  it("com menos de dois pontos não há tendência", () => {
    expect(tendencia([null, 3])).toBeNull();
  });
});

describe("estado do mural", () => {
  const card = (state: "ok" | "warn" | "crit" | "nosignal") => ({ state });
  it("o pior manda, e sem sinal nunca parece saudável", () => {
    expect(estadoDoMural([card("ok"), card("crit"), card("crit")])).toEqual({ tom: "crit", texto: "2 críticos" });
    expect(estadoDoMural([card("ok"), card("warn")])).toEqual({ tom: "warn", texto: "1 em atenção" });
    expect(estadoDoMural([card("ok"), card("nosignal")])).toEqual({ tom: "warn", texto: "1 sem sinal" });
    expect(estadoDoMural([card("ok"), card("ok")])).toEqual({ tom: "ok", texto: "Tudo em ordem" });
    expect(estadoDoMural([])).toEqual({ tom: "neutro", texto: "Nenhum servidor" });
  });
});

describe("há quanto tempo, para ler de longe", () => {
  it("minutos, horas e dias inteiros", () => {
    expect(haQuanto(20)).toBe("agora");
    expect(haQuanto(12 * 60)).toBe("há 12 min");
    expect(haQuanto(5 * 3600)).toBe("há 5 h");
    expect(haQuanto(42.4 * 86400)).toBe("há 42 dias");
    expect(haQuanto(86400 * 1.5)).toBe("há 36 h");
  });
});

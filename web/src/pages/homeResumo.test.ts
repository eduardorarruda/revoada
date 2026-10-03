// Os indicadores do topo da Início são lidos de relance e viram decisão. Estes
// testes prendem as regras que os mantêm honestos — sobretudo as duas maneiras de
// um painel de monitoramento mentir com cara de certeza: mostrar o último valor
// de um servidor que já morreu, e mostrar 0% onde ninguém mediu nada.
import { describe, it, expect } from "vitest";
import { piorRecurso, quantosMedindo, recursosNoLimite, resumoFrota, vereditoDaFrota } from "./homeResumo";
import type { HealthCard, HealthMetric } from "../api";

const AGORA = Date.UTC(2026, 7, 10, 12, 0, 0);
const seg = (n: number) => Math.floor(AGORA / 1000) - n;

function metrica(pct: number, over: Partial<HealthMetric> = {}): HealthMetric {
  // ts recente + passo de 60s => "fresh" pelo mesmo critério dos painéis.
  return { pct, state: pct >= 90 ? "crit" : pct >= 70 ? "warn" : "ok", ts: seg(30), step_seconds: 60, ...over };
}

function card(host: string, over: Partial<HealthCard> = {}): HealthCard {
  return {
    host,
    state: "ok",
    up: true,
    cpu: metrica(10),
    mem: metrica(20),
    disk: metrica(30),
    ...over,
  };
}

describe("pior recurso da frota", () => {
  it("escolhe o maior percentual entre quem está reportando", () => {
    const r = piorRecurso([card("a", { cpu: metrica(12) }), card("b", { cpu: metrica(77) })], "cpu", AGORA);
    expect(r).toMatchObject({ host: "b", pct: 77, state: "warn" });
  });

  it("IGNORA servidor sem sinal — o número dele é história, não é o agora", () => {
    const morto = card("morto", { up: false, state: "nosignal", cpu: metrica(99) });
    const vivo = card("vivo", { cpu: metrica(15) });
    expect(piorRecurso([morto, vivo], "cpu", AGORA)).toMatchObject({ host: "vivo", pct: 15 });
  });

  it("IGNORA medida velha, mesmo com o servidor no ar (o coletor daquele recurso calou)", () => {
    // 10 minutos de idade com passo de 60s: bem além dos ~3 passos de validade.
    const calado = card("calado", { cpu: metrica(95, { ts: seg(600) }) });
    const ativo = card("ativo", { cpu: metrica(40) });
    expect(piorRecurso([calado, ativo], "cpu", AGORA)).toMatchObject({ host: "ativo", pct: 40 });
  });

  it("IGNORA 'nodata' — ausência de medição nunca vira 0%", () => {
    const semDado = card("sem", { cpu: { pct: 0, state: "nodata" } });
    expect(piorRecurso([semDado], "cpu", AGORA)).toBeNull();
  });

  it("sem ninguém medindo, devolve null (a tela mostra '—', não um zero verde)", () => {
    expect(piorRecurso([], "cpu", AGORA)).toBeNull();
    expect(piorRecurso(null, "mem", AGORA)).toBeNull();
  });

  it("empate desempata pelo hostname — o indicador não troca de nome sozinho", () => {
    const cards = [card("zeta", { mem: metrica(50) }), card("alfa", { mem: metrica(50) })];
    expect(piorRecurso(cards, "mem", AGORA)?.host).toBe("alfa");
    expect(piorRecurso([...cards].reverse(), "mem", AGORA)?.host).toBe("alfa");
  });

  it("carrega bytes usados/totais quando o recurso os tem (RAM e disco)", () => {
    const c = card("a", { mem: metrica(60, { used_bytes: 1000, total_bytes: 2000 }) });
    expect(piorRecurso([c], "mem", AGORA)).toMatchObject({ usedBytes: 1000, totalBytes: 2000 });
  });

  it("quantosMedindo conta só quem entrou de fato na conta", () => {
    const cards = [
      card("vivo1"),
      card("vivo2"),
      card("morto", { up: false, state: "nosignal" }),
      card("calado", { cpu: metrica(30, { ts: seg(900) }) }),
    ];
    expect(quantosMedindo(cards, "cpu", AGORA)).toBe(2);
  });
});

describe("resumo da frota", () => {
  it("separa quem precisa de olho de quem está em ordem, pior primeiro", () => {
    const cards = [
      card("ok-b"),
      card("critico", { state: "crit" }),
      card("ok-a"),
      card("sem-sinal", { state: "nosignal", up: false }),
      card("atencao", { state: "warn" }),
    ];
    const r = resumoFrota(cards);
    expect(r.comProblema.map((c) => c.host)).toEqual(["critico", "sem-sinal", "atencao"]);
    expect(r.emOrdem.map((c) => c.host)).toEqual(["ok-a", "ok-b"]);
    expect(r.total).toBe(5);
    expect(r.noAr).toBe(4);
  });

  it("ordena pelo nome AMIGÁVEL quando ele existe, que é o que a pessoa lê", () => {
    const cards = [card("srv-002"), card("srv-001")];
    const apelido = (h: string) => (h === "srv-002" ? "Alfa" : "Zulu");
    expect(resumoFrota(cards, apelido).emOrdem.map((c) => c.host)).toEqual(["srv-002", "srv-001"]);
  });
});

// "Precisa de atenção agora" e o veredito usam a MESMA lista de recursos no
// limite que o Mural pinta — senão uma tela diz crítico e a outra, tranquilo.
describe("recursos no limite", () => {
  it("lista cada recurso fora do ok, pior primeiro, só de quem reporta e mediu agora", () => {
    const cards = [
      card("a", { state: "warn", mem: metrica(75) }),
      card("b", { state: "crit", cpu: metrica(99), disk: metrica(80) }),
      card("morto", { up: false, state: "nosignal", cpu: metrica(99) }),
      card("velho", { state: "crit", cpu: metrica(95, { ts: seg(3600) }) }),
    ];
    expect(recursosNoLimite(cards, AGORA)).toEqual([
      { host: "b", recurso: "cpu", pct: 99, state: "crit" },
      { host: "b", recurso: "disk", pct: 80, state: "warn" },
      { host: "a", recurso: "mem", pct: 75, state: "warn" },
    ]);
  });

  it("frota toda no verde: lista vazia", () => {
    expect(recursosNoLimite([card("a")], AGORA)).toEqual([]);
  });
});

// O veredito é a frase grande no topo da Início. Ele é lido antes de qualquer
// número — e por isso não pode prometer calma que não mediu.
describe("veredito da frota", () => {
  const base = { hostsTotal: 3, hostsUp: 3, alertasCrit: 0, alertasOutros: 0, sitesTotal: 2, sitesUp: 2, sitesFora: 0, fontesFalhas: [] as string[] };

  it("tudo de pé e nenhum alerta: em ordem, e diz o que foi olhado", () => {
    const v = vereditoDaFrota(base);
    expect(v.tom).toBe("ok");
    expect(v.titulo).toBe("Tudo em ordem");
    expect(v.detalhe).toBe("3 servidores reportando, 2 sites respondendo e nenhum alerta em disparo.");
  });

  it("problemas viram uma lista curta, o pior primeiro", () => {
    const v = vereditoDaFrota({ ...base, hostsUp: 1, alertasCrit: 2, alertasOutros: 1, sitesUp: 1, sitesFora: 1 });
    expect(v.tom).toBe("crit");
    expect(v.titulo).toBe("2 alertas críticos · 1 site fora do ar · 2 servidores sem sinal");
  });

  // Site lento (DEGRADADO/SUSPEITO) não está fora do ar. Dizer "fora do ar" em
  // vermelho para ele é o painel afirmando o que não mediu.
  it("site degradado é atenção, não 'fora do ar'", () => {
    const v = vereditoDaFrota({ ...base, sitesUp: 1, sitesFora: 0 });
    expect(v.tom).toBe("warn");
    expect(v.titulo).toBe("1 site com problema");
  });

  it("só aviso não é crítico", () => {
    const v = vereditoDaFrota({ ...base, alertasOutros: 1 });
    expect(v.tom).toBe("warn");
    expect(v.titulo).toBe("1 alerta em disparo");
  });

  it("servidor sem sinal sozinho é aviso, no singular", () => {
    const v = vereditoDaFrota({ ...base, hostsUp: 2 });
    expect(v.tom).toBe("warn");
    expect(v.titulo).toBe("1 servidor sem sinal");
  });

  // Uma fonte que falhou pode estar escondendo justamente o problema.
  it("fonte que falhou impede o 'tudo em ordem'", () => {
    const v = vereditoDaFrota({ ...base, fontesFalhas: ["alertas"] });
    expect(v.tom).toBe("desconhecido");
    expect(v.titulo).toBe("Não dá para afirmar que está tudo bem");
    expect(v.detalhe).toContain("alertas");
  });

  // O Mural pinta o servidor de vermelho (CPU 99%) — a Início não pode dizer
  // "Tudo em ordem" ao mesmo tempo. Recurso no limite entra no veredito.
  it("servidor com recurso crítico impede o 'tudo em ordem'", () => {
    const v = vereditoDaFrota({ ...base, hostsCrit: 1 });
    expect(v.tom).toBe("crit");
    expect(v.titulo).toBe("1 servidor no limite");
    expect(v.detalhe).toContain("no limite");
  });

  it("recurso em atenção é aviso, e vem depois do que está fora do ar", () => {
    const v = vereditoDaFrota({ ...base, sitesUp: 1, sitesFora: 1, hostsWarn: 2 });
    expect(v.tom).toBe("crit");
    expect(v.titulo).toBe("1 site fora do ar · 2 servidores em atenção");
    expect(vereditoDaFrota({ ...base, hostsWarn: 1 }).tom).toBe("warn");
  });

  it("frota vazia não é 'tudo em ordem': é começo", () => {
    const v = vereditoDaFrota({ ...base, hostsTotal: 0, hostsUp: 0, sitesTotal: 0, sitesUp: 0, sitesFora: 0 });
    expect(v.tom).toBe("vazio");
  });
});

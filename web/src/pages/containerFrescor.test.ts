import { describe, it, expect } from "vitest";
import { containerUsage } from "./containers";
import { containerFreshness } from "../metrics/lastPoint";
import type { ContainerStatus } from "../api";

// O defeito que estes testes travam foi MEDIDO em 13/08/2026: um container que rodava
// a ~4% de CPU apareceu na tela com 52,4%, verde, por mais de dois minutos — ao lado
// de um ladrilho de CPU do host que, esse sim, dizia corretamente "sem dado". O
// container era o único objeto do painel servido sem carimbo de tempo: a única
// validade era uma janela de 5 minutos do servidor, que nunca chegava à interface.

const base: ContainerStatus = {
  name: "api",
  image: "api:1.0",
  state: "running",
  health: "healthy",
  running: true,
  restarts: 0,
  cpu_pct: 52.4,
  mem_used_bytes: 1_073_741_824,
  status: "ok",
};

const agora = Date.parse("2026-08-13T12:00:00Z");
const segundosAtras = (n: number) => Math.floor(agora / 1000) - n;

describe("frescor do container", () => {
  it("dentro da cadência, o número é publicado", () => {
    const c = { ...base, ts: segundosAtras(10), step_seconds: 15 };
    expect(containerFreshness(c, agora).state).toBe("fresh");
    expect(containerUsage(c, agora)).toContain("cpu 52,4%");
  });

  it("passado o frescor, o número SAI da tela e a idade entra no lugar", () => {
    // 4 minutos com passo de 15 s: muito além de 3× o passo e do piso de 2 min.
    const c = { ...base, ts: segundosAtras(240), step_seconds: 15 };
    expect(containerFreshness(c, agora).state).toBe("stale");
    const texto = containerUsage(c, agora);
    expect(texto).toContain("sem dado recente");
    // O que não pode, em hipótese alguma, é o valor velho continuar sendo exibido
    // como se fosse a medida de agora.
    expect(texto).not.toContain("52,4");
  });

  it("agente em modo cron (passo de 60 s) não é acusado de velho a cada minuto", () => {
    // A régua é 3× o passo REAL, não uma constante: quem reporta 1×/min continua
    // fresco aos 100 s. Sem isso, metade da frota apareceria sempre sem dado.
    const c = { ...base, ts: segundosAtras(100), step_seconds: 60 };
    expect(containerFreshness(c, agora).state).toBe("fresh");
  });

  it("backend sem o campo novo continua funcionando (nada de tela vazia)", () => {
    const { ...semTS } = base;
    expect(containerFreshness(semTS, agora).state).toBe("fresh");
    expect(containerUsage(semTS, agora)).toContain("cpu 52,4%");
  });

  it("container parado não mostra uso nenhum, fresco ou não", () => {
    const c: ContainerStatus = { ...base, running: false, state: "exited", ts: segundosAtras(5) };
    expect(containerUsage(c, agora)).toBeNull();
  });
});

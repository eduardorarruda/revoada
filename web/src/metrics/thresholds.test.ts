import { describe, it, expect } from "vitest";
import {
  DEFAULT_THRESHOLDS,
  resolveThreshold,
  resourceOfMetric,
  thresholdColorVar,
  thresholdForMetric,
  thresholdState,
} from "./thresholds";
import type { HostThreshold } from "../api";

const LISTA: HostThreshold[] = [
  { hostname: "", metric: "cpu", warn: 70, crit: 90 },
  { hostname: "", metric: "mem", warn: 70, crit: 90 },
  { hostname: "", metric: "disk", warn: 75, crit: 90 },
  { hostname: "app-prod-01", metric: "cpu", warn: 50, crit: 65 },
];

describe("limiares de saúde — precedência", () => {
  it("override do host vence o global", () => {
    expect(resolveThreshold("cpu", "app-prod-01", LISTA)).toEqual({ warn: 50, crit: 65 });
  });

  it("host sem override cai no global configurado", () => {
    expect(resolveThreshold("cpu", "outro-host", LISTA)).toEqual({ warn: 70, crit: 90 });
  });

  it("sem host no contexto usa o global da métrica, não o do host algum", () => {
    expect(resolveThreshold("disk", undefined, LISTA)).toEqual({ warn: 75, crit: 90 });
  });

  it("lista ausente (ainda carregando ou sem permissão) cai no default embutido", () => {
    expect(resolveThreshold("cpu", "app-prod-01", null)).toEqual(DEFAULT_THRESHOLDS.cpu);
    expect(resolveThreshold("disk", null, [])).toEqual(DEFAULT_THRESHOLDS.disk);
  });

  it("swap não é configurável e sempre usa o default (mesmo critério de RAM)", () => {
    expect(resolveThreshold("swap", "app-prod-01", LISTA)).toEqual({ warn: 70, crit: 90 });
  });
});

describe("limiares de saúde — defaults batem com o servidor", () => {
  it("CPU e RAM são 70/90, disco é 75/90 (health.Defaults)", () => {
    expect(DEFAULT_THRESHOLDS.cpu).toEqual({ warn: 70, crit: 90 });
    expect(DEFAULT_THRESHOLDS.mem).toEqual({ warn: 70, crit: 90 });
    expect(DEFAULT_THRESHOLDS.disk).toEqual({ warn: 75, crit: 90 });
  });

  it("o antigo 70/85 cravado no gauge não existe mais", () => {
    for (const t of Object.values(DEFAULT_THRESHOLDS)) {
      expect(t.crit).not.toBe(85);
    }
  });
});

describe("métrica → recurso com semáforo", () => {
  it("reconhece as utilizações de sistema e de container", () => {
    expect(resourceOfMetric("system.cpu.utilization")).toBe("cpu");
    expect(resourceOfMetric("container.memory.utilization")).toBe("mem");
    expect(resourceOfMetric("system.filesystem.utilization")).toBe("disk");
    expect(resourceOfMetric("system.paging.utilization")).toBe("swap");
  });

  it("métrica que não é utilização de recurso não ganha semáforo", () => {
    expect(resourceOfMetric("system.processes.count")).toBeNull();
    expect(resourceOfMetric("system.network.io.bytes_recv")).toBeNull();
    expect(thresholdForMetric("system.uptime", "app-prod-01", LISTA)).toBeNull();
  });

  it("thresholdForMetric junta métrica + host", () => {
    expect(thresholdForMetric("system.cpu.utilization", "app-prod-01", LISTA)).toEqual({ warn: 50, crit: 65 });
  });
});

describe("classificação (mesma regra do servidor)", () => {
  const cpu = DEFAULT_THRESHOLDS.cpu;

  it("CPU a 87% é ATENÇÃO, não crítico — o cenário do defeito", () => {
    expect(thresholdState(87, cpu)).toBe("warn");
    expect(thresholdColorVar(thresholdState(87, cpu))).toBe("--warn");
  });

  it("vira crítico exatamente no limiar do alerta", () => {
    expect(thresholdState(89.9, cpu)).toBe("warn");
    expect(thresholdState(90, cpu)).toBe("crit");
  });

  it("abaixo do aviso é saudável", () => {
    expect(thresholdState(69.9, cpu)).toBe("ok");
    expect(thresholdState(70, cpu)).toBe("warn");
  });

  it("disco tolera mais que CPU até 75%: 72% é atenção em CPU e ainda saudável em disco", () => {
    expect(thresholdState(72, DEFAULT_THRESHOLDS.cpu)).toBe("warn");
    expect(thresholdState(72, DEFAULT_THRESHOLDS.disk)).toBe("ok");
    expect(thresholdState(76, DEFAULT_THRESHOLDS.disk)).toBe("warn");
  });

  it("sem dado não é 'ok' — é cinza, nunca verde", () => {
    expect(thresholdState(null, cpu)).toBeNull();
    expect(thresholdState(Number.NaN, cpu)).toBeNull();
    expect(thresholdColorVar(null)).toBe("--text-3");
  });
});

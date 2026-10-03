import { resumoDoServidor } from "./hostResumo";
import type { HostMetrics } from "../api";

const AGORA = 1_790_000_000;
const m = (pct: number, state: "ok" | "warn" | "crit" | "nodata" = "ok", idade = 30) => ({ pct, state, ts: AGORA - idade, step_seconds: 60 });
const base: HostMetrics = {
  host: "srv",
  up: true,
  uptime_secs: 3 * 86400,
  cpu: m(96.24, "crit"),
  mem: { ...m(61.2), used_bytes: 5 * 1024 ** 3, total_bytes: 8 * 1024 ** 3 },
  disks: [
    { mount: "/", ...m(40), used_bytes: 1, total_bytes: 2 },
    { mount: "/var", ...m(88.5, "warn"), used_bytes: 1, total_bytes: 2 },
  ],
  net: { rx_bps: 2048, tx_bps: 512 },
  procs: 214,
};

describe("resumo do servidor (topo do detalhe)", () => {
  it("mostra o que está medido, com o tom do estado", () => {
    const r = Object.fromEntries(resumoDoServidor(base, AGORA * 1000).map((k) => [k.rotulo, k]));
    expect(r["CPU"]).toMatchObject({ valor: "96,2%", tone: "crit" });
    expect(r["RAM"]).toMatchObject({ valor: "61,2%", sub: "5 GB de 8 GB" });
    expect(r["Disco mais cheio"]).toMatchObject({ valor: "88,5%", tone: "warn", sub: "em /var" });
    expect(r["Rede, recebendo"]).toMatchObject({ valor: "2 KB/s", sub: "↑ 512 B/s enviando" });
    expect(r["Processos"]).toMatchObject({ valor: "214" });
    expect(r["No ar há"]).toMatchObject({ valor: "3 d" });
  });

  // A regra de todo indicador do painel: número velho não é o estado de agora.
  it("dado velho ou não medido vira travessão, nunca o último valor", () => {
    const velho = { ...base, cpu: m(96.24, "crit", 3600), procs: null, net: {} };
    const r = Object.fromEntries(resumoDoServidor(velho, AGORA * 1000).map((k) => [k.rotulo, k]));
    expect(r["CPU"]).toMatchObject({ valor: null, sub: "sem dado recente" });
    expect(r["CPU"].tone).toBeUndefined();
    expect(r["Processos"].valor).toBeNull();
    expect(r["Rede, recebendo"].valor).toBeNull();
  });

  it("servidor sem sinal não tem resumo de recurso", () => {
    expect(resumoDoServidor({ ...base, up: false }, AGORA * 1000).every((k) => k.valor === null)).toBe(true);
  });
});

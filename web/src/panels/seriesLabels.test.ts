import { describe, it, expect } from "vitest";
import { buildSeriesLabels } from "./seriesLabels";

// hostLabel "amigável" de brinquedo: mapeia alguns hosts, senão devolve o próprio.
const friendly = (h: string) => ({ "srv-03": "app-01", "srv-02": "db-01" } as Record<string, string>)[h] ?? h;

describe("buildSeriesLabels", () => {
  it("com só host variando, mostra apenas o nome amigável (caso do gráfico real)", () => {
    // Duas séries que diferem só pelo host; todo o resto é metadado de host/agente.
    const series = [
      {
        labels: {
          "agent.version": "0.7.0",
          arch: "x86_64",
          host: "srv-03",
          "host.cpu.cores": "2",
          "host.name": "srv-03",
          kernel: "6.8.0-124-generic",
          os: "linux",
          platform: "ubuntu",
        },
      },
      {
        labels: {
          "agent.version": "0.7.0",
          arch: "x86_64",
          host: "srv-02",
          "host.cpu.cores": "8",
          "host.name": "srv-02",
          kernel: "6.8.0-136-generic",
          os: "linux",
          platform: "ubuntu",
        },
      },
    ];
    expect(buildSeriesLabels(series, "cpu", friendly)).toEqual(["app-01", "db-01"]);
  });

  it("host + dimensão real (mountpoint) mantém a dimensão como chave=valor", () => {
    const series = [
      { labels: { host: "srv-03", mountpoint: "/" } },
      { labels: { host: "srv-03", mountpoint: "/home" } },
    ];
    expect(buildSeriesLabels(series, "disk", friendly)).toEqual([
      "app-01 · mountpoint=/",
      "app-01 · mountpoint=/home",
    ]);
  });

  it("série única sem nada a distinguir cai no host amigável", () => {
    const series = [{ labels: { host: "srv-02", os: "linux", kernel: "x" } }];
    expect(buildSeriesLabels(series, "mem", friendly)).toEqual(["db-01"]);
  });

  it("sem host nem dimensão limpa, usa o texto de fallback", () => {
    const series = [{ labels: { os: "linux", platform: "ubuntu" } }];
    expect(buildSeriesLabels(series, "uptime", friendly)).toEqual(["uptime"]);
  });

  it("groupBy explícito manda: só aquela dimensão", () => {
    const series = [
      { labels: { host: "srv-03", device: "eth0" } },
      { labels: { host: "srv-03", device: "eth1" } },
    ];
    expect(buildSeriesLabels(series, "net", friendly, "device")).toEqual([
      "device=eth0",
      "device=eth1",
    ]);
  });

  it("groupBy por host resolve para nome amigável", () => {
    const series = [{ labels: { host: "srv-03" } }, { labels: { host: "srv-02" } }];
    expect(buildSeriesLabels(series, "cpu", friendly, "host")).toEqual(["app-01", "db-01"]);
  });

  it("duas dimensões reais variando aparecem ambas, host primeiro", () => {
    const series = [
      { labels: { host: "srv-03", cpu: "0", mode: "user" } },
      { labels: { host: "srv-03", cpu: "1", mode: "system" } },
    ];
    expect(buildSeriesLabels(series, "cpu", friendly)).toEqual([
      "app-01 · cpu=0 · mode=user",
      "app-01 · cpu=1 · mode=system",
    ]);
  });
  it("a imagem do container não polui a legenda (nome e digest são a mesma coisa)", () => {
    // O agente manda `image` ora com o nome, ora com o digest: sem filtrar, a legenda
    // ganhava um segundo eixo para o MESMO container e a linha ficava ilegível.
    const series = [
      { labels: { host: "srv-03", container: "api", image: "ghcr.io/app:1.2" } },
      { labels: { host: "srv-03", container: "web", image: "sha256:0b1c2d3e4f5a" } },
    ];
    expect(buildSeriesLabels(series, "cpu", friendly)).toEqual([
      "app-01 · container=api",
      "app-01 · container=web",
    ]);
  });
});

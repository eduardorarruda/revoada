import { describe, it, expect } from "vitest";
import { aggBloqueada, metricMeta, metricLabel, KNOWN_METRIC_NAMES } from "./dict";
import { formatValue, formatMetric, formatDuration, formatCount, unitFormatter } from "../format";

// INVENTÁRIO das métricas realmente GRAVADAS no armazenamento, venham de onde vierem.
//
// Antes esta lista só transcrevia o agente, e por isso o teste passava enquanto as 7
// métricas `synthetic.http.*` — escritas pelo PRÓPRIO servidor
// (server/internal/sitecheck/checker.go) — não tinham entrada nenhuma no dicionário.
// Elas aparecem no seletor do /explore, porque a lista de lá sai de
// `SELECT DISTINCT metric` no armazenamento e não faz ideia de quem gravou; o painel
// então mostrava o nome técnico humanizado e o número SEM unidade ("114,23", sem
// dizer se era ms, s ou nada). Cobertura que só olha um produtor não é cobertura.
//
// Ao mexer no agente OU no que o servidor grava, mexa AQUI e no dicionário — os dois
// testes abaixo travam nas duas direções (métrica emitida sem entrada; entrada
// apontando para métrica que ninguém grava).
const EMITTED = [
  // agent/internal/collect/host.go
  "system.cpu.utilization",
  "system.cpu.steal",
  "system.cpu.utilization.total",
  "system.cpu.load_average.1m",
  "system.cpu.load_average.5m",
  "system.cpu.load_average.15m",
  "system.memory.utilization",
  "system.memory.used",
  "system.memory.total",
  "system.memory.available",
  "system.paging.utilization",
  "system.paging.used",
  "system.paging.total",
  "system.network.io.bytes_recv",
  "system.network.io.bytes_sent",
  "system.processes.count",
  "system.uptime",
  "system.filesystem.utilization",
  "system.filesystem.used",
  "system.filesystem.total",
  "system.filesystem.available",
  // agent/internal/collect/containers.go
  "container.running",
  "container.state",
  "container.restarts",
  "container.cpu.utilization",
  "container.memory.utilization",
  "container.memory.usage",
  "container.memory.limit",
  // agent/internal/collect/self.go
  "agent.self.goroutines",
  "agent.self.memory.heap_bytes",
  "agent.self.cpu.percent",
  "agent.self.memory.rss_bytes",
  // server/internal/sitecheck/checker.go (writeMetrics) — gravadas pelo SERVIDOR, não
  // pelo agente. Aparecem no seletor do /explore como qualquer outra.
  "synthetic.http.total_ms",
  "synthetic.http.ttfb_ms",
  "synthetic.http.dns_ms",
  "synthetic.http.connect_ms",
  "synthetic.http.tls_ms",
  "synthetic.http.cert_days_left",
  "synthetic.http.up",
];

// Chaves do dicionário que NÃO são métricas do agente e por isso não entram em
// EMITTED. Cada exceção precisa de uma justificativa escrita — sem esta lista, a
// verificação de mão dupla abaixo viraria letra morta.
const NAO_SAO_METRICAS: Record<string, string> = {
  // Atributo de recurso (label) que chega junto das séries e aparece na rotulagem;
  // nunca é o `metric` de uma consulta. Ver gateway/internal/otlp/convert.go.
  "host.cpu.cores": "atributo de recurso (nº de núcleos), não uma série temporal",
};

describe("dicionário de métricas", () => {
  it("toda métrica emitida pelo agente tem entrada conhecida", () => {
    for (const name of EMITTED) {
      const m = metricMeta(name);
      expect(m.known, `métrica emitida pelo agente sem entrada no dicionário: ${name}`).toBe(true);
      expect(m.label.length).toBeGreaterThan(0);
      expect(m.description.length, `métrica sem descrição explicativa: ${name}`).toBeGreaterThan(0);
    }
  });

  it("nenhuma entrada do dicionário aponta para métrica que o agente não emite", () => {
    const emitidas = new Set(EMITTED);
    const orfas = KNOWN_METRIC_NAMES.filter((n) => !emitidas.has(n) && !(n in NAO_SAO_METRICAS));
    expect(
      orfas,
      `entradas do dicionário sem métrica correspondente no agente (nome errado?): ${orfas.join(", ")}`,
    ).toEqual([]);
  });

  it("as chaves do dicionário são todas conhecidas", () => {
    for (const name of KNOWN_METRIC_NAMES) {
      expect(metricMeta(name).known).toBe(true);
    }
  });

  it("métricas que saíam sem unidade agora têm a unidade certa", () => {
    // Regressão do defeito: estes números apareciam crus na tela.
    expect(metricMeta("system.network.io.bytes_recv").unit).toBe("bytes");
    expect(metricMeta("system.network.io.bytes_sent").unit).toBe("bytes");
    expect(metricMeta("system.memory.available").unit).toBe("bytes");
    expect(metricMeta("system.filesystem.available").unit).toBe("bytes");
    expect(metricMeta("system.uptime").unit).toBe("duration_s");
    expect(metricMeta("system.cpu.steal").unit).toBe("percent");
  });

  it("as 7 métricas de sondagem do servidor têm unidade — não saem como número cru", () => {
    // O defeito: "114,23" na tela, sem dizer se era ms, s ou nada.
    for (const m of ["total_ms", "ttfb_ms", "dns_ms", "connect_ms", "tls_ms"]) {
      expect(metricMeta(`synthetic.http.${m}`).unit).toBe("duration_ms");
    }
    expect(metricMeta("synthetic.http.cert_days_left").unit).toBe("days");
    expect(metricMeta("synthetic.http.up").unit).toBe("bool");
    // E o formatador realmente escreve a unidade.
    expect(formatMetric("synthetic.http.ttfb_ms", 114.23)).toBe("114 ms");
    expect(formatMetric("synthetic.http.total_ms", 2400)).toBe("2,4 s");
    expect(formatMetric("synthetic.http.cert_days_left", 42)).toBe("42 dias");
  });

  it("milissegundos fora do dicionário não são lidos como segundos", () => {
    expect(metricMeta("app.request.latency_ms").unit).toBe("duration_ms");
    expect(formatValue(250, "duration_ms")).toBe("250 ms");
  });

  it("container.restarts é acumulado, zera ao recriar, e não aceita soma", () => {
    const m = metricMeta("container.restarts");
    expect(m.label).toContain("acumulado");
    expect(m.description).toMatch(/recriad|zera/i);
    expect(aggBloqueada("container.restarts", "sum")).not.toBeNull();
    expect(aggBloqueada("container.restarts", "max")).toBeNull();
    expect(aggBloqueada("system.cpu.utilization", "sum")).toBeNull();
  });

  it("a RAM do container avisa que o denominador pode ser a máquina inteira", () => {
    // Mesmo número, dois significados: "% do meu limite" e "% da máquina" — com o
    // mesmo semáforo 70/90. Medido em dev: os 7 containers reportavam limite = MemTotal.
    expect(metricMeta("container.memory.utilization").description).toMatch(/limite/i);
    expect(metricMeta("container.memory.utilization").description).toMatch(/máquina/i);
  });

  it("carga média NÃO é percentual (é fila de execução, adimensional)", () => {
    for (const m of ["1m", "5m", "15m"]) {
      expect(metricMeta(`system.cpu.load_average.${m}`).unit).toBe("none");
    }
    expect(formatValue(4.2, metricMeta("system.cpu.load_average.1m").unit)).not.toContain("%");
  });

  it("infere unidade pelo sufixo quando a métrica é desconhecida", () => {
    expect(metricMeta("foo.bar.utilization").unit).toBe("percent");
    expect(metricMeta("foo.bar.bytes").unit).toBe("bytes");
    expect(metricMeta("foo.bar.cores").unit).toBe("cores");
    expect(metricMeta("foo.bar.running").unit).toBe("bool");
    expect(metricMeta("foo.bar.seconds").unit).toBe("duration_s");
    expect(metricMeta("foo.bar.count").unit).toBe("count");
  });

  it("métrica desconhecida deriva rótulo do nome técnico e marca known:false", () => {
    const m = metricMeta("weird.custom.thing");
    expect(m.known).toBe(false);
    expect(m.unit).toBe("none");
    expect(m.label).toBe("Weird custom thing");
  });

  it("metricLabel devolve o rótulo amigável", () => {
    // "CPU (%)" é a SOMA (consumo + roubo do hipervisor) — o número que o painel
    // destaca. A parcela consumida tem rótulo próprio para que as duas nunca sejam
    // confundidas numa mesma tela.
    expect(metricLabel("system.cpu.utilization.total")).toBe("CPU (%)");
    expect(metricLabel("system.cpu.utilization")).toBe("CPU consumida (%)");
  });
});

describe("formatador único de valores", () => {
  it("percent com vírgula decimal e sufixo %", () => {
    expect(formatValue(42.34, "percent")).toBe("42,3%");
    expect(formatValue(42.34, "percent", { compact: true })).toBe("42%");
  });
  it("bytes com escala base 1024 pt-BR", () => {
    expect(formatValue(8589934592, "bytes")).toBe("8 GB");
  });
  it("bytes_per_s acrescenta /s", () => {
    expect(formatValue(1677721, "bytes_per_s")).toBe("1,6 MB/s");
  });
  it("count inteiro com milhar; compacto quando grande", () => {
    expect(formatCount(12480)).toBe("12.480");
    expect(formatValue(1200000, "count")).toMatch(/mi/);
  });
  it("duration escala ms/s/min/h", () => {
    expect(formatDuration(0.004)).toBe("4 ms");
    expect(formatDuration(42)).toBe("42 s");
    expect(formatDuration(180)).toBe("3 min");
    expect(formatDuration(7200)).toBe("2 h");
  });
  it("bool vira sim/não; cores inteiro", () => {
    expect(formatValue(1, "bool")).toBe("sim");
    expect(formatValue(0, "bool")).toBe("não");
    expect(formatValue(8, "cores")).toBe("8");
  });
  it("valor inválido vira travessão", () => {
    expect(formatValue(null, "percent")).toBe("—");
    expect(formatValue(NaN, "bytes")).toBe("—");
    expect(formatValue(undefined, "count")).toBe("—");
  });
  it("formatMetric resolve unidade pelo nome", () => {
    expect(formatMetric("system.memory.used", 8589934592)).toBe("8 GB");
    expect(formatMetric("system.cpu.utilization", 42.34)).toBe("42,3%");
  });
  it("unitFormatter devolve função reutilizável", () => {
    const f = unitFormatter("bytes");
    expect(f(1024)).toBe("1 KB");
    expect(f(null)).toBe("—");
  });
});

// As métricas do PRÓPRIO painel (formato Prometheus, separadas por underscore) caíam
// todas em unidade "none" porque as regras de sufixo exigiam ponto. Quem abrisse o
// Explorador via bytes como "2576" e segundos como "47" — número pelado, sem unidade,
// exatamente o que o formatador único existe para impedir.
describe("métricas do painel (underscore) têm unidade e trava de acumulado", () => {
  it("infere unidade pelo sufixo mesmo com underscore", () => {
    expect(metricMeta("revoada_nats_stream_bytes").unit).toBe("bytes");
    expect(metricMeta("revoada_agent_ts_age_seconds").unit).toBe("duration_s");
    expect(metricMeta("revoada_auth_cache_size").unit).toBe("count");
  });

  it("contador acumulado não pode ser somado nem mediado", () => {
    expect(aggBloqueada("revoada_writer_rows_written_total", "avg")).toBeTruthy();
    expect(aggBloqueada("revoada_writer_rows_written_total", "sum")).toBeTruthy();
    // max continua liberado: é o valor no fim do período, que tem significado.
    expect(aggBloqueada("revoada_writer_rows_written_total", "max")).toBeNull();
    // E métrica que não é acumulado segue livre.
    expect(aggBloqueada("system.cpu.utilization", "avg")).toBeNull();
  });
});

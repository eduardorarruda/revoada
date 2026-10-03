// Resumo do topo do detalhe do servidor: CPU, RAM, disco mais cheio, rede, processos
// e tempo no ar, na faixa de indicadores (components/Kpi.tsx).
//
// Mesmas regras de honestidade da Início (homeResumo.ts): métrica velha ou não
// medida vira "—", nunca o último número como se fosse de agora; servidor sem sinal
// não tem recurso nenhum a mostrar.
import type { HealthMetric, HostMetrics } from "../api";
import { formatBytes, formatDuration, formatRateBytes, formatValue } from "../format";
import { healthFreshness } from "../metrics/lastPoint";

export interface ItemResumo {
  rotulo: string;
  valor: string | null;
  sub?: string;
  tone?: "warn" | "crit";
}

function tom(state: HealthMetric["state"]): "warn" | "crit" | undefined {
  return state === "crit" || state === "warn" ? state : undefined;
}

function recurso(rotulo: string, m: HealthMetric | null | undefined, agoraMs: number, sub?: string): ItemResumo {
  if (!m || healthFreshness(m, agoraMs).state !== "fresh") {
    return { rotulo, valor: null, sub: m && m.state !== "nodata" ? "sem dado recente" : "não medido" };
  }
  return { rotulo, valor: formatValue(m.pct, "percent"), tone: tom(m.state), sub };
}

export function resumoDoServidor(h: HostMetrics, agoraMs = Date.now()): ItemResumo[] {
  if (!h.up) {
    return ["CPU", "RAM", "Disco mais cheio", "Rede, recebendo", "Processos", "No ar há"].map((rotulo) => ({
      rotulo,
      valor: null,
      sub: "servidor sem sinal",
    }));
  }
  const ram =
    h.mem.used_bytes != null && h.mem.total_bytes
      ? `${formatBytes(h.mem.used_bytes)} de ${formatBytes(h.mem.total_bytes)}`
      : undefined;
  const discos = h.disks.filter((d) => healthFreshness(d, agoraMs).state === "fresh");
  const pior = discos.reduce<(typeof discos)[number] | null>((a, d) => (!a || d.pct > a.pct ? d : a), null);
  const temRede = h.net.rx_bps != null;
  return [
    recurso("CPU", h.cpu, agoraMs, "consumo + roubo"),
    recurso("RAM", h.mem, agoraMs, ram),
    pior
      ? { rotulo: "Disco mais cheio", valor: formatValue(pior.pct, "percent"), tone: tom(pior.state), sub: `em ${pior.mount}` }
      : { rotulo: "Disco mais cheio", valor: null, sub: "não medido" },
    temRede
      ? {
          rotulo: "Rede, recebendo",
          valor: formatRateBytes(h.net.rx_bps ?? 0),
          sub: h.net.tx_bps != null ? `↑ ${formatRateBytes(h.net.tx_bps)} enviando` : "recebendo",
        }
      : { rotulo: "Rede, recebendo", valor: null, sub: "taxa ainda não calculada" },
    { rotulo: "Processos", valor: h.procs != null ? String(h.procs) : null, sub: h.procs != null ? "rodando agora" : "não medido" },
    { rotulo: "No ar há", valor: h.uptime_secs > 0 ? formatDuration(h.uptime_secs) : null, sub: "desde o último boot" },
  ];
}

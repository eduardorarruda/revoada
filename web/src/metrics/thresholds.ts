// Limiares de saúde (semáforo) — FONTE ÚNICA da verdade no front.
//
// O painel tinha três verdades para o mesmo número: o gauge do dashboard pintava
// vermelho a partir de 85, o card do Mural de Saúde ficava âmbar (limiar real
// warn 70 / crit 90) e o alerta só disparava em 90. CPU a 87% aparecia vermelha,
// âmbar e sem alerta na MESMA tela.
//
// Aqui os limiares vêm de um lugar só, na mesma ordem de precedência do backend
// (`server/internal/health/health.go`):
//
//   1. override do host           (linha de host_thresholds com hostname = <host>)
//   2. limiar global configurado  (linha com hostname = "")
//   3. default embutido           (DEFAULT_THRESHOLDS, idêntico ao do servidor)
//
// Quando não há host no contexto (painel multi-host, dashboard genérico sem servidor
// escolhido), caímos direto no global/default da MÉTRICA — nunca num 70/85 inventado.

import type { HostThreshold } from "../api";

/** Recursos que têm semáforo. `swap` segue o mesmo critério de memória. */
export type ResourceKind = "cpu" | "mem" | "disk" | "swap";

export interface Threshold {
  /** Percentual a partir do qual o recurso fica em ATENÇÃO (âmbar). */
  warn: number;
  /** Percentual a partir do qual o recurso fica CRÍTICO (vermelho). */
  crit: number;
}

/**
 * Defaults embutidos, espelho exato de `health.Defaults` no servidor. Disco é mais
 * rígido de propósito: disco cheio quebra serviço de forma abrupta e é lento de
 * remediar.
 */
export const DEFAULT_THRESHOLDS: Record<ResourceKind, Threshold> = {
  cpu: { warn: 70, crit: 90 },
  mem: { warn: 70, crit: 90 },
  swap: { warn: 70, crit: 90 },
  disk: { warn: 75, crit: 90 },
};

/**
 * resourceOfMetric mapeia o nome técnico da métrica para o recurso com semáforo.
 * Devolve null para métricas que NÃO são utilização de recurso (bytes, contagem,
 * carga média…) — essas não ganham cor por limiar, e é isso que impede um gauge de
 * "processos" ficar vermelho por passar de 90.
 */
export function resourceOfMetric(metric: string): ResourceKind | null {
  switch (metric) {
    // A soma (`.total`) entra na família "cpu" porque é a mesma grandeza medida na
    // mesma escala — e é ela que virou o número principal do painel. Sem isto, a
    // tela pintaria de cinza justamente a série que passou a mandar.
    //
    // `system.cpu.steal` fica DE FORA de propósito. Ela é percentual e é sobre CPU,
    // mas a escala de preocupação é outra: 5% de steal sustentado já é sinal de host
    // físico lotado, enquanto 5% de CPU não é nada. Herdar 70/90 pintaria 20% de
    // roubo — degradação séria — de verde. Sem família, a tela mostra o número sem
    // afirmar um veredito que ninguém calibrou.
    case "system.cpu.utilization.total":
    case "system.cpu.utilization":
    case "container.cpu.utilization":
      return "cpu";
    case "system.memory.utilization":
    case "container.memory.utilization":
      return "mem";
    case "system.filesystem.utilization":
      return "disk";
    case "system.paging.utilization":
      return "swap";
    default:
      return null;
  }
}

/**
 * resolveThreshold aplica a precedência override-do-host → global → default.
 * `list` é a resposta de `/api/host-thresholds`; `null` (ainda carregando, ou
 * usuário sem permissão de leitura) cai no default embutido, que é o mesmo do
 * servidor — então a cor continua batendo com o alerta.
 */
export function resolveThreshold(
  kind: ResourceKind,
  hostname: string | undefined | null,
  list: HostThreshold[] | null | undefined,
): Threshold {
  const fallback = DEFAULT_THRESHOLDS[kind];
  if (!list || list.length === 0) return fallback;
  // A tabela só guarda cpu/mem/disk; swap não é configurável e usa o default.
  const configurable = kind === "swap" ? null : kind;
  if (!configurable) return fallback;
  const host = (hostname ?? "").trim();
  if (host) {
    const override = list.find((t) => t.hostname === host && t.metric === configurable);
    if (override) return { warn: override.warn, crit: override.crit };
  }
  const global = list.find((t) => t.hostname === "" && t.metric === configurable);
  if (global) return { warn: global.warn, crit: global.crit };
  return fallback;
}

/**
 * thresholdForMetric é o atalho usado pelos painéis: nome da métrica + host →
 * limiar. Devolve null quando a métrica não tem semáforo (nada a colorir).
 */
export function thresholdForMetric(
  metric: string,
  hostname: string | undefined | null,
  list: HostThreshold[] | null | undefined,
): Threshold | null {
  const kind = resourceOfMetric(metric);
  if (!kind) return null;
  return resolveThreshold(kind, hostname, list);
}

export type ThresholdState = "ok" | "warn" | "crit";

/**
 * thresholdState classifica um percentual, com a MESMA regra do servidor
 * (`health.Classify`): crit quando >= crit, warn quando >= warn, senão ok.
 * Assim o gauge muda de cor exatamente no ponto em que o alerta dispara.
 */
export function thresholdState(pct: number | null | undefined, t: Threshold): ThresholdState | null {
  if (pct == null || !Number.isFinite(pct)) return null;
  if (pct >= t.crit) return "crit";
  if (pct >= t.warn) return "warn";
  return "ok";
}

/** Token de cor do design system para cada estado (nunca hex cravado). */
export function thresholdColorVar(state: ThresholdState | null): string {
  switch (state) {
    case "crit":
      return "--crit";
    case "warn":
      return "--warn";
    case "ok":
      return "--ok";
    default:
      // Sem dado: cinza neutro. Um "0% verde" mentiria dizendo que está saudável.
      return "--text-3";
  }
}

/**
 * thresholdHint é a explicação em PT-BR do limiar em vigor, para o tooltip do
 * painel. O operador precisa saber POR QUE aquilo está âmbar e a partir de quanto
 * vira vermelho.
 */
export function thresholdHint(t: Threshold, hostname?: string | null): string {
  const escopo = hostname ? ` (limiar deste servidor)` : "";
  return `Atenção a partir de ${t.warn}% e crítico a partir de ${t.crit}%${escopo}. É o mesmo limiar que dispara o alerta.`;
}

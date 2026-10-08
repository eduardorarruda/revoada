// Utilitários de formatação compartilhados do Revoada.
// Ponto único de verdade para tempo relativo/absoluto, rótulos de severidade e
// FORMATAÇÃO DE VALORES DE MÉTRICA COM UNIDADE — as telas importam daqui para
// manter consistência (eixos, tooltips, legendas, tabelas, Gauge e Stat usam
// exatamente o mesmo formatador; a unidade vem do dicionário em metrics/dict.ts).

import { metricMeta, type Unit } from "./metrics/dict";

// --- Fuso horário da operação -----------------------------------------------
// TODO horário exibido no Revoada é o de BRASÍLIA, independentemente do fuso
// da máquina que abre a tela. Sem isto, um kiosk/TV ou VM em UTC (padrão de imagem
// de servidor) mostraria tudo 3 h à frente e NADA na tela diria o fuso — o operador
// leria "incidente às 17:20" quando foi às 14:20. `toLocaleString("pt-BR")` define
// só o FORMATO (dd/mm, vírgula decimal), nunca o fuso: por isso todo formatador de
// data/hora do front passa por aqui, com `timeZone` explícito.
export const APP_TIME_ZONE = "America/Sao_Paulo";
/** Rótulo curto para deixar o fuso VISÍVEL ao lado dos horários. */
export const APP_TIME_ZONE_LABEL = "horário de Brasília";

// toDate normaliza qualquer entrada aceita pelos formatadores. Devolve null para
// valor ausente/inválido, para a tela mostrar "—" em vez de "Invalid Date".
function toDate(ts: string | number | Date | null | undefined): Date | null {
  if (ts == null || ts === "") return null;
  const d = ts instanceof Date ? ts : new Date(ts);
  return Number.isFinite(d.getTime()) ? d : null;
}

// Cache de Intl.DateTimeFormat por combinação de opções (construir é caro e estes
// formatadores rodam em listas grandes de logs/traces).
const dtfCache = new Map<string, Intl.DateTimeFormat>();
function dtf(opts: Intl.DateTimeFormatOptions): Intl.DateTimeFormat {
  const key = JSON.stringify(opts);
  let f = dtfCache.get(key);
  if (!f) {
    f = new Intl.DateTimeFormat("pt-BR", { timeZone: APP_TIME_ZONE, ...opts });
    dtfCache.set(key, f);
  }
  return f;
}

/**
 * Data + hora no fuso de Brasília. Ex.: "07/08/2026 14:32:10".
 * `seconds: false` omite os segundos; `withZone: true` acrescenta o fuso ("GMT-3"),
 * usado nos tooltips para o horário nunca ficar ambíguo.
 */
export function formatDateTime(
  ts: string | number | Date | null | undefined,
  opts: { seconds?: boolean; withZone?: boolean } = {},
): string {
  const d = toDate(ts);
  if (!d) return "—";
  const { seconds = true, withZone = false } = opts;
  return dtf({
    day: "2-digit",
    month: "2-digit",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    ...(seconds ? { second: "2-digit" as const } : {}),
    ...(withZone ? { timeZoneName: "short" as const } : {}),
  }).format(d);
}

/** Só a hora, no fuso de Brasília. Ex.: "14:32:10" (ou "14:32" sem segundos). */
export function formatTime(
  ts: string | number | Date | null | undefined,
  opts: { seconds?: boolean } = {},
): string {
  const d = toDate(ts);
  if (!d) return "—";
  const { seconds = true } = opts;
  return dtf({
    hour: "2-digit",
    minute: "2-digit",
    ...(seconds ? { second: "2-digit" as const } : {}),
    hourCycle: "h23",
  }).format(d);
}

/** Só a data, no fuso de Brasília. Ex.: "07/08/2026". */
export function formatDate(ts: string | number | Date | null | undefined): string {
  const d = toDate(ts);
  if (!d) return "—";
  return dtf({ day: "2-digit", month: "2-digit", year: "numeric" }).format(d);
}

/**
 * Formata um instante em duas representações complementares:
 *  - `rel`: texto relativo curto em PT-BR ("agora", "há 5 min", "há 2 h", "há 3 d")
 *           — para exibir na tela (glanceable).
 *  - `abs`: data/hora absoluta NO FUSO DE BRASÍLIA, com o fuso escrito — para usar
 *           em `title=` (hover).
 *
 * Valores inválidos (null, NaN, data inválida) retornam `{ rel: "—", abs: "" }`,
 * nunca uma data quebrada na tela.
 */
export function fmtRelAbs(ts: string | number | Date): { rel: string; abs: string } {
  const d = ts instanceof Date ? ts : new Date(ts);
  const ms = d.getTime();
  if (!Number.isFinite(ms)) return { rel: "—", abs: "" };

  const abs = `${formatDateTime(d, { withZone: true })} (${APP_TIME_ZONE_LABEL})`;

  const diffMs = Date.now() - ms;
  const abs_s = Math.floor(Math.abs(diffMs) / 1000);
  const future = diffMs < 0;

  let rel: string;
  if (abs_s < 45) {
    rel = "agora";
  } else {
    const min = Math.floor(abs_s / 60);
    const hr = Math.floor(abs_s / 3600);
    const day = Math.floor(abs_s / 86400);
    let core: string;
    if (day >= 1) core = `${day} d`;
    else if (hr >= 1) core = `${hr} h`;
    else core = `${Math.max(1, min)} min`;
    rel = future ? `em ${core}` : `há ${core}`;
  }

  return { rel, abs };
}

/**
 * Formata uma quantidade de bytes em texto legível em PT-BR, base 1024
 * (B, KB, MB, GB, TB, PB), com no máximo 1 casa decimal e vírgula decimal.
 * Ex.: 5583457484 → "5,2 GB"; 8589934592 → "8 GB"; 0 → "0 B".
 * Valores inválidos (NaN, negativos) retornam "—".
 */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "—";
  if (bytes < 1) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  const i = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)));
  const value = bytes / 1024 ** i;
  const n = new Intl.NumberFormat("pt-BR", { maximumFractionDigits: 1 }).format(value);
  return `${n} ${units[i]}`;
}

/**
 * Frescor de um dado por OBJETO (host/container). Distingue explicitamente três
 * situações que a interface NÃO pode confundir (regra 5.1 do Revoada):
 *  - "fresh":  recebido dentro do intervalo esperado de coleta.
 *  - "stale":  existe, mas o último dado passou do intervalo → "sem dado recente".
 *  - "nodata": nunca recebeu dado (timestamp ausente/ inválido).
 * Atenção: "removido" (objeto que deixou de existir) é um estado do BACKEND, não é
 * inferível daqui — a tela o trata à parte quando o backend o sinaliza.
 */
export type DataFreshness = "fresh" | "stale" | "nodata";

export function dataFreshness(
  ts: string | number | Date | null | undefined,
  staleAfterSeconds = 120,
  // `now` injetável só para teste; em produção é sempre o relógio real.
  now: number = Date.now(),
): { state: DataFreshness; ageSeconds: number | null } {
  if (ts == null || ts === "") return { state: "nodata", ageSeconds: null };
  const t = ts instanceof Date ? ts.getTime() : new Date(ts).getTime();
  if (!Number.isFinite(t)) return { state: "nodata", ageSeconds: null };
  const age = Math.max(0, Math.floor((now - t) / 1000));
  return { state: age > staleAfterSeconds ? "stale" : "fresh", ageSeconds: age };
}

// num formata um número em pt-BR (vírgula decimal, ponto de milhar) com no máximo
// `frac` casas. Base de todos os formatadores de valor.
function num(value: number, frac = 1): string {
  return new Intl.NumberFormat("pt-BR", { maximumFractionDigits: frac }).format(value);
}

/**
 * formatCount formata contagens inteiras. Abaixo de 100 mil mostra o inteiro com
 * separador de milhar ("12.480"); acima, escala compacta pt-BR ("1,2 mi").
 */
export function formatCount(value: number): string {
  if (!Number.isFinite(value)) return "—";
  if (Math.abs(value) < 100000) return new Intl.NumberFormat("pt-BR", { maximumFractionDigits: 0 }).format(value);
  return new Intl.NumberFormat("pt-BR", { notation: "compact", maximumFractionDigits: 1 }).format(value);
}

/**
 * formatDuration formata uma duração em SEGUNDOS na maior unidade legível
 * (ms/s/min/h), pt-BR. Ex.: 0,004 → "4 ms"; 42 → "42 s"; 180 → "3 min"; 7200 → "2 h".
 */
export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return "—";
  if (seconds < 1) return `${num(seconds * 1000, 0)} ms`;
  if (seconds < 60) return `${num(seconds, seconds < 10 ? 1 : 0)} s`;
  if (seconds < 3600) return `${num(seconds / 60, 1)} min`;
  if (seconds < 86400) return `${num(seconds / 3600, 1)} h`;
  return `${num(seconds / 86400, 1)} d`;
}

/**
 * formatRateBytes formata uma taxa de bytes/segundo reaproveitando a escala de
 * `formatBytes` e acrescentando "/s". Ex.: 1677721 → "1,6 MB/s".
 */
export function formatRateBytes(bytesPerSec: number): string {
  if (!Number.isFinite(bytesPerSec) || bytesPerSec < 0) return "—";
  return `${formatBytes(bytesPerSec)}/s`;
}

/**
 * formatValue é o FORMATADOR ÚNICO por unidade. Todo valor de métrica na interface
 * passa por aqui, garantindo unidade consistente em eixos, tooltips, legendas,
 * tabelas, Gauge e Stat. Valor inválido → "—" (nunca número quebrado).
 *
 * `opts.compact` reduz casas para ticks de eixo; `opts.percentFrac` ajusta as casas
 * do percentual (default 1).
 */
export function formatValue(
  value: number | null | undefined,
  unit: Unit,
  opts: { compact?: boolean; percentFrac?: number } = {},
): string {
  if (value == null || !Number.isFinite(value)) return "—";
  switch (unit) {
    case "percent":
      return `${num(value, opts.compact ? 0 : opts.percentFrac ?? 1)}%`;
    case "bytes":
      return formatBytes(value);
    case "bytes_per_s":
      return formatRateBytes(value);
    case "count":
      return formatCount(value);
    case "duration_s":
      return formatDuration(value);
    // As fases da sondagem HTTP já vêm em MILISSEGUNDOS. Reaproveita a mesma escala
    // de `formatDuration` convertendo para segundos: assim "114,23" vira "114 ms" e
    // um TTFB de 2.400 vira "2,4 s", em vez de um número cru sem unidade nenhuma.
    case "duration_ms":
      return formatDuration(value / 1000);
    case "days":
      return `${num(value, 0)} ${Math.abs(value) === 1 ? "dia" : "dias"}`;
    case "cores":
      return num(value, 0);
    case "bool":
      return value >= 0.5 ? "sim" : "não";
    // Custo de chamada de IA costuma ser fração de centavo: abaixo de US$ 0,01 ganha
    // casas, senão "US$ 0,00" diria que não custou nada (também no eixo do gráfico,
    // onde 2 casas punham "US$ 0" em todas as marcas).
    case "usd":
      return `US$ ${num(value, value !== 0 && Math.abs(value) < 0.01 ? 4 : 2)}`;
    default:
      return num(value, opts.compact ? 1 : 2);
  }
}

/**
 * formatUptimePercent formata uma DISPONIBILIDADE (uptime) em percentual.
 *
 * Regra deliberadamente diferente de `formatValue(v, "percent")`: uptime NUNCA
 * arredonda para cima. Um site que ficou fora do ar por 4 minutos em 24 h tem
 * 99,72% — mas 99,996% arredondado viraria "100%", afirmando na tela que NADA
 * caiu. Aqui usamos PISO com 2 casas: só exibe "100,00%" quem realmente ficou o
 * período inteiro no ar.
 */
export function formatUptimePercent(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return "—";
  const clamped = Math.min(100, Math.max(0, value));
  // Epsilon absorve o erro de ponto flutuante (99,99 * 100 = 9998,999…), que sem
  // ele transformaria 99,99% em 99,98%.
  const floored = Math.floor(clamped * 100 + 1e-9) / 100;
  return `${new Intl.NumberFormat("pt-BR", { minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(floored)}%`;
}

/**
 * formatMetric formata o valor de uma métrica pelo NOME técnico, resolvendo a
 * unidade no dicionário. Atalho para `formatValue(v, metricMeta(name).unit)`.
 */
export function formatMetric(name: string, value: number | null | undefined, opts?: { compact?: boolean }): string {
  return formatValue(value, metricMeta(name).unit, opts);
}

/**
 * unitFormatter devolve uma função que formata um número na unidade dada — pronta
 * para alimentar o eixo/legenda do uPlot e os painéis. `compact` para ticks de eixo.
 */
/**
 * Rótulos do eixo Y: compactos, mas nunca repetidos. Numa escala estreita (CPU entre
 * 99% e 100%) o compacto arredondava tudo para "100%" e a escala mentia; aqui as
 * casas decimais crescem só até os rótulos ficarem distintos.
 */
export function rotulosEixo(splits: (number | null)[], unit: Unit): string[] {
  const distintos = (r: string[]) => new Set(r).size === r.length;
  let r = splits.map((v) => formatValue(v, unit, { compact: true }));
  for (const casas of [1, 2]) {
    if (distintos(r) || unit !== "percent") break;
    r = splits.map((v) => formatValue(v, unit, { percentFrac: casas }));
  }
  return r;
}

export function unitFormatter(unit: Unit, compact = false): (v: number | null | undefined) => string {
  return (v) => formatValue(v, unit, { compact });
}

/**
 * Rótulos de severidade em PT-BR maiúsculo (uso em Badge/estados críticos).
 * Cobre as chaves reais que aparecem no código: "critical"/"crit",
 * "warning"/"warn", "info". Normalize a chave para minúsculas antes de consultar.
 */
export const SEV_LABEL: Record<string, string> = {
  critical: "CRÍTICO",
  crit: "CRÍTICO",
  warning: "ATENÇÃO",
  warn: "ATENÇÃO",
  info: "INFO",
  ok: "OK",
  none: "SEM SINAL",
};

/**
 * Rank de severidade para ordenação "pior primeiro" (desc):
 * crítico=3, atenção=2, info=1, resto=0.
 */
export function sevRank(sev: string): number {
  switch (sev?.toLowerCase()) {
    case "critical":
    case "crit":
      return 3;
    case "warning":
    case "warn":
      return 2;
    case "info":
      return 1;
    default:
      return 0;
  }
}

/**
 * mensagemDeErro extrai a explicação que o SERVIDOR escreveu, descartando o
 * envelope técnico.
 *
 * O backend recusa consultas impossíveis com um texto que ensina o que fazer
 * (ex.: a janela máxima da agregação "Último"). Esse texto chega embrulhado em
 * `Error: 400: <mensagem>` — e as telas ou jogavam tudo fora, trocando por um
 * genérico "ajuste os filtros" (que é palpite: o filtro não era o problema), ou
 * despejavam o envelope inteiro na cara do operador, com "Error:" e o código HTTP.
 *
 * `alternativa` é usada só quando não há mensagem do servidor — falha de rede,
 * por exemplo, em que não existe explicação para mostrar.
 */
export function mensagemDeErro(e: unknown, alternativa: string): string {
  const bruto = e instanceof Error ? e.message : typeof e === "string" ? e : "";
  // Descasca "Error: ", e em seguida um "<status>: " numérico, se houver.
  const semPrefixo = bruto.replace(/^Error:\s*/i, "").replace(/^\d{3}:\s*/, "").trim();
  if (!semPrefixo) return alternativa;
  // Sobrou só um código sem texto? Não ajuda ninguém.
  if (/^\d{3}$/.test(semPrefixo)) return alternativa;
  return semPrefixo;
}

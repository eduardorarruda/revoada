// Último ponto de uma série — COM o instante em que ele foi coletado.
//
// Dois defeitos moravam aqui:
//
//  1. "sem dado vira 0": os painéis faziam `lastValue(data) ?? 0`. Um host morto
//     virava "Uptime: 0 s", "Processos: 0" e um gauge de CPU em 0% VERDE — um zero
//     legítimo e a ausência de dado eram indistinguíveis na tela.
//
//  2. "valor velho apresentado como atual": a busca varria a janela inteira de trás
//     para frente até achar o último não-nulo, e o selo de frescor media a idade da
//     MENSAGEM do WebSocket, não a do DADO. Num painel de 24 h com o host caído há
//     6 h, o número de 6 horas atrás aparecia com bolinha verde "ao vivo".
//
// A correção é devolver `{ value, ts }` e classificar a IDADE DO PONTO contra o
// passo da consulta. Reaproveita `dataFreshness` (o mesmo classificador do
// componente LastSeen), para haver um único conceito de fresco/velho/sem dado.

import { dataFreshness, type DataFreshness } from "../format";

export interface LastPoint {
  /** Último valor não-nulo da série, ou null quando a série inteira é vazia/nula. */
  value: number | null;
  /** Instante (epoch em SEGUNDOS) desse valor, ou null quando não há valor. */
  ts: number | null;
}

export const NO_POINT: LastPoint = { value: null, ts: null };

/**
 * lastPoint devolve o último ponto não-nulo junto do seu carimbo de tempo.
 * `ts` e `values` são as colunas alinhadas da Query API; se `ts` for menor que
 * `values` (não deve acontecer), o carimbo sai null em vez de errado.
 */
export function lastPoint(ts: number[] | undefined, values: (number | null)[] | undefined): LastPoint {
  if (!values || values.length === 0) return NO_POINT;
  for (let i = values.length - 1; i >= 0; i--) {
    const v = values[i];
    if (v == null || !Number.isFinite(v)) continue;
    const at = ts && i < ts.length ? ts[i] : null;
    return { value: v, ts: at != null && Number.isFinite(at) ? at : null };
  }
  return NO_POINT;
}

/**
 * staleAfterSeconds define quanto tempo um ponto continua valendo como "atual":
 * cerca de TRÊS passos da consulta, com piso de 2 minutos para não marcar como
 * velho um passo curto demais.
 *
 * Por que três e não dois: o servidor descarta o balde que ainda está aberto, então
 * o ponto mais novo já nasce com até um passo de idade. Com orçamento de dois
 * passos sobrava um só para buraco real — e um único minuto sem ingestão (restart
 * do gateway, hiccup de rede) apagava o número da tela e escrevia "sem dado", que
 * é justamente o susto que a janela de validade existe para evitar.
 */
export function staleAfterSeconds(stepSeconds: number | undefined): number {
  const step = stepSeconds && stepSeconds > 0 ? stepSeconds : 60;
  return Math.max(120, step * 3);
}

/**
 * pointFreshness classifica o último ponto:
 *  - "nodata": não há ponto (ou não sabemos quando ele foi coletado);
 *  - "stale":  existe, mas é mais velho que ~3× o passo → NÃO pode ser lido como atual;
 *  - "fresh":  dentro do esperado.
 * `ageSeconds` acompanha para a tela poder dizer "há 6 h" em vez de só "velho".
 */
export function pointFreshness(
  p: LastPoint,
  stepSeconds: number | undefined,
  now: number = Date.now(),
): { state: DataFreshness; ageSeconds: number | null } {
  if (p.value == null || p.ts == null) return { state: "nodata", ageSeconds: null };
  return dataFreshness(new Date(p.ts * 1000), staleAfterSeconds(stepSeconds), now);
}

/**
 * displayValue é a decisão final do painel: que número mostrar.
 * Só devolve número quando o ponto é FRESCO. Velho e ausente devolvem null, e o
 * painel renderiza "—" com a idade ao lado — nunca um zero verde nem um valor de
 * seis horas atrás fingindo ser de agora.
 */
export function displayValue(p: LastPoint, stepSeconds: number | undefined, now?: number): number | null {
  return pointFreshness(p, stepSeconds, now).state === "fresh" ? p.value : null;
}

/**
 * Ladrilho de saúde (cpu/ram/swap/disco) vindo de /api/host-metrics e /api/health-wall.
 * Declarado estruturalmente (e não importado de `api.ts`) para este módulo continuar
 * sendo só de cálculo: `HealthMetric` e `DiskMount` encaixam aqui por forma.
 */
export interface HealthTile {
  pct: number;
  state: "ok" | "warn" | "crit" | "nodata";
  /** Instante da amostra (epoch em SEGUNDOS). Ausente = o backend não datou. */
  ts?: number;
  /** Passo real observado na série (segundos). Ausente = cadência desconhecida. */
  step_seconds?: number;
}

/**
 * healthFreshness aplica ao ladrilho de saúde o MESMO critério dos painéis: o valor
 * vale enquanto for mais novo que ~3× o passo real da série (com piso de 2 min).
 *
 * Por que existe: o número de cpu/ram/disco chegava sem idade, e a validade era uma
 * constante do servidor (5 min em /api/host-metrics, 2 min no mural, 3× o passo nos
 * painéis — três relógios diferentes para a mesma pergunta). Quando um coletor
 * silencia e os outros seguem, o host continua "no ar" e o ladrilho mudo repetia o
 * último valor pintado com a cor do limiar. Aqui ele vira "velho", com a idade ao lado.
 *
 * Backend antigo (sem `ts`) cai em "fresh": preserva o comportamento atual em vez de
 * apagar a tela inteira por falta de um campo novo.
 */
export function healthFreshness(
  tile: HealthTile | null | undefined,
  now?: number,
): { state: DataFreshness; ageSeconds: number | null } {
  if (!tile || tile.state === "nodata") return { state: "nodata", ageSeconds: null };
  if (tile.ts == null) return { state: "fresh", ageSeconds: null };
  return pointFreshness({ value: tile.pct, ts: tile.ts }, tile.step_seconds, now);
}

/**
 * panelFreshness resolve o SELO do painel juntando as duas leituras que antes
 * andavam separadas:
 *
 *  - a saúde da CONEXÃO (idade da última mensagem do WebSocket) e
 *  - a idade do DADO em si.
 *
 * Vale o pior dos dois. Antes só a conexão contava, e por isso um painel de 24 h
 * com o host morto há 6 h exibia o número antigo com bolinha verde "ao vivo": o
 * WebSocket estava perfeito; o dado é que era velho.
 */
export function panelFreshness(opts: {
  /** false quando o cliente ao vivo está reconectando. */
  connected: boolean;
  /** Segundos desde a última mensagem do WebSocket (null = nenhuma ainda). */
  messageAgeSeconds?: number | null;
  point: LastPoint;
  step?: number;
  now?: number;
}): { status: "live" | "stale" | "reconnecting"; ageSeconds?: number } {
  if (!opts.connected) return { status: "reconnecting" };
  const dado = pointFreshness(opts.point, opts.step, opts.now);
  if (dado.state === "stale") return { status: "stale", ageSeconds: dado.ageSeconds ?? undefined };
  const msg = opts.messageAgeSeconds;
  if (msg != null && msg > 10) return { status: "stale", ageSeconds: msg };
  return { status: "live", ageSeconds: msg ?? undefined };
}

/**
 * containerFreshness diz se os números de um container ainda descrevem o presente.
 *
 * Container era o ÚNICO objeto do painel julgado por uma janela fixa do servidor (5
 * min) que nunca chegava à tela. Medido em 13/08/2026: um container rodando a ~4% de
 * CPU foi exibido como 52,4% por mais de dois minutos, verde — no mesmo cartão em
 * que o ladrilho de CPU do host, que tem carimbo de tempo, dizia corretamente "sem
 * dado". A régua agora é a mesma dos ladrilhos: ~3× o passo real, com piso de 2 min.
 *
 * Backend antigo (sem `ts`) cai em "fresh": preservar o comportamento anterior é
 * melhor do que esvaziar a lista de containers por causa de um campo novo.
 */
export function containerFreshness(
  c: { ts?: number; step_seconds?: number },
  now?: number,
): { state: DataFreshness; ageSeconds: number | null } {
  if (c.ts == null) return { state: "fresh", ageSeconds: null };
  return pointFreshness({ value: 0, ts: c.ts }, c.step_seconds, now);
}

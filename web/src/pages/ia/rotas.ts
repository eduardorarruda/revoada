// Rotas e links das telas de Agentes de IA (#/ia/*).
//
// A janela de tempo mora na URL (?janela=6h) para atravessar as abas e para um link
// copiado abrir a mesma visão para quem recebe.
import { JANELA_PADRAO } from "../../components/JanelaTempo";

export type VistaIa =
  | { vista: "visao" }
  | { vista: "execucoes" }
  | { vista: "replay"; traceId: string }
  | { vista: "ferramentas" }
  | { vista: "precos" };

export type AbaIa = "visao" | "execucoes" | "ferramentas" | "precos";

export const ABAS_IA: readonly { id: AbaIa; rotulo: string; caminho: string }[] = [
  { id: "visao", rotulo: "Visão geral", caminho: "/ia" },
  { id: "execucoes", rotulo: "Execuções", caminho: "/ia/execucoes" },
  { id: "ferramentas", rotulo: "Ferramentas", caminho: "/ia/ferramentas" },
  { id: "precos", rotulo: "Modelos e preços", caminho: "/ia/precos" },
];

const PREFIXO_REPLAY = "/ia/execucoes/";

export function vistaDaRota(rota: string): VistaIa {
  if (rota.startsWith(PREFIXO_REPLAY)) {
    const traceId = decodeURIComponent(rota.slice(PREFIXO_REPLAY.length));
    if (traceId) return { vista: "replay", traceId };
  }
  if (rota === "/ia/execucoes") return { vista: "execucoes" };
  if (rota === "/ia/ferramentas") return { vista: "ferramentas" };
  if (rota === "/ia/precos") return { vista: "precos" };
  return { vista: "visao" };
}

/** A aba marcada: o replay pertence a Execuções. */
export function abaDaVista(v: VistaIa): AbaIa {
  return v.vista === "replay" ? "execucoes" : v.vista;
}

/** Link "#/ia/…?janela=…&…" sem parâmetros vazios. A janela padrão não polui a URL. */
export function hrefIa(caminho: string, params: Record<string, string | undefined> = {}): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (!v) continue;
    if (k === "janela" && v === JANELA_PADRAO) continue;
    p.set(k, v);
  }
  const q = p.toString();
  return `#${caminho}${q ? `?${q}` : ""}`;
}

export function hrefReplay(traceId: string, janela?: string): string {
  return hrefIa(`${PREFIXO_REPLAY}${encodeURIComponent(traceId)}`, { janela });
}

/** Waterfall do trace na tela de Traces (abre direto no trace). */
export function hrefWaterfall(traceId: string): string {
  return `#/traces?trace=${encodeURIComponent(traceId)}`;
}

/** Janelas da Visão do Servidor (espelha WINDOWS de HostDetail.tsx). */
const JANELAS_HOST: readonly { id: string; segundos: number }[] = [
  { id: "1h", segundos: 3600 },
  { id: "6h", segundos: 6 * 3600 },
  { id: "24h", segundos: 24 * 3600 },
  { id: "7d", segundos: 7 * 86400 },
  { id: "30d", segundos: 30 * 86400 },
];

/**
 * Visão do Servidor numa janela que AINDA CONTÉM o início da execução. As janelas
 * de lá são relativas a agora; escolhemos a menor que alcança aquele instante,
 * para o momento da execução não ficar espremido num canto do gráfico.
 */
export function hrefHostNaHora(host: string, inicioMs: number, agoraMs: number = Date.now()): string {
  const atrasS = Math.max(0, (agoraMs - inicioMs) / 1000);
  const j = JANELAS_HOST.find((w) => w.segundos >= atrasS) ?? JANELAS_HOST[JANELAS_HOST.length - 1];
  return `#/hosts/${encodeURIComponent(host)}?janela=${j.id}`;
}

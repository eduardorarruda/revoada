// Cálculo dos indicadores do topo da tela Início.
//
// Fica separado da tela, e é puro, porque é aqui que mora a promessa de que o
// número grande na cara do usuário está certo. Um indicador de topo é lido de
// relance e vira decisão ("está tudo bem, posso ir almoçar") — se ele errar, erra
// silenciosamente. Então as regras estão escritas em um lugar só, e testadas.
//
// As três regras que não se negociam:
//
//  1. Servidor sem sinal NÃO entra na conta de pior recurso. O último número que
//     ele mandou é história, não é o estado de agora — e um servidor caído com
//     "CPU 96%" congelada de ontem sequestraria o indicador para sempre.
//  2. Métrica velha também não entra. O frescor é POR RECURSO (o coletor de
//     disco pode calar enquanto o de CPU segue), e é o mesmo critério usado nos
//     ladrilhos e nos painéis — `healthFreshness`, ~3× o passo real da série.
//  3. Quando não sobra ninguém medindo, o resultado é `null` — a tela mostra "—".
//     Nunca 0%: zero é uma medição, ausência não é.
import type { HealthCard, HealthMetric } from "../api";
import { healthFreshness } from "../metrics/lastPoint";

export type Recurso = "cpu" | "mem" | "disk";

export interface PiorRecurso {
  /** Hostname técnico — a tela traduz para o nome amigável. */
  host: string;
  pct: number;
  /** Semáforo do backend para ESTE recurso neste host. */
  state: "ok" | "warn" | "crit";
  usedBytes?: number;
  totalBytes?: number;
}

/** Um servidor só conta para os indicadores se estiver reportando agora. */
function reportando(c: HealthCard): boolean {
  return c.up && c.state !== "nosignal";
}

/** A métrica só conta se foi medida NESTA janela (nem ausente, nem velha). */
function medidaAgora<T extends HealthMetric>(m: T | undefined, agora?: number): m is T {
  return !!m && Number.isFinite(m.pct) && healthFreshness(m, agora).state === "fresh";
}

/**
 * piorRecurso devolve o servidor com o maior percentual de uso do recurso, entre
 * os que estão reportando e cuja medida daquele recurso é recente. Empate
 * desempata pelo hostname, só para o indicador não trocar de nome sozinho a cada
 * atualização quando dois servidores estão iguais.
 */
export function piorRecurso(
  cards: HealthCard[] | null | undefined,
  recurso: Recurso,
  agora?: number,
): PiorRecurso | null {
  let melhor: PiorRecurso | null = null;
  for (const c of cards ?? []) {
    if (!reportando(c)) continue;
    const m = c[recurso];
    if (!medidaAgora(m, agora)) continue;
    const cand: PiorRecurso = {
      host: c.host,
      pct: m.pct,
      state: m.state === "nodata" ? "ok" : m.state,
      usedBytes: m.used_bytes,
      totalBytes: m.total_bytes,
    };
    if (
      melhor === null ||
      cand.pct > melhor.pct ||
      (cand.pct === melhor.pct && cand.host.localeCompare(melhor.host) < 0)
    ) {
      melhor = cand;
    }
  }
  return melhor;
}

/**
 * quantosMedindo diz quantos servidores entraram na conta daquele recurso. É o
 * que permite a tela ser honesta na legenda: "pior entre 4 medindo" deixa claro
 * que o número não fala pela frota inteira quando parte dela está calada.
 */
export function quantosMedindo(
  cards: HealthCard[] | null | undefined,
  recurso: Recurso,
  agora?: number,
): number {
  let n = 0;
  for (const c of cards ?? []) {
    if (reportando(c) && medidaAgora(c[recurso], agora)) n++;
  }
  return n;
}

export interface RecursoNoLimite {
  host: string;
  recurso: Recurso;
  pct: number;
  state: "warn" | "crit";
}

const RECURSOS: Recurso[] = ["cpu", "mem", "disk"];

/**
 * recursosNoLimite lista cada recurso em atenção/crítico, pior primeiro (estado,
 * depois percentual). Mesmo filtro do pior recurso: só quem está reportando e
 * mediu agora — um servidor mudo é "sem sinal", não "CPU 99%". É a lista que a
 * Início usa no veredito e em "Precisa de atenção agora", para nunca contradizer
 * o Mural (que pinta o servidor pelo mesmo semáforo do backend).
 */
export function recursosNoLimite(
  cards: HealthCard[] | null | undefined,
  agora?: number,
): RecursoNoLimite[] {
  const lista: RecursoNoLimite[] = [];
  for (const c of cards ?? []) {
    if (!reportando(c)) continue;
    for (const recurso of RECURSOS) {
      const m = c[recurso];
      if (!medidaAgora(m, agora) || (m.state !== "warn" && m.state !== "crit")) continue;
      lista.push({ host: c.host, recurso, pct: m.pct, state: m.state });
    }
  }
  const peso = { crit: 2, warn: 1 };
  return lista.sort((a, b) => peso[b.state] - peso[a.state] || b.pct - a.pct || a.host.localeCompare(b.host));
}

export interface ResumoFrota {
  total: number;
  noAr: number;
  /** Servidores que pedem olhada: crítico, atenção ou sem sinal. */
  comProblema: HealthCard[];
  /** Servidores reportando e sem nenhum recurso fora do limiar. */
  emOrdem: HealthCard[];
}

// Pior primeiro dentro de cada grupo: crítico > sem sinal > atenção > ok.
const ORDEM: Record<HealthCard["state"], number> = { crit: 4, nosignal: 3, warn: 2, ok: 1 };

/**
 * resumoFrota separa o que precisa de olho do que está em ordem, já ordenado.
 * `rotulo` traduz hostname → nome amigável para o desempate ficar na ordem que a
 * pessoa lê na tela, e não na ordem interna dos hostnames.
 */
export function resumoFrota(
  cards: HealthCard[] | null | undefined,
  rotulo: (host: string) => string = (h) => h,
): ResumoFrota {
  const lista = cards ?? [];
  const ordenar = (a: HealthCard, b: HealthCard) => {
    const r = ORDEM[b.state] - ORDEM[a.state];
    return r !== 0 ? r : rotulo(a.host).localeCompare(rotulo(b.host), "pt-BR");
  };
  const comProblema = lista.filter((c) => c.state !== "ok").sort(ordenar);
  const emOrdem = lista.filter((c) => c.state === "ok").sort(ordenar);
  return {
    total: lista.length,
    noAr: lista.filter(reportando).length,
    comProblema,
    emOrdem,
  };
}

// --- Veredito ---------------------------------------------------------------
// A frase grande do topo da Início. Mesmas regras de honestidade dos números:
// uma fonte que falhou pode estar escondendo justamente o problema, então com
// fonte falha não existe "tudo em ordem".

export type TomVeredito = "ok" | "warn" | "crit" | "desconhecido" | "vazio";

export interface EntradaVeredito {
  hostsTotal: number;
  hostsUp: number;
  alertasCrit: number;
  alertasOutros: number;
  sitesTotal: number;
  sitesUp: number;
  /** Sites DOWN de fato. Os que não estão UP nem DOWN (degradado, suspeito) não
   *  estão fora do ar — entram como "com problema", em tom de atenção. */
  sitesFora: number;
  /** Fontes cuja última leitura falhou ("alertas", "sites", "hosts"). */
  fontesFalhas: string[];
  /** Servidores reportando com algum recurso crítico / só em atenção (Mural). */
  hostsCrit?: number;
  hostsWarn?: number;
}

export interface Veredito {
  tom: TomVeredito;
  titulo: string;
  detalhe: string;
}

function contar(n: number, singular: string, plural: string): string {
  return `${n} ${n === 1 ? singular : plural}`;
}

export function vereditoDaFrota(e: EntradaVeredito): Veredito {
  if (e.fontesFalhas.length > 0) {
    return {
      tom: "desconhecido",
      titulo: "Não dá para afirmar que está tudo bem",
      detalhe: `A última leitura de ${e.fontesFalhas.join(", ")} falhou, o painel tenta de novo sozinho.`,
    };
  }
  if (e.hostsTotal === 0 && e.sitesTotal === 0) {
    return {
      tom: "vazio",
      titulo: "Nada monitorado ainda",
      detalhe: "Instale o agente em um servidor ou cadastre um site para começar.",
    };
  }
  const semSinal = e.hostsTotal - e.hostsUp;
  const sitesFora = e.sitesFora;
  const sitesComProblema = Math.max(0, e.sitesTotal - e.sitesUp - sitesFora);
  const hostsCrit = e.hostsCrit ?? 0;
  const hostsWarn = e.hostsWarn ?? 0;
  // Ordem = gravidade: alerta crítico, site fora, recurso no limite, servidor mudo, aviso.
  const partes: string[] = [];
  if (e.alertasCrit > 0) partes.push(contar(e.alertasCrit, "alerta crítico", "alertas críticos"));
  if (sitesFora > 0) partes.push(contar(sitesFora, "site fora do ar", "sites fora do ar"));
  if (hostsCrit > 0) partes.push(contar(hostsCrit, "servidor no limite", "servidores no limite"));
  if (semSinal > 0) partes.push(contar(semSinal, "servidor sem sinal", "servidores sem sinal"));
  if (hostsWarn > 0) partes.push(contar(hostsWarn, "servidor em atenção", "servidores em atenção"));
  if (sitesComProblema > 0) partes.push(contar(sitesComProblema, "site com problema", "sites com problema"));
  if (e.alertasOutros > 0 && e.alertasCrit === 0) {
    partes.push(contar(e.alertasOutros, "alerta em disparo", "alertas em disparo"));
  }
  if (partes.length === 0) {
    return {
      tom: "ok",
      titulo: "Tudo em ordem",
      detalhe: `${contar(e.hostsUp, "servidor reportando", "servidores reportando")}, ${contar(
        e.sitesUp,
        "site respondendo",
        "sites respondendo",
      )} e nenhum alerta em disparo.`,
    };
  }
  const foraDoAr = e.alertasCrit > 0 || sitesFora > 0;
  const tom: TomVeredito = foraDoAr || hostsCrit > 0 ? "crit" : "warn";
  return {
    tom,
    titulo: partes.join(" · "),
    detalhe: foraDoAr
      ? "Há algo fora do ar agora. A lista abaixo mostra o que olhar primeiro."
      : tom === "crit"
        ? "Nada fora do ar, mas há servidor no limite agora. A lista abaixo mostra qual recurso."
        : "Nada fora do ar, mas há o que conferir. A lista abaixo mostra o quê.",
  };
}

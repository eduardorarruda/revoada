// Regras puras da TV: o estado geral do cabeçalho e a seta de tendência dos blocos.
import type { TVStatus } from "../../api";

export type TomTV = "ok" | "warn" | "crit" | "neutro";

function contar(n: number, um: string, varios: string): string {
  return `${n} ${n === 1 ? um : varios}`;
}

/** Estado da parede: sem resposta ainda, nada é afirmado; crítico vence aviso. */
export function estadoDaTV(status: Pick<TVStatus, "criticals" | "warnings"> | null): { tom: TomTV; texto: string } {
  if (!status) return { tom: "neutro", texto: "Conectando…" };
  if (status.criticals.length > 0) return { tom: "crit", texto: contar(status.criticals.length, "crítico", "críticos") };
  if (status.warnings.length > 0) return { tom: "warn", texto: contar(status.warnings.length, "aviso", "avisos") };
  return { tom: "ok", texto: "Tudo em ordem" };
}

/**
 * Tendência da janela curta do bloco: primeiro ponto medido contra o último.
 * Variação abaixo de 2% do valor é "estável" — seta piscando para ruído de medição
 * ensinaria a parede a ser ignorada.
 */
export function tendencia(valores: (number | null)[]): "sobe" | "desce" | "estavel" | null {
  const medidos = valores.filter((v): v is number => v != null && Number.isFinite(v));
  if (medidos.length < 2) return null;
  const primeiro = medidos[0];
  const ultimo = medidos[medidos.length - 1];
  const escala = Math.max(Math.abs(primeiro), Math.abs(ultimo), 1e-9);
  if (Math.abs(ultimo - primeiro) / escala < 0.02) return "estavel";
  return ultimo > primeiro ? "sobe" : "desce";
}

/** Estado do mural de saúde: o pior cartão manda; sem sinal conta como atenção. */
export function estadoDoMural(cards: { state: "ok" | "warn" | "crit" | "nosignal" }[]): { tom: TomTV; texto: string } {
  if (cards.length === 0) return { tom: "neutro", texto: "Nenhum servidor" };
  const n = (s: string) => cards.filter((c) => c.state === s).length;
  if (n("crit") > 0) return { tom: "crit", texto: contar(n("crit"), "crítico", "críticos") };
  if (n("warn") > 0) return { tom: "warn", texto: `${n("warn")} em atenção` };
  if (n("nosignal") > 0) return { tom: "warn", texto: `${n("nosignal")} sem sinal` };
  return { tom: "ok", texto: "Tudo em ordem" };
}

/** "há 12 min", "há 5 h", "há 42 dias": número inteiro, que se lê a 5 m ("42,4 d" não). */
export function haQuanto(segundos: number): string {
  if (segundos < 60) return "agora";
  if (segundos < 3600) return `há ${Math.round(segundos / 60)} min`;
  if (segundos < 48 * 3600) return `há ${Math.round(segundos / 3600)} h`;
  return `há ${Math.round(segundos / 86400)} dias`;
}

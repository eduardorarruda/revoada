// Veredito da Visão geral de IA: a frase do topo, no mesmo formato da Início.
// "Última 1 h: US$ 12,40 em 3.214 chamadas, 1,8% com erro, p95 de 4,2 s".
import type { IaTotais } from "../../api.ia";
import type { Veredito } from "../homeResumo";
import { fmtInteiro, fmtMs, fmtPct, fmtUsd } from "./formato";

/** A partir daqui a taxa de erro das chamadas de modelo pede atenção. */
export const TAXA_ERRO_ATENCAO = 0.01;
/** A partir daqui, crítico: uma em cada vinte chamadas falhando. */
export const TAXA_ERRO_CRITICA = 0.05;

function plural(n: number, um: string, varios: string): string {
  return `${fmtInteiro(n)} ${n === 1 ? um : varios}`;
}

/** Taxa de erro; se o servidor não mandou, calcula das contagens. */
export function taxaErro(t: Pick<IaTotais, "taxa_erro" | "chamadas" | "erros">): number | null {
  if (t.taxa_erro != null) return t.taxa_erro;
  return t.chamadas > 0 ? t.erros / t.chamadas : null;
}

/** Por que o custo é parcial, em uma frase. Vazio quando não é. */
export function motivoParcial(t: Pick<IaTotais, "custo_parcial" | "chamadas_sem_preco" | "sem_tokens">): string {
  if (!t.custo_parcial) return "";
  const partes: string[] = [];
  if (t.chamadas_sem_preco > 0) partes.push(`${plural(t.chamadas_sem_preco, "chamada", "chamadas")} sem preço cadastrado`);
  if (t.sem_tokens > 0) partes.push(`${plural(t.sem_tokens, "chamada", "chamadas")} sem tokens informados`);
  const porque = partes.length > 0 ? partes.join(" e ") : "há chamadas sem preço ou sem tokens";
  return `Custo parcial: ${porque}. O valor real é maior que o mostrado.`;
}

function tomDaTaxa(taxa: number | null): Veredito["tom"] {
  if (taxa == null) return "desconhecido";
  if (taxa >= TAXA_ERRO_CRITICA) return "crit";
  if (taxa >= TAXA_ERRO_ATENCAO) return "warn";
  return "ok";
}

export function vereditoIa(t: IaTotais, fraseJanela: string): Veredito {
  if (t.chamadas === 0) {
    return {
      tom: "vazio",
      titulo: `${fraseJanela}: nenhuma chamada de modelo`,
      detalhe:
        "Quando suas aplicações de IA mandarem spans por OpenTelemetry, custo, tokens, erros e o passo a passo aparecem aqui.",
    };
  }
  const taxa = taxaErro(t);
  const custo = t.custo_usd == null ? "custo não informado" : `${fmtUsd(t.custo_usd)}${t.custo_parcial ? " (parcial)" : ""}`;
  const p95 = t.latencia_p95_ms == null ? "p95 não informado" : `p95 de ${fmtMs(t.latencia_p95_ms)}`;
  const titulo = `${fraseJanela}: ${custo} em ${plural(t.chamadas, "chamada", "chamadas")}, ${fmtPct(taxa)} com erro, ${p95}`;

  const tom = tomDaTaxa(taxa);
  const detalhes: string[] = [];
  if (tom === "crit") detalhes.push(`A taxa de erro passou de ${fmtPct(TAXA_ERRO_CRITICA)}.`);
  else if (tom === "warn") detalhes.push(`A taxa de erro passou de ${fmtPct(TAXA_ERRO_ATENCAO)}.`);
  const parcial = motivoParcial(t);
  if (parcial) detalhes.push(parcial);
  detalhes.push(
    `${plural(t.execucoes, "execução", "execuções")} e ${plural(t.ferramentas_chamadas, "chamada", "chamadas")} de ferramenta (${fmtInteiro(t.ferramentas_erros)} com erro).`,
  );
  return { tom, titulo, detalhe: detalhes.join(" ") };
}

// Formatação dos números da tela de Agentes de IA, em pt-BR.
//
// Regra do contrato (docs/api-ia.md): `null` é "não informado / sem preço", nunca
// zero. Todo formatador aqui recebe `number | null` e devolve um texto explícito
// para a ausência — zero é medição, ausência não é.
import { formatDuration } from "../../format";
import type { IaCustoOrigem } from "../../api.ia";

export const NAO_INFORMADO = "não informado";
export const SEM_PRECO = "sem preço";

/** A partir daqui os tokens vão para a escala compacta ("18,4 mil", "1,8 mi"). */
const LIMIAR_COMPACTO = 10_000;

const inteiro = new Intl.NumberFormat("pt-BR", { maximumFractionDigits: 0 });
const compacto = new Intl.NumberFormat("pt-BR", { notation: "compact", maximumFractionDigits: 1 });

/**
 * Dólar. Valores abaixo de um centavo ganham casas até mostrar dois dígitos
 * significativos: uma chamada de US$ 0,00003 não pode aparecer como "US$ 0,00".
 * O símbolo é montado à mão (e não com style: "currency") para ter espaço comum.
 */
export function fmtUsd(v: number | null, vazio: string = NAO_INFORMADO): string {
  if (v == null || !Number.isFinite(v)) return vazio;
  const abs = Math.abs(v);
  const opcoes: Intl.NumberFormatOptions =
    abs > 0 && abs < 0.01
      ? { maximumSignificantDigits: 2 }
      : { minimumFractionDigits: 2, maximumFractionDigits: 2 };
  return `US$ ${new Intl.NumberFormat("pt-BR", opcoes).format(v)}`;
}

/** Tokens: inteiro com milhar até 10 mil; depois, escala compacta ("1,8 mi"). */
export function fmtTokens(v: number | null): string {
  if (v == null || !Number.isFinite(v)) return NAO_INFORMADO;
  // O Intl separa "1,8" de "mi" com espaço não separável; trocamos pelo comum para
  // o texto se comportar igual ao resto da tela (busca, cópia, testes).
  return Math.abs(v) < LIMIAR_COMPACTO ? inteiro.format(v) : compacto.format(v).replace(/\u00a0/g, " ");
}

/** Milissegundos na maior unidade legível ("820 ms", "4,2 s", "1,5 min"). */
export function fmtMs(v: number | null): string {
  if (v == null || !Number.isFinite(v)) return NAO_INFORMADO;
  return formatDuration(v / 1000);
}

/** Fração 0..1 como porcentagem com até uma casa ("1,8%"). */
export function fmtPct(v: number | null): string {
  if (v == null || !Number.isFinite(v)) return NAO_INFORMADO;
  return `${new Intl.NumberFormat("pt-BR", { maximumFractionDigits: 1 }).format(v * 100)}%`;
}

export function fmtInteiro(v: number): string {
  return inteiro.format(v);
}

/** "2025-10-01" ou "2025-10-01T00:00:00Z" → "01/10/2025" (a data do preço, sem fuso). */
export function fmtDataPreco(iso: string): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(iso);
  return m ? `${m[3]}/${m[2]}/${m[1]}` : iso;
}

const MESES = ["jan", "fev", "mar", "abr", "mai", "jun", "jul", "ago", "set", "out", "nov", "dez"] as const;

/** Versão da tabela de referência ("2025-10") como gente lê: "out/2025". Fora do formato, volta como veio. */
export function fmtReferencia(ref: string): string {
  const m = /^(\d{4})-(\d{2})$/.exec(ref.trim());
  const mes = m ? MESES[Number(m[2]) - 1] : undefined;
  return m && mes ? `${mes}/${m[1]}` : ref;
}

/** Origem de uma linha de preço: "referencia-2025-10" → "tabela de referência (out/2025)"; vazia → "não informada". */
export function fmtOrigemPreco(origem: string): string {
  const o = origem.trim();
  if (!o) return "não informada";
  const ref = /^referencia-(.+)$/.exec(o);
  return ref ? `tabela de referência (${fmtReferencia(ref[1])})` : o;
}

/** Custo de um passo e a origem dele, dita com honestidade. */
export function fmtCustoPasso(p: {
  custo_usd: number | null;
  custo_origem: IaCustoOrigem | "";
  preco_data: string;
}): { valor: string; origem: string } {
  switch (p.custo_origem) {
    case "estimado":
      return {
        valor: fmtUsd(p.custo_usd),
        origem: p.preco_data ? `estimado com preço de ${fmtDataPreco(p.preco_data)}` : "estimado pela tabela de preços",
      };
    case "informado":
      return { valor: fmtUsd(p.custo_usd), origem: "informado pela biblioteca" };
    case "sem_preco":
      return { valor: SEM_PRECO, origem: "o modelo não tem preço cadastrado" };
    case "sem_tokens":
      return { valor: "sem tokens", origem: "a biblioteca não informou tokens" };
    default:
      return { valor: fmtUsd(p.custo_usd), origem: "" };
  }
}

/** "2 de modelo · 1 de ferramenta", sem as partes zeradas ("1 de modelo"). */
export function passosDaExecucao(modelo: number, ferramenta: number): string {
  const partes = [
    modelo > 0 ? `${fmtInteiro(modelo)} de modelo` : "",
    ferramenta > 0 ? `${fmtInteiro(ferramenta)} de ferramenta` : "",
  ].filter(Boolean);
  return partes.length ? partes.join(" · ") : "nenhuma chamada";
}

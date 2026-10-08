// Kpi — o indicador de topo do painel: rótulo pequeno, número grande, uma linha
// de contexto embaixo.
//
// Existe para que a resposta que a pessoa foi buscar apareça ANTES de qualquer
// lista. Na Início, os números moravam abaixo da grade de servidores: com uma
// frota grande, "quantos alertas eu tenho agora" ficava a três rolagens de
// distância no celular.
//
// Regras que o componente impõe (e por isso ele existe, em vez de cada tela
// desenhar o seu):
//  - número em `tabular` — a coluna não dança quando o valor muda;
//  - `valor` ausente vira "—", nunca 0: zero é medição, ausência não é;
//  - a cor semântica só entra em `warn`/`crit`; estado normal fica na cor do
//    texto, senão o painel inteiro vira um mural colorido onde nada se destaca.
import type { ReactNode } from "react";
import { Skeleton } from "./index";
import { CountUp } from "../motion";
import { separar } from "../motion/CountUp";

export interface KpiProps {
  /** Rótulo curto, em caixa alta pelo CSS. */
  rotulo: string;
  /** O número. `null`/`undefined` vira travessão. */
  valor: ReactNode;
  /** Uma linha de contexto: onde, de quantos, desde quando. */
  sub?: ReactNode;
  /** Cor semântica. Ausente = calmo (cor de texto). */
  tone?: "warn" | "crit";
  /** Torna o cartão clicável (vira <a>). */
  href?: string;
  /** Detalhe no hover — números exatos, idade da medição, etc. */
  title?: string;
  loading?: boolean;
}

export function Kpi({ rotulo, valor, sub, tone, href, title, loading }: KpiProps) {
  const conteudo = (
    <>
      <span className="kpi__rotulo">{rotulo}</span>
      {loading ? (
        <Skeleton width={72} height={32} />
      ) : (
        <span className={`kpi__valor tabular${tone ? ` kpi__valor--${tone}` : ""}`}>
          {valor == null || typeof valor === "string" || typeof valor === "number" ? (
            <ValorKpi texto={valor == null ? "—" : String(valor)} />
          ) : (
            valor
          )}
        </span>
      )}
      {sub && !loading && <span className="kpi__sub">{sub}</span>}
    </>
  );
  return href ? (
    <a className="kpi" href={href} title={title}>
      {conteudo}
    </a>
  ) : (
    <div className="kpi" title={title}>
      {conteudo}
    </div>
  );
}

/**
 * Número com a unidade menor ao lado ("223,6" grande, "KB/s" menor), na mesma
 * linha: junto e do mesmo tamanho, "223,6 KB/s" quebrava em duas linhas no cartão
 * estreito e a unidade disputava atenção com o número. Mesmo desenho da TV.
 *
 * A unidade pode vir ANTES ("US$ 12,40"): ela também fica menor e fora do número,
 * para o cartão de 400px não estourar e o CountUp animar só os dígitos.
 */
/** Símbolo de moeda que pode vir antes do número ("US$", "R$", "€"). */
const MOEDA_ANTES = /^[A-Z]{0,3}[$€£]$/;

function ValorKpi({ texto }: { texto: string }) {
  // Mesma estrutura com ou sem número (ver NumeroTV): o CountUp não pode ser
  // remontado quando o valor vira "—", senão perde o último valor medido. Por isso
  // as unidades entram como `cond && <span/>`: a posição do CountUp não muda.
  const p = separar(texto);
  const antes = p && MOEDA_ANTES.test(p.antes.trim()) ? p.antes.trim() : "";
  const depois = p?.depois.trim() ?? "";
  const inicio = p && antes ? p.antes.length : 0;
  const fim = p && depois ? texto.length - p.depois.length : texto.length;
  return (
    <>
      {antes && <span className="kpi__unidade kpi__unidade--antes">{antes}</span>}
      <CountUp texto={texto.slice(inicio, fim)} />
      {depois && <span className="kpi__unidade">{depois}</span>}
    </>
  );
}

/** Faixa de indicadores. Mobile-first: 2 colunas no celular, 3 no tablet, 6 no desktop. */
export function KpiGrid({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={className ? `kpi-grid ${className}` : "kpi-grid"}>{children}</div>;
}

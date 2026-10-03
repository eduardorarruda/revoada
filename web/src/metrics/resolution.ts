// O que o gráfico está REALMENTE mostrando: agregação e resolução efetiva.
//
// Dois silêncios custavam interpretação errada:
//
//  1. Nenhum gráfico dizia a AGREGAÇÃO. O dashboard de fábrica usa `max` para disco
//     e rede; o detalhe do servidor fixa `avg`. O operador lia o PICO como se fosse
//     o valor típico (ou o contrário).
//
//  2. A RESOLUÇÃO muda sozinha com a janela: o servidor troca `metrics` (bruto) por
//     `metrics_1m` e depois `metrics_1h` conforme o período cresce. Um pico de um
//     minuto simplesmente some ao olhar 30 dias — e o operador concluía que o
//     incidente não existiu. A resposta da Query API já traz o campo `table`; até
//     agora nenhum componente o lia.
//
// Este módulo transforma esses dois dados numa frase curta em PT-BR para o rodapé
// do painel.

import { formatDuration } from "../format";
import type { Unit } from "./dict";

/** Rótulos das agregações oferecidas na interface. */
export const AGG_LABEL: Record<string, string> = {
  avg: "média",
  max: "máximo",
  min: "mínimo",
  sum: "soma",
  last: "último valor",
  rate: "taxa",
};

/** aggLabel traduz o código da agregação; desconhecida volta como veio. */
export function aggLabel(agg: string | undefined | null): string {
  const key = (agg ?? "avg").toLowerCase();
  return AGG_LABEL[key] ?? key;
}

/** Nome da fonte de dados por trás da tabela escolhida pelo servidor. */
export function sourceLabel(table: string | undefined | null): string {
  switch (table) {
    case "metrics":
      return "dado bruto";
    case "metrics_1m":
      return "resumo de 1 min";
    case "metrics_1h":
      return "resumo de 1 h";
    default:
      return "";
  }
}

/**
 * smoothingWarning explica, em PT-BR, o que se PERDE naquela resolução. Sem esta
 * frase, um pico curto que sumiu num período longo é lido como "não aconteceu".
 */
export function smoothingWarning(table: string | undefined | null): string {
  switch (table) {
    case "metrics_1m":
      return "picos com menos de 1 min ficam suavizados";
    case "metrics_1h":
      return "picos com menos de 1 h ficam suavizados";
    default:
      return "";
  }
}

/** Primeira letra maiúscula, sem mexer no resto. */
function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/**
 * panelFootnote monta a nota discreta do rodapé do painel. Ex.:
 *
 *   "Máximo a cada 5 min · fonte: resumo de 1 min — picos com menos de 1 min ficam suavizados"
 *   "Média a cada 1 min · fonte: dado bruto"
 *
 * Tolerante: `table` e `step` são opcionais (a API pode não ter respondido ainda, ou
 * um painel pode não conhecer o passo) e a frase encolhe sem quebrar.
 */
export function panelFootnote(opts: {
  agg?: string | null;
  table?: string | null;
  step?: number | null;
  /** Agregação que o servidor REALMENTE calculou, quando diferiu da pedida. */
  aggEfetivo?: string | null;
}): string {
  const partes: string[] = [];
  const trocou = !!opts.aggEfetivo && opts.aggEfetivo !== (opts.agg ?? "avg");
  const agg = capitalize(aggLabel(trocou ? opts.aggEfetivo : opts.agg));
  partes.push(opts.step && opts.step > 0 ? `${agg} a cada ${formatDuration(opts.step)}` : `${agg} por ponto`);
  if (trocou) {
    // Silenciar a troca seria devolver outra estatística com o nome errado — o
    // defeito que a mudança do servidor existe para acabar.
    partes.push(`você pediu ${aggLabel(opts.agg)}, mas nesta janela os dados são resumidos e não guardam esse valor`);
  }

  const fonte = sourceLabel(opts.table);
  if (fonte) {
    const aviso = smoothingWarning(opts.table);
    partes.push(aviso ? `fonte: ${fonte}, ${aviso}` : `fonte: ${fonte}`);
  }
  return partes.join(" · ");
}

/**
 * aggSuffix é o sufixo do TÍTULO do painel, para a agregação aparecer sem precisar
 * do tooltip. `avg` não recebe sufixo: é o comportamento esperado por padrão e
 * poluiria todos os títulos. Máximo/mínimo/soma/último, sim — são os que enganam.
 */
export function aggSuffix(agg: string | undefined | null): string {
  const key = (agg ?? "avg").toLowerCase();
  if (key === "avg" || key === "") return "";
  return `, ${aggLabel(key)}`;
}

/**
 * unitForAgg devolve a unidade EFETIVA do painel: ela depende da agregação, não só
 * da métrica.
 *
 * `rate` mede quanto um contador acumulado andou dentro do balde, dividido pela
 * duração dele — o resultado é "por segundo". Sem este ajuste, o painel de rede
 * mostraria "12,3 MB" onde o valor significa "12,3 MB/s", que é a diferença entre
 * um tráfego trivial e um link saturado.
 */
export function unitForAgg(unit: Unit, agg: string | undefined | null): Unit {
  if ((agg ?? "").toLowerCase() !== "rate") return unit;
  return unit === "bytes" ? "bytes_per_s" : unit;
}

import { useMemo, useState } from "react";
import { PanelFrame } from "./PanelFrame";
import type { PanelBaseProps } from "./types";
import { cssVar } from "./chart";
import { formatValue } from "../format";
import type { Unit } from "../metrics/dict";

export interface Column {
  key: string;
  label: string;
  numeric?: boolean;
  /** Unidade da coluna: faz o número sair formatado ("42,3%") em vez de cru. */
  unit?: Unit;
  threshold?: { warn: number; crit: number }; // colore a célula
}

export type Cell = string | number | null;
export type Row = Record<string, Cell>;

// Painel tabela: ordenável, com células coloridas por threshold.
//
// A coluna numérica passa pelo formatador único (unidade do dicionário). Antes
// fazia `String(row[c.key])` e imprimia "42.337182817682" — ponto decimal inglês,
// 12 casas e nenhuma unidade — na mesma tela em que o gráfico ao lado mostrava
// "42,3%".
export function TablePanel({ columns, rows, ...base }: PanelBaseProps & { columns: Column[]; rows: Row[] }) {
  const [sortKey, setSortKey] = useState<string>(columns[0]?.key ?? "");
  const [asc, setAsc] = useState(true);

  const sorted = useMemo(() => {
    const copy = [...rows];
    copy.sort((a, b) => {
      const av = a[sortKey];
      const bv = b[sortKey];
      // Sem dado vai para o fim em ordem crescente (não se compara "—" com número).
      if (av == null && bv == null) return 0;
      if (av == null) return 1;
      if (bv == null) return -1;
      if (av === bv) return 0;
      const cmp = av < bv ? -1 : 1;
      return asc ? cmp : -cmp;
    });
    return copy;
  }, [rows, sortKey, asc]);

  const toggle = (key: string) => {
    if (key === sortKey) setAsc(!asc);
    else {
      setSortKey(key);
      setAsc(true);
    }
  };

  const cellColor = (col: Column, v: Cell): string | undefined => {
    if (!col.threshold || typeof v !== "number") return undefined;
    if (v >= col.threshold.crit) return cssVar("--crit");
    if (v >= col.threshold.warn) return cssVar("--warn");
    return undefined;
  };

  // Texto da célula: número com unidade, ausência explícita como "—".
  const cellText = (col: Column, v: Cell): string => {
    if (v == null) return "—";
    if (typeof v === "number") return formatValue(v, col.unit ?? "none");
    return v;
  };

  return (
    <PanelFrame {...base}>
      {/* Rolagem horizontal própria: com a coluna "Atualizado" a tabela passou a ter 3
          colunas e não pode empurrar a página inteira para o lado em 375px. */}
      <div className="ptable-wrap">
      <table className="ptable tabular">
        <thead>
          <tr>
            {columns.map((c) => (
              <th key={c.key} onClick={() => toggle(c.key)}>
                {c.label} {sortKey === c.key ? (asc ? "▲" : "▼") : ""}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {sorted.map((row, i) => (
            <tr key={i}>
              {columns.map((c) => (
                <td
                  key={c.key}
                  className={c.numeric ? "num" : ""}
                  style={{ color: row[c.key] == null ? "var(--text-3)" : cellColor(c, row[c.key]) }}
                >
                  {cellText(c, row[c.key])}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      </div>
    </PanelFrame>
  );
}

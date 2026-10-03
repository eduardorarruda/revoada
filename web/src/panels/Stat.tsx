import { PanelFrame } from "./PanelFrame";
import type { PanelBaseProps } from "./types";
import { cssVar } from "./chart";
import { formatValue } from "../format";
import { LastSeen } from "../components";

// Painel de número grande com tendência e sparkline.
//
// `value: null` significa SEM DADO (ou dado velho demais para ser apresentado como
// atual) e sai como "—" em cinza. Antes o chamador fazia `?? 0`, e um host morto
// aparecia como "Uptime: 0 s" e "Processos: 0" — zero legítimo e ausência de dado
// eram a mesma coisa na tela.
export function StatPanel({
  value,
  valueTs,
  trend,
  spark,
  staleAfterSeconds,
  ...base
}: PanelBaseProps & {
  value: number | null;
  /** Instante do valor (epoch em ms), para dizer "sem dado há X" quando velho. */
  valueTs?: number | null;
  trend?: number;
  spark?: number[];
  staleAfterSeconds?: number;
}) {
  // Valor + unidade pelo formatador único (unidade do dicionário via base.unit).
  const hasValue = value != null && Number.isFinite(value);
  const fmt = formatValue(value, base.unit ?? "none");
  return (
    <PanelFrame {...base}>
      <div className="stat__row">
        <span
          className={`stat__value tabular${hasValue ? "" : " panel__value--nodata"}`}
          title={hasValue ? undefined : "Nenhum valor recente para esta métrica"}
        >
          {fmt}
        </span>
        {trend != null && (
          <span className={`tabular ${trend >= 0 ? "stat__trend--up" : "stat__trend--down"}`}>
            {trend >= 0 ? "▲" : "▼"} {new Intl.NumberFormat("pt-BR", { maximumFractionDigits: 1 }).format(Math.abs(trend))}%
          </span>
        )}
      </div>
      {!hasValue && (
        <p className="panel__stale">
          {valueTs != null ? <LastSeen ts={valueTs} staleAfterSeconds={staleAfterSeconds} prefix="dado de" /> : "não medido nesta janela"}
        </p>
      )}
      {hasValue && spark && spark.length > 1 && <Sparkline values={spark} />}
    </PanelFrame>
  );
}

function Sparkline({ values }: { values: number[] }) {
  const w = 200;
  const h = 36;
  const min = Math.min(...values);
  const max = Math.max(...values);
  const range = max - min || 1;
  const pts = values
    .map((v, i) => `${(i / (values.length - 1)) * w},${h - ((v - min) / range) * h}`)
    .join(" ");
  return (
    <svg viewBox={`0 0 ${w} ${h}`} width="100%" height={h} preserveAspectRatio="none" style={{ marginTop: "var(--sp-2)" }}>
      <polyline points={pts} fill="none" stroke={cssVar("--accent")} strokeWidth="1.5" />
    </svg>
  );
}

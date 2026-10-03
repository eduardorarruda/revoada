import { PanelFrame } from "./PanelFrame";
import type { PanelBaseProps } from "./types";
import { cssVar } from "./chart";
import { formatValue } from "../format";
import { thresholdColorVar, thresholdState, type Threshold } from "../metrics/thresholds";
import { LastSeen } from "../components";

// Painel gauge (arco SVG) com semáforo alimentado pelo LIMIAR CONFIGURADO.
//
// Antes o arco cravava 70/85/100: CPU a 87% ficava VERMELHA no dashboard enquanto o
// Mural (limiar real 70/90) mostrava âmbar e nenhum alerta disparava. Agora o
// limiar chega pronto de fora (override do host → global → default) e a cor muda
// exatamente onde o alerta dispara.
export function GaugePanel({
  value,
  valueTs,
  min = 0,
  max = 100,
  threshold,
  staleAfterSeconds,
  ...base
}: PanelBaseProps & {
  /** null = sem dado (ou dado velho demais). Renderiza "—", NUNCA 0. */
  value: number | null;
  /** Instante do valor (epoch em ms) — usado para dizer "sem dado há X". */
  valueTs?: number | null;
  min?: number;
  max?: number;
  /**
   * Limiar em vigor para esta métrica/host. AUSENTE = a métrica não é utilização de
   * recurso (bytes, contagem, carga média): o arco fica na cor neutra de série, sem
   * fingir um semáforo de saúde que não existe para ela.
   */
  threshold?: Threshold;
  staleAfterSeconds?: number;
}) {
  // Unidade vem do dicionário via base.unit; gauge é tipicamente percentual.
  const unit = base.unit ?? "percent";
  const hasValue = value != null && Number.isFinite(value);

  // Sem dado: arco zerado e cinza. Um arco em 0% VERDE afirmaria "saudável" sobre
  // uma máquina que pode estar morta.
  const pct = hasValue ? Math.max(0, Math.min(1, (value - min) / (max - min))) : 0;
  const angle = Math.PI * pct; // semicírculo
  const cx = 100, cy = 100, r = 80;
  const x = cx - r * Math.cos(angle);
  const y = cy - r * Math.sin(angle);
  const color = !hasValue ? "--text-3" : threshold ? thresholdColorVar(thresholdState(value, threshold)) : "--info"; // azul: neutro FORA do semáforo. --accent tem o mesmo matiz de
  // --warn (~40°), então um painel sem limiar (Processos, Carga) parecia estar em
  // atenção sem estar — e num arco isolado não há referência para o olho comparar.

  return (
    <PanelFrame {...base}>
      <div style={{ textAlign: "center" }}>
        <svg viewBox="0 0 200 120" width="100%" height={120} role="img" aria-label={`${base.title}: ${formatValue(value, unit)}`}>
          <path d={`M 20 100 A ${r} ${r} 0 0 1 180 100`} fill="none" stroke={cssVar("--bg-3")} strokeWidth="14" strokeLinecap="round" />
          {hasValue && (
            <path
              d={`M 20 100 A ${r} ${r} 0 0 1 ${x} ${y}`}
              fill="none"
              stroke={cssVar(color)}
              strokeWidth="14"
              strokeLinecap="round"
            />
          )}
        </svg>
        <div
          className="stat__value tabular"
          style={{ marginTop: "calc(-1 * var(--sp-5))", color: cssVar(color) }}
          title={
            !hasValue
              ? "Não medido nesta janela"
              : threshold
                ? `Atenção a partir de ${threshold.warn}% · crítico a partir de ${threshold.crit}%`
                : "Esta métrica não tem limiar de saúde configurável, o gauge mostra o valor, sem semáforo."
          }
        >
          {formatValue(value, unit)}
        </div>
        {!hasValue && (
          <p className="panel__stale">
            {valueTs != null ? <LastSeen ts={valueTs} staleAfterSeconds={staleAfterSeconds} prefix="dado de" /> : "não medido nesta janela"}
          </p>
        )}
      </div>
    </PanelFrame>
  );
}

import { PanelFrame } from "./PanelFrame";
import type { PanelBaseProps } from "./types";
import { cssVar } from "./chart";
import { formatValue } from "../format";
import { thresholdColorVar, thresholdState, type Threshold } from "../metrics/thresholds";

export interface BarItem {
  label: string;
  /** null = sem dado recente para esta série. Renderiza "—" e barra vazia. */
  value: number | null;
  max?: number;
}

// Painel bar gauge: barras horizontais com cor por LIMIAR CONFIGURADO.
//
// Antes a cor vinha de 0,7/0,85 cravados na proporção da barra — discordando do
// limiar real (70/90 para CPU e RAM, 75/90 para disco) e, portanto, do alerta.
// `threshold` ausente = métrica sem semáforo de saúde: as barras usam a cor neutra
// de série em vez de inventar um limiar.
export function BarGaugePanel({
  items,
  threshold,
  ...base
}: PanelBaseProps & { items: BarItem[]; threshold?: Threshold }) {
  const unit = base.unit ?? "none";
  return (
    <PanelFrame {...base}>
      {items.map((it) => {
        const max = it.max ?? 100;
        const valor = it.value != null && Number.isFinite(it.value) ? it.value : null;
        const hasValue = valor != null;
        const fill = valor != null ? Math.max(0, Math.min(1, valor / max)) : 0;
        // O semáforo compara o VALOR com o limiar (percentual), não a proporção da
        // barra — é o mesmo número que o alerta olha.
        const color = valor == null ? "--text-3" : threshold ? thresholdColorVar(thresholdState(valor, threshold)) : "--info"; // azul: neutro FORA do semáforo. --accent tem o mesmo matiz de
  // --warn (~40°), então um painel sem limiar (Processos, Carga) parecia estar em
  // atenção sem estar — e num arco isolado não há referência para o olho comparar.
        return (
          <div className="bargauge__row" key={it.label}>
            <span style={{ color: "var(--text-2)" }}>{it.label}</span>
            <span className="bargauge__track">
              <span className="bargauge__fill" style={{ width: `${fill * 100}%`, background: cssVar(color) }} />
            </span>
            <span
              className="tabular"
              style={hasValue ? undefined : { color: "var(--text-3)" }}
              title={hasValue ? undefined : "Não medido nesta janela"}
            >
              {formatValue(it.value, unit)}
            </span>
          </div>
        );
      })}
    </PanelFrame>
  );
}

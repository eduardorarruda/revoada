// Fronteira de carregamento do heatmap — o único painel que depende de ECharts.
//
// Defeito medido: `panels/index.ts` exportava TimeSeriesPanel (uPlot, leve) e
// HeatmapPanel (ECharts, pesado) do mesmo barril. Como HostDetail, Explore,
// DashboardView e DashboardEdit importam desse barril, o Rollup juntava tudo num
// chunk compartilhado — `Heatmap-*.js` com 566 kB / 197,71 kB gzip descia em TODA
// tela de gráfico, enquanto o HeatmapPanel é usado por UM lugar: pages/DevPanels.tsx,
// a página de vitrine de componentes.
//
// A correção é uma fronteira de import dinâmico: o barril passa a exportar este
// invólucro (estático e minúsculo), e o ECharts só é buscado quando um heatmap é
// realmente renderizado. A API pública do painel não muda — quem usa continua
// escrevendo <HeatmapPanel …/> vindo de "../panels".
import { lazy, Suspense } from "react";
import { PanelFrame } from "./PanelFrame";
import type { PanelBaseProps } from "./types";

export type HeatmapPanelProps = PanelBaseProps & {
  xLabels: string[];
  yLabels: string[];
  data: number[][];
};

// `import type` acima é apagado na compilação; só este import() cria a fronteira.
const HeatmapImpl = lazy(() => import("./Heatmap").then((m) => ({ default: m.HeatmapPanel })));

export function HeatmapPanel({ xLabels, yLabels, data, ...base }: HeatmapPanelProps) {
  return (
    // Enquanto o chunk do ECharts desce, a moldura já aparece com título e descrição
    // no estado "carregando" — o painel nunca some da grade nem salta de altura.
    <Suspense fallback={<PanelFrame {...base} state="loading">{null}</PanelFrame>}>
      <HeatmapImpl {...base} xLabels={xLabels} yLabels={yLabels} data={data} />
    </Suspense>
  );
}

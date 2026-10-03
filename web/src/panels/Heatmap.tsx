import { useEffect, useRef } from "react";
// Import seletivo via echarts/core: registramos SÓ o gráfico de heatmap e os
// componentes usados (grid/tooltip/visualMap) + renderer canvas. Assim o
// tree-shaking descarta o resto da lib (linhas, mapas, gauges, etc.).
import * as echarts from "echarts/core";
import { HeatmapChart } from "echarts/charts";
import { GridComponent, TooltipComponent, VisualMapComponent } from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import { PanelFrame } from "./PanelFrame";

echarts.use([HeatmapChart, GridComponent, TooltipComponent, VisualMapComponent, CanvasRenderer]);
import type { PanelBaseProps } from "./types";
import { cssVar } from "./chart";

// data[y][x] = valor. xLabels/yLabels rotulam os eixos.
export function HeatmapPanel({
  xLabels,
  yLabels,
  data,
  ...base
}: PanelBaseProps & { xLabels: string[]; yLabels: string[]; data: number[][] }) {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!ref.current || base.state === "loading") return;
    const chart = echarts.init(ref.current, undefined, { renderer: "canvas" });
    const points: [number, number, number][] = [];
    let maxV = 0;
    data.forEach((rowArr, y) =>
      rowArr.forEach((v, x) => {
        points.push([x, y, v]);
        if (v > maxV) maxV = v;
      }),
    );
    chart.setOption({
      grid: { left: 60, right: 16, top: 10, bottom: 30 },
      xAxis: { type: "category", data: xLabels, axisLabel: { color: cssVar("--text-3") } },
      yAxis: { type: "category", data: yLabels, axisLabel: { color: cssVar("--text-3") } },
      visualMap: {
        min: 0,
        max: maxV || 1,
        calculable: false,
        orient: "horizontal",
        left: "center",
        bottom: -6,
        show: false,
        inRange: { color: [cssVar("--bg-3"), cssVar("--accent"), cssVar("--crit")] },
      },
      series: [{ type: "heatmap", data: points }],
      tooltip: { position: "top" },
    });
    const ro = new ResizeObserver(() => chart.resize());
    ro.observe(ref.current);
    return () => {
      ro.disconnect();
      chart.dispose();
    };
  }, [xLabels, yLabels, data, base.state]);

  return (
    <PanelFrame {...base}>
      <div ref={ref} className="echart-wrap" style={{ height: 200 }} />
    </PanelFrame>
  );
}

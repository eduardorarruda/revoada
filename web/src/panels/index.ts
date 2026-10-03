export { PanelFrame } from "./PanelFrame";
export { TimeSeriesPanel } from "./TimeSeries";
export { StatPanel } from "./Stat";
export { GaugePanel } from "./Gauge";
export { BarGaugePanel } from "./BarGauge";
export { TablePanel } from "./Table";
export { StateTimelinePanel } from "./StateTimeline";
// Heatmap sai do barril por uma fronteira LAZY: é o único painel que usa ECharts, e
// exportá-lo direto daqui arrastava 197,71 kB gzip para toda tela de gráfico que
// importa deste arquivo. Ver panels/HeatmapLazy.tsx.
export { HeatmapPanel } from "./HeatmapLazy";
export { TextPanel } from "./Text";
export type { PanelBaseProps, PanelState, TimeSeriesData, SeriesData } from "./types";

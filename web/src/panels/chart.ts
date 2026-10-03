// Utilitários de gráfico: lê tokens CSS (uPlot/ECharts precisam de cores literais).
export function cssVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

// Paleta de séries (colorblind-safe), derivada dos tokens semânticos + neutros.
export function seriesPalette(): string[] {
  return [
    cssVar("--accent"),
    cssVar("--info"),
    cssVar("--violet"),
    cssVar("--warn"),
    cssVar("--ok"),
    cssVar("--crit"),
  ];
}

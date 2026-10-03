// Fase F — "Escopo do painel": camada amigável sobre `query.filters`/`query.metric`.
// Módulo puro (sem React/echarts/uplot) para ser testável isoladamente e reusado
// pelo PanelModal em DashboardEdit.tsx.
//
// "Servidor inteiro" (padrão) = sem recorte de serviço; opcionalmente um `filters.host`.
// "Container específico" = `filters.container` preenchido — o recorte concreto de
// "serviço" hoje é o container Docker (único com métricas reais). Serviços descobertos
// (mysql/nginx) só terão métricas quando houver exporters (etapa futura).

// Uma linha do editor de filtros (rótulo + valor).
export type FilterRow = { label: string; value: string };

export type PanelScope = "server" | "container";

// Métrica assumida ao entrar no escopo de container quando a atual não é de container.
export const DEFAULT_CONTAINER_METRIC = "container.cpu.utilization";

// Métrica usada para descobrir os containers existentes (label `container`).
export const CONTAINER_LABEL_METRIC = "container.running";

export function isContainerMetric(metric: string): boolean {
  return metric.startsWith("container.");
}

// Infere o escopo a partir dos filtros salvos: tem `container` preenchido → "container";
// senão → "server". É assim que um painel existente reabre no escopo certo.
export function inferScope(rows: FilterRow[]): PanelScope {
  return rows.some((r) => r.label.trim() === "container" && r.value.trim() !== "") ? "container" : "server";
}

// Lê o valor de um filtro por rótulo (vazio quando ausente).
export function filterValue(rows: FilterRow[], label: string): string {
  return rows.find((r) => r.label.trim() === label)?.value ?? "";
}

// Define (valor não vazio) ou remove (valor vazio) uma linha de filtro por rótulo,
// preservando as demais. É a ponte entre a UI de escopo e o `query.filters`.
export function setFilter(rows: FilterRow[], label: string, value: string): FilterRow[] {
  const others = rows.filter((r) => r.label.trim() !== label);
  return value ? [...others, { label, value }] : others;
}

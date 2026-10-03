// valorDoAlerta: o valor de um alerta escrito na língua da métrica.
//
// Nasceu na TV, onde "valor atual 761,7" (a idade do último sinal, em segundos) era
// mostrado em letra grande para quem está a 5 m e não tem como adivinhar a unidade.
import { formatMetric, formatValue } from "../../format";

// Nome reservado da regra de fábrica "servidor parou de reportar"
// (server/internal/alerting/heartbeat.go): o valor é a IDADE do último sinal, em s.
export const HEARTBEAT_METRIC = "revoada.host.heartbeat";

function tempo(segundos: number): string {
  if (segundos < 60) return `${Math.round(segundos)} s`;
  if (segundos < 3600) return `${Math.round(segundos / 60)} min`;
  return `${Math.round(segundos / 3600)} h`;
}

export function valorDoAlerta(metrica: string | undefined, valor: number): string {
  if (metrica === HEARTBEAT_METRIC) return `${tempo(valor)} sem sinal`;
  if (metrica === "container.running") return "container parado";
  if (!metrica) return formatValue(valor, "none");
  return formatMetric(metrica, valor);
}

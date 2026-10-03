// Rótulos de tempo dos gráficos em pt-BR, no horário de Brasília.
//
// O padrão do uPlot é americano ("4:30pm", "9/24/26"). Num painel em português
// isso obriga a pessoa a converter de cabeça justamente na hora do incidente.
import { APP_TIME_ZONE } from "../format";

const hora = new Intl.DateTimeFormat("pt-BR", { timeZone: APP_TIME_ZONE, hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
const horaSeg = new Intl.DateTimeFormat("pt-BR", {
  timeZone: APP_TIME_ZONE,
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hourCycle: "h23",
});
const dia = new Intl.DateTimeFormat("pt-BR", { timeZone: APP_TIME_ZONE, day: "2-digit", month: "2-digit" });
const completo = new Intl.DateTimeFormat("pt-BR", {
  timeZone: APP_TIME_ZONE,
  day: "2-digit",
  month: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hourCycle: "h23",
});

const DIA_S = 86400;

/**
 * Rótulos do eixo X. `splits` em segundos (epoch). Janela de até ~3 dias mostra a
 * hora, com a data na segunda linha no primeiro tique e sempre que o dia vira.
 * Janela maior mostra só a data.
 */
export function rotulosTempo(splits: number[], janelaS: number): string[] {
  if (janelaS > 3 * DIA_S) return splits.map((s) => dia.format(s * 1000));
  // Zoom fino (tiques a menos de 1 min): sem os segundos, todos diriam "16:30".
  const passo = splits.length > 1 ? splits[1] - splits[0] : Infinity;
  const fmtHora = passo < 60 ? horaSeg : hora;
  let diaAnterior = "";
  return splits.map((s) => {
    const d = dia.format(s * 1000);
    const h = fmtHora.format(s * 1000);
    const virou = d !== diaAnterior;
    diaAnterior = d;
    return virou ? `${h}\n${d}` : h;
  });
}

/** Instante completo para a legenda do cursor: "24/09, 16:30:05". */
export function instante(s: number | null): string {
  return s == null ? "—" : completo.format(s * 1000);
}

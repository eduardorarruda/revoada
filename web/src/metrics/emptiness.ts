// Quando um painel deve dizer "Sem dados no período".
//
// Defeito medido: as três telas de gráfico (Visão do Servidor, Explore e a
// visualização de dashboard) decidiam o estado vazio por `resp.ts.length ? "ok" :
// "empty"`. Desde que a Query API passou a devolver GRADE DENSA — uma linha por
// balde do período, mesmo sem nenhuma amostra dentro dele — `ts` nunca mais volta
// vazio. Logo o estado "empty" deixou de acontecer e a mensagem "Sem dados no
// período." (panels/PanelFrame.tsx) ficou INALCANÇÁVEL.
//
// Prova: /api/tv/query para um host inexistente devolveu `ts` com 5 baldes e
// `series: []`. O painel entrava em "ok" e desenhava um gráfico em branco — que se
// lê como "nada aconteceu aqui", exatamente o oposto de "não medimos isto". Host
// mudo, métrica inexistente ou fora de escopo caíam todos nesse silêncio.
//
// O gatilho correto é o CONTEÚDO das séries, não o comprimento do eixo do tempo:
// nenhuma série, ou nenhuma série com ao menos um valor numérico.

/** Forma mínima compartilhada por QueryResponse.series e TimeSeriesData.series. */
export interface SeriesLike {
  values?: (number | null)[] | null;
}

/**
 * hasAnyValue: existe ao menos UM ponto numérico em qualquer série?
 * Nulo, NaN e Infinity não contam — são "não medido", não valor.
 */
export function hasAnyValue(series: readonly SeriesLike[] | undefined | null): boolean {
  if (!series) return false;
  for (const s of series) {
    const values = s?.values;
    if (!values) continue;
    for (const v of values) {
      if (v != null && Number.isFinite(v)) return true;
    }
  }
  return false;
}

/**
 * isEmptyResult é a pergunta que o painel faz: devo mostrar "Sem dados no período."?
 * True quando a resposta não traz nenhum valor real — grade densa de nulos inclusa.
 */
export function isEmptyResult(series: readonly SeriesLike[] | undefined | null): boolean {
  return !hasAnyValue(series);
}

/** panelStateFor traduz a resposta direto no estado do PanelFrame. */
export function panelStateFor(series: readonly SeriesLike[] | undefined | null): "ok" | "empty" {
  return isEmptyResult(series) ? "empty" : "ok";
}

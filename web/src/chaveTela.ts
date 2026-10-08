// Chave de remontagem da tela (App.tsx): a barreira de erro e a transição de
// entrada são por TELA, e não por caminho. Seções com abas internas (Agentes de
// IA: #/ia, #/ia/execucoes, #/ia/precos…) trocam de aba sem remontar: o cabeçalho
// e as abas ficam parados e só o conteúdo da aba entra; com a chave pelo caminho,
// cada clique numa aba repetia a entrada da página inteira e perdia o estado dela.

/** Prefixos de seções cujas abas vivem dentro de uma única página. */
const SECOES_COM_ABAS = ["/ia"] as const;

export function chaveDaTela(routePath: string): string {
  for (const secao of SECOES_COM_ABAS) {
    if (routePath === secao || routePath.startsWith(`${secao}/`)) return secao;
  }
  return routePath;
}

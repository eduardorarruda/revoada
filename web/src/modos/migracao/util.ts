// Utilitários do modo migração.

import { mensagemDeErro } from "../../format";

// mensagemErro devolve a explicação que o servidor escreveu (sem o prefixo "403: ").
// `alternativa` só aparece quando o servidor não explicou nada (ex.: falha de rede);
// cada tela passa a sua, dizendo o que não foi feito.
export function mensagemErro(
  e: unknown,
  alternativa = "A operação de migração falhou sem explicação do servidor. Tente de novo.",
): string {
  return mensagemDeErro(e, alternativa);
}

export const NOME_TRANSFORMACAO: Record<string, string> = {
  nenhuma: "copiar como está",
  converter_tipo: "converter tipo",
  charset: "converter charset",
  aparar: "tirar espaços",
  valor_padrao: "valor padrão se nulo",
  constante: "valor fixo",
  mapa_valores: "trocar valores",
  concatenar: "juntar colunas",
  dividir: "dividir texto",
  data_formato: "texto → data",
};

export const NOME_ACAO: Record<string, string> = {
  copiar: "Copiar para",
  criar_no_destino: "Criar no destino",
  ignorar: "Não migrar",
};

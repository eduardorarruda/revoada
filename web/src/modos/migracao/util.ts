// Utilitários do modo migração.

// mensagemErro tira o prefixo "403: " que o cliente da API põe nos erros.
export function mensagemErro(e: unknown): string {
  const m = e instanceof Error ? e.message : String(e);
  return m.replace(/^\d{3}:\s*/, "").trim() || "Algo deu errado. Tente de novo.";
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

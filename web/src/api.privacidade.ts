// Dados da política de privacidade (GET /api/privacidade, rota pública). Fica fora do
// api.ts porque a página é pública: não passa pelo refresh de sessão nem pela
// reautenticação do `api<T>`.

export interface Privacidade {
  controlador: string;
  contato: string;
  retencao_dias: number;
  auditoria_dias: number;
  sessao_inatividade_horas: number;
  sessao_max_horas: number;
}

/** Padrões do Revoada (server/internal/config): usados enquanto a consulta não volta
 *  e quando ela falha — aí a página avisa que são os padrões. */
export const PRIVACIDADE_PADRAO: Privacidade = {
  controlador: "",
  contato: "",
  retencao_dias: 120,
  auditoria_dias: 365,
  sessao_inatividade_horas: 24,
  sessao_max_horas: 168,
};

export async function buscarPrivacidade(): Promise<Privacidade> {
  const res = await fetch("/api/privacidade");
  if (!res.ok) throw new Error(String(res.status));
  return res.json();
}

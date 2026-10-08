// Cliente da API de agentes de IA (contrato em docs/api-ia.md).
//
// Fica fora do api.ts (que já passa de 2.500 linhas) mas usa o mesmo `api<T>`: o
// mesmo refresh de sessão, a mesma reautenticação e o mesmo selo de conexão.
//
// Regra do contrato que os tipos carregam: `null` é "não informado / sem preço",
// nunca zero. Todo campo que o backend pode deixar sem valor está tipado como
// `number | null`, para o compilador obrigar a tela a decidir o que mostrar.
import { api } from "./api";

// --- filtros -----------------------------------------------------------------

/** Parâmetros comuns de consulta. `from`/`to` em RFC 3339 (o contrato aceita
 *  RFC 3339 ou epoch; RFC 3339 não deixa dúvida entre segundos e milissegundos). */
export interface IaFiltro {
  from?: string;
  to?: string;
  service?: string;
  agente?: string;
  modelo?: string;
  host?: string;
}

export interface IaFiltroExecucoes extends IaFiltro {
  status?: "erro";
  /** Custo mínimo em dólar. */
  custo_min?: number;
  conversa_id?: string;
  /** Até 500 (teto do servidor). */
  limit?: number;
}

/** Monta a query string sem parâmetros vazios. Exportada para teste. */
export function queryIa(f: object): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(f)) {
    if (v === undefined || v === null || v === "") continue;
    p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : "";
}

// --- resumo ------------------------------------------------------------------

export interface IaJanela {
  from_ms: number;
  to_ms: number;
  passo_s: number;
}

export interface IaTotais {
  chamadas: number;
  erros: number;
  /** 0..1. null quando não houve chamada (não dá para dividir por zero). */
  taxa_erro: number | null;
  execucoes: number;
  ferramentas_chamadas: number;
  ferramentas_erros: number;
  tokens_entrada: number;
  tokens_saida: number;
  tokens_cache_leitura: number;
  /** Chamadas de modelo cuja biblioteca não informou tokens. */
  sem_tokens: number;
  /** Informado + estimado; null se nada pôde ser calculado. */
  custo_usd: number | null;
  custo_informado_usd: number | null;
  /** true quando houve chamada sem preço ou sem tokens: o custo real é maior. */
  custo_parcial: boolean;
  chamadas_sem_preco: number;
  latencia_p50_ms: number | null;
  latencia_p95_ms: number | null;
  latencia_p99_ms: number | null;
}

export interface IaPontoSerie {
  ts_ms: number;
  custo_usd: number | null;
  chamadas: number;
  erros: number;
}

/** Preço vigente resumido, como vem em `por_modelo[].preco`. */
export interface IaPrecoVigente {
  entrada_por_1m: number | null;
  saida_por_1m: number | null;
  vigente_desde: string;
  origem: string;
}

export interface IaModeloResumo {
  provedor: string;
  modelo: string;
  chamadas: number;
  erros: number;
  tokens_entrada: number;
  tokens_saida: number;
  custo_usd: number | null;
  sem_preco: boolean;
  latencia_p95_ms: number | null;
  preco: IaPrecoVigente | null;
}

export interface IaAgenteResumo {
  agente: string;
  service: string;
  chamadas: number;
  erros: number;
  custo_usd: number | null;
  tokens_entrada: number;
  tokens_saida: number;
}

export interface IaFerramentaResumo {
  ferramenta: string;
  chamadas: number;
  erros: number;
  latencia_p95_ms: number | null;
}

/** Idade da tabela de preços de referência que vem com o Revoada. */
export interface IaReferenciaPrecos {
  referencia: string;
  dias_desde_atualizacao: number | null;
  desatualizada: boolean;
}

export interface IaResumo {
  janela: IaJanela;
  totais: IaTotais;
  serie: IaPontoSerie[];
  por_modelo: IaModeloResumo[];
  por_agente: IaAgenteResumo[];
  ferramentas: IaFerramentaResumo[];
  precos: IaReferenciaPrecos;
}

export function getIaResumo(f: IaFiltro = {}): Promise<IaResumo> {
  return api<IaResumo>(`/api/ia/resumo${queryIa(f)}`);
}

// --- execuções ---------------------------------------------------------------

export type IaStatusExecucao = "ok" | "erro";

export interface IaExecucao {
  trace_id: string;
  inicio_ms: number;
  duracao_ms: number;
  service: string;
  host: string;
  agente: string;
  conversa_id: string;
  chamadas_modelo: number;
  chamadas_ferramenta: number;
  erros: number;
  tokens_entrada: number | null;
  tokens_saida: number | null;
  custo_usd: number | null;
  custo_parcial: boolean;
  modelos: string[];
  status: IaStatusExecucao;
}

export function listIaExecucoes(f: IaFiltroExecucoes = {}): Promise<{ execucoes: IaExecucao[] }> {
  return api<{ execucoes: IaExecucao[] }>(`/api/ia/execucoes${queryIa(f)}`);
}

// --- replay ------------------------------------------------------------------

/** De onde veio o custo de um passo (ver "Conceitos" no contrato). */
export type IaCustoOrigem = "informado" | "estimado" | "sem_preco" | "sem_tokens";

export interface IaTotaisExecucao {
  chamadas_modelo: number;
  chamadas_ferramenta: number;
  erros: number;
  tokens_entrada: number | null;
  tokens_saida: number | null;
  custo_usd: number | null;
  custo_parcial: boolean;
}

export interface IaPasso {
  span_id: string;
  parent_span_id: string;
  ts_ms: number;
  duracao_ms: number;
  /** Nível na árvore de spans de IA (0 = raiz). */
  profundidade: number;
  operacao: string;
  nome: string;
  provedor: string;
  modelo: string;
  agente: string;
  ferramenta: string;
  chamada_id: string;
  tokens_entrada: number | null;
  tokens_saida: number | null;
  tokens_cache_leitura: number | null;
  custo_usd: number | null;
  /** Vazio em passos que não são chamada de modelo. */
  custo_origem: IaCustoOrigem | "";
  /** Data do preço usado na estimativa (AAAA-MM-DD); vazio fora de "estimado". */
  preco_data: string;
  erro: string;
  motivos_fim: string[];
  com_conteudo: boolean;
}

export interface IaRepeticao {
  ferramenta: string;
  vezes: number;
  primeiro_span_id: string;
}

export interface IaExecucaoDetalhe {
  trace_id: string;
  agente: string;
  service: string;
  host: string;
  conversa_id: string;
  inicio_ms: number;
  duracao_ms: number;
  totais: IaTotaisExecucao;
  passos: IaPasso[];
  repeticoes: IaRepeticao[];
  conteudo: { disponivel: boolean; pode_ver: boolean };
}

export function getIaExecucao(traceId: string): Promise<IaExecucaoDetalhe> {
  return api<IaExecucaoDetalhe>(`/api/ia/execucoes/${encodeURIComponent(traceId)}`);
}

export interface IaMensagem {
  span_id: string;
  lado: "entrada" | "saida";
  papel: string;
  ordem: number;
  texto: string;
  truncado: boolean;
  redigido: boolean;
}

/** Conteúdo (prompts e respostas). 403 para leitor; 404 sem conteúdo gravado.
 *  Cada leitura entra na trilha de auditoria do servidor. */
export function getIaConteudo(traceId: string): Promise<{ mensagens: IaMensagem[] }> {
  return api<{ mensagens: IaMensagem[] }>(`/api/ia/execucoes/${encodeURIComponent(traceId)}/conteudo`);
}

// --- ferramentas ---------------------------------------------------------------

export interface IaFerramenta {
  ferramenta: string;
  chamadas: number;
  erros: number;
  taxa_erro: number | null;
  latencia_p50_ms: number | null;
  latencia_p95_ms: number | null;
  erros_frequentes: { erro: string; vezes: number }[];
}

export function listIaFerramentas(f: IaFiltro = {}): Promise<{ ferramentas: IaFerramenta[] }> {
  return api<{ ferramentas: IaFerramenta[] }>(`/api/ia/ferramentas${queryIa(f)}`);
}

// --- preços ------------------------------------------------------------------

export interface IaPreco {
  id: number;
  provedor: string;
  /** Aceita `*` no fim como prefixo: `gpt-4o-mini*` cobre `gpt-4o-mini-2024-07-18`. */
  modelo: string;
  entrada_por_1m: number | null;
  saida_por_1m: number | null;
  cache_leitura_por_1m: number | null;
  cache_escrita_por_1m: number | null;
  moeda: string;
  vigente_desde: string;
  origem: string;
}

export interface IaModeloSemPreco {
  provedor: string;
  modelo: string;
  chamadas: number;
}

export interface IaPrecosResposta {
  precos: IaPreco[];
  modelos_sem_preco: IaModeloSemPreco[];
  referencia: IaReferenciaPrecos;
}

/** Corpo do POST: uma linha nova. `id` e `origem` são do servidor. */
export type IaPrecoNovo = Omit<IaPreco, "id" | "origem">;

export function listIaPrecos(): Promise<IaPrecosResposta> {
  return api<IaPrecosResposta>("/api/ia/precos");
}

/** Admin. Trocar um preço é criar uma linha com `vigente_desde` posterior. */
export async function criarIaPreco(p: IaPrecoNovo): Promise<void> {
  await api<unknown>("/api/ia/precos", { method: "POST", body: JSON.stringify(p) });
}

/** Admin. */
export async function apagarIaPreco(id: number): Promise<void> {
  await api<unknown>(`/api/ia/precos/${id}`, { method: "DELETE" });
}

// --- purge -------------------------------------------------------------------

/** Frase que o servidor exige para apagar todo o conteúdo de IA. */
export const FRASE_PURGE_TODO_CONTEUDO = "APAGAR TODO O CONTEÚDO DE IA";

export type IaPurge =
  | { alvo: "conversa" | "trace"; valor: string }
  | { alvo: "todo_conteudo"; frase: string };

/** Admin. Apaga chamadas e conteúdo de uma conversa, de um trace ou todo o conteúdo. */
export async function purgeIa(p: IaPurge): Promise<void> {
  await api<unknown>("/api/ia/purge", { method: "POST", body: JSON.stringify(p) });
}

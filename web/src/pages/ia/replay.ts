// Lógica pura do replay de uma execução: que tipo de passo é, onde ele cai na
// linha do tempo, quais passos formam uma repetição e a requisição "copiável".
import type { IaMensagem, IaPasso, IaRepeticao } from "../../api.ia";
import { fmtTokens, NAO_INFORMADO } from "./formato";

export type TipoPasso = "modelo" | "ferramenta" | "agente" | "outro";

/** Operações que chamam um modelo (as que consomem tokens e custam dinheiro). */
const OPERACOES_MODELO = new Set(["chat", "text_completion", "generate_content", "embeddings"]);
const OPERACOES_AGENTE = new Set(["invoke_agent", "create_agent"]);

/**
 * O contrato só nomeia as operações de modelo. Ferramenta e agente são reconhecidos
 * pela operação da convenção GenAI (execute_tool / invoke_agent) OU pelo campo
 * preenchido, para não depender de qual dialeto o normalizador do gateway gravou.
 */
export function tipoDoPasso(p: Pick<IaPasso, "operacao" | "ferramenta" | "agente">): TipoPasso {
  if (OPERACOES_MODELO.has(p.operacao)) return "modelo";
  if (p.operacao === "execute_tool" || p.ferramenta) return "ferramenta";
  if (OPERACOES_AGENTE.has(p.operacao) || p.agente) return "agente";
  return "outro";
}

export const ROTULO_TIPO: Record<TipoPasso, string> = {
  modelo: "modelo",
  ferramenta: "ferramenta",
  agente: "agente",
  outro: "passo",
};

/** A frase do passo na linha do tempo. */
export function rotuloDoPasso(p: IaPasso): string {
  switch (tipoDoPasso(p)) {
    case "modelo":
      if (p.operacao === "embeddings") return `Gerou embeddings com ${p.modelo || "modelo não informado"}`;
      return `Chamou o modelo ${p.modelo || "não informado"}`;
    case "ferramenta":
      return `Usou a ferramenta ${p.ferramenta || p.nome}`;
    case "agente":
      return `Agente ${p.agente || p.nome}`;
    default:
      return p.nome || p.operacao || "Passo";
  }
}

/** Posição do passo na barra da execução, em % (largura mínima para ser visível). */
export function barraDoPasso(
  p: Pick<IaPasso, "ts_ms" | "duracao_ms">,
  inicioMs: number,
  duracaoMs: number,
): { inicioPct: number; larguraPct: number } {
  if (duracaoMs <= 0) return { inicioPct: 0, larguraPct: 100 };
  const limitar = (v: number) => Math.min(100, Math.max(0, v));
  const inicioPct = limitar(((p.ts_ms - inicioMs) / duracaoMs) * 100);
  const larguraPct = Math.max(0.5, Math.min(100 - inicioPct, (p.duracao_ms / duracaoMs) * 100));
  return { inicioPct, larguraPct };
}

export interface MarcaRepeticao {
  repeticao: IaRepeticao;
  /** 1 = primeira chamada da sequência. */
  n: number;
}

/**
 * Marca os passos de cada repetição: a partir de `primeiro_span_id`, as próximas
 * `vezes` chamadas daquela ferramenta (os passos de modelo entre elas são o agente
 * decidindo chamar de novo, e não quebram a sequência).
 */
export function passosRepetidos(passos: IaPasso[], repeticoes: IaRepeticao[]): Map<string, MarcaRepeticao> {
  const marcas = new Map<string, MarcaRepeticao>();
  for (const r of repeticoes) {
    const inicio = passos.findIndex((p) => p.span_id === r.primeiro_span_id);
    if (inicio < 0) continue;
    let n = 0;
    for (let i = inicio; i < passos.length && n < r.vezes; i++) {
      const p = passos[i];
      if (tipoDoPasso(p) !== "ferramenta" || (p.ferramenta || p.nome) !== r.ferramenta) continue;
      n += 1;
      marcas.set(p.span_id, { repeticao: r, n });
    }
  }
  return marcas;
}

export function fraseRepeticao(r: IaRepeticao): string {
  return `${r.ferramenta} chamada ${r.vezes} vezes seguidas: possível loop`;
}

// Termos da convenção GenAI do OpenTelemetry, ditos em português. O valor cru
// continua visível ao lado (ou no title): é o que a pessoa procura no código dela.
// Valor que não está aqui aparece cru, nunca some.
const ROTULO_OPERACAO: Record<string, string> = {
  chat: "conversa com o modelo",
  text_completion: "completar texto",
  generate_content: "gerar conteúdo",
  embeddings: "gerar embeddings",
  execute_tool: "executar ferramenta",
  invoke_agent: "acionar agente",
  create_agent: "criar agente",
  agent_step: "passo do agente",
};

const ROTULO_MOTIVO_FIM: Record<string, string> = {
  stop: "terminou a resposta",
  length: "cortado no limite de tokens",
  tool_calls: "pediu para usar ferramenta",
  "tool-calls": "pediu para usar ferramenta",
  tool_call: "pediu para usar ferramenta",
  content_filter: "bloqueado pelo filtro de conteúdo",
};

const ROTULO_PAPEL: Record<string, string> = {
  user: "pessoa",
  assistant: "modelo",
  system: "instruções do sistema",
  tool: "ferramenta",
};

/** Rótulo em português de um valor conhecido; desconhecido volta cru. */
function traduzir(tabela: Record<string, string>, cru: string): string {
  return Object.hasOwn(tabela, cru) ? tabela[cru] : cru;
}

/** Operação do passo: `rotulo` em português e `cru` só quando a tradução difere. */
export function rotuloOperacao(operacao: string): { rotulo: string; cru: string } {
  if (!operacao) return { rotulo: "não informada", cru: "" };
  const rotulo = traduzir(ROTULO_OPERACAO, operacao);
  return { rotulo, cru: rotulo === operacao ? "" : operacao };
}

/** Motivos do fim da resposta ("stop" → "terminou a resposta"), sem repetir. */
export function rotuloMotivosFim(motivos: string[]): string {
  if (motivos.length === 0) return NAO_INFORMADO;
  return [...new Set(motivos.map((m) => traduzir(ROTULO_MOTIVO_FIM, m)))].join(", ");
}

/** Papel da mensagem ("user" → "pessoa"). */
export function rotuloPapel(papel: string): string {
  return papel ? traduzir(ROTULO_PAPEL, papel) : "papel não informado";
}

/**
 * Tokens do passo em texto honesto: os dois lados nulos viram uma frase só
 * ("tokens não informados"), e não "não informado → não informado tokens".
 */
export function textoTokens(
  p: Pick<IaPasso, "tokens_entrada" | "tokens_saida" | "tokens_cache_leitura">,
  comCache = false,
): string {
  if (p.tokens_entrada == null && p.tokens_saida == null) return "tokens não informados";
  const partes = [`entrada ${fmtTokens(p.tokens_entrada)}`, `saída ${fmtTokens(p.tokens_saida)}`];
  if (comCache) partes.push(`cache ${fmtTokens(p.tokens_cache_leitura)}`);
  return partes.join(" · ");
}

export function mensagensDoPasso(mensagens: IaMensagem[], spanId: string, lado?: IaMensagem["lado"]): IaMensagem[] {
  return mensagens
    .filter((m) => m.span_id === spanId && (lado == null || m.lado === lado))
    .sort((a, b) => (a.lado === b.lado ? a.ordem - b.ordem : a.lado === "entrada" ? -1 : 1));
}

export interface RequisicaoOpenAI {
  model: string;
  messages: { role: string; content: string }[];
}

/**
 * Requisição no formato OpenAI (chat completions) com as mensagens de ENTRADA do
 * passo. O Revoada não chama o modelo (ADR 008): entrega o JSON para a pessoa rodar
 * onde quiser. Trechos redigidos vão como estão; o aviso fica por conta da tela.
 */
export function requisicaoOpenAI(passo: Pick<IaPasso, "span_id" | "modelo">, mensagens: IaMensagem[]): RequisicaoOpenAI {
  return {
    model: passo.modelo,
    messages: mensagensDoPasso(mensagens, passo.span_id, "entrada").map((m) => ({ role: m.papel, content: m.texto })),
  };
}

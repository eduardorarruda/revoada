// Veredito da conferência de conteúdo (ARQUITETURA §9.4) — lógica pura, sem React, para a
// tela de Execução. Responde à pergunta "esta migração está mesmo certa?" em uma
// linha e, por tabela, diz o que foi conferido (contagem, chaves, conteúdo coluna a
// coluna), o que ficou de fora e por quê, e onde diverge (pela chave, nunca pelo valor).
import type {
  ColunaNaoConferida,
  DivergenciaConteudo,
  ResumoExecucao,
  ResumoVerificacao,
} from "../../api";

export type Tom = "ok" | "warn" | "crit";

// Estado de uma conferência: confere, diverge, ou não foi feita (null).
export type Estado = boolean | null;

export interface LinhaVerificacao {
  origem: string;
  destino: string;
  linhas: number;
  contagem: Estado;
  chaves: Estado;
  conteudo: Estado;
  colunas: number; // colunas cujo conteúdo foi comparado
  naoConferidas: ColunaNaoConferida[];
  divergencias: DivergenciaConteudo[];
  divergentes?: number;
  ausentes?: number;
  porColuna?: Record<string, number>;
  motivo?: string;
  onde?: "tabela" | "staging";
  semChave?: boolean;
  // dupla conferência pelo texto dos bancos (só na verificação sob demanda)
  texto?: {
    estado: Estado; // true = os dois bancos imprimem o mesmo; false = divergem; null = não rodou
    colunas: number;
    parciais: ColunaNaoConferida[];
    fora: ColunaNaoConferida[];
    divergencias: DivergenciaConteudo[];
    divergentes: number;
    porColuna?: Record<string, number>;
    motivo?: string;
  };
}

export interface VereditoConteudo {
  tom: Tom;
  titulo: string;
  detalhe: string;
  // a segunda conferência, em uma linha (só na verificação sob demanda)
  texto?: { tom: Tom | "neutro"; frase: string };
  linhas: LinhaVerificacao[];
}

const n = (x: number) => x.toLocaleString("pt-BR");
const plural = (x: number, um: string, varios: string) => `${n(x)} ${x === 1 ? um : varios}`;

/** Veredito da conferência feita DENTRO da execução: o gravado × o relido do destino. */
export function vereditoDaExecucao(r: ResumoExecucao): VereditoConteudo {
  const linhas: LinhaVerificacao[] = (r.tabelas ?? []).map((t) => ({
    origem: t.origem,
    destino: t.destino,
    linhas: t.gravadas,
    contagem: t.linhas === t.gravadas,
    chaves: t.checksum_ok ? true : null,
    // resumo de agente antigo não tem conteudo_ok: não conferido, nunca "ok"
    conteudo: t.divergencias?.length ? false : t.conteudo_ok === true ? true : null,
    colunas: t.colunas_conferidas?.length ?? 0,
    naoConferidas: t.colunas_nao_conferidas ?? [],
    divergencias: t.divergencias ?? [],
    motivo: t.conteudo_motivo,
  }));
  return montar(linhas, r.linhas, "entre o que foi gravado e o que o destino devolveu ao ser relido");
}

/** Veredito da verificação sob demanda: a origem (convertida) × o destino, linha a linha. */
export function vereditoDaVerificacao(v: ResumoVerificacao): VereditoConteudo {
  const linhas: LinhaVerificacao[] = (v.tabelas ?? []).map((t) => ({
    origem: t.origem,
    destino: t.destino,
    linhas: t.linhas,
    // ausente no destino = a contagem das linhas da origem não fecha
    contagem: !(t.ausentes > 0 || t.motivo?.startsWith("a contagem")),
    // linha que não deu para localizar (chave gerada pelo destino/repetida): não conferida
    chaves: t.sem_chave || (t.nao_localizadas ?? 0) > 0 ? null : t.ausentes > 0 ? false : true,
    conteudo: t.divergentes > 0 || (t.divergencias?.length ?? 0) > 0 ? false : t.conteudo_ok ? true : null,
    colunas: t.colunas_conferidas?.length ?? 0,
    naoConferidas: t.colunas_nao_conferidas ?? [],
    divergencias: t.divergencias ?? [],
    divergentes: t.divergentes,
    ausentes: t.ausentes,
    porColuna: t.por_coluna,
    motivo: t.motivo,
    onde: t.onde,
    semChave: t.sem_chave,
    texto: t.texto && {
      estado: t.texto.divergentes > 0 || t.texto.ausentes > 0 ? false : t.texto.ok ? true : null,
      colunas: t.texto.colunas?.length ?? 0,
      parciais: t.texto.parciais ?? [],
      fora: t.texto.nao_conferidas ?? [],
      divergencias: t.texto.divergencias ?? [],
      divergentes: t.texto.divergentes + t.texto.ausentes,
      porColuna: t.texto.por_coluna,
      motivo: t.texto.motivo,
    },
  }));
  const v1 = montar(linhas, v.linhas, "entre a origem e o destino, linha a linha");
  return { ...v1, ...comTexto(v1, linhas, v.colunas_texto) };
}

/**
 * Junta a segunda conferência (o texto que cada banco imprime) ao veredito. Ela é
 * independente da primeira: pega erro de LEITURA (driver, tipo, fuso), que a primeira
 * não vê porque compara o lido-e-convertido com o gravado.
 */
function comTexto(v1: VereditoConteudo, linhas: LinhaVerificacao[], colunasTexto?: number): Partial<VereditoConteudo> {
  const rodou = linhas.filter((l) => l.texto && l.texto.estado !== null);
  const divergem = linhas.filter((l) => l.texto?.estado === false);
  if (!linhas.some((l) => l.texto)) {
    return { texto: { tom: "neutro", frase: "Dupla conferência pelo texto dos bancos: não disponível neste resultado (agente anterior)." } };
  }
  if (divergem.length > 0) {
    const nomes = divergem.map((l) => l.destino).join(", ");
    const frase = `Os próprios bancos discordam em ${plural(divergem.length, "tabela", "tabelas")}: ${nomes} — o texto que a origem e o destino imprimem não é o mesmo.`;
    if (v1.tom === "crit") return { texto: { tom: "crit", frase } };
    return {
      tom: "crit",
      titulo: `Divergência que só o texto dos bancos viu, em ${plural(divergem.length, "tabela", "tabelas")}: ${nomes}`,
      detalhe:
        "A comparação do que foi lido com o que foi gravado bateu, mas a origem e o destino imprimem valores diferentes: " +
        "sinal de erro na LEITURA da origem (driver, tipo, fuso). Veja as colunas e as chaves abaixo.",
      texto: { tom: "crit", frase },
    };
  }
  if (rodou.length === 0) {
    return { texto: { tom: "warn", frase: "Dupla conferência pelo texto dos bancos: nenhuma coluna pôde ser conferida (veja o motivo em cada tabela)." } };
  }
  const n = colunasTexto ?? rodou.reduce((s, l) => s + (l.texto?.colunas ?? 0), 0);
  return {
    texto: {
      tom: "ok",
      frase: `Conferido pelos próprios bancos: ${plural(n, "coluna", "colunas")} — o texto que a origem imprime é o mesmo que o destino imprime.`,
    },
  };
}

function montar(linhas: LinhaVerificacao[], total: number, entre: string): VereditoConteudo {
  const colunas = linhas.reduce((s, l) => s + l.colunas, 0);
  const fora = linhas.reduce((s, l) => s + l.naoConferidas.length, 0);
  const divergentes = linhas.filter((l) => l.conteudo === false || l.contagem === false || l.chaves === false);
  const semConteudo = linhas.filter((l) => l.conteudo === null);
  if (divergentes.length > 0) {
    const nomes = divergentes.map((l) => l.destino).join(", ");
    return {
      tom: "crit",
      titulo: `Conteúdo divergente em ${plural(divergentes.length, "tabela", "tabelas")}: ${nomes}`,
      detalhe: "O destino não guardou exatamente o que veio da origem. Veja abaixo as colunas e as chaves das linhas.",
      linhas,
    };
  }
  if (semConteudo.length > 0) {
    return {
      tom: "warn",
      titulo: `Conteúdo não conferido em ${plural(semConteudo.length, "tabela", "tabelas")}`,
      detalhe:
        "O conteúdo destas tabelas não foi comparado coluna a coluna — o motivo está em cada uma. " +
        "Depois da conclusão, “Verificar de novo” compara origem e destino linha a linha.",
      linhas,
    };
  }
  const titulo = `Conteúdo conferido: ${plural(total, "linha", "linhas")} × ${plural(colunas, "coluna", "colunas")} idênticas ${entre}`;
  if (fora > 0) {
    return {
      tom: "warn",
      titulo,
      detalhe: `${plural(fora, "coluna ficou", "colunas ficaram")} de fora da comparação — o motivo está em cada tabela.`,
      linhas,
    };
  }
  return { tom: "ok", titulo, detalhe: "Contagem, chaves e conteúdo de todas as colunas gravadas batem.", linhas };
}

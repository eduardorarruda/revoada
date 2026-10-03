// Amarração SERVIDOR → CHAVE de ingestão, para a tela de atualização dos agentes.
//
// # O problema que isto resolve
//
// A tela de atualização listava `/api/agents`, cujo campo `hostname` é o APELIDO que
// o operador digitou ao criar a chave — não o nome da máquina. Medido em produção: as
// 8 chaves se chamavam "Loja Exemplo", "ola", "Teste traces", "Nuvem WHM",
// "Cliente Interno"; os servidores reais são `srv-01`,
// `vps-demo.exemplo.com`, `whm-demo.exemplo.com`, `srv-02`,
// `mail.exemplo.com.br`. Interseção: nenhuma. Quem abria a tela para segurar a
// atualização de um servidor não encontrava servidor nenhum ali.
//
// A lista passa a ser de SERVIDORES (o inventário de `/api/hosts`, a mesma fonte da
// tela de Infraestrutura). Mas o freio continua gravado na CHAVE — é o único
// identificador que o painel reconhece na hora em que o agente pergunta se há versão
// nova (POST /api/agent/update-check autentica por serverkey; o hostname não entra na
// decisão). Mudar o freio para ser por hostname exigiria que o painel soubesse o
// hostname naquele momento, e ele não sabe.
//
// Logo: a tela mostra servidor e resolve servidor → chave aqui. Quando a resolução
// não é segura, a tela DESLIGA a ação e diz o porquê. Um freio aplicado na chave
// errada pararia a atualização de outro servidor — é pior que não oferecer o freio.
//
// # As duas pontes, em ordem de confiança
//
//  1. RELATADA: o agente informou o próprio hostname ao consultar a atualização
//     (`update_report.hostname`). É identidade de máquina, dita pela máquina —
//     ponte exata. O painel já guarda esse campo; falta o agente passar a enviá-lo
//     (a frota 0.7.0 não envia).
//  2. APELIDO: o nome digitado na chave bate com o hostname técnico ou com o nome
//     amigável do servidor. É palpite de operador, não identidade — vale como
//     vínculo, mas a tela precisa dizer que veio daí.
//
// Qualquer disputa (duas chaves para o mesmo servidor, um apelido que alcança dois
// servidores) vira "ambíguo": sem ação, com explicação.
import type { AgentKey, HostDetail } from "../api";

/** Como (e se) um servidor foi amarrado a uma chave de ingestão. */
export type VinculoTipo = "relatado" | "apelido" | "ambiguo" | "nenhum";

export interface Vinculo {
  tipo: VinculoTipo;
  /** A chave amarrada. Só existe em "relatado" e "apelido" — os casos em que agir é seguro. */
  chave?: AgentKey;
  /** Chaves em disputa (tipo "ambiguo"), para a tela poder nomeá-las. */
  candidatas: AgentKey[];
}

export interface Vinculos {
  /** hostname técnico do servidor → vínculo. */
  porHost: Map<string, Vinculo>;
  /**
   * Chaves que NÃO ficaram amarradas com segurança a nenhum servidor: as órfãs de
   * verdade e as que estão em disputa. Vão para uma lista à parte na tela — sumir
   * com elas tiraria do operador a única forma de segurá-las.
   */
  soltas: AgentKey[];
}

const SEM_VINCULO: Vinculo = { tipo: "nenhum", candidatas: [] };

function norm(s?: string | null): string {
  return (s ?? "").trim().toLowerCase();
}

function agrupar(chaves: AgentKey[], chaveDe: (c: AgentKey) => string): Map<string, AgentKey[]> {
  const m = new Map<string, AgentKey[]>();
  for (const c of chaves) {
    const k = chaveDe(c);
    if (!k) continue;
    const atual = m.get(k);
    if (atual) atual.push(c);
    else m.set(k, [c]);
  }
  return m;
}

// preferirAtivas: chave revogada não recebe binário nenhum (o update-check devolve 401),
// então ela nunca deve ganhar de uma chave ativa na disputa por um servidor. Só sobra
// como último recurso — e aí a tela mostra "chave revogada", que é a informação útil.
function preferirAtivas(cs: AgentKey[]): AgentKey[] {
  const ativas = cs.filter((c) => !c.revoked);
  return ativas.length > 0 ? ativas : cs;
}

function unicas(cs: AgentKey[]): AgentKey[] {
  const vistos = new Set<string>();
  return cs.filter((c) => (vistos.has(c.id) ? false : (vistos.add(c.id), true)));
}

/**
 * vincularChaves amarra cada servidor do inventário a, no máximo, uma chave.
 *
 * Duas passadas, e a ordem importa: TODO relato é resolvido antes de qualquer
 * apelido. Sem isso, um apelido genérico poderia roubar a chave de um servidor que a
 * máquina já tinha reivindicado por nome próprio.
 */
export function vincularChaves(hosts: HostDetail[], chaves: AgentKey[]): Vinculos {
  const porHost = new Map<string, Vinculo>();
  const amarradas = new Set<string>();

  const porRelato = agrupar(chaves, (c) => norm(c.update_report?.hostname));
  const porApelido = agrupar(chaves, (c) => norm(c.hostname));

  // alcance: quantos SERVIDORES um mesmo nome alcança. Um apelido que casa com dois
  // servidores (o hostname técnico de um é o nome amigável do outro) não amarra nada:
  // escolher um dos dois seria chutar em qual máquina o freio vai cair.
  const alcance = new Map<string, number>();
  for (const h of hosts) {
    for (const n of new Set([norm(h.hostname), norm(h.display_name)].filter(Boolean))) {
      alcance.set(n, (alcance.get(n) ?? 0) + 1);
    }
  }

  // Passada 1 — o que a própria máquina disse.
  for (const h of hosts) {
    const cs = preferirAtivas(porRelato.get(norm(h.hostname)) ?? []);
    if (cs.length === 1) {
      porHost.set(h.hostname, { tipo: "relatado", chave: cs[0], candidatas: cs });
      amarradas.add(cs[0].id);
    } else if (cs.length > 1) {
      // Duas chaves ativas dizendo ser a mesma máquina: reinstalação com chave nova
      // sem revogar a antiga, ou chave copiada para outro servidor. Segurar "a"
      // chave aqui teria 50% de chance de segurar a errada.
      porHost.set(h.hostname, { tipo: "ambiguo", candidatas: cs });
      for (const c of cs) amarradas.add(c.id);
    }
  }

  // Passada 2 — o nome que alguém digitou na chave.
  for (const h of hosts) {
    if (porHost.has(h.hostname)) continue;
    const nomes = [...new Set([norm(h.hostname), norm(h.display_name)].filter(Boolean))];
    // Nome que alcança dois servidores é ambíguo para OS DOIS — inclusive para o
    // segundo da lista, que já veria a chave marcada como disputada. Por isso este
    // caso é decidido antes de filtrar o que já foi amarrado: senão o segundo
    // servidor cairia em "sem chave identificada", que é uma afirmação diferente
    // (e falsa) sobre a mesma situação.
    const nomeCompartilhado = nomes.some((n) => (porApelido.get(n)?.length ?? 0) > 0 && (alcance.get(n) ?? 0) > 1);
    if (nomeCompartilhado) {
      const disputadas = preferirAtivas(unicas(nomes.flatMap((n) => porApelido.get(n) ?? [])));
      porHost.set(h.hostname, { tipo: "ambiguo", candidatas: disputadas });
      for (const c of disputadas) amarradas.add(c.id);
      continue;
    }
    const cs = preferirAtivas(
      unicas(nomes.flatMap((n) => (porApelido.get(n) ?? []).filter((c) => !amarradas.has(c.id)))),
    );
    if (cs.length === 0) {
      porHost.set(h.hostname, SEM_VINCULO);
    } else if (cs.length > 1) {
      porHost.set(h.hostname, { tipo: "ambiguo", candidatas: cs });
      for (const c of cs) amarradas.add(c.id);
    } else {
      porHost.set(h.hostname, { tipo: "apelido", chave: cs[0], candidatas: cs });
      amarradas.add(cs[0].id);
    }
  }

  // soltas: tudo que não virou vínculo SEGURO (nem "relatado" nem "apelido").
  const seguras = new Set<string>();
  for (const v of porHost.values()) if (v.chave) seguras.add(v.chave.id);
  return { porHost, soltas: chaves.filter((c) => !seguras.has(c.id)) };
}

/** vinculoDe devolve o vínculo de um servidor, ou "nenhum" — nunca undefined. */
export function vinculoDe(v: Vinculos, hostname: string): Vinculo {
  return v.porHost.get(hostname) ?? SEM_VINCULO;
}

/**
 * VINCULO_META traduz o vínculo para o operador. `podeAgir` é o que liga ou desliga
 * o botão de freio: só agimos quando dá para afirmar em QUAL máquina o freio cai.
 */
export const VINCULO_META: Record<
  VinculoTipo,
  { label: string; state: "ok" | "warn" | "neutral"; podeAgir: boolean; ajuda: string }
> = {
  relatado: {
    label: "confirmado pelo agente",
    state: "ok",
    podeAgir: true,
    ajuda:
      "O próprio agente instalado nesta máquina informou este nome ao perguntar ao painel se há versão nova. É a amarração exata: o freio cai neste servidor, e em nenhum outro.",
  },
  apelido: {
    label: "pelo nome da chave",
    state: "warn",
    podeAgir: true,
    ajuda:
      "A amarração veio do nome que alguém digitou ao criar a chave de acesso, que bate com este servidor. Serve, mas é palpite de cadastro, não confirmação da máquina. Ela vira exata quando o agente passar a informar o próprio nome ao consultar atualização.",
  },
  ambiguo: {
    label: "mais de uma chave possível",
    state: "warn",
    podeAgir: false,
    ajuda:
      "Duas ou mais chaves de acesso poderiam ser deste servidor, e o painel não tem como decidir qual. Segurar a errada pararia a atualização de outro servidor, então a ação fica desligada. Resolva revogando a chave que não é mais usada (Infraestrutura → Adicionar servidor → Chaves de acesso).",
  },
  nenhum: {
    label: "sem chave identificada",
    state: "neutral",
    podeAgir: false,
    ajuda:
      "O painel não conseguiu dizer qual chave de acesso pertence a este servidor: nenhum agente informou este nome e nenhuma chave foi batizada com ele. Sem isso não há onde gravar o freio, na hora em que o agente pergunta pela atualização, o painel só o reconhece pela chave. As chaves que sobraram estão listadas logo abaixo, em “Chaves sem servidor identificado”; se você souber qual é a deste servidor, o freio pode ser aplicado por lá.",
  },
};

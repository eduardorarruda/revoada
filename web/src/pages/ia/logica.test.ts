import { describe, expect, it } from "vitest";
import type { IaMensagem } from "../../api.ia";
import { detalhe, passo, totais } from "./fixtures";
import {
  barraDoPasso,
  fraseRepeticao,
  passosRepetidos,
  requisicaoOpenAI,
  ROTULO_TIPO,
  rotuloDoPasso,
  rotuloMotivosFim,
  rotuloOperacao,
  rotuloPapel,
  textoTokens,
  tipoDoPasso,
} from "./replay";
import { abaDaVista, hrefHostNaHora, hrefIa, hrefReplay, vistaDaRota } from "./rotas";
import { motivoParcial, vereditoIa } from "./veredito";

describe("vereditoIa", () => {
  it("título é um julgamento estável; os números vão no detalhe", () => {
    const v = vereditoIa(totais({ custo_parcial: false }), "Hoje");
    expect(v.tom).toBe("warn");
    expect(v.titulo).toBe("Erro acima do normal: 1,8% das chamadas");
    expect(v.detalhe).toContain("Hoje: US$ 12,40 em 3.214 chamadas, p95 de 4,2 s.");
  });

  it("títulos por tom: em ordem, atenção, crítico", () => {
    expect(vereditoIa(totais({ taxa_erro: 0.002 }), "x").titulo).toBe("Agentes de IA em ordem");
    expect(vereditoIa(totais({ taxa_erro: 0.08 }), "x").titulo).toBe("Muitas chamadas falhando: 8%");
    // O título não depende do custo: atualizar o número não troca (nem anima) a frase.
    expect(vereditoIa(totais({ taxa_erro: 0.002, custo_usd: 99 }), "x").titulo).toBe("Agentes de IA em ordem");
  });

  it("custo parcial aparece no detalhe, com o motivo", () => {
    const v = vereditoIa(totais(), "Última 1 h");
    expect(v.detalhe).toContain("US$ 12,40 (parcial)");
    expect(v.detalhe).toContain("3 chamadas sem preço cadastrado e 14 chamadas sem tokens informados");
  });

  it("custo e p95 nulos são ditos como não informados, nunca zero", () => {
    const v = vereditoIa(totais({ custo_usd: null, latencia_p95_ms: null }), "Hoje");
    expect(v.detalhe).toContain("custo não informado");
    expect(v.detalhe).toContain("p95 não informado");
    expect(`${v.titulo} ${v.detalhe}`).not.toMatch(/US\$ 0/);
  });

  it("plural certo para uma chamada só", () => {
    const v = vereditoIa(totais({ chamadas: 1, erros: 0, taxa_erro: 0, chamadas_sem_preco: 1, sem_tokens: 0 }), "x");
    expect(v.detalhe).toContain("em 1 chamada,");
    expect(v.detalhe).toContain("1 chamada sem preço cadastrado");
  });

  it("janela sem chamadas explica em vez de mostrar zeros", () => {
    const v = vereditoIa(totais({ chamadas: 0 }), "Última 1 h");
    expect(v.tom).toBe("vazio");
    expect(v.titulo).toBe("Última 1 h: nenhuma chamada de modelo");
  });

  it("sem custo parcial, sem motivo", () => {
    expect(motivoParcial(totais({ custo_parcial: false }))).toBe("");
  });
});

describe("replay", () => {
  it("classifica o tipo do passo e escreve a frase", () => {
    expect(tipoDoPasso(passo())).toBe("modelo");
    expect(rotuloDoPasso(passo())).toBe("Chamou o modelo gpt-4o-mini");
    const d = detalhe();
    expect(rotuloDoPasso(d.passos[0])).toBe("Agente Atendente");
    expect(rotuloDoPasso(d.passos[2])).toBe("Usou a ferramenta clima");
  });

  it("marca as chamadas repetidas mesmo com o modelo entre elas", () => {
    const d = detalhe();
    const m = passosRepetidos(d.passos, d.repeticoes);
    expect([...m.keys()]).toEqual(["b7", "b8", "b9"]);
    expect(m.get("b9")?.n).toBe(3);
  });

  it("posiciona o passo na barra da execução", () => {
    expect(barraDoPasso({ ts_ms: 2000, duracao_ms: 500 }, 1000, 5000)).toEqual({ inicioPct: 20, larguraPct: 10 });
    expect(barraDoPasso({ ts_ms: 1000, duracao_ms: 0 }, 1000, 5000).larguraPct).toBe(0.5);
  });

  it("operação em português, com o valor cru ao lado; desconhecida fica crua", () => {
    expect(rotuloOperacao("chat")).toEqual({ rotulo: "conversa com o modelo", cru: "chat" });
    expect(rotuloOperacao("text_completion").rotulo).toBe("completar texto");
    expect(rotuloOperacao("generate_content").rotulo).toBe("gerar conteúdo");
    expect(rotuloOperacao("embeddings").rotulo).toBe("gerar embeddings");
    expect(rotuloOperacao("execute_tool").rotulo).toBe("executar ferramenta");
    expect(rotuloOperacao("invoke_agent").rotulo).toBe("acionar agente");
    expect(rotuloOperacao("create_agent").rotulo).toBe("criar agente");
    expect(rotuloOperacao("agent_step").rotulo).toBe("passo do agente");
    expect(rotuloOperacao("rerank")).toEqual({ rotulo: "rerank", cru: "" });
    expect(rotuloOperacao("")).toEqual({ rotulo: "não informada", cru: "" });
    // Nome herdado de Object.prototype não é tradução.
    expect(rotuloOperacao("toString")).toEqual({ rotulo: "toString", cru: "" });
  });

  it("motivo do fim em português, sem repetir; desconhecido fica cru", () => {
    expect(rotuloMotivosFim(["stop"])).toBe("terminou a resposta");
    expect(rotuloMotivosFim(["length"])).toBe("cortado no limite de tokens");
    expect(rotuloMotivosFim(["tool_calls", "tool-calls", "tool_call"])).toBe("pediu para usar ferramenta");
    expect(rotuloMotivosFim(["content_filter"])).toBe("bloqueado pelo filtro de conteúdo");
    expect(rotuloMotivosFim(["stop", "max_tokens_custom"])).toBe("terminou a resposta, max_tokens_custom");
    expect(rotuloMotivosFim([])).toBe("não informado");
  });

  it("papel da mensagem em português; desconhecido fica cru", () => {
    expect(rotuloPapel("user")).toBe("pessoa");
    expect(rotuloPapel("assistant")).toBe("modelo");
    expect(rotuloPapel("system")).toBe("instruções do sistema");
    expect(rotuloPapel("tool")).toBe("ferramenta");
    expect(rotuloPapel("developer")).toBe("developer");
    expect(rotuloPapel("")).toBe("papel não informado");
  });

  it("tokens: os dois nulos viram uma frase; um nulo é dito como não informado", () => {
    const nulos = { tokens_entrada: null, tokens_saida: null, tokens_cache_leitura: null };
    expect(textoTokens(nulos)).toBe("tokens não informados");
    expect(textoTokens(nulos, true)).toBe("tokens não informados");
    expect(textoTokens({ tokens_entrada: 120, tokens_saida: 30, tokens_cache_leitura: 100 })).toBe("entrada 120 · saída 30");
    expect(textoTokens({ tokens_entrada: 120, tokens_saida: null, tokens_cache_leitura: null }, true)).toBe(
      "entrada 120 · saída não informado · cache não informado",
    );
    // Zero é medição: aparece como 0.
    expect(textoTokens({ tokens_entrada: 0, tokens_saida: 0, tokens_cache_leitura: 0 }, true)).toBe("entrada 0 · saída 0 · cache 0");
  });

  it("rótulos curtos de tipo e frase do loop sem travessão", () => {
    expect(ROTULO_TIPO).toEqual({ modelo: "modelo", ferramenta: "ferramenta", agente: "agente", outro: "passo" });
    const frase = fraseRepeticao({ ferramenta: "clima", vezes: 3, primeiro_span_id: "b7" });
    expect(frase).toBe("clima chamada 3 vezes seguidas: possível loop");
    expect(frase).not.toContain("—");
  });

  it("monta a requisição OpenAI só com a ENTRADA do passo, na ordem", () => {
    const msgs: IaMensagem[] = [
      { span_id: "a1", lado: "entrada", papel: "user", ordem: 1, texto: "Qual o clima?", truncado: false, redigido: false },
      { span_id: "a1", lado: "entrada", papel: "system", ordem: 0, texto: "Você é útil.", truncado: false, redigido: false },
      { span_id: "a1", lado: "saida", papel: "assistant", ordem: 0, texto: "Ensolarado", truncado: false, redigido: false },
      { span_id: "a2", lado: "entrada", papel: "user", ordem: 0, texto: "outro passo", truncado: false, redigido: false },
    ];
    expect(requisicaoOpenAI(passo({ span_id: "a1", modelo: "gpt-4o-mini" }), msgs)).toEqual({
      model: "gpt-4o-mini",
      messages: [
        { role: "system", content: "Você é útil." },
        { role: "user", content: "Qual o clima?" },
      ],
    });
  });
});

describe("rotas", () => {
  it("lê a vista pela rota, com o replay sob Execuções", () => {
    expect(vistaDaRota("/ia")).toEqual({ vista: "visao" });
    expect(vistaDaRota("/ia/precos")).toEqual({ vista: "precos" });
    const r = vistaDaRota("/ia/execucoes/ab%2Fcd");
    expect(r).toEqual({ vista: "replay", traceId: "ab/cd" });
    expect(abaDaVista(r)).toBe("execucoes");
  });

  it("links levam a janela, exceto a padrão", () => {
    expect(hrefIa("/ia/execucoes", { janela: "24h", agente: "Atendente" })).toBe("#/ia/execucoes?janela=24h&agente=Atendente");
    expect(hrefIa("/ia", { janela: "1h" })).toBe("#/ia");
    expect(hrefReplay("ab/cd", "6h")).toBe("#/ia/execucoes/ab%2Fcd?janela=6h");
  });

  it("host na hora escolhe a menor janela que ainda contém a execução", () => {
    const agora = 100 * 3600_000;
    expect(hrefHostNaHora("web-01", agora - 30 * 60_000, agora)).toBe("#/hosts/web-01?janela=1h");
    expect(hrefHostNaHora("web-01", agora - 3 * 3600_000, agora)).toBe("#/hosts/web-01?janela=6h");
    expect(hrefHostNaHora("web-01", agora - 90 * 86400_000, agora)).toBe("#/hosts/web-01?janela=30d");
  });
});

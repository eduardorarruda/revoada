import { describe, expect, it } from "vitest";
import type { IaMensagem } from "../../api.ia";
import { detalhe, passo, totais } from "./fixtures";
import { barraDoPasso, passosRepetidos, requisicaoOpenAI, rotuloDoPasso, tipoDoPasso } from "./replay";
import { abaDaVista, hrefHostNaHora, hrefIa, hrefReplay, vistaDaRota } from "./rotas";
import { motivoParcial, vereditoIa } from "./veredito";

describe("vereditoIa", () => {
  it("resume a janela numa frase, como na Início", () => {
    const v = vereditoIa(totais({ custo_parcial: false }), "Hoje");
    expect(v.titulo).toBe("Hoje: US$ 12,40 em 3.214 chamadas, 1,8% com erro, p95 de 4,2 s");
    expect(v.tom).toBe("warn");
  });

  it("custo parcial aparece no título e o motivo no detalhe", () => {
    const v = vereditoIa(totais(), "Última 1 h");
    expect(v.titulo).toContain("US$ 12,40 (parcial)");
    expect(v.detalhe).toContain("3 chamadas sem preço cadastrado e 14 chamadas sem tokens informados");
  });

  it("custo e p95 nulos são ditos como não informados, nunca zero", () => {
    const v = vereditoIa(totais({ custo_usd: null, latencia_p95_ms: null }), "Hoje");
    expect(v.titulo).toContain("custo não informado");
    expect(v.titulo).toContain("p95 não informado");
    expect(v.titulo).not.toMatch(/US\$ 0/);
  });

  it("tom pela taxa de erro: ok, atenção, crítico", () => {
    expect(vereditoIa(totais({ taxa_erro: 0.002 }), "x").tom).toBe("ok");
    expect(vereditoIa(totais({ taxa_erro: 0.08 }), "x").tom).toBe("crit");
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

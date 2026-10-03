import { describe, expect, it } from "vitest";
import type { ResumoExecucao, ResumoTabelaExecucao, ResumoVerificacao, ResumoVerificacaoTabela } from "../../api";
import { vereditoDaExecucao, vereditoDaVerificacao } from "./verificacao";

const tabela = (t: Partial<ResumoTabelaExecucao>): ResumoTabelaExecucao => ({
  origem: "CLIENTES",
  destino: "clientes",
  linhas: 10,
  gravadas: 10,
  checksum_ok: true,
  conteudo_ok: true,
  colunas_conferidas: ["id", "nome"],
  duracao_ms: 1,
  ...t,
});

const execucao = (tabelas: ResumoTabelaExecucao[]): ResumoExecucao => ({
  execucao: "ex_1",
  tabelas,
  linhas: tabelas.reduce((s, t) => s + t.gravadas, 0),
  manifesto: { fase: "concluido", itens: [] },
  duracao_ms: 1,
});

describe("veredito da execução", () => {
  it("tudo conferido: linhas × colunas idênticas", () => {
    const v = vereditoDaExecucao(execucao([tabela({}), tabela({ destino: "pedidos", gravadas: 2, linhas: 2, colunas_conferidas: ["id"] })]));
    expect(v.tom).toBe("ok");
    expect(v.titulo).toBe(
      "Conteúdo conferido: 12 linhas × 3 colunas idênticas entre o que foi gravado e o que o destino devolveu ao ser relido",
    );
    expect(v.linhas[0]).toMatchObject({ contagem: true, chaves: true, conteudo: true, colunas: 2 });
  });

  it("coluna de fora vira atenção, com o motivo na tabela", () => {
    const v = vereditoDaExecucao(
      execucao([tabela({ colunas_nao_conferidas: [{ coluna: "criado_em", motivo: "nasceu no destino (DEFAULT)" }] })]),
    );
    expect(v.tom).toBe("warn");
    expect(v.detalhe).toContain("1 coluna ficou de fora");
    expect(v.linhas[0].naoConferidas[0].coluna).toBe("criado_em");
  });

  it("divergência é crítica e aponta a tabela", () => {
    const v = vereditoDaExecucao(
      execucao([tabela({ conteudo_ok: false, divergencias: [{ chave: "1", colunas: ["nome"] }] }), tabela({ destino: "pedidos" })]),
    );
    expect(v.tom).toBe("crit");
    expect(v.titulo).toContain("1 tabela: clientes");
    expect(v.linhas[0].conteudo).toBe(false);
  });

  it("resumo de agente antigo (sem conteudo_ok) nunca sai como conferido", () => {
    const antigo = tabela({});
    delete antigo.conteudo_ok;
    delete antigo.colunas_conferidas;
    const v = vereditoDaExecucao(execucao([antigo]));
    expect(v.tom).toBe("warn");
    expect(v.titulo).toBe("Conteúdo não conferido em 1 tabela");
    expect(v.linhas[0].conteudo).toBeNull();
  });
});

const vt = (t: Partial<ResumoVerificacaoTabela>): ResumoVerificacaoTabela => ({
  origem: "CLIENTES",
  destino: "clientes",
  onde: "tabela",
  linhas: 5,
  identicas: 5,
  divergentes: 0,
  ausentes: 0,
  colunas_conferidas: ["id", "nome", "email"],
  conteudo_ok: true,
  duracao_ms: 1,
  ...t,
});

const verif = (tabelas: ResumoVerificacaoTabela[]): ResumoVerificacao => ({
  execucao: "ex_1",
  fase: "concluido",
  tabelas,
  linhas: tabelas.reduce((s, t) => s + t.linhas, 0),
  identicas: tabelas.reduce((s, t) => s + t.identicas, 0),
  divergentes: tabelas.reduce((s, t) => s + t.divergentes, 0),
  ausentes: tabelas.reduce((s, t) => s + t.ausentes, 0),
  conteudo_ok: tabelas.every((t) => t.conteudo_ok),
  verificado_em: "2026-10-02T12:00:00Z",
  duracao_ms: 1,
});

describe("veredito da verificação sob demanda", () => {
  it("origem e destino idênticos", () => {
    const v = vereditoDaVerificacao(verif([vt({})]));
    expect(v.tom).toBe("ok");
    expect(v.titulo).toBe("Conteúdo conferido: 5 linhas × 3 colunas idênticas entre a origem e o destino, linha a linha");
  });

  it("linha ausente reprova contagem e chaves", () => {
    const v = vereditoDaVerificacao(
      verif([vt({ identicas: 4, ausentes: 1, conteudo_ok: false, divergencias: [{ chave: "9", ausente: true }] })]),
    );
    expect(v.tom).toBe("crit");
    expect(v.linhas[0]).toMatchObject({ contagem: false, chaves: false });
  });

  it("divergência de conteúdo traz a contagem por coluna", () => {
    const v = vereditoDaVerificacao(
      verif([vt({ identicas: 4, divergentes: 1, por_coluna: { email: 1 }, conteudo_ok: false, divergencias: [{ chave: "3", colunas: ["email"] }] })]),
    );
    expect(v.tom).toBe("crit");
    expect(v.linhas[0]).toMatchObject({ contagem: true, chaves: true, conteudo: false, porColuna: { email: 1 } });
  });

  it("linhas não localizadas (chave gerada pelo destino) não deixam as chaves como conferidas", () => {
    const v = vereditoDaVerificacao(
      verif([vt({ identicas: 3, nao_localizadas: 2, conteudo_ok: false, motivo: "2 linhas têm a chave gerada pelo destino" })]),
    );
    expect(v.tom).toBe("warn");
    expect(v.linhas[0]).toMatchObject({ chaves: null, conteudo: null });
  });

  it("tabela sem chave e com dados antigos: não conferida, com motivo", () => {
    const v = vereditoDaVerificacao(
      verif([vt({ sem_chave: true, identicas: 0, colunas_conferidas: [], conteudo_ok: false, motivo: "a tabela não tem chave e já tinha linhas" })]),
    );
    expect(v.tom).toBe("warn");
    expect(v.linhas[0]).toMatchObject({ chaves: null, conteudo: null });
  });

  it("divergência que só o texto dos bancos viu (erro de leitura) é crítica", () => {
    const v = vereditoDaVerificacao({
      ...verif([
        vt({
          texto: { ok: false, linhas: 5, identicas: 4, divergentes: 1, ausentes: 0, colunas: ["id", "dia"], por_coluna: { dia: 1 },
            divergencias: [{ chave: "1", colunas: ["dia"] }] },
        }),
      ]),
      texto_ok: false,
      colunas_texto: 2,
    });
    expect(v.tom).toBe("crit");
    expect(v.titulo).toContain("só o texto dos bancos viu");
    expect(v.texto).toMatchObject({ tom: "crit" });
    expect(v.linhas[0].conteudo).toBe(true);
    expect(v.linhas[0].texto).toMatchObject({ estado: false, divergentes: 1, porColuna: { dia: 1 } });
  });

  it("os dois batendo: conferido também pelos próprios bancos", () => {
    const v = vereditoDaVerificacao({
      ...verif([vt({ texto: { ok: true, linhas: 5, identicas: 5, divergentes: 0, ausentes: 0, colunas: ["id", "nome"],
        nao_conferidas: [{ coluna: "email", motivo: "passa pela transformação" }] } })]),
      texto_ok: true,
      colunas_texto: 2,
    });
    expect(v.tom).toBe("ok");
    expect(v.texto).toEqual({ tom: "ok", frase: expect.stringContaining("Conferido pelos próprios bancos: 2 colunas") });
    expect(v.linhas[0].texto?.fora[0].coluna).toBe("email");
  });

  it("resultado de agente anterior: a dupla conferência aparece como indisponível", () => {
    const v = vereditoDaVerificacao(verif([vt({})]));
    expect(v.tom).toBe("ok");
    expect(v.texto?.tom).toBe("neutro");
  });
});

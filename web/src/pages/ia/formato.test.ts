import { describe, expect, it } from "vitest";
import {
  fmtCustoPasso,
  fmtInteiro,
  fmtMs,
  fmtOrigemPreco,
  fmtPct,
  fmtReferencia,
  fmtTokens,
  fmtUsd,
  NAO_INFORMADO,
  SEM_PRECO,
} from "./formato";

describe("fmtUsd", () => {
  it("dólar com vírgula decimal e duas casas", () => {
    expect(fmtUsd(12.4)).toBe("US$ 12,40");
    expect(fmtUsd(1234.5)).toBe("US$ 1.234,50");
  });

  it("centavos de centavo não viram zero", () => {
    expect(fmtUsd(0.0021)).toBe("US$ 0,0021");
    expect(fmtUsd(0.00003)).toBe("US$ 0,00003");
  });

  it("zero medido continua zero", () => {
    expect(fmtUsd(0)).toBe("US$ 0,00");
  });

  it("null nunca vira zero", () => {
    expect(fmtUsd(null)).toBe(NAO_INFORMADO);
    expect(fmtUsd(null, SEM_PRECO)).toBe("sem preço");
  });
});

describe("fmtTokens", () => {
  it("escala compacta em pt-BR", () => {
    expect(fmtTokens(1_840_000)).toBe("1,8 mi");
    expect(fmtTokens(220_000)).toBe("220 mil");
    expect(fmtTokens(4_200)).toBe("4.200");
    expect(fmtTokens(0)).toBe("0");
  });

  it("null é não informado", () => {
    expect(fmtTokens(null)).toBe(NAO_INFORMADO);
  });
});

describe("fmtMs", () => {
  it("milissegundos na maior unidade legível", () => {
    expect(fmtMs(4200)).toBe("4,2 s");
    expect(fmtMs(820)).toBe("820 ms");
    expect(fmtMs(90_000)).toBe("1,5 min");
  });

  it("null é não informado", () => {
    expect(fmtMs(null)).toBe(NAO_INFORMADO);
  });
});

describe("fmtPct e fmtInteiro", () => {
  it("fração vira porcentagem com uma casa", () => {
    expect(fmtPct(0.018)).toBe("1,8%");
    expect(fmtPct(0)).toBe("0%");
    expect(fmtPct(null)).toBe(NAO_INFORMADO);
  });

  it("inteiro com separador de milhar", () => {
    expect(fmtInteiro(3214)).toBe("3.214");
  });
});

describe("fmtCustoPasso", () => {
  it("diz de onde o custo veio, com a data do preço", () => {
    expect(fmtCustoPasso({ custo_usd: 0.00003, custo_origem: "estimado", preco_data: "2025-10-01" })).toEqual({
      valor: "US$ 0,00003",
      origem: "estimado com preço de 01/10/2025",
    });
    expect(fmtCustoPasso({ custo_usd: 0.5, custo_origem: "informado", preco_data: "" }).origem).toBe(
      "informado pela biblioteca",
    );
  });

  it("sem preço e sem tokens não inventam valor", () => {
    expect(fmtCustoPasso({ custo_usd: null, custo_origem: "sem_preco", preco_data: "" })).toEqual({
      valor: "sem preço",
      origem: "o modelo não tem preço cadastrado",
    });
    expect(fmtCustoPasso({ custo_usd: null, custo_origem: "sem_tokens", preco_data: "" }).valor).toBe("sem tokens");
  });
});

describe("origem do preço", () => {
  it("referência vira texto de gente; vazio é 'não informada'", () => {
    expect(fmtReferencia("2025-10")).toBe("out/2025");
    expect(fmtReferencia("v3")).toBe("v3");
    expect(fmtOrigemPreco("referencia-2025-10")).toBe("tabela de referência (out/2025)");
    expect(fmtOrigemPreco("")).toBe("não informada");
    expect(fmtOrigemPreco("manual")).toBe("manual");
  });
});

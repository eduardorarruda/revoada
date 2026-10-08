import { describe, expect, it } from "vitest";
import { chaveDaTela } from "./chaveTela";

describe("chaveDaTela", () => {
  it("as abas de Agentes de IA compartilham a chave: trocar de aba não remonta o cabeçalho", () => {
    const chaves = ["/ia", "/ia/execucoes", "/ia/execucoes/abc123", "/ia/ferramentas", "/ia/precos"].map(chaveDaTela);
    expect(new Set(chaves)).toEqual(new Set(["/ia"]));
  });

  it("as demais telas continuam com a chave pelo caminho", () => {
    expect(chaveDaTela("/hosts")).toBe("/hosts");
    expect(chaveDaTela("/hosts/web-01")).toBe("/hosts/web-01");
    // Só o prefixo exato conta: /iaxyz é outra tela.
    expect(chaveDaTela("/iaxyz")).toBe("/iaxyz");
  });
});

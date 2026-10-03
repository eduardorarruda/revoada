import { describe, it, expect } from "vitest";
import { keyMatch } from "./instalar/comum";

// knownIds já vem normalizado (trim + lowercase) na tela — reproduzimos aqui.
const known = new Set(["srv-03", "mail.exemplo.com.br", "servidor principal", "loja exemplo"]);

describe("keyMatch", () => {
  it("confere pelo hostname técnico", () => {
    expect(keyMatch("srv-03", known)).toBe("confere");
    expect(keyMatch("mail.exemplo.com.br", known)).toBe("confere");
  });

  it("confere pelo nome de exibição (tolerando maiúsculas/espaços)", () => {
    expect(keyMatch("Servidor Principal", known)).toBe("confere");
    expect(keyMatch("  Loja Exemplo ", known)).toBe("confere");
  });

  it("divergente quando não corresponde a nenhum servidor", () => {
    expect(keyMatch("Nuvem WHM", known)).toBe("divergente");
    expect(keyMatch("srv-inexistente", known)).toBe("divergente");
  });

  it("sem-id quando a identificação está vazia/em branco", () => {
    expect(keyMatch("", known)).toBe("sem-id");
    expect(keyMatch("   ", known)).toBe("sem-id");
  });
});

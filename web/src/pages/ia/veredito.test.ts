import { describe, expect, it } from "vitest";
import { plural } from "./veredito";

describe("plural", () => {
  it("singular só para um; milhar com ponto", () => {
    expect(plural(1, "chamada", "chamadas")).toBe("1 chamada");
    expect(plural(0, "chamada", "chamadas")).toBe("0 chamadas");
    expect(plural(2, "execução", "execuções")).toBe("2 execuções");
    expect(plural(3214, "chamada", "chamadas")).toBe("3.214 chamadas");
  });
});

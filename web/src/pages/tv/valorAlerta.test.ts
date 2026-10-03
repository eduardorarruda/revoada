import { valorDoAlerta } from "./valorAlerta";

// A TV é lida a metros de distância: "valor atual 761,7" não diz nada. O valor
// precisa sair na língua da métrica.
describe("valor do alerta na TV", () => {
  it("regra de ausência vira tempo sem sinal", () => {
    expect(valorDoAlerta("revoada.host.heartbeat", 761.7)).toBe("13 min sem sinal");
    expect(valorDoAlerta("revoada.host.heartbeat", 7300)).toBe("2 h sem sinal");
  });

  it("container parado não mostra número", () => {
    expect(valorDoAlerta("container.running", 0)).toBe("container parado");
  });

  it("métrica conhecida sai com a unidade do dicionário", () => {
    expect(valorDoAlerta("system.cpu.utilization", 94.26)).toBe("94,3%");
  });

  it("sem métrica, o número ao menos sai no formato brasileiro", () => {
    expect(valorDoAlerta(undefined, 92.54718)).toBe("92,55");
  });
});

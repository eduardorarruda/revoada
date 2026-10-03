import { containerFilters } from "./Alerts";

// A regra "Container caído" com escopo "todos" precisa gravar filtro VAZIO. Enumerar
// os containers do dia produz o mesmo alerta hoje e para de pegar os novos amanhã —
// foi assim que uma regra em produção acumulou 240 nomes fixos e obrigava a reabrir o
// cadastro a cada container criado.

describe("escopo de containers na regra de container caído", () => {
  it("'todos' não grava filtro nenhum, para acompanhar containers novos", () => {
    expect(containerFilters("todos", [])).toEqual({});
  });

  it("'todos' ignora seleção remanescente da troca de modo", () => {
    // O usuário marcou alguns, depois voltou para "todos": a marcação não pode
    // vazar para o filtro e congelar o escopo sem ele perceber.
    expect(containerFilters("todos", ["web", "worker"])).toEqual({});
  });

  it("'escolhidos' grava a lista separada por vírgula", () => {
    expect(containerFilters("escolhidos", ["web", "worker"])).toEqual({ container: "web,worker" });
  });

  it("'escolhidos' sem nada marcado vira 'todos' em vez de regra que não vigia nada", () => {
    expect(containerFilters("escolhidos", [])).toEqual({});
  });

  it("descarta entradas vazias e espaços", () => {
    expect(containerFilters("escolhidos", [" web ", "", "  ", "db"])).toEqual({ container: "web,db" });
  });
});

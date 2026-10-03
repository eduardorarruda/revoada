import { canaisExistentes } from "./Alerts";

// A regra "Servidor > 90" mostrava "1 canal(is)" com NENHUMA caixa marcada no
// formulário. O ID guardado era de um canal apagado: aparecia na contagem, não tinha
// caixa para desmarcar, e grudava em toda edição seguinte — a pessoa desmarcava tudo
// e a regra continuava dizendo que tinha um canal.

const canais = [{ id: 2 }, { id: 7 }, { id: 9 }];

describe("canais de uma regra", () => {
  it("não conta canal que foi apagado", () => {
    // [4, 2, 7] com o canal 4 já apagado: a regra tem DOIS canais, não três.
    expect(canaisExistentes([4, 2, 7], canais)).toEqual([2, 7]);
  });

  it("regra que só apontava para canal apagado não tem canal nenhum", () => {
    // Era este o caso do print: "1 canal(is)" e nada marcado.
    expect(canaisExistentes([4], canais)).toEqual([]);
  });

  it("preserva a ordem e os canais válidos", () => {
    expect(canaisExistentes([9, 2], canais)).toEqual([9, 2]);
  });

  it("lista vazia continua vazia (= todos os canais ativos)", () => {
    expect(canaisExistentes([], canais)).toEqual([]);
    expect(canaisExistentes(null, canais)).toEqual([]);
    expect(canaisExistentes(undefined, canais)).toEqual([]);
  });

  it("sem a lista de canais carregada, devolve o que veio", () => {
    // Enquanto a chamada não voltou não dá para saber quem é fantasma; sumir com
    // tudo faria a coluna piscar "todos" e assustar quem está olhando.
    expect(canaisExistentes([4, 2], [])).toEqual([4, 2]);
  });
});

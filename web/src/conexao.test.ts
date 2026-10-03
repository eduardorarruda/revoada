// O selo de conexão só vale se souber dizer "não". Estes testes prendem as três
// regras que fazem dele um indicador e não um enfeite: falha de rede derruba,
// 5xx derruba, e resposta do servidor que NÃO seja 5xx (401, 403, 404, 422)
// mantém "no ar" — porque servidor que responde "não" é servidor vivo.
import { describe, it, expect, beforeEach } from "vitest";
import {
  estadoConexao,
  idadeUltimoSucesso,
  marcarFalha,
  marcarSucesso,
  ouvirConexao,
  resetarConexaoParaTeste,
} from "./conexao";

describe("estado de conexão", () => {
  beforeEach(() => resetarConexaoParaTeste());

  it("começa em 'no ar' e sem idade — nada foi tentado ainda", () => {
    expect(estadoConexao()).toBe("no-ar");
    expect(idadeUltimoSucesso()).toBeNull();
  });

  it("falha de rede (sem resposta HTTP) derruba para 'reconectando'", () => {
    marcarFalha();
    expect(estadoConexao()).toBe("reconectando");
  });

  it("5xx derruba: o servidor está lá, mas não está servindo", () => {
    marcarFalha(503);
    expect(estadoConexao()).toBe("reconectando");
  });

  it.each([400, 401, 403, 404, 409, 422, 499])(
    "%d NÃO derruba — é o servidor respondendo, e a conexão está inteira",
    (status) => {
      marcarFalha(status);
      expect(estadoConexao()).toBe("no-ar");
    },
  );

  it("uma resposta boa depois da queda devolve para 'no ar'", () => {
    marcarFalha();
    expect(estadoConexao()).toBe("reconectando");
    marcarSucesso();
    expect(estadoConexao()).toBe("no-ar");
  });

  it("a idade conta do último sucesso, em segundos", () => {
    marcarSucesso();
    const agora = Date.now();
    expect(idadeUltimoSucesso(agora)).toBe(0);
    expect(idadeUltimoSucesso(agora + 45_000)).toBe(45);
  });

  it("avisa os ouvintes só quando o estado MUDA (nada de re-render à toa)", () => {
    const vistos: string[] = [];
    const parar = ouvirConexao((e) => vistos.push(e));
    marcarSucesso(); // já estava "no-ar" → não avisa
    marcarFalha(); // muda → avisa
    marcarFalha(500); // continua caído → não avisa de novo
    marcarSucesso(); // volta → avisa
    parar();
    marcarFalha(); // já desinscrito → não chega nada
    expect(vistos).toEqual(["reconectando", "no-ar"]);
  });
});

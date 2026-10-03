import { comoRodar } from "./instalar/comum";
import { AGENT_OS_OPTIONS, filenameFromDisposition } from "../api";

// O instalador só cumpre a promessa ("baixe e rode") se a tela disser a coisa
// certa para cada sistema: no Windows não existe sudo, e no Linux não existe
// "Executar como administrador". Errar aqui é entregar uma instrução impossível.

describe("instrução de como rodar o instalador", () => {
  it("Linux e macOS usam sudo com o nome real do arquivo", () => {
    expect(comoRodar("linux", "instalar-revoada-app-01.sh")).toContain(
      "sudo sh instalar-revoada-app-01.sh",
    );
    expect(comoRodar("macos", "instalar-revoada-app-01.command")).toContain(
      "sudo sh instalar-revoada-app-01.command",
    );
  });

  it("Windows manda executar como administrador, sem sudo", () => {
    const t = comoRodar("windows", "instalar-revoada-app-01.exe");
    expect(t).toContain("Executar como administrador");
    expect(t).not.toContain("sudo");
  });

  it("cobre todos os sistemas oferecidos no seletor", () => {
    // Se um sistema novo entrar na lista sem instrução própria, ele cairia no
    // texto do Linux sem ninguém perceber.
    for (const o of AGENT_OS_OPTIONS) {
      expect(comoRodar(o.id, `arquivo${o.ext}`)).toContain(`arquivo${o.ext}`);
    }
  });
});

describe("nome do arquivo baixado", () => {
  it("sai do cabeçalho da resposta — é o backend que decide a extensão", () => {
    expect(filenameFromDisposition('attachment; filename="instalar-revoada-app-01.exe"')).toBe(
      "instalar-revoada-app-01.exe",
    );
  });

  it("tem um nome de reserva quando o cabeçalho falta", () => {
    expect(filenameFromDisposition(null)).toBe("instalar-revoada");
    expect(filenameFromDisposition("attachment")).toBe("instalar-revoada");
  });
});

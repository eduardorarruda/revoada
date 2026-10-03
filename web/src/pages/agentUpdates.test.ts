import { describe, expect, it } from "vitest";
import { ESTADO_RELATO, metaDoEstado, motivoObrigatorio } from "./AgentUpdates";

// Regra do formulário: parar ou prender a frota exige explicação. Quem chega às 3h
// da manhã e encontra a frota sem atualizar não tem como adivinhar se aquilo é um
// incidente em andamento ou um freio que alguém esqueceu de soltar em março.
describe("motivo obrigatório na política da frota", () => {
  it("desligar exige motivo", () => {
    expect(motivoObrigatorio(true, "")).toBe(true);
  });

  it("fixar uma versão exige motivo", () => {
    expect(motivoObrigatorio(false, "0.9.1")).toBe(true);
  });

  it("voltar ao normal (ligado, sem versão fixada) não exige nada", () => {
    // Não há o que explicar depois: o comportamento padrão é o que se espera.
    expect(motivoObrigatorio(false, "")).toBe(false);
    expect(motivoObrigatorio(false, "   ")).toBe(false);
  });
});

describe("tradução do relato do agente", () => {
  it("sem_promotor vira um rótulo que diz o que fazer, não 'atrasado'", () => {
    // Este host não tem quem promova o binário novo (cron/launchd/systemd < 231):
    // ele NUNCA se atualiza sozinho. Sem rótulo próprio, ele aparece só como uma
    // versão atrasada e o operador espera para sempre por uma troca que, por
    // construção, não vai acontecer.
    const m = metaDoEstado("sem_promotor");
    expect(m.state).toBe("warn");
    expect(m.ajuda).toMatch(/instalador/i);
    expect(m.label).not.toMatch(/atrasad/i);
  });

  it("falha de troca é crítica, não um aviso qualquer", () => {
    expect(metaDoEstado("promocao_nao_ocorreu").state).toBe("crit");
    expect(metaDoEstado("erro_download").state).toBe("crit");
  });

  it("estar em dia é o único estado 'ok'", () => {
    expect(metaDoEstado("em_dia").state).toBe("ok");
    const oks = Object.values(ESTADO_RELATO).filter((m) => m.state === "ok");
    expect(oks).toHaveLength(1);
  });

  it("estado novo no agente aparece cru em vez de sumir da tela", () => {
    const m = metaDoEstado("estado_que_ainda_nao_existe");
    expect(m.label).toBe("estado_que_ainda_nao_existe");
    expect(m.state).toBe("neutral");
  });

  it("todo estado que o agente sabe emitir tem tradução", () => {
    // Espelha agent/internal/selfupdate/selfupdate.go: se um estado novo entrar lá
    // sem entrar aqui, este teste avisa antes de o operador ver a palavra crua.
    const doAgente = [
      "em_dia",
      "erro",
      "erro_consulta",
      "erro_download",
      "estagiado",
      "estagiado_reiniciando",
      "promocao_nao_ocorreu",
      "recusado_downgrade",
      "sem_promotor",
    ];
    for (const e of doAgente) expect(ESTADO_RELATO[e], `estado sem tradução: ${e}`).toBeDefined();
  });
});

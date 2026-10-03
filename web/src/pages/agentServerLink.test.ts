import { describe, expect, it } from "vitest";
import { VINCULO_META, vincularChaves, vinculoDe } from "./agentServerLink";
import type { AgentKey, HostDetail } from "../api";

function host(hostname: string, display_name = ""): HostDetail {
  return {
    hostname,
    display_name,
    os: "linux",
    kernel: "",
    arch: "amd64",
    cpu_model: "",
    cpu_cores: 2,
    ips: "",
    agent_version: "0.7.0",
    uptime_secs: 0,
    last_seen: "2026-08-10T10:00:00Z",
    up: true,
  };
}

function chave(id: string, apelido: string, opts: { relatado?: string; revoked?: boolean } = {}): AgentKey {
  return {
    id,
    serverkey: "abcd…01",
    tenant_id: "default",
    hostname: apelido,
    revoked: opts.revoked ?? false,
    last_seen: null,
    created_at: "2026-08-01T10:00:00Z",
    update_report: opts.relatado ? { versao_atual: "0.9.0", os: "linux", arch: "amd64", hostname: opts.relatado } : undefined,
  };
}

describe("amarração servidor → chave", () => {
  it("o hostname RELATADO pelo agente vence o apelido da chave", () => {
    // O apelido "Loja Exemplo" foi digitado por gente e casa com o nome amigável do
    // OUTRO servidor; o relato é a máquina falando de si. Se o apelido ganhasse, o
    // freio cairia no servidor errado.
    const hosts = [host("srv-01", "Loja Exemplo"), host("srv-02")];
    const chaves = [chave("k1", "Loja Exemplo"), chave("k2", "qualquer coisa", { relatado: "srv-01" })];
    const v = vincularChaves(hosts, chaves);
    const srv = vinculoDe(v, "srv-01");
    expect(srv.tipo).toBe("relatado");
    expect(srv.chave?.id).toBe("k2");
  });

  it("sem relato, o apelido da chave amarra — e a tela sabe que veio daí", () => {
    const v = vincularChaves([host("mail.exemplo.com.br")], [chave("k1", "  MAIL.EXEMPLO.COM.BR ")]);
    const l = vinculoDe(v, "mail.exemplo.com.br");
    expect(l.tipo).toBe("apelido");
    expect(l.chave?.id).toBe("k1");
    expect(VINCULO_META.apelido.podeAgir).toBe(true);
  });

  it("o apelido também casa pelo nome amigável do servidor", () => {
    const v = vincularChaves([host("srv-01", "Loja Exemplo")], [chave("k1", "Loja Exemplo")]);
    expect(vinculoDe(v, "srv-01").tipo).toBe("apelido");
  });

  it("servidor sem nenhuma chave correspondente não inventa vínculo", () => {
    // O caso de produção: 8 chaves ("ola", "Teste traces", "Nuvem WHM"…) e 5
    // servidores reais, sem interseção nenhuma.
    const v = vincularChaves([host("srv-01")], [chave("k1", "Nuvem WHM"), chave("k2", "ola")]);
    const l = vinculoDe(v, "srv-01");
    expect(l.tipo).toBe("nenhum");
    expect(l.chave).toBeUndefined();
    expect(VINCULO_META.nenhum.podeAgir).toBe(false);
  });

  it("duas chaves reivindicando a mesma máquina viram ambíguo, sem ação", () => {
    // Reinstalação com chave nova sem revogar a antiga. Escolher uma seria 50% de
    // chance de segurar a atualização do servidor errado.
    const v = vincularChaves(
      [host("srv-01")],
      [chave("k1", "a", { relatado: "srv-01" }), chave("k2", "b", { relatado: "srv-01" })],
    );
    const l = vinculoDe(v, "srv-01");
    expect(l.tipo).toBe("ambiguo");
    expect(l.chave).toBeUndefined();
    expect(l.candidatas.map((c) => c.id).sort()).toEqual(["k1", "k2"]);
    expect(VINCULO_META.ambiguo.podeAgir).toBe(false);
  });

  it("apelido que alcança dois servidores não amarra nenhum", () => {
    // "banco" é o hostname técnico de um servidor E o nome amigável de outro.
    const hosts = [host("banco"), host("srv-02", "banco")];
    const v = vincularChaves(hosts, [chave("k1", "banco")]);
    expect(vinculoDe(v, "banco").tipo).toBe("ambiguo");
    expect(vinculoDe(v, "srv-02").tipo).toBe("ambiguo");
  });

  it("chave ativa ganha da revogada na disputa pelo mesmo servidor", () => {
    // Chave revogada não recebe binário nenhum (o update-check devolve 401): amarrar
    // nela seria oferecer um freio que não freia nada.
    const v = vincularChaves(
      [host("srv-01")],
      [chave("k1", "srv-01", { revoked: true }), chave("k2", "srv-01")],
    );
    const l = vinculoDe(v, "srv-01");
    expect(l.tipo).toBe("apelido");
    expect(l.chave?.id).toBe("k2");
  });

  it("uma chave não amarra dois servidores", () => {
    // O relato do agente é exato; o apelido igual do outro servidor não pode roubar
    // a mesma chave, senão dois freios diferentes gravariam no mesmo lugar.
    const hosts = [host("srv-01"), host("outro", "k-apelido")];
    const chaves = [chave("k1", "k-apelido", { relatado: "srv-01" })];
    const v = vincularChaves(hosts, chaves);
    expect(vinculoDe(v, "srv-01").chave?.id).toBe("k1");
    expect(vinculoDe(v, "outro").tipo).toBe("nenhum");
  });

  it("chaves sem vínculo seguro ficam em 'soltas' para o operador ainda alcançá-las", () => {
    // Sumir com elas tiraria a única forma de segurar a atualização daqueles hosts —
    // que é exatamente o que a tela existia para oferecer.
    const v = vincularChaves(
      [host("srv-01")],
      [chave("k1", "srv-01"), chave("k2", "Nuvem WHM"), chave("k3", "ola")],
    );
    expect(v.soltas.map((c) => c.id).sort()).toEqual(["k2", "k3"]);
  });

  it("hostname relatado vazio não casa com nada (nem com apelido vazio)", () => {
    // A frota 0.7.0 não informa hostname. Vazio é "não sei" — nunca um coringa que
    // amarra qualquer servidor.
    const v = vincularChaves([host("srv-01")], [chave("k1", "", { relatado: "" })]);
    expect(vinculoDe(v, "srv-01").tipo).toBe("nenhum");
    expect(v.soltas).toHaveLength(1);
  });

  it("só os vínculos exato e por apelido permitem agir", () => {
    const podem = Object.entries(VINCULO_META)
      .filter(([, m]) => m.podeAgir)
      .map(([k]) => k)
      .sort();
    expect(podem).toEqual(["apelido", "relatado"]);
  });
});

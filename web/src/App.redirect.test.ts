import { describe, it, expect } from "vitest";
import { hostDashboardRedirect } from "./App";

// hostDashboardRedirect decide se um UID de dashboard "host-…" (auto-gerado) deve
// abrir a Visão do Servidor (#/hosts/<host>) em vez do dashboard antigo.
describe("hostDashboardRedirect", () => {
  it("dashboard por-servidor abre o detalhe do host (hostname com pontos preservado)", () => {
    expect(hostDashboardRedirect("host-vps-demo.exemplo.com", "")).toBe(
      "/hosts/vps-demo.exemplo.com",
    );
    expect(hostDashboardRedirect("host-mail.exemplo.com.br", "")).toBe("/hosts/mail.exemplo.com.br");
  });

  it("genérico com ?var-host= (deep link de alerta) abre o servidor escolhido", () => {
    expect(hostDashboardRedirect("host-visao-geral", "var-host=srv-02")).toBe("/hosts/srv-02");
  });

  it("genérico sem host manda escolher na lista de servidores", () => {
    expect(hostDashboardRedirect("host-visao-geral", "")).toBe("/hosts");
  });

  it("dashboard normal (UID sem prefixo host-) não é redirecionado", () => {
    expect(hostDashboardRedirect("meu-dashboard", "")).toBeNull();
    expect(hostDashboardRedirect("infra-geral", "var-host=x")).toBeNull();
  });

  it("hostname com caractere especial é codificado para a URL", () => {
    expect(hostDashboardRedirect("host-a b", "")).toBe("/hosts/a%20b");
  });
});

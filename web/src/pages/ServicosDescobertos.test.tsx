import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { ServicosChips, ServicosPorServidor, servicosDoHost } from "./ServicosDescobertos";
import type { HostService } from "../api";

const whm: HostService[] = [
  { hostname: "whm.exemplo", kind: "mysql", detail: "mariadbd", source: "process", discovered_at: "2026-09-25T13:00:00Z" },
  {
    hostname: "whm.exemplo",
    kind: "php-fpm",
    detail: "8.1, 8.2, 8.3, 8.4",
    source: "process",
    discovered_at: "2026-09-25T13:05:00Z",
  },
  { hostname: "whm.exemplo", kind: "apache", detail: "httpd", source: "process", discovered_at: "2026-09-25T13:05:00Z" },
  {
    hostname: "mail.exemplo",
    kind: "docker",
    detail: "sogo,mariadb,rspamd",
    source: "docker",
    discovered_at: "2026-09-25T13:05:00Z",
  },
];

describe("ServicosChips", () => {
  it("mostra Apache, PHP-FPM com as versões e MariaDB, na ordem de leitura", () => {
    render(<ServicosChips servicos={servicosDoHost(whm, "whm.exemplo")} />);
    const lista = screen.getByRole("list", { name: "Serviços descobertos" });
    const nomes = [...lista.querySelectorAll(".svc__nome")].map((n) => n.textContent);
    expect(nomes).toEqual(["Apache", "PHP-FPM", "MariaDB"]);
    expect(screen.getByText("PHP 8.1, 8.2, 8.3 e 8.4")).toBeInTheDocument();
    expect(screen.getByText("processo httpd")).toBeInTheDocument();
  });

  it("Docker abre e fecha a lista de containers por um botão acessível", () => {
    render(<ServicosChips servicos={servicosDoHost(whm, "mail.exemplo")} />);
    expect(screen.getByText("3 containers")).toBeInTheDocument();
    const abrir = screen.getByRole("button", { name: "Ver containers" });
    expect(abrir).toHaveAttribute("aria-expanded", "false");
    // A lista existe desde o início (o aria-controls precisa apontar para um id
    // real), mas escondida; só o clique a revela.
    expect(screen.getByText("rspamd")).not.toBeVisible();
    expect(document.getElementById(abrir.getAttribute("aria-controls") ?? "")).not.toBeNull();
    fireEvent.click(abrir);
    expect(screen.getByRole("button", { name: "Ocultar containers" })).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("rspamd")).toBeVisible();
  });

  it("sem serviços não desenha lista vazia", () => {
    const { container } = render(<ServicosChips servicos={[]} />);
    expect(container.querySelector("ul")).toBeNull();
  });
});

describe("ServicosPorServidor", () => {
  it("um bloco por servidor com o nome amigável e o hostname técnico", () => {
    render(<ServicosPorServidor services={whm} hostLabel={(h) => (h === "whm.exemplo" ? "WHM GCloud" : h)} />);
    expect(screen.getByText("2 servidores · 4 serviços")).toBeInTheDocument();
    // Cada servidor é um título (h3) e o id da lista de containers é único por servidor.
    expect(screen.getByRole("heading", { level: 3, name: "WHM GCloud" })).toBeInTheDocument();
    const ids = [...document.querySelectorAll("[id]")].map((e) => e.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect(screen.getByRole("link", { name: "WHM GCloud" })).toHaveAttribute("href", "#/hosts/whm.exemplo");
    expect(screen.getByText("whm.exemplo")).toBeInTheDocument();
    // Sem apelido, o hostname não é repetido.
    expect(screen.getAllByText("mail.exemplo")).toHaveLength(1);
  });

  it("vazio: estado explicativo, sem tabela", () => {
    render(<ServicosPorServidor services={[]} hostLabel={(h) => h} />);
    expect(screen.getByText("Nenhum serviço descoberto ainda")).toBeInTheDocument();
  });
});

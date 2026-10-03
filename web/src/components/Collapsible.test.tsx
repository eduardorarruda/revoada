// O Collapsible é a peça que sustenta a diretriz de minimalismo do painel: se ele
// escondesse conteúdo sem dizer o que escondeu, ou se abrisse só no mouse, teria
// piorado a tela em vez de limpá-la. Estes testes prendem esse contrato.
import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { Collapsible } from "./Collapsible";
import { PageHeader } from "./PageHeader";

describe("Collapsible", () => {
  beforeEach(() => localStorage.clear());

  it("nasce fechado e é o <details> nativo (teclado e Ctrl+F funcionam de graça)", () => {
    const { container } = render(
      <Collapsible titulo="Servidores em ordem">
        <p>conteúdo</p>
      </Collapsible>,
    );
    const det = container.querySelector("details");
    expect(det).toBeTruthy();
    expect(det).not.toHaveAttribute("open");
    // O conteúdo continua no DOM mesmo fechado — é o que faz a busca do navegador
    // encontrá-lo e o leitor de tela anunciá-lo.
    expect(screen.getByText("conteúdo")).toBeInTheDocument();
  });

  it("mostra contador e resumo — recolher não pode virar esconder", () => {
    render(
      <Collapsible titulo="Servidores em ordem" contador={7} resumo="nenhum acima do limiar">
        <p>x</p>
      </Collapsible>,
    );
    expect(screen.getByText("7")).toBeInTheDocument();
    expect(screen.getByText("nenhum acima do limiar")).toBeInTheDocument();
  });

  it("contador ZERO não aparece: 'Ver todos (0)' convida a abrir gaveta vazia", () => {
    const { container } = render(
      <Collapsible titulo="Nada aqui" contador={0}>
        <p>x</p>
      </Collapsible>,
    );
    expect(container.querySelector(".colap__contador")).toBeNull();
  });

  it("lembra aberto/fechado entre visitas quando recebe persistKey", () => {
    const { container, unmount } = render(
      <Collapsible titulo="Primeiros passos" persistKey="teste">
        <p>x</p>
      </Collapsible>,
    );
    const det = container.querySelector("details") as HTMLDetailsElement;
    det.open = true;
    fireEvent(det, new Event("toggle", { bubbles: false }));
    unmount();

    const segunda = render(
      <Collapsible titulo="Primeiros passos" persistKey="teste">
        <p>x</p>
      </Collapsible>,
    );
    expect(segunda.container.querySelector("details")).toHaveAttribute("open");
  });

  it("sem persistKey NÃO grava nada — preferência de tela não polui o armazenamento", () => {
    const { container } = render(
      <Collapsible titulo="Sem chave">
        <p>x</p>
      </Collapsible>,
    );
    const det = container.querySelector("details") as HTMLDetailsElement;
    det.open = true;
    fireEvent(det, new Event("toggle", { bubbles: false }));
    expect(localStorage.length).toBe(0);
  });

  it("no modo controlado quem manda é a tela (Guia: busca e sumário abrem a seção)", () => {
    const aoMudar = vi.fn();
    const { container, rerender } = render(
      <Collapsible titulo="Glossário" open={false} onOpenChange={aoMudar}>
        <p>x</p>
      </Collapsible>,
    );
    expect(container.querySelector("details")).not.toHaveAttribute("open");
    rerender(
      <Collapsible titulo="Glossário" open onOpenChange={aoMudar}>
        <p>x</p>
      </Collapsible>,
    );
    expect(container.querySelector("details")).toHaveAttribute("open");
  });
});

describe("PageHeader", () => {
  it("subtítulo curto continua um parágrafo comum", () => {
    const { container } = render(<PageHeader title="Alertas" subtitle="Regras que vigiam suas métricas" />);
    expect(container.querySelector(".colap")).toBeNull();
    expect(screen.getByText("Regras que vigiam suas métricas")).toBeInTheDocument();
  });

  it("no desktop, subtítulo longo TAMBÉM fica visível — lá sobra espaço", () => {
    // Sem matchMedia (o jsdom não o implementa), useMobile responde "não é
    // estreita" — que é justamente o caminho que mostra tudo.
    const longo =
      "Cadastre usuários e escolha quais servidores cada um pode ver, editar e receber alerta. " +
      "Grupos de servidores agilizam quando vários usuários compartilham o mesmo conjunto.";
    const { container } = render(<PageHeader title="Usuários" subtitle={longo} />);
    expect(container.querySelector(".colap")).toBeNull();
    expect(screen.getByText(longo)).toBeInTheDocument();
  });

  it("no celular, subtítulo longo vira 'Sobre esta tela' — sem sumir do DOM", () => {
    // Finge uma tela estreita para o useMobile. O jsdom não tem matchMedia, então
    // este é o único jeito de exercitar o caminho do celular.
    vi.stubGlobal("matchMedia", (q: string) => ({
      matches: q === "(max-width: 767px)",
      media: q,
      addEventListener: () => {},
      removeEventListener: () => {},
    }));
    const longo =
      "Cadastre usuários e escolha quais servidores cada um pode ver, editar e receber alerta. " +
      "Grupos de servidores agilizam quando vários usuários compartilham o mesmo conjunto.";
    const { container } = render(<PageHeader title="Usuários" subtitle={longo} />);
    expect(container.querySelector(".colap")).not.toBeNull();
    expect(screen.getByText("Sobre esta tela")).toBeInTheDocument();
    // O texto continua lá dentro: recolhido, não removido.
    expect(screen.getByText(longo)).toBeInTheDocument();
    vi.unstubAllGlobals();
  });
});

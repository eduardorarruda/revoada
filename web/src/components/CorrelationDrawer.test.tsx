import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { CorrelationData } from "../api";

// O drawer busca na API ao abrir; tudo mockado para rodar sem backend.
const correlate = vi.fn();
const getTrace = vi.fn();
vi.mock("../api", () => ({
  correlate: (...a: unknown[]) => correlate(...a),
  getTrace: (...a: unknown[]) => getTrace(...a),
  searchLogs: () => Promise.resolve([]),
  api: () => Promise.resolve({ rows: [] }),
}));

const { CorrelationDrawer } = await import("./CorrelationDrawer");

const dados: CorrelationData = {
  service: "checkout",
  from: 0,
  to: 0,
  deploys: [],
  traces: [
    { trace_id: "abc123", start_ms: 0, root_service: "checkout", root_name: "POST /pagar", duration_ms: 812, spans: 3, has_error: 1, partial: false },
  ],
  logs: [],
};

beforeEach(() => {
  correlate.mockReset();
  getTrace.mockReset();
  correlate.mockResolvedValue(dados);
  getTrace.mockResolvedValue([]);
});

function abrirDe(origem: HTMLElement) {
  origem.focus();
  act(() => {
    window.dispatchEvent(new CustomEvent("revoada:correlate", { detail: { service: "checkout", tsMs: 1_700_000_000_000 } }));
  });
}

describe("CorrelationDrawer — acessibilidade", () => {
  it("abre como diálogo modal rotulado e recebe o foco", () => {
    render(
      <>
        <button type="button">gráfico</button>
        <CorrelationDrawer />
      </>,
    );
    abrirDe(screen.getByRole("button", { name: "gráfico" }));
    const painel = screen.getByRole("dialog", { name: /Correlação · checkout/ });
    expect(painel).toHaveAttribute("aria-modal", "true");
    expect(painel).toContainElement(document.activeElement as HTMLElement);
  });

  it("Esc fecha e devolve o foco a quem abriu", () => {
    render(
      <>
        <button type="button">gráfico</button>
        <CorrelationDrawer />
      </>,
    );
    const origem = screen.getByRole("button", { name: "gráfico" });
    abrirDe(origem);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(origem).toHaveFocus();
  });

  it("a linha de trace é um botão (Tab + Enter/Espaço nativos) que abre a correlação do trace", async () => {
    render(<CorrelationDrawer />);
    abrirDe(document.body);
    const linha = await screen.findByRole("button", { name: /checkout POST \/pagar/ });
    expect(linha.tagName).toBe("BUTTON");
    expect(linha).toHaveAttribute("type", "button");

    fireEvent.click(linha);
    await waitFor(() => expect(getTrace).toHaveBeenCalledWith("abc123"));
  });
});

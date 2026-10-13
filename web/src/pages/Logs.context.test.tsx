import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { LogRow } from "../api";
import { ToastProvider } from "../components";

// A tela de Logs consulta a API e os nomes dos servidores; tudo mockado para o
// teste rodar sem backend. Só importa aqui o "Ver contexto" de uma linha.
const linha: LogRow = {
  t: 1_700_000_000_000,
  service: "checkout",
  severity: "ERROR",
  severity_num: 17,
  body: "falha ao cobrar o cartão",
  labels: {},
  trace_id: "",
  span_id: "",
};
const vizinha: LogRow = { ...linha, t: linha.t - 1000, severity: "INFO", severity_num: 9, body: "iniciando cobrança" };

const logContext = vi.fn();
vi.mock("../api", () => ({
  getRole: () => "user",
  createLogMetric: vi.fn(),
  deleteLogMetric: vi.fn(),
  listLogMetrics: () => Promise.resolve({ log_metrics: [] }),
  openCorrelation: vi.fn(),
  logContext: (...a: unknown[]) => logContext(...a),
  logHistogram: () => Promise.resolve({ buckets: [], step: 60 }),
  logPatterns: () => Promise.resolve([]),
  logServices: () => Promise.resolve({ services: [] }),
  logSources: () => Promise.resolve({ sources: [] }),
  openLogTail: () => () => {},
  searchLogs: () => Promise.resolve([linha]),
}));
vi.mock("../hooks/useHostNames", () => ({
  useHostNames: () => ({ hostLabel: (h: string) => h, names: {}, loading: false, refresh: () => {} }),
}));
vi.mock("../hooks/useHostContainers", () => ({
  useHostContainers: () => ({ hosts: [], containersOf: () => [], loading: false, refresh: () => {} }),
}));

const { Logs } = await import("./Logs");

beforeEach(() => {
  logContext.mockReset();
  logContext.mockResolvedValue([vizinha, linha]);
});

describe("Logs — modal de Contexto", () => {
  it("abre como diálogo rotulado, com o foco dentro, e Esc devolve o foco a 'Ver contexto'", async () => {
    render(
      <ToastProvider>
        <Logs />
      </ToastProvider>,
    );
    // Expande a linha para chegar à ação "Ver contexto".
    // A busca roda após um debounce de 250 ms; folga extra para máquina carregada.
    fireEvent.click(await screen.findByRole("button", { name: /Expandir detalhes do log de checkout/ }, { timeout: 5000 }));
    const verContexto = screen.getByRole("button", { name: "Ver contexto" });
    verContexto.focus();
    fireEvent.click(verContexto);

    const dialogo = await screen.findByRole("dialog", { name: "Contexto, checkout" }, { timeout: 5000 });
    expect(dialogo).toHaveAttribute("aria-modal", "true");
    expect(dialogo).toContainElement(document.activeElement as HTMLElement);
    expect(within(dialogo).getByText("iniciando cobrança")).toBeInTheDocument();
    expect(logContext).toHaveBeenCalledWith("checkout", linha.t, 15);

    fireEvent.keyDown(document, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(verContexto).toHaveFocus();
  }, 20_000);
});

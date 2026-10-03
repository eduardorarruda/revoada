import { render, screen, waitFor } from "@testing-library/react";
import type { PublicStatus } from "../api";

// A página pública consulta /api/status sem login. Mockamos a chamada para o teste
// rodar sem backend.
const publicStatus = vi.fn();
vi.mock("../api", () => ({
  publicStatus: () => publicStatus(),
}));

const { StatusPage } = await import("./StatusPage");

// Resposta transcrita do formato REAL de GET /api/status
// (server/internal/statuspage/statuspage.go): uptime é OBJETO, não número, e
// `percent` vem null quando a cobertura não autoriza afirmar disponibilidade.
const data: PublicStatus = {
  overall: "operacional",
  updated_at: "2026-08-09T21:16:10Z",
  sites: [
    {
      name: "Portal Revoada",
      group: "Institucional",
      url: "https://example.com/",
      state: "UP",
      kind: "http",
      uptime_day: { percent: 99.91, coverage: 100, samples: 288 },
      uptime_month: { percent: null, coverage: 4.4, samples: 329 },
      uptime_90d: null,
    },
  ],
};

describe("status page pública — uptime", () => {
  beforeEach(() => {
    publicStatus.mockReset();
    publicStatus.mockResolvedValue(data);
  });

  it("mostra o percentual medido (o guarda antigo reprovava o objeto e imprimia '—')", async () => {
    render(<StatusPage />);
    // Piso com 2 casas: 99,91% sai como 99,91%, nunca arredondado para 100.
    expect(await screen.findByText("99,91%")).toBeInTheDocument();
  });

  it("mostra a cobertura ao lado do percentual", async () => {
    render(<StatusPage />);
    expect(await screen.findByText(/dia · 100% medido/)).toBeInTheDocument();
  });

  it("escreve 'sem dados suficientes' quando percent é null (nunca 100%)", async () => {
    render(<StatusPage />);
    await waitFor(() => expect(screen.getByText(/sem dados suficientes/)).toBeInTheDocument());
    expect(screen.queryByText("100,00%")).not.toBeInTheDocument();
  });

  it("uptime_90d nulo inteiro (sem 90 dias de histórico) não quebra e não inventa número", async () => {
    render(<StatusPage />);
    const noventa = await screen.findByText("90d");
    expect(noventa).toBeInTheDocument();
    expect(noventa.parentElement?.textContent).toContain("—");
  });

  it("sitemap mostra páginas no ar em vez de uptime (o pai não sonda nada)", async () => {
    publicStatus.mockResolvedValue({
      ...data,
      sites: [
        {
          name: "Loja",
          group: "Geral",
          url: "https://example.com/sitemap.xml",
          state: "DOWN",
          kind: "sitemap",
          pages: { total: 10, down: 3, degraded: 1 },
          uptime_day: null,
          uptime_month: null,
          uptime_90d: null,
        },
      ],
    } satisfies PublicStatus);
    render(<StatusPage />);
    expect(await screen.findByText("6/10")).toBeInTheDocument();
  });
});

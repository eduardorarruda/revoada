import { render, screen, waitFor, within } from "@testing-library/react";
import { acharJanela } from "../../components/JanelaTempo";
import { resumo, totais } from "./fixtures";

// Sem backend: o resumo é mockado. O gráfico (uPlot, canvas) é trocado por um
// marcador — o que se testa aqui é o texto que a pessoa lê, não o desenho.
const getIaResumo = vi.fn();
vi.mock("../../api.ia", () => ({ getIaResumo: (...a: unknown[]) => getIaResumo(...a) }));
vi.mock("../../panels", () => ({
  TimeSeriesPanel: ({ title }: { title: string }) => <div data-testid="grafico">{title}</div>,
}));

const { VisaoGeral } = await import("./VisaoGeral");

beforeEach(() => {
  getIaResumo.mockReset();
});

function renderizar() {
  return render(<VisaoGeral janela={acharJanela("24h")} versao={0} />);
}

describe("Visão geral de IA", () => {
  it("abre com o veredito em uma frase", async () => {
    getIaResumo.mockResolvedValue(resumo({ totais: totais({ custo_parcial: false }) }));
    renderizar();
    expect(
      await screen.findByText("Últimas 24 h: US$ 12,40 em 3.214 chamadas, 1,8% com erro, p95 de 4,2 s", { selector: "h2" }),
    ).toBeInTheDocument();
    // A janela vai para a API como intervalo absoluto.
    const filtro = getIaResumo.mock.calls[0][0] as { from: string; to: string };
    expect(Date.parse(filtro.to) - Date.parse(filtro.from)).toBe(24 * 3600 * 1000);
  });

  it("custo parcial fica visível e explicado", async () => {
    getIaResumo.mockResolvedValue(resumo());
    renderizar();
    expect(await screen.findByText(/US\$ 12,40 \(parcial\)/, { selector: "h2" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Mais informações sobre custo parcial" })).toBeInTheDocument();
    expect(screen.getAllByText(/3 chamadas sem preço cadastrado e 14 chamadas sem tokens informados/).length).toBeGreaterThan(0);
  });

  it("nulo vira 'não informado' ou 'sem preço', nunca zero", async () => {
    getIaResumo.mockResolvedValue(
      resumo({ totais: totais({ custo_usd: null, latencia_p95_ms: null, latencia_p50_ms: null, latencia_p99_ms: null }) }),
    );
    renderizar();
    await screen.findByText(/custo não informado/, { selector: "h2" });
    expect(screen.getAllByText("não informado").length).toBeGreaterThanOrEqual(2); // KPIs de custo e p95

    // O modelo sem preço: custo "sem preço" e p95 "não informado" na linha dele.
    const linha = screen.getAllByText("acme-large").map((el) => el.closest("tr")).find(Boolean) as HTMLElement;
    expect(within(linha).getAllByText("sem preço").length).toBeGreaterThan(0);
    expect(within(linha).getByText("não informado")).toBeInTheDocument();
    expect(within(linha).queryByText(/US\$ 0/)).not.toBeInTheDocument();
  });

  it("avisa modelos sem preço e tabela desatualizada, com link para os preços", async () => {
    getIaResumo.mockResolvedValue(resumo());
    renderizar();
    const aviso = await screen.findByRole("region", { name: "Preços a conferir" });
    expect(within(aviso).getByText(/1 modelo sem preço/)).toBeInTheDocument();
    expect(within(aviso).getByText(/370 dias/)).toBeInTheDocument();
    expect(within(aviso).getByRole("link", { name: "Abrir Modelos e preços" })).toHaveAttribute("href", "#/ia/precos?janela=24h");
  });

  it("sem pendência de preço, sem aviso", async () => {
    getIaResumo.mockResolvedValue(
      resumo({
        por_modelo: resumo().por_modelo.filter((m) => !m.sem_preco),
        precos: { referencia: "2026-09", dias_desde_atualizacao: 10, desatualizada: false },
      }),
    );
    renderizar();
    await screen.findAllByTestId("grafico");
    expect(screen.queryByRole("region", { name: "Preços a conferir" })).not.toBeInTheDocument();
  });

  it("agentes linkam para as execuções filtradas e ferramentas com erro aparecem", async () => {
    getIaResumo.mockResolvedValue(resumo());
    renderizar();
    expect(await screen.findByRole("link", { name: "Atendente" })).toHaveAttribute(
      "href",
      "#/ia/execucoes?janela=24h&agente=Atendente",
    );
    expect(screen.getByText("buscar_pedido")).toBeInTheDocument();
    expect(screen.queryByText("clima")).not.toBeInTheDocument(); // zero erros: fora da lista
  });

  it("falha de carga explica e oferece tentar de novo", async () => {
    getIaResumo.mockRejectedValue(new Error("502: gateway fora"));
    renderizar();
    await waitFor(() => expect(screen.getByText("gateway fora")).toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Tentar de novo" })).toBeInTheDocument();
  });
});

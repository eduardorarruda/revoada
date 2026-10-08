import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { acharJanela } from "../../components/JanelaTempo";
import { resumo, totais } from "./fixtures";

// Sem backend: o resumo é mockado. O gráfico (uPlot, canvas) é trocado por um
// marcador — o que se testa aqui é o texto que a pessoa lê, não o desenho.
const getIaResumo = vi.fn();
vi.mock("../../api.ia", () => ({ getIaResumo: (...a: unknown[]) => getIaResumo(...a) }));
vi.mock("../../panels", () => ({
  TimeSeriesPanel: ({ title, unit }: { title: string; unit?: string }) => (
    <div data-testid="grafico" data-unit={unit}>
      {title}
    </div>
  ),
}));

const { VisaoGeral } = await import("./VisaoGeral");

beforeEach(() => {
  getIaResumo.mockReset();
});

function renderizar(janela = "24h", mudarJanela?: (id: string) => void) {
  return render(<VisaoGeral janela={acharJanela(janela)} versao={0} mudarJanela={mudarJanela} />);
}

/** O cartão do indicador pelo rótulo. */
function kpi(rotulo: string): HTMLElement {
  return screen.getByText(rotulo, { selector: ".kpi__rotulo" }).closest(".kpi") as HTMLElement;
}

describe("Visão geral de IA", () => {
  it("abre com um veredito de julgamento; os números da janela vão no detalhe", async () => {
    getIaResumo.mockResolvedValue(resumo({ totais: totais({ custo_parcial: false }) }));
    renderizar();
    expect(await screen.findByText("Erro acima do normal: 1,8% das chamadas", { selector: "h2" })).toBeInTheDocument();
    expect(screen.getByText(/Últimas 24 h: US\$ 12,40 em 3\.214 chamadas, p95 de 4,2 s\./, { selector: "p.veredito__detalhe" })).toBeInTheDocument();
    // A janela vai para a API como intervalo absoluto.
    const filtro = getIaResumo.mock.calls[0][0] as { from: string; to: string };
    expect(Date.parse(filtro.to) - Date.parse(filtro.from)).toBe(24 * 3600 * 1000);
  });

  it("enquanto a janela nova carrega, o veredito continua dizendo a janela do dado na tela", async () => {
    getIaResumo.mockResolvedValueOnce(resumo({ totais: totais({ custo_parcial: false }) }));
    const { rerender } = renderizar("1h");
    await screen.findByText(/^Última 1 h: US\$ 12,40/, { selector: "p.veredito__detalhe" });

    let responder: (v: unknown) => void = () => {};
    getIaResumo.mockReturnValueOnce(new Promise((r) => (responder = r)));
    rerender(<VisaoGeral janela={acharJanela("7d")} versao={0} />);
    // A busca de 7 dias está no ar: o dado (e a frase) ainda são da última 1 h.
    await waitFor(() => expect(getIaResumo).toHaveBeenCalledTimes(2));
    expect(screen.getByText(/^Última 1 h:/, { selector: "p.veredito__detalhe" })).toBeInTheDocument();
    expect(screen.queryByText(/Últimos 7 dias/)).not.toBeInTheDocument();

    await act(async () => responder(resumo({ totais: totais({ custo_parcial: false, custo_usd: 80 }) })));
    expect(await screen.findByText(/^Últimos 7 dias: US\$ 80,00/, { selector: "p.veredito__detalhe" })).toBeInTheDocument();
  });

  it("custo parcial fica visível e explicado", async () => {
    getIaResumo.mockResolvedValue(resumo());
    renderizar();
    expect(await screen.findByText(/US\$ 12,40 \(parcial\)/, { selector: "p.veredito__detalhe" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Mais informações sobre custo parcial" })).toBeInTheDocument();
    expect(screen.getAllByText(/3 chamadas sem preço cadastrado e 14 chamadas sem tokens informados/).length).toBeGreaterThan(0);
  });

  it("indicador sem valor mostra travessão e diz 'não informado' embaixo, nunca zero", async () => {
    getIaResumo.mockResolvedValue(
      resumo({ totais: totais({ custo_usd: null, latencia_p95_ms: null, latencia_p50_ms: null, latencia_p99_ms: null }) }),
    );
    renderizar();
    await screen.findByText(/custo não informado/, { selector: "p.veredito__detalhe" });
    for (const rotulo of ["Custo", "Latência p95"]) {
      const cartao = kpi(rotulo);
      expect(within(cartao).getByText("—")).toBeInTheDocument();
      expect(within(cartao).getByText("não informado")).toBeInTheDocument();
      expect(cartao).not.toHaveTextContent(/US\$ 0|0 ms/);
    }

    // O modelo sem preço: custo "sem preço" e p95 "não informado" na linha dele.
    const linha = screen.getAllByText("acme-large").map((el) => el.closest("tr")).find(Boolean) as HTMLElement;
    expect(within(linha).getAllByText("sem preço").length).toBeGreaterThan(0);
    expect(within(linha).getByText("não informado")).toBeInTheDocument();
    expect(within(linha).queryByText(/US\$ 0/)).not.toBeInTheDocument();
  });

  it("o símbolo do dólar fica fora do número no indicador de custo", async () => {
    getIaResumo.mockResolvedValue(resumo({ totais: totais({ custo_parcial: false }) }));
    renderizar();
    await screen.findByText("Erro acima do normal: 1,8% das chamadas", { selector: "h2" });
    const cartao = kpi("Custo");
    expect(within(cartao).getByText("US$")).toHaveClass("kpi__unidade--antes");
    expect(within(cartao).getByText("valor da biblioteca + estimativa pela tabela")).toBeInTheDocument();
    expect(kpi("Latência p95")).toHaveAttribute("title", "95% das chamadas terminaram em até este tempo");
  });

  it("plural certo: 1 execução, 1 chamada falhou, 1 modelo sem preço", async () => {
    getIaResumo.mockResolvedValue(resumo({ totais: totais({ execucoes: 1, erros: 1, sem_tokens: 1 }) }));
    renderizar();
    await screen.findByRole("note", { name: "Preços a conferir" });
    expect(within(kpi("Chamadas de modelo")).getByText("1 execução")).toBeInTheDocument();
    expect(within(kpi("Com erro")).getByText("1 chamada falhou")).toBeInTheDocument();
    expect(within(kpi("Tokens de saída")).getByText("1 chamada sem tokens informados")).toBeInTheDocument();
    expect(screen.getByText(/1 modelo sem preço/)).toBeInTheDocument();
  });

  it("gráfico de custo em dólar", async () => {
    getIaResumo.mockResolvedValue(resumo());
    renderizar();
    const graficos = await screen.findAllByTestId("grafico");
    expect(graficos.find((g) => g.textContent === "Custo no tempo")).toHaveAttribute("data-unit", "usd");
  });

  it("avisa modelos sem preço e tabela desatualizada, com link para os preços", async () => {
    getIaResumo.mockResolvedValue(resumo());
    renderizar();
    const aviso = await screen.findByRole("note", { name: "Preços a conferir" });
    expect(within(aviso).getByText(/1 modelo sem preço/)).toBeInTheDocument();
    expect(within(aviso).getByText(/out\/2025/)).toBeInTheDocument();
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
    expect(screen.queryByRole("note", { name: "Preços a conferir" })).not.toBeInTheDocument();
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
    expect(screen.getByRole("button", { name: "Mais informações sobre agentes que mais gastam" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Mais informações sobre ferramentas com mais erros" })).toBeInTheDocument();
  });

  it("janela sem chamadas: mantém o veredito e troca o resto por um guia, com 'Ampliar para 7 dias'", async () => {
    getIaResumo.mockResolvedValue(resumo({ totais: totais({ chamadas: 0 }), serie: [] }));
    const mudar = vi.fn();
    renderizar("1h", mudar);
    expect(await screen.findByText("Última 1 h: nenhuma chamada de modelo", { selector: "h2" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Nenhuma chamada de IA nesta janela" })).toBeInTheDocument();
    expect(screen.getByText("Aponte o exportador OTLP para o gateway do Revoada")).toBeInTheDocument();
    expect(screen.queryByTestId("grafico")).not.toBeInTheDocument();
    expect(document.querySelector(".kpi")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Ampliar para 7 dias" }));
    expect(mudar).toHaveBeenCalledWith("7d");
  });

  it("já em 7 dias sem chamadas: oferece o Guia em vez de ampliar", async () => {
    getIaResumo.mockResolvedValue(resumo({ totais: totais({ chamadas: 0 }), serie: [] }));
    renderizar("7d", vi.fn());
    expect(await screen.findByRole("button", { name: "Abrir o Guia" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Ampliar/ })).not.toBeInTheDocument();
  });

  it("carregando: esqueleto com a forma da tela (seis indicadores)", () => {
    getIaResumo.mockReturnValue(new Promise(() => {}));
    renderizar();
    expect(screen.getByRole("status")).toHaveTextContent("Carregando o resumo de IA");
    expect(document.querySelectorAll(".kpi")).toHaveLength(6);
  });

  it("falha de carga explica e oferece tentar de novo", async () => {
    getIaResumo.mockRejectedValue(new Error("502: gateway fora"));
    renderizar();
    const alerta = await screen.findByRole("alert", { name: "A consulta falhou" });
    expect(within(alerta).getByText("gateway fora")).toBeInTheDocument();
    getIaResumo.mockResolvedValue(resumo());
    fireEvent.click(within(alerta).getByRole("button", { name: "Tentar de novo" }));
    expect(await screen.findAllByTestId("grafico")).toHaveLength(2);
  });
});

import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { acharJanela } from "../../components/JanelaTempo";
import { execucao } from "./fixtures";

const listIaExecucoes = vi.fn();
const listIaFerramentas = vi.fn();
vi.mock("../../api.ia", () => ({
  listIaExecucoes: (...a: unknown[]) => listIaExecucoes(...a),
  listIaFerramentas: (...a: unknown[]) => listIaFerramentas(...a),
}));

const { Execucoes, filtroDaApi } = await import("./Execucoes");
const { Ferramentas } = await import("./Ferramentas");

beforeEach(() => {
  listIaExecucoes.mockReset();
  listIaFerramentas.mockReset();
});

describe("Execuções", () => {
  it("cada linha leva ao replay; custo nulo é 'não informado' e parcial é sinalizado", async () => {
    listIaExecucoes.mockResolvedValue({
      execucoes: [
        execucao(),
        execucao({ trace_id: "t2", agente: "Resumidor", custo_usd: null, tokens_entrada: null, status: "erro", erros: 2 }),
        execucao({ trace_id: "t3", agente: "Triador", custo_parcial: true }),
      ],
    });
    render(<Execucoes janela={acharJanela("6h")} versao={0} params={new URLSearchParams()} />);
    expect(await screen.findByRole("link", { name: "Atendente" })).toHaveAttribute("href", "#/ia/execucoes/4bf92f3577b34da6?janela=6h");

    const linhaNula = screen.getByRole("link", { name: "Resumidor" }).closest("tr") as HTMLElement;
    expect(within(linhaNula).getAllByText(/não informado/).length).toBe(2); // tokens de entrada e custo
    expect(within(linhaNula).getByText("erro (2)")).toBeInTheDocument();
    expect(within(linhaNula).queryByText(/US\$ 0/)).not.toBeInTheDocument();

    const linhaParcial = screen.getByRole("link", { name: "Triador" }).closest("tr") as HTMLElement;
    expect(within(linhaParcial).getByText("parcial")).toBeInTheDocument();
  });

  it("filtros da URL viram filtros da API", async () => {
    listIaExecucoes.mockResolvedValue({ execucoes: [] });
    render(<Execucoes janela={acharJanela("1h")} versao={0} params={new URLSearchParams("agente=Atendente&status=erro")} />);
    await waitFor(() => expect(listIaExecucoes).toHaveBeenCalled());
    expect(listIaExecucoes.mock.calls[0][0]).toMatchObject({ agente: "Atendente", status: "erro", limit: 200 });
    expect(await screen.findByText(/Nenhuma execução com esses filtros/)).toBeInTheDocument();
  });

  it("trocar o status refaz a consulta", async () => {
    listIaExecucoes.mockResolvedValue({ execucoes: [] });
    render(<Execucoes janela={acharJanela("1h")} versao={0} params={new URLSearchParams()} />);
    await waitFor(() => expect(listIaExecucoes).toHaveBeenCalledTimes(1));
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "erro" } });
    await waitFor(() => expect(listIaExecucoes).toHaveBeenCalledTimes(2), { timeout: 2000 });
    expect(listIaExecucoes.mock.calls[1][0]).toMatchObject({ status: "erro" });
  });

  it("custo mínimo aceita vírgula e ignora texto inválido", () => {
    const j = acharJanela("1h");
    const base = { agente: "", modelo: "", status: "" as const };
    expect(filtroDaApi({ ...base, custoMin: "0,05" }, j).custo_min).toBe(0.05);
    expect(filtroDaApi({ ...base, custoMin: "abc" }, j).custo_min).toBeUndefined();
  });
});

describe("Ferramentas", () => {
  it("lista taxa de erro, latências e erros frequentes; latência nula é 'não informado'", async () => {
    listIaFerramentas.mockResolvedValue({
      ferramentas: [
        {
          ferramenta: "buscar_pedido",
          chamadas: 120,
          erros: 6,
          taxa_erro: 0.05,
          latencia_p50_ms: 80,
          latencia_p95_ms: 410,
          erros_frequentes: [{ erro: "TimeoutError", vezes: 5 }],
        },
        { ferramenta: "clima", chamadas: 3, erros: 0, taxa_erro: 0, latencia_p50_ms: null, latencia_p95_ms: null, erros_frequentes: [] },
      ],
    });
    render(<Ferramentas janela={acharJanela("1h")} versao={0} />);
    const linha = (await screen.findByText("buscar_pedido")).closest("tr") as HTMLElement;
    expect(within(linha).getByText("5%")).toBeInTheDocument();
    expect(within(linha).getByText("TimeoutError")).toBeInTheDocument();
    expect(within(linha).getByText("410 ms")).toBeInTheDocument();
    const clima = screen.getByText("clima").closest("tr") as HTMLElement;
    expect(within(clima).getAllByText("não informado")).toHaveLength(2);
    expect(within(clima).getByText("nenhum erro")).toBeInTheDocument();
  });
});

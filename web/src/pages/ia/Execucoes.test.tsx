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
    expect(await screen.findByRole("heading", { name: "Nenhuma execução com esses filtros" })).toBeInTheDocument();
  });

  it("vazio com filtro oferece limpar; limpar zera os campos e refaz a consulta sem filtro", async () => {
    listIaExecucoes.mockResolvedValue({ execucoes: [] });
    render(<Execucoes janela={acharJanela("1h")} versao={0} params={new URLSearchParams("agente=Atendente&custo_min=0,5")} />);
    const vazio = await screen.findByRole("heading", { name: "Nenhuma execução com esses filtros" });
    const estado = vazio.closest(".empty-state") as HTMLElement;
    fireEvent.click(within(estado).getByRole("button", { name: "Limpar filtros" }));
    expect(screen.getByLabelText("Agente")).toHaveValue("");
    expect(screen.getByLabelText("Custo mínimo (US$)")).toHaveValue("");
    await waitFor(() => expect(listIaExecucoes).toHaveBeenCalledTimes(2), { timeout: 2000 });
    expect(listIaExecucoes.mock.calls[1][0]).toMatchObject({ agente: undefined, custo_min: undefined });
    // Sem filtro digitado, o botão "Limpar filtros" do topo some.
    expect(await screen.findByRole("heading", { name: "Nenhuma execução nesta janela" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Limpar filtros" })).not.toBeInTheDocument();
  });

  it("vazio sem filtro oferece ampliar a janela para 24 h", async () => {
    listIaExecucoes.mockResolvedValue({ execucoes: [] });
    const mudar = vi.fn();
    render(<Execucoes janela={acharJanela("1h")} versao={0} params={new URLSearchParams()} mudarJanela={mudar} />);
    await screen.findByRole("heading", { name: "Nenhuma execução nesta janela" });
    fireEvent.click(screen.getByRole("button", { name: "Ampliar para 24 h" }));
    expect(mudar).toHaveBeenCalledWith("24h");
  });

  it("custo mínimo inválido mostra o erro no campo e é ignorado na consulta", async () => {
    listIaExecucoes.mockResolvedValue({ execucoes: [] });
    render(<Execucoes janela={acharJanela("1h")} versao={0} params={new URLSearchParams()} />);
    await waitFor(() => expect(listIaExecucoes).toHaveBeenCalledTimes(1));
    const campo = screen.getByLabelText("Custo mínimo (US$)");
    fireEvent.change(campo, { target: { value: "abc" } });
    expect(campo).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByRole("alert")).toHaveTextContent("Use um número, ex.: 0,05. O filtro está sendo ignorado.");
    await waitFor(() => expect(listIaExecucoes).toHaveBeenCalledTimes(2), { timeout: 2000 });
    expect(listIaExecucoes.mock.calls[1][0]).toMatchObject({ custo_min: undefined });
  });

  it("título diz quantas execuções, no plural certo, e anuncia ao leitor de tela", async () => {
    listIaExecucoes.mockResolvedValue({ execucoes: [execucao({ chamadas_modelo: 1, chamadas_ferramenta: 2 })] });
    render(<Execucoes janela={acharJanela("1h")} versao={0} params={new URLSearchParams()} />);
    expect(await screen.findByText("1 execução", { selector: ".card__title *" })).toBeInTheDocument();
    expect(screen.getByText("1 execução encontrada.")).toHaveAttribute("aria-live", "polite");
    expect(screen.getByText("1 de modelo · 2 de ferramenta")).toBeInTheDocument();
  });

  it("início relativo ('há 4 min'), com a data absoluta no title", async () => {
    listIaExecucoes.mockResolvedValue({ execucoes: [execucao({ inicio_ms: Date.now() - 4 * 60_000 })] });
    render(<Execucoes janela={acharJanela("1h")} versao={0} params={new URLSearchParams()} />);
    const inicio = await screen.findByText("há 4 min");
    expect(inicio.getAttribute("title")).toMatch(/\d{2}\/\d{2}\/\d{4}/);
  });

  it("falha sem dado anterior oferece tentar de novo", async () => {
    listIaExecucoes.mockRejectedValueOnce(new Error("503: ClickHouse fora")).mockResolvedValue({ execucoes: [execucao()] });
    render(<Execucoes janela={acharJanela("1h")} versao={0} params={new URLSearchParams()} />);
    const alerta = await screen.findByRole("alert", { name: "A consulta falhou" });
    expect(alerta).toHaveTextContent("ClickHouse fora");
    fireEvent.click(within(alerta).getByRole("button", { name: "Tentar de novo" }));
    expect(await screen.findByRole("link", { name: "Atendente" })).toBeInTheDocument();
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
    expect(screen.getByText("2 ferramentas", { selector: ".card__title *" })).toBeInTheDocument();
  });

  it("sem chamadas de ferramenta: estado vazio explicativo", async () => {
    listIaFerramentas.mockResolvedValue({ ferramentas: [] });
    render(<Ferramentas janela={acharJanela("24h")} versao={0} mudarJanela={vi.fn()} />);
    expect(await screen.findByRole("heading", { name: "Nenhuma chamada de ferramenta nesta janela" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Ampliar para 7 dias" })).toBeInTheDocument();
  });

  it("falha sem dado anterior oferece tentar de novo", async () => {
    listIaFerramentas.mockRejectedValueOnce(new Error("502: gateway fora")).mockResolvedValue({ ferramentas: [] });
    render(<Ferramentas janela={acharJanela("1h")} versao={0} />);
    const alerta = await screen.findByRole("alert", { name: "A consulta falhou" });
    fireEvent.click(within(alerta).getByRole("button", { name: "Tentar de novo" }));
    await waitFor(() => expect(listIaFerramentas).toHaveBeenCalledTimes(2));
    expect(await screen.findByRole("heading", { name: "Nenhuma chamada de ferramenta nesta janela" })).toBeInTheDocument();
  });
});

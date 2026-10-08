import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { IaPrecosResposta } from "../../api.ia";
import { ToastProvider } from "../../components";
import { validarPreco, camposIniciais } from "./PrecoForm";

const listIaPrecos = vi.fn();
const criarIaPreco = vi.fn();
const apagarIaPreco = vi.fn();
const isAdmin = vi.fn(() => true);
vi.mock("../../api.ia", () => ({
  listIaPrecos: (...a: unknown[]) => listIaPrecos(...a),
  criarIaPreco: (...a: unknown[]) => criarIaPreco(...a),
  apagarIaPreco: (...a: unknown[]) => apagarIaPreco(...a),
}));
vi.mock("../../api", () => ({ isAdmin: () => isAdmin() }));

const { Precos } = await import("./Precos");

const resposta: IaPrecosResposta = {
  precos: [
    {
      id: 1,
      provedor: "openai",
      modelo: "gpt-4o-mini*",
      entrada_por_1m: 0.15,
      saida_por_1m: 0.6,
      cache_leitura_por_1m: 0.075,
      cache_escrita_por_1m: null,
      moeda: "USD",
      vigente_desde: "2025-10-01T00:00:00Z",
      origem: "referencia-2025-10",
    },
  ],
  modelos_sem_preco: [{ provedor: "acme", modelo: "acme-large", chamadas: 42 }],
  referencia: { referencia: "2025-10", dias_desde_atualizacao: 370, desatualizada: true },
};

beforeEach(() => {
  listIaPrecos.mockReset().mockResolvedValue(resposta);
  criarIaPreco.mockReset().mockResolvedValue(undefined);
  apagarIaPreco.mockReset().mockResolvedValue(undefined);
  isAdmin.mockReturnValue(true);
});

function renderizar() {
  return render(
    <ToastProvider>
      <Precos versao={0} />
    </ToastProvider>,
  );
}

describe("Modelos e preços", () => {
  it("mostra a tabela, o prefixo e a idade da referência; nulo não vira zero", async () => {
    renderizar();
    expect(await screen.findByText("gpt-4o-mini*")).toBeInTheDocument();
    expect(screen.getByText("prefixo")).toBeInTheDocument();
    expect(screen.getByText("não cadastrado")).toBeInTheDocument(); // cache de escrita nulo
    expect(screen.getByRole("region", { name: "Tabela de referência desatualizada" })).toHaveTextContent("370 dias");
  });

  it("leitor vê a tabela, mas não o formulário, os botões de cadastrar nem os de apagar", async () => {
    isAdmin.mockReturnValue(false);
    renderizar();
    await screen.findByText("gpt-4o-mini*");
    expect(screen.queryByRole("form", { name: "Cadastrar preço" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cadastrar preço" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Apagar preço/ })).not.toBeInTheDocument();
    expect(screen.getByText(/Só administradores cadastram e apagam preços/)).toBeInTheDocument();
  });

  it("'Cadastrar preço' de um modelo sem preço preenche o formulário", async () => {
    renderizar();
    const lista = (await screen.findByText("acme-large")).closest("li") as HTMLElement;
    fireEvent.click(within(lista).getByRole("button", { name: "Cadastrar preço" }));
    const form = screen.getByRole("form", { name: "Cadastrar preço" });
    expect(within(form).getByLabelText(/Provedor/)).toHaveValue("acme");
    expect(within(form).getByLabelText(/^Modelo/)).toHaveValue("acme-large");
  });

  it("admin cadastra um preço com o corpo do contrato", async () => {
    renderizar();
    const form = await screen.findByRole("form", { name: "Cadastrar preço" });
    const preencher = (rotulo: RegExp, valor: string) => fireEvent.change(within(form).getByLabelText(rotulo), { target: { value: valor } });
    preencher(/Provedor/, "openai");
    preencher(/^Modelo/, "gpt-5*");
    preencher(/^Entrada/, "1,25");
    preencher(/^Saída/, "10");
    preencher(/Vale a partir de/, "2026-10-01");
    fireEvent.click(within(form).getByRole("button", { name: "Cadastrar preço" }));
    await waitFor(() => expect(criarIaPreco).toHaveBeenCalledTimes(1));
    expect(criarIaPreco).toHaveBeenCalledWith({
      provedor: "openai",
      modelo: "gpt-5*",
      entrada_por_1m: 1.25,
      saida_por_1m: 10,
      cache_leitura_por_1m: null,
      cache_escrita_por_1m: null,
      moeda: "USD",
      vigente_desde: "2026-10-01T00:00:00Z",
    });
    await waitFor(() => expect(listIaPrecos).toHaveBeenCalledTimes(2)); // recarrega a tabela
  });

  it("apagar pede confirmação nomeada antes de chamar a API", async () => {
    renderizar();
    fireEvent.click(await screen.findByRole("button", { name: "Apagar preço de gpt-4o-mini*" }));
    expect(apagarIaPreco).not.toHaveBeenCalled();
    const dialogo = screen.getByRole("dialog");
    fireEvent.click(within(dialogo).getByRole("button", { name: "Apagar o preço de gpt-4o-mini*" }));
    await waitFor(() => expect(apagarIaPreco).toHaveBeenCalledWith(1));
  });
});

describe("validarPreco", () => {
  const base = { ...camposIniciais(), provedor: "openai", modelo: "gpt-4o", entrada: "2,5", saida: "10", vigenteDesde: "2026-01-01" };

  it("* só no fim", () => {
    expect(validarPreco({ ...base, modelo: "gpt-4o*" }).preco?.modelo).toBe("gpt-4o*");
    expect(validarPreco({ ...base, modelo: "gpt*-4o" }).erros.modelo).toMatch(/só vale no fim/);
  });

  it("preço obrigatório, não negativo; cache vazio vira null", () => {
    expect(validarPreco({ ...base, entrada: "" }).erros.entrada).toBeDefined();
    expect(validarPreco({ ...base, saida: "-1" }).erros.saida).toBeDefined();
    expect(validarPreco({ ...base, cacheLeitura: "abc" }).erros.cacheLeitura).toBeDefined();
    expect(validarPreco(base).preco).toMatchObject({ entrada_por_1m: 2.5, cache_leitura_por_1m: null });
  });
});

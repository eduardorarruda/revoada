import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { ToastProvider } from "../../components";

const purgeIa = vi.fn();
vi.mock("../../api.ia", () => ({
  purgeIa: (...a: unknown[]) => purgeIa(...a),
  FRASE_PURGE_TODO_CONTEUDO: "APAGAR TODO O CONTEÚDO DE IA",
}));

const { DadosPrivacidade } = await import("./DadosPrivacidade");

beforeEach(() => {
  purgeIa.mockReset().mockResolvedValue(undefined);
});

function renderizar() {
  render(
    <ToastProvider>
      <DadosPrivacidade />
    </ToastProvider>,
  );
  // A seção é um <details> fechado: abre como a pessoa abriria.
  fireEvent.click(screen.getByText("Dados e privacidade"));
}

function bloco(nome: string): HTMLElement {
  return screen.getByRole("form", { name: nome });
}

function confirmar(botao: RegExp) {
  const dialogo = screen.getByRole("dialog");
  fireEvent.click(within(dialogo).getByRole("button", { name: botao }));
}

describe("Dados e privacidade", () => {
  it("apagar uma conversa: confirma antes e manda alvo + valor", async () => {
    renderizar();
    const form = bloco("Apagar uma conversa");
    fireEvent.change(within(form).getByLabelText("Id da conversa"), { target: { value: "  conv-8f2a " } });
    fireEvent.click(within(form).getByRole("button", { name: "Apagar conversa" }));
    expect(purgeIa).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog", { name: "Apagar a conversa conv-8f2a?" })).toHaveTextContent(/leva alguns segundos/);
    confirmar(/^Apagar a conversa conv-8f2a$/);
    await waitFor(() => expect(purgeIa).toHaveBeenCalledWith({ alvo: "conversa", valor: "conv-8f2a" }));
    expect(await screen.findByText(/Pedido para apagar a conversa conv-8f2a enviado\. .*alguns segundos/)).toBeInTheDocument();
    expect(within(form).getByLabelText("Id da conversa")).toHaveValue("");
  });

  it("conversa vazia não chama a API e diz o que falta", () => {
    renderizar();
    const form = bloco("Apagar uma conversa");
    fireEvent.click(within(form).getByRole("button", { name: "Apagar conversa" }));
    expect(within(form).getByRole("alert")).toHaveTextContent("Informe o id da conversa.");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("apagar uma execução valida o trace id em hexadecimal e manda em minúsculas", async () => {
    renderizar();
    const form = bloco("Apagar uma execução");
    const campo = within(form).getByLabelText("Id da execução (trace id)");
    fireEvent.change(campo, { target: { value: "xyz-123" } });
    fireEvent.click(within(form).getByRole("button", { name: "Apagar execução" }));
    expect(within(form).getByRole("alert")).toHaveTextContent(/só tem 0–9 e a–f/);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

    fireEvent.change(campo, { target: { value: "4BF92F3577B34DA6" } });
    fireEvent.click(within(form).getByRole("button", { name: "Apagar execução" }));
    confirmar(/^Apagar a execução 4bf92f3577b34da6$/);
    await waitFor(() => expect(purgeIa).toHaveBeenCalledWith({ alvo: "trace", valor: "4bf92f3577b34da6" }));
  });

  it("todo o conteúdo exige a frase exata e explica que custo e tokens continuam", async () => {
    renderizar();
    const form = bloco("Apagar todo o conteúdo gravado");
    expect(form).toHaveTextContent(/Custo, tokens, latência e o passo a passo das execuções continuam/);
    const botao = within(form).getByRole("button", { name: "Apagar todo o conteúdo" });
    const campo = within(form).getByLabelText("Para confirmar, digite a frase");
    expect(botao).toBeDisabled();

    fireEvent.change(campo, { target: { value: "apagar todo o conteudo de ia" } });
    expect(botao).toBeDisabled();
    expect(within(form).getByRole("alert")).toHaveTextContent("A frase ainda não confere");

    fireEvent.change(campo, { target: { value: "APAGAR TODO O CONTEÚDO DE IA" } });
    expect(botao).toBeEnabled();
    fireEvent.click(botao);
    expect(screen.getByRole("dialog")).toHaveTextContent(/Custo, tokens e o passo a passo continuam/);
    confirmar(/^Apagar todo o conteúdo gravado/);
    await waitFor(() =>
      expect(purgeIa).toHaveBeenCalledWith({ alvo: "todo_conteudo", frase: "APAGAR TODO O CONTEÚDO DE IA" }),
    );
  });

  it("falha do servidor vira toast de erro e mantém o que foi digitado", async () => {
    purgeIa.mockRejectedValue(new Error("400: nenhuma execução encontrada para esta conversa"));
    renderizar();
    const form = bloco("Apagar uma conversa");
    fireEvent.change(within(form).getByLabelText("Id da conversa"), { target: { value: "conv-x" } });
    fireEvent.click(within(form).getByRole("button", { name: "Apagar conversa" }));
    confirmar(/^Apagar a conversa conv-x$/);
    expect(await screen.findByText(/nenhuma execução encontrada para esta conversa/)).toBeInTheDocument();
    expect(within(form).getByLabelText("Id da conversa")).toHaveValue("conv-x");
  });
});

import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { IaMensagem } from "../../api.ia";
import { ToastProvider } from "../../components";
import { detalhe } from "./fixtures";

const getIaExecucao = vi.fn();
const getIaConteudo = vi.fn();
vi.mock("../../api.ia", () => ({
  getIaExecucao: (...a: unknown[]) => getIaExecucao(...a),
  getIaConteudo: (...a: unknown[]) => getIaConteudo(...a),
}));

const { Replay } = await import("./Replay");

const mensagens: IaMensagem[] = [
  { span_id: "a1", lado: "entrada", papel: "system", ordem: 0, texto: "Você é o atendente.", truncado: false, redigido: false },
  { span_id: "a1", lado: "entrada", papel: "user", ordem: 1, texto: "Meu CPF é [CPF]", truncado: false, redigido: true },
  { span_id: "a1", lado: "saida", papel: "assistant", ordem: 0, texto: "Vou consultar.", truncado: true, redigido: false },
  { span_id: "b7", lado: "entrada", papel: "tool", ordem: 0, texto: '{"cidade":"Recife"}', truncado: false, redigido: false },
];

beforeEach(() => {
  getIaExecucao.mockReset();
  getIaConteudo.mockReset();
});

function renderizar() {
  return render(
    <ToastProvider>
      <Replay traceId="4bf92f3577b34da6" janelaId="6h" versao={0} />
    </ToastProvider>,
  );
}

/** Título do painel de detalhe (o passo escolhido). */
const tituloDetalhe = () => document.querySelector(".ia-replay__detalhe .card__title")?.textContent;

describe("replay da execução", () => {
  it("cabeçalho com totais e os links para o waterfall e o host", async () => {
    getIaExecucao.mockResolvedValue(detalhe());
    renderizar();
    expect(await screen.findByRole("heading", { name: "Execução de Atendente" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Ver no waterfall/ })).toHaveAttribute("href", "#/traces?trace=4bf92f3577b34da6");
    expect(screen.getByRole("link", { name: /Ver host nesta hora/ }).getAttribute("href")).toMatch(/^#\/hosts\/web-01\?janela=/);
    expect(screen.getByRole("link", { name: "← Execuções" })).toHaveAttribute("href", "#/ia/execucoes?janela=6h");
  });

  it("← → andam entre os passos e o detalhe acompanha", async () => {
    getIaExecucao.mockResolvedValue(detalhe());
    renderizar();
    const lista = await screen.findByRole("list", { name: "Passos da execução" });
    const botoes = within(lista).getAllByRole("button");
    expect(tituloDetalhe()).toBe("Agente Atendente");
    expect(botoes[0]).toHaveAttribute("aria-current", "step");

    botoes[0].focus();
    fireEvent.keyDown(botoes[0], { key: "ArrowRight" });
    expect(tituloDetalhe()).toBe("Chamou o modelo gpt-4o-mini");
    expect(botoes[1]).toHaveFocus();

    fireEvent.keyDown(botoes[1], { key: "ArrowLeft" });
    expect(tituloDetalhe()).toBe("Agente Atendente");
    fireEvent.keyDown(botoes[0], { key: "ArrowLeft" }); // no começo, fica no começo
    expect(tituloDetalhe()).toBe("Agente Atendente");
    fireEvent.keyDown(botoes[0], { key: "End" });
    expect(botoes[botoes.length - 1]).toHaveAttribute("aria-current", "step");
  });

  it("destaca a repetição como possível loop e leva à primeira chamada", async () => {
    getIaExecucao.mockResolvedValue(detalhe());
    renderizar();
    const aviso = await screen.findByRole("region", { name: "clima chamada 3 vezes seguidas — possível loop" });
    expect(screen.getByText("repetição 1 de 3")).toBeInTheDocument();
    expect(screen.getByText("repetição 3 de 3")).toBeInTheDocument();
    fireEvent.click(within(aviso).getByRole("button", { name: "Ir para a primeira chamada" }));
    expect(tituloDetalhe()).toBe("Usou a ferramenta clima");
    // O foco vai junto: é o botão do passo escolhido (o 1º "clima", com o selo 1 de 3).
    const focado = document.activeElement as HTMLElement;
    expect(focado).toHaveAttribute("aria-current", "step");
    expect(within(focado).getByText("repetição 1 de 3")).toBeInTheDocument();
  });

  it("custo diz de onde veio e erro aparece em destaque", async () => {
    getIaExecucao.mockResolvedValue(detalhe());
    renderizar();
    await screen.findByRole("list", { name: "Passos da execução" });
    expect(screen.getByText(/estimado com preço de 01\/10\/2025/)).toBeInTheDocument();
    expect(screen.getAllByText(/sem preço · o modelo não tem preço cadastrado/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/sem tokens · a biblioteca não informou tokens/).length).toBeGreaterThan(0);
    expect(screen.getByText("RateLimitError: 429").closest("button")).toHaveClass("ia-passo--erro");
  });
});

describe("conversa", () => {
  it("captura desligada explica como ligar", async () => {
    getIaExecucao.mockResolvedValue(detalhe({ conteudo: { disponivel: false, pode_ver: true } }));
    renderizar();
    expect(await screen.findByRole("region", { name: "A captura de conteúdo está desligada" })).toBeInTheDocument();
    expect(screen.getByText("REVOADA_GENAI_CONTEUDO")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Mostrar conversa" })).not.toBeInTheDocument();
  });

  it("sem permissão explica, sem botão e sem buscar", async () => {
    getIaExecucao.mockResolvedValue(detalhe({ conteudo: { disponivel: true, pode_ver: false } }));
    renderizar();
    expect(await screen.findByRole("region", { name: "Sem permissão para ver a conversa" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Mostrar conversa" })).not.toBeInTheDocument();
    expect(getIaConteudo).not.toHaveBeenCalled();
  });

  it("403 do servidor vira a mesma explicação de permissão", async () => {
    getIaExecucao.mockResolvedValue(detalhe());
    getIaConteudo.mockRejectedValue(Object.assign(new Error("sem permissão"), { codigo: "sem_permissao" }));
    renderizar();
    fireEvent.click(await screen.findByRole("button", { name: "Mostrar conversa" }));
    expect(await screen.findByRole("region", { name: "Sem permissão para ver a conversa" })).toBeInTheDocument();
  });

  it("só busca no clique, e mostra papéis, redigido e truncado por passo", async () => {
    getIaExecucao.mockResolvedValue(detalhe());
    getIaConteudo.mockResolvedValue({ mensagens });
    renderizar();
    const botao = await screen.findByRole("button", { name: "Mostrar conversa" });
    expect(getIaConteudo).not.toHaveBeenCalled();
    fireEvent.click(botao);
    const grupo = await screen.findByRole("region", { name: "Chamou o modelo gpt-4o-mini" });
    expect(within(grupo).getByText("redigido")).toBeInTheDocument();
    expect(within(grupo).getByText("truncado")).toBeInTheDocument();
    expect(within(grupo).getByText("system")).toBeInTheDocument();
    const args = screen.getByText('{"cidade":"Recife"}');
    expect(args).toHaveClass("ia-mono");
  });
});

describe("copiar como requisição", () => {
  it("fica desligado até a conversa carregar e copia o JSON no formato OpenAI", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    getIaExecucao.mockResolvedValue(detalhe());
    getIaConteudo.mockResolvedValue({ mensagens });
    renderizar();

    const lista = await screen.findByRole("list", { name: "Passos da execução" });
    fireEvent.click(within(lista).getAllByRole("button")[1]); // o passo de modelo a1
    expect(screen.getByRole("button", { name: /Copiar como requisição/ })).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: "Mostrar conversa" }));
    await screen.findByRole("region", { name: "Chamou o modelo gpt-4o-mini" });
    const copiar = screen.getByRole("button", { name: /Copiar como requisição/ });
    expect(copiar).toBeEnabled();
    fireEvent.click(copiar);

    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    expect(JSON.parse(writeText.mock.calls[0][0] as string)).toEqual({
      model: "gpt-4o-mini",
      messages: [
        { role: "system", content: "Você é o atendente." },
        { role: "user", content: "Meu CPF é [CPF]" },
      ],
    });
    expect(await screen.findByText(/Requisição copiada no formato OpenAI.*redigidos ou truncados/)).toBeInTheDocument();
  });
});

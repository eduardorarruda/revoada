import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { IaMensagem } from "../../api.ia";
import { ToastProvider } from "../../components";
import { detalhe } from "./fixtures";

const getIaExecucao = vi.fn();
const getIaConteudo = vi.fn();
const purgeIa = vi.fn();
vi.mock("../../api.ia", () => ({
  getIaExecucao: (...a: unknown[]) => getIaExecucao(...a),
  getIaConteudo: (...a: unknown[]) => getIaConteudo(...a),
  purgeIa: (...a: unknown[]) => purgeIa(...a),
}));
let admin = false;
vi.mock("../../api", async (original) => ({
  ...(await original<typeof import("../../api")>()),
  isAdmin: () => admin,
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
  purgeIa.mockReset();
  admin = false;
});

function renderizar() {
  return render(
    <ToastProvider>
      <Replay traceId="4bf92f3577b34da6" janelaId="6h" versao={0} />
    </ToastProvider>,
  );
}

/** Nome do passo escolhido, no painel de detalhe. */
const tituloDetalhe = () => document.querySelector(".ia-replay__detalhe .ia-detalhe__titulo")?.textContent;

/** Aviso pelo título (o papel ARIA dele é detalhe de comum.tsx). */
async function aviso(titulo: string): Promise<HTMLElement> {
  const el = (await screen.findByText(titulo)).closest(".ia-aviso");
  if (!(el instanceof HTMLElement)) throw new Error(`aviso "${titulo}" não encontrado`);
  return el;
}

/** Finge a largura da tela: só a consulta de tela larga (>= 1100px) muda. */
function fingirTela(larga: boolean) {
  vi.stubGlobal("matchMedia", (q: string) => ({
    matches: q === "(min-width: 1100px)" ? larga : false,
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("replay da execução", () => {
  it("cabeçalho com totais e os links para o waterfall e o host", async () => {
    getIaExecucao.mockResolvedValue(detalhe());
    renderizar();
    expect(await screen.findByRole("heading", { name: "Execução de Atendente" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Ver na cascata do trace/ })).toHaveAttribute("href", "#/traces?trace=4bf92f3577b34da6");
    expect(screen.getByRole("link", { name: /Ver host nesta hora/ }).getAttribute("href")).toMatch(/^#\/hosts\/web-01\?janela=/);
    expect(screen.getByRole("link", { name: /Voltar às execuções/ })).toHaveAttribute("href", "#/ia/execucoes?janela=6h");
  });

  it("KPIs fora do cartão do cabeçalho, com custo nulo dito como não informado (nunca US$ 0)", async () => {
    getIaExecucao.mockResolvedValue(detalhe());
    const { container } = renderizar();
    await screen.findByRole("heading", { name: "Execução de Atendente" });
    const grade = container.querySelector(".kpi-grid");
    expect(grade).not.toBeNull();
    expect(grade?.closest(".card")).toBeNull();
    const custo = within(grade as HTMLElement).getByText("Custo").closest(".kpi") as HTMLElement;
    expect(custo).toHaveTextContent(/não informado · parcial/);
    expect(custo).not.toHaveTextContent(/US\$ 0/);
    expect(within(grade as HTMLElement).getByText("Com erro")).toBeInTheDocument();
  });

  it("↑ ↓ também andam, e o leitor de tela ouve a posição do passo", async () => {
    getIaExecucao.mockResolvedValue(detalhe());
    renderizar();
    const lista = await screen.findByRole("list", { name: "Passos da execução" });
    const botoes = within(lista).getAllByRole("button");
    expect(botoes[0]).toHaveTextContent(/^Passo 1 de 7\./);
    botoes[0].focus();
    fireEvent.keyDown(botoes[0], { key: "ArrowDown" });
    expect(botoes[1]).toHaveFocus();
    expect(botoes[1]).toHaveAttribute("aria-current", "step");
    expect(tituloDetalhe()).toBe("Chamou o modelo gpt-4o-mini");
    expect(document.querySelector(".ia-replay__detalhe .card__title")?.textContent).toBe("Passo 2 de 7");
    fireEvent.keyDown(botoes[1], { key: "ArrowUp" });
    expect(botoes[0]).toHaveFocus();
    expect(tituloDetalhe()).toBe("Agente Atendente");
    fireEvent.keyDown(botoes[0], { key: "Home" });
    expect(botoes[0]).toHaveAttribute("aria-current", "step");
  });

  it("passo com erro continua marcado como escolhido (a seleção não some)", async () => {
    getIaExecucao.mockResolvedValue(detalhe());
    renderizar();
    const lista = await screen.findByRole("list", { name: "Passos da execução" });
    const comErro = screen.getByText("RateLimitError: 429").closest("button") as HTMLElement;
    fireEvent.click(comErro);
    expect(comErro).toHaveClass("ia-passo--erro", "ia-passo--sel");
    expect(comErro.querySelector(".ia-passo__sel")).not.toBeNull();
    // Só um passo tem a pílula de seleção.
    expect(lista.querySelectorAll(".ia-passo__sel")).toHaveLength(1);
    // O erro aparece no detalhe como aviso crítico.
    const painel = document.querySelector(".ia-replay__detalhe") as HTMLElement;
    expect(within(painel).getByText("Este passo terminou com erro").closest(".ia-aviso")).toHaveClass("ia-aviso--crit");
  });

  it("detalhe traduz operação e motivo do fim, e guarda os ids em Identificadores", async () => {
    getIaExecucao.mockResolvedValue(detalhe());
    renderizar();
    const lista = await screen.findByRole("list", { name: "Passos da execução" });
    fireEvent.click(within(lista).getAllByRole("button")[1]); // modelo a1
    const painel = document.querySelector(".ia-replay__detalhe") as HTMLElement;
    expect(within(painel).getByText("conversa com o modelo")).toBeInTheDocument();
    expect(within(painel).getByText("(chat)")).toHaveClass("ia-mono");
    expect(within(painel).getByText("pediu para usar ferramenta")).toBeInTheDocument();
    expect(within(painel).getByText("entrada 120 · saída 30 · cache 100")).toBeInTheDocument();
    expect(within(painel).getByText("Identificadores").closest("details")).not.toHaveAttribute("open");
    expect(within(painel).getByText("Id do passo (span)")).toBeInTheDocument();
    expect(within(painel).getByText("a1")).toHaveClass("ia-mono");
  });

  it("tokens nulos viram uma frase só, nunca 'não informado → não informado'", async () => {
    getIaExecucao.mockResolvedValue(detalhe());
    renderizar();
    const lista = await screen.findByRole("list", { name: "Passos da execução" });
    const comErro = screen.getByText("RateLimitError: 429").closest("button") as HTMLElement;
    expect(within(comErro).getByText("tokens não informados")).toBeInTheDocument();
    expect(lista).not.toHaveTextContent(/não informado → não informado/);
    fireEvent.click(comErro);
    const painel = document.querySelector(".ia-replay__detalhe") as HTMLElement;
    expect(within(painel).getByText("tokens não informados")).toBeInTheDocument();
  });

  it("no celular, o detalhe abre dentro do passo escolhido", async () => {
    fingirTela(false);
    getIaExecucao.mockResolvedValue(detalhe());
    renderizar();
    const lista = await screen.findByRole("list", { name: "Passos da execução" });
    expect(document.querySelector(".ia-replay__detalhe")).toBeNull();
    const botoes = within(lista).getAllByRole("button", { current: "step" });
    const itemSel = botoes[0].closest("li") as HTMLElement;
    expect(itemSel.querySelector(".ia-detalhe__titulo")?.textContent).toBe("Agente Atendente");

    fireEvent.keyDown(botoes[0], { key: "ArrowDown" });
    const novo = within(lista).getByRole("button", { current: "step" }).closest("li") as HTMLElement;
    expect(novo).not.toBe(itemSel);
    expect(novo.querySelector(".ia-detalhe__titulo")?.textContent).toBe("Chamou o modelo gpt-4o-mini");
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
    const loop = await screen.findByRole("note", { name: "clima chamada 3 vezes seguidas: possível loop" });
    expect(screen.getByText("repetição 1 de 3")).toBeInTheDocument();
    expect(screen.getByText("repetição 3 de 3")).toBeInTheDocument();
    expect(document.querySelectorAll(".ia-passo__destaque")).toHaveLength(0);
    fireEvent.click(within(loop).getByRole("button", { name: "Ir para a primeira chamada" }));
    // Os três passos da repetição acendem (e só eles).
    expect(document.querySelectorAll(".ia-passo__destaque")).toHaveLength(3);
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
    const caixa = await aviso("A captura de conteúdo está desligada");
    // O "como ligar" fica recolhido, mas no DOM (Ctrl+F e leitor de tela acham).
    const comoLigar = within(caixa).getByText("Como ligar a captura").closest("details");
    expect(comoLigar).not.toHaveAttribute("open");
    expect(within(comoLigar as HTMLElement).getByText("REVOADA_GENAI_CONTEUDO")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Mostrar conversa" })).not.toBeInTheDocument();
  });

  it("sem permissão explica, sem botão e sem buscar", async () => {
    getIaExecucao.mockResolvedValue(detalhe({ conteudo: { disponivel: true, pode_ver: false } }));
    renderizar();
    expect(await aviso("Sem permissão para ver a conversa")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Mostrar conversa" })).not.toBeInTheDocument();
    expect(getIaConteudo).not.toHaveBeenCalled();
  });

  it("403 do servidor vira a mesma explicação de permissão", async () => {
    getIaExecucao.mockResolvedValue(detalhe());
    getIaConteudo.mockRejectedValue(Object.assign(new Error("sem permissão"), { codigo: "sem_permissao" }));
    renderizar();
    fireEvent.click(await screen.findByRole("button", { name: "Mostrar conversa" }));
    expect(await aviso("Sem permissão para ver a conversa")).toBeInTheDocument();
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
    // Papéis em português, com o valor cru no title.
    expect(within(grupo).getByText("instruções do sistema")).toHaveAttribute("title", "system");
    expect(within(grupo).getByText("pessoa")).toHaveAttribute("title", "user");
    expect(within(grupo).getByText("modelo", { selector: ".ia-chip" })).toHaveAttribute("title", "assistant");
    const args = screen.getByText('{"cidade":"Recife"}');
    expect(args).toHaveClass("ia-mono");
  });

  it("carregando mostra o bando e esqueletos no lugar do botão", async () => {
    getIaExecucao.mockResolvedValue(detalhe());
    getIaConteudo.mockReturnValue(new Promise(() => {}));
    renderizar();
    fireEvent.click(await screen.findByRole("button", { name: "Mostrar conversa" }));
    expect(screen.getByRole("status", { name: "Carregando a conversa" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Mostrar conversa" })).not.toBeInTheDocument();
  });

  it("mensagem longa chega recolhida e abre com Mostrar tudo", async () => {
    const longa = Array.from({ length: 20 }, (_, i) => `linha ${i + 1}`).join("\n");
    getIaExecucao.mockResolvedValue(detalhe());
    getIaConteudo.mockResolvedValue({
      mensagens: [{ span_id: "a1", lado: "saida", papel: "assistant", ordem: 0, texto: longa, truncado: false, redigido: false }],
    });
    renderizar();
    fireEvent.click(await screen.findByRole("button", { name: "Mostrar conversa" }));
    const botao = await screen.findByRole("button", { name: "Mostrar tudo" });
    expect(botao).toHaveAttribute("aria-expanded", "false");
    const recorte = document.getElementById(botao.getAttribute("aria-controls") ?? "");
    expect(recorte).toHaveClass("ia-msg__recorte--fechado");
    fireEvent.click(botao);
    expect(botao).toHaveAttribute("aria-expanded", "true");
    expect(botao).toHaveTextContent("Mostrar menos");
    expect(recorte).not.toHaveClass("ia-msg__recorte--fechado");
    // Mensagem curta não ganha o botão.
    expect(screen.getAllByRole("button", { name: /Mostrar (tudo|menos)/ })).toHaveLength(1);
  });

  it("o nome do passo na conversa leva até ele no passo a passo", async () => {
    getIaExecucao.mockResolvedValue(detalhe());
    getIaConteudo.mockResolvedValue({ mensagens });
    renderizar();
    fireEvent.click(await screen.findByRole("button", { name: "Mostrar conversa" }));
    fireEvent.click(await screen.findByRole("button", { name: "Usou a ferramenta clima" }));
    expect(tituloDetalhe()).toBe("Usou a ferramenta clima");
    expect(document.activeElement).toHaveAttribute("aria-current", "step");
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

describe("apagar a execução", () => {
  it("não aparece para quem não é admin", async () => {
    getIaExecucao.mockResolvedValue(detalhe());
    renderizar();
    await screen.findByRole("heading", { name: "Execução de Atendente" });
    expect(screen.queryByRole("button", { name: /Apagar esta execução/ })).not.toBeInTheDocument();
  });

  it("admin confirma, o trace é apagado e a tela volta para a lista", async () => {
    admin = true;
    purgeIa.mockResolvedValue(undefined);
    getIaExecucao.mockResolvedValue(detalhe());
    renderizar();
    fireEvent.click(await screen.findByRole("button", { name: /Apagar esta execução/ }));
    const dialogo = screen.getByRole("dialog");
    expect(dialogo).toHaveTextContent(/alguns segundos/);
    expect(purgeIa).not.toHaveBeenCalled();
    await act(async () => {
      fireEvent.click(within(dialogo).getByRole("button", { name: "Apagar esta execução" }));
    });
    expect(purgeIa).toHaveBeenCalledWith({ alvo: "trace", valor: "4bf92f3577b34da6" });
    await waitFor(() => expect(window.location.hash).toBe("#/ia/execucoes?janela=6h"));
    expect(await screen.findByText(/Execução apagada/)).toBeInTheDocument();
  });

  it("falha ao apagar avisa, devolve o botão e fica na tela", async () => {
    admin = true;
    window.location.hash = "#/ia/execucoes/4bf92f3577b34da6";
    purgeIa.mockRejectedValue(new Error("500: falhou no servidor"));
    getIaExecucao.mockResolvedValue(detalhe());
    renderizar();
    fireEvent.click(await screen.findByRole("button", { name: /Apagar esta execução/ }));
    await act(async () => {
      fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Apagar esta execução" }));
    });
    expect(purgeIa).toHaveBeenCalledTimes(1);
    expect(await screen.findByRole("button", { name: /Apagar esta execução/ })).toBeEnabled();
    expect(screen.queryByText(/Apagando/)).not.toBeInTheDocument();
    expect(window.location.hash).toBe("#/ia/execucoes/4bf92f3577b34da6");
    expect(screen.queryByText(/Execução apagada/)).not.toBeInTheDocument();
  });

  it("cancelar não apaga nada", async () => {
    admin = true;
    getIaExecucao.mockResolvedValue(detalhe());
    renderizar();
    fireEvent.click(await screen.findByRole("button", { name: /Apagar esta execução/ }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Cancelar" }));
    expect(purgeIa).not.toHaveBeenCalled();
  });
});

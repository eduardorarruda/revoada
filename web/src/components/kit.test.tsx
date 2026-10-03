import { act, fireEvent, render, screen } from "@testing-library/react";
import { ConfirmDialog, DataTable, InfoTip, Modal, ToastProvider, useToast } from "./index";

describe("Modal", () => {
  it("abre, fecha por Esc e prende o foco (Tab circula)", () => {
    const onClose = vi.fn();
    const { rerender } = render(
      <Modal open onClose={onClose} title="Título">
        <button>Um</button>
        <button>Dois</button>
      </Modal>,
    );
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    // foco preso: com o último focável ativo, Tab volta para o primeiro (✕ Fechar)
    const fechar = screen.getByLabelText("Fechar");
    screen.getByText("Dois").focus();
    fireEvent.keyDown(document, { key: "Tab" });
    expect(document.activeElement).toBe(fechar);

    // Esc fecha
    fireEvent.keyDown(document, { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);

    // open=false remove o diálogo
    rerender(
      <Modal open={false} onClose={onClose} title="Título">
        <button>Um</button>
      </Modal>,
    );
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});

// Regressão: o painel se atualiza sozinho a cada 5–15 s, e cada atualização
// re-renderiza a tela que abriu o modal — com um `onClose` NOVO (as telas passam
// arrow function inline). O efeito do foco dependia de `onClose`, então rodava de
// novo a cada atualização: devolvia o foco ao botão de fora e focava o ✕ do modal.
// Quem estava digitando perdia o cursor no meio da palavra.
describe("Modal — atualização da tela de trás", () => {
  it("não tira o foco do campo quando o pai re-renderiza com outro onClose", () => {
    const tela = (onClose: () => void) => (
      <Modal open onClose={onClose} title="Novo check">
        <input aria-label="Endereço" />
      </Modal>
    );
    const { rerender } = render(tela(() => {}));
    const campo = screen.getByLabelText("Endereço");
    campo.focus();
    expect(document.activeElement).toBe(campo);

    rerender(tela(() => {})); // o polling da tela de trás
    rerender(tela(() => {}));
    expect(document.activeElement).toBe(campo);
  });

  it("Esc chama o onClose MAIS RECENTE, não o da primeira renderização", () => {
    const primeiro = vi.fn();
    const atual = vi.fn();
    const { rerender } = render(
      <Modal open onClose={primeiro} title="T">
        <input aria-label="c" />
      </Modal>,
    );
    rerender(
      <Modal open onClose={atual} title="T">
        <input aria-label="c" />
      </Modal>,
    );
    fireEvent.keyDown(document, { key: "Escape" });
    expect(atual).toHaveBeenCalledTimes(1);
    expect(primeiro).not.toHaveBeenCalled();
  });
});

describe("ConfirmDialog", () => {
  it("não confirma com Enter — só com clique explícito", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    render(
      <ConfirmDialog
        open
        onCancel={onCancel}
        onConfirm={onConfirm}
        verb="Apagar"
        target="a regra CPU alta"
        consequences="O histórico é mantido por 30 dias."
        danger
      />,
    );
    // nome acessível do diálogo vem do título via aria-labelledby
    expect(screen.getByRole("dialog", { name: "Apagar a regra CPU alta?" })).toBeInTheDocument();

    // Enter NÃO deve confirmar
    fireEvent.keyDown(document, { key: "Enter" });
    expect(onConfirm).not.toHaveBeenCalled();

    // botão de confirmar rotula verbo + alvo; clique explícito confirma
    fireEvent.click(screen.getByRole("button", { name: "Apagar a regra CPU alta" }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });
});

describe("InfoTip", () => {
  it("alterna no clique e fecha com Esc", () => {
    render(<InfoTip text="Explicação do campo" title="Limite" />);
    const btn = screen.getByRole("button", { name: "Mais informações sobre Limite" });
    expect(screen.queryByRole("tooltip")).toBeNull();

    fireEvent.click(btn);
    expect(screen.getByRole("tooltip")).toBeInTheDocument();
    expect(screen.getByText("Explicação do campo")).toBeInTheDocument();

    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("tooltip")).toBeNull();
  });
});

describe("DataTable", () => {
  it("renderiza o empty state quando não há linhas", () => {
    render(
      <DataTable<{ a: string }>
        columns={[{ key: "a", label: "A" }]}
        rows={[]}
        keyFn={(r) => r.a}
        empty={<div>nada cadastrado ainda</div>}
      />,
    );
    expect(screen.getByText("nada cadastrado ainda")).toBeInTheDocument();
    expect(screen.queryByRole("table")).toBeNull();
  });
});

describe("Toast", () => {
  function Probe() {
    const t = useToast();
    return (
      <button onClick={() => t.success("Salvo com sucesso")}>disparar</button>
    );
  }

  it("mostra o toast de sucesso e ele some sozinho em 4s", () => {
    vi.useFakeTimers();
    try {
      render(
        <ToastProvider>
          <Probe />
        </ToastProvider>,
      );
      fireEvent.click(screen.getByText("disparar"));
      expect(screen.getByText("Salvo com sucesso")).toBeInTheDocument();

      act(() => {
        vi.advanceTimersByTime(4000);
      });
      expect(screen.queryByText("Salvo com sucesso")).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });
});

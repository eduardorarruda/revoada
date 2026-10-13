import { useState } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { HelpPanel } from "./HelpPanel";
import { Modal } from "./Modal";

// A gaveta de ajuda se declara role=dialog aria-modal: quem navega por teclado
// tem de cair dentro dela ao abrir, não conseguir sair com Tab e voltar ao botão
// que a abriu quando fecha. Antes ela só ouvia o Esc.
function ComBotao() {
  const [aberto, setAberto] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setAberto(true)}>
        abrir ajuda
      </button>
      <HelpPanel
        open={aberto}
        onClose={() => setAberto(false)}
        title="Ajuda dos logs"
        sections={[
          { heading: "Busca", body: <a href="#/docs">ver a sintaxe</a> },
          { heading: "Filtros", body: "texto" },
        ]}
      />
    </>
  );
}

function abrir() {
  const botao = screen.getByRole("button", { name: "abrir ajuda" });
  botao.focus();
  fireEvent.click(botao);
  return botao;
}

describe("HelpPanel — foco de diálogo modal", () => {
  it("ao abrir, leva o foco para dentro do painel", () => {
    render(<ComBotao />);
    abrir();
    const painel = screen.getByRole("dialog", { name: "Ajuda dos logs" });
    expect(painel).toContainElement(document.activeElement as HTMLElement);
    expect(screen.getByRole("button", { name: /fechar/ })).toHaveFocus();
  });

  it("Tab no último focável volta ao primeiro; Shift+Tab no primeiro vai ao último", () => {
    render(<ComBotao />);
    abrir();
    const fechar = screen.getByRole("button", { name: /fechar/ });
    const link = screen.getByRole("link", { name: "ver a sintaxe" });

    link.focus();
    fireEvent.keyDown(document, { key: "Tab" });
    expect(fechar).toHaveFocus();

    fireEvent.keyDown(document, { key: "Tab", shiftKey: true });
    expect(link).toHaveFocus();
  });

  it("Esc fecha e devolve o foco ao botão que abriu", () => {
    render(<ComBotao />);
    const botao = abrir();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(botao).toHaveFocus();
  });

  it("os títulos das seções são cabeçalhos (h3), navegáveis pelo leitor de tela", () => {
    render(<ComBotao />);
    abrir();
    const titulos = screen.getAllByRole("heading", { level: 3 }).map((h) => h.textContent);
    expect(titulos).toEqual(["Busca", "Filtros"]);
  });
});

describe("diálogos empilhados", () => {
  // O "Contexto" dos Logs abre por cima do drill-down do histograma: um Esc tem
  // de fechar só o de cima, e o foco volta para dentro do de baixo.
  function Empilhados({ fecharBaixo, fecharCima }: { fecharBaixo: () => void; fecharCima: () => void }) {
    const [cima, setCima] = useState(false);
    return (
      <Modal open onClose={fecharBaixo} title="De baixo">
        <button type="button" onClick={() => setCima(true)}>
          abrir o de cima
        </button>
        <Modal
          open={cima}
          onClose={() => {
            fecharCima();
            setCima(false);
          }}
          title="De cima"
        >
          <p>conteúdo</p>
        </Modal>
      </Modal>
    );
  }

  it("Esc fecha só o diálogo do topo e devolve o foco ao de baixo", () => {
    const fecharBaixo = vi.fn();
    const fecharCima = vi.fn();
    render(<Empilhados fecharBaixo={fecharBaixo} fecharCima={fecharCima} />);
    const abridor = screen.getByRole("button", { name: "abrir o de cima" });
    abridor.focus();
    fireEvent.click(abridor);
    expect(screen.getByRole("dialog", { name: "De cima" })).toBeInTheDocument();

    fireEvent.keyDown(document, { key: "Escape" });
    expect(fecharCima).toHaveBeenCalledTimes(1);
    expect(fecharBaixo).not.toHaveBeenCalled();
    expect(abridor).toHaveFocus();

    fireEvent.keyDown(document, { key: "Escape" });
    expect(fecharBaixo).toHaveBeenCalledTimes(1);
  });
});

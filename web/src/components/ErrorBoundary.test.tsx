import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { ErrorBoundary } from "./ErrorBoundary";

// Sem barreira, uma exceção em um único gráfico apagava o painel INTEIRO — menu,
// topo, tudo — e a pessoa via uma tela preta sem explicação.
describe("ErrorBoundary", () => {
  function Quebra({ quebrar }: { quebrar: boolean }) {
    if (quebrar) throw new Error("bbox sem números");
    return <p>conteúdo ok</p>;
  }

  it("mostra o erro explicado em vez de apagar a tela, e tenta de novo", () => {
    const silenciar = vi.spyOn(console, "error").mockImplementation(() => {});
    function Tela() {
      const [quebrar, setQuebrar] = useState(true);
      return (
        <>
          <button onClick={() => setQuebrar(false)}>consertar</button>
          <ErrorBoundary>
            <Quebra quebrar={quebrar} />
          </ErrorBoundary>
        </>
      );
    }
    render(<Tela />);
    expect(screen.getByRole("alert")).toHaveTextContent("Esta tela encontrou um erro");
    expect(screen.getByRole("alert")).toHaveTextContent("bbox sem números");

    fireEvent.click(screen.getByText("consertar"));
    fireEvent.click(screen.getByRole("button", { name: "Tentar de novo" }));
    expect(screen.getByText("conteúdo ok")).toBeInTheDocument();
    silenciar.mockRestore();
  });
});

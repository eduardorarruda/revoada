import { render, screen } from "@testing-library/react";
import { Kpi, KpiGrid } from "./Kpi";

describe("Kpi", () => {
  it("valor ausente vira travessão, nunca zero, e o motivo vai na linha de baixo", () => {
    render(<Kpi rotulo="Custo" valor={null} sub="não informado" />);
    expect(screen.getByText("—")).toBeInTheDocument();
    expect(screen.getByText("não informado")).toHaveClass("kpi__sub");
    expect(screen.queryByText(/0/)).not.toBeInTheDocument();
  });

  it("moeda antes do número fica fora do número e menor", () => {
    const { container } = render(<Kpi rotulo="Custo" valor="US$ 12,40" />);
    const moeda = screen.getByText("US$");
    expect(moeda).toHaveClass("kpi__unidade", "kpi__unidade--antes");
    // A moeda vem antes do número, dentro do mesmo valor.
    expect(container.querySelector(".kpi__valor")?.firstElementChild).toBe(moeda);
  });

  it("unidade depois do número continua menor ao lado; prefixo que não é moeda fica no texto", () => {
    render(
      <KpiGrid className="extra">
        <Kpi rotulo="Latência" valor="4,2 s" />
        <Kpi rotulo="Duração" valor="≥ 900 ms" />
      </KpiGrid>,
    );
    expect(screen.getByText("s")).toHaveClass("kpi__unidade");
    expect(screen.getByText("ms")).toHaveClass("kpi__unidade");
    expect(document.querySelector(".kpi__unidade--antes")).toBeNull();
    expect(document.querySelector(".kpi-grid")).toHaveClass("extra");
  });

  it("carregando mostra esqueleto no lugar do número e esconde a linha de contexto", () => {
    render(<Kpi rotulo="Custo" valor={null} sub="não informado" loading />);
    expect(screen.queryByText("—")).not.toBeInTheDocument();
    expect(screen.queryByText("não informado")).not.toBeInTheDocument();
  });
});

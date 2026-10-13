import { render, screen } from "@testing-library/react";
import { RodapeLegal } from "./RodapeLegal";

describe("RodapeLegal", () => {
  it("leva à política de privacidade e ao código aberto, numa navegação nomeada", () => {
    render(<RodapeLegal />);
    expect(screen.getByRole("navigation", { name: "Informações legais" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Política de privacidade" })).toHaveAttribute("href", "#/privacidade");
    const codigo = screen.getByRole("link", { name: /código aberto/ });
    expect(codigo).toHaveAttribute("rel", "noopener noreferrer");
  });
});

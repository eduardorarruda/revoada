import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Bando } from "./Bando";

describe("Bando", () => {
  it("anuncia o estado para leitores de tela e desenha cinco pássaros", () => {
    const { container } = render(<Bando rotulo="Carregando a tela" />);
    expect(screen.getByRole("status", { name: "Carregando a tela" })).toBeTruthy();
    expect(container.querySelectorAll(".bando__ave")).toHaveLength(5);
  });
});

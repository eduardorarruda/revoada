import { render, screen } from "@testing-library/react";
import { Badge, FreshnessIndicator } from "./index";

describe("Badge", () => {
  it("comunica estado por ícone + rótulo, não só por cor", () => {
    render(<Badge state="crit">DOWN</Badge>);
    const badge = screen.getByText("DOWN").closest(".badge")!;
    expect(badge).toHaveClass("badge--crit");
    // há um ícone além do texto (acessibilidade — não depende de cor)
    expect(badge.querySelector(".badge__icon")).not.toBeNull();
  });
});

describe("FreshnessIndicator", () => {
  it("mostra a idade quando ao vivo", () => {
    render(<FreshnessIndicator status="live" ageSeconds={5} />);
    expect(screen.getByRole("status")).toHaveTextContent("ao vivo · 5s");
  });

  it("indica reconexão", () => {
    render(<FreshnessIndicator status="reconnecting" />);
    expect(screen.getByRole("status")).toHaveTextContent("reconectando");
  });
});

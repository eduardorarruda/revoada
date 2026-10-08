import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { MODELOS_IA, ModelosIA } from "./ModelosIA";

describe("ModelosIA", () => {
  it("cada modelo usa uma série llm.* com agregação que faz sentido", () => {
    for (const m of MODELOS_IA) {
      expect(m.preset.metric.startsWith("llm.")).toBe(true);
      // p95 por minuto não se soma nem se tira média: só máximo faz sentido
      if (m.preset.metric === "llm.latencia_p95_ms") expect(m.preset.agg).toBe("max");
      if (m.preset.metric === "llm.custo_usd") expect(m.preset.agg).toBe("sum");
    }
  });

  it("escolher um modelo entrega o preset ao assistente", () => {
    const escolher = vi.fn();
    render(<ModelosIA aberto onFechar={() => {}} onEscolher={escolher} />);
    fireEvent.click(screen.getByRole("button", { name: "Gasto com IA na última hora" }));
    expect(escolher).toHaveBeenCalledWith(expect.objectContaining({ metric: "llm.custo_usd", window_seconds: 3600 }));
  });

  it("cada modelo é um cartão de escolha .opcao: nome = título, explicação como descrição", () => {
    render(<ModelosIA aberto onFechar={() => {}} onEscolher={() => {}} />);
    const botoes = screen.getAllByRole("button").filter((b) => b.classList.contains("opcao"));
    expect(botoes).toHaveLength(MODELOS_IA.length);
    const loop = screen.getByRole("button", { name: "Agente em loop" });
    expect(loop).toHaveClass("opcao");
    expect(loop).toHaveAccessibleDescription(/fez mais de N chamadas de modelo/);
    expect(loop.querySelector(".opcao__icone")).not.toBeNull();
    // Sem estilo inline nem classes inexistentes.
    expect(document.querySelector("[style]")).toBeNull();
    expect(document.querySelector(".muted, .modelos-ia")).toBeNull();
    // Sem travessão no texto da interface.
    expect(document.body.textContent).not.toContain("—");
  });
});

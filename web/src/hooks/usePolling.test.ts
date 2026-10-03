import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { renderHook } from "@testing-library/react";
import { act } from "react";
import { usePolling } from "./usePolling";

// Controla document.visibilityState (jsdom não deixa atribuir direto).
function setVisibilidade(estado: DocumentVisibilityState) {
  Object.defineProperty(document, "visibilityState", { value: estado, configurable: true });
  document.dispatchEvent(new Event("visibilitychange"));
}

beforeEach(() => {
  vi.useFakeTimers();
  Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
});

afterEach(() => {
  vi.useRealTimers();
});

describe("usePolling", () => {
  it("busca uma vez no boot mesmo com a aba escondida", () => {
    // O defeito: a TV/kiosk sobe com a aba em segundo plano e o boot era suprimido —
    // a tela ficava em "Carregando…" para sempre, sem erro e sem idade. Medido:
    // /#/tv/<token> passou 52 s sem uma única chamada.
    Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true });
    const fn = vi.fn();
    renderHook(() => usePolling(fn, 5000, []));
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("não REPETE enquanto a aba está escondida", () => {
    Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true });
    const fn = vi.fn();
    renderHook(() => usePolling(fn, 5000, []));
    act(() => {
      vi.advanceTimersByTime(30000);
    });
    expect(fn).toHaveBeenCalledTimes(1); // só o boot
  });

  it("com a aba visível, repete no intervalo", () => {
    const fn = vi.fn();
    renderHook(() => usePolling(fn, 5000, []));
    expect(fn).toHaveBeenCalledTimes(1);
    act(() => {
      vi.advanceTimersByTime(10000);
    });
    expect(fn).toHaveBeenCalledTimes(3);
  });

  it("ao voltar a aba, refaz a busca na hora e retoma o intervalo", () => {
    const fn = vi.fn();
    renderHook(() => usePolling(fn, 5000, []));
    act(() => setVisibilidade("hidden"));
    act(() => {
      vi.advanceTimersByTime(20000);
    });
    expect(fn).toHaveBeenCalledTimes(1);
    act(() => setVisibilidade("visible"));
    expect(fn).toHaveBeenCalledTimes(2); // refetch imediato
    act(() => {
      vi.advanceTimersByTime(5000);
    });
    expect(fn).toHaveBeenCalledTimes(3);
  });

  it("não chama depois de desmontado", () => {
    const fn = vi.fn();
    const { unmount } = renderHook(() => usePolling(fn, 5000, []));
    unmount();
    act(() => {
      vi.advanceTimersByTime(30000);
    });
    expect(fn).toHaveBeenCalledTimes(1);
  });
});

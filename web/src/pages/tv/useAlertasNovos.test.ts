import { act, renderHook } from "@testing-library/react";
import { vi } from "vitest";
import type { TVAlert, TVStatus } from "../../api";
import { useAlertasNovos } from "./useAlertasNovos";

const alerta = (id: string, severity: string, since = "2026-09-27T12:00:00Z"): TVAlert => ({
  id,
  rule: `Regra ${id}`,
  host: "srv",
  severity,
  since,
});
const status = (alerts: TVAlert[]): TVStatus => ({ criticals: [], warnings: [], deploys: [], alerts });

describe("useAlertasNovos — o aviso de alerta novo da TV", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("a primeira leitura só semeia: alertas já ativos no boot não viram aviso", () => {
    const onNovos = vi.fn();
    const { result } = renderHook(({ s }) => useAlertasNovos(s, { onNovos }), {
      initialProps: { s: status([alerta("a", "critical")]) as TVStatus | null },
    });
    expect(result.current.burst).toBeNull();
    expect(onNovos).not.toHaveBeenCalled();
  });

  it("alerta novo de atenção vira aviso por 30 s; info não", () => {
    const { result, rerender } = renderHook(({ s }) => useAlertasNovos(s), { initialProps: { s: status([]) as TVStatus | null } });
    rerender({ s: status([alerta("i", "info")]) });
    expect(result.current.burst).toBeNull();
    rerender({ s: status([alerta("i", "info"), alerta("w", "warning")]) });
    expect(result.current.burst?.id).toBe("w");
    act(() => {
      vi.advanceTimersByTime(30000);
    });
    expect(result.current.burst).toBeNull();
  });

  it("vários novos: o mais grave primeiro e os demais no '+N'", () => {
    const { result, rerender } = renderHook(({ s }) => useAlertasNovos(s), { initialProps: { s: status([]) as TVStatus | null } });
    rerender({ s: status([alerta("w", "warning"), alerta("c", "critical")]) });
    expect(result.current.burst?.id).toBe("c");
    expect(result.current.pendentes).toBe(1);
  });

  it("onNovos recebe os graves de cada leitura e atrasoMs segura o aviso", () => {
    const onNovos = vi.fn();
    const { result, rerender } = renderHook(({ s }) => useAlertasNovos(s, { atrasoMs: 1200, onNovos }), {
      initialProps: { s: status([]) as TVStatus | null },
    });
    rerender({ s: status([alerta("w", "warning"), alerta("i", "info")]) });
    expect(onNovos).toHaveBeenCalledTimes(1);
    expect(onNovos.mock.calls[0][0].map((a: TVAlert) => a.id)).toEqual(["w"]);
    expect(result.current.burst).toBeNull();
    act(() => {
      vi.advanceTimersByTime(1200);
    });
    expect(result.current.burst?.id).toBe("w");
  });

  it("desligado (ativo: false) não detecta nada", () => {
    const onNovos = vi.fn();
    const { result, rerender } = renderHook(({ s }) => useAlertasNovos(s, { ativo: false, onNovos }), {
      initialProps: { s: status([]) as TVStatus | null },
    });
    rerender({ s: status([alerta("c", "critical")]) });
    expect(result.current.burst).toBeNull();
    expect(onNovos).not.toHaveBeenCalled();
  });

  it("o mesmo alerta com `since` novo (resolveu e voltou) conta como novo", () => {
    const { result, rerender } = renderHook(({ s }) => useAlertasNovos(s), {
      initialProps: { s: status([alerta("w", "warning", "2026-09-27T10:00:00Z")]) as TVStatus | null },
    });
    rerender({ s: status([alerta("w", "warning", "2026-09-27T11:00:00Z")]) });
    expect(result.current.burst?.since).toBe("2026-09-27T11:00:00Z");
  });
});

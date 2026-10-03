import { describe, it, expect } from "vitest";
import { dataFreshness } from "../format";

describe("dataFreshness — sem dado recente vs fora do ar", () => {
  const now = Date.now();

  it("dado recente é fresh", () => {
    const r = dataFreshness(new Date(now - 30_000), 120);
    expect(r.state).toBe("fresh");
    expect(r.ageSeconds).toBeGreaterThanOrEqual(29);
  });

  it("dado além do intervalo é stale (sem dado recente, NÃO 'fora do ar')", () => {
    const r = dataFreshness(new Date(now - 5 * 60_000), 120);
    expect(r.state).toBe("stale");
  });

  it("timestamp ausente/inválido é nodata (nunca recebeu)", () => {
    expect(dataFreshness(null).state).toBe("nodata");
    expect(dataFreshness("").state).toBe("nodata");
    expect(dataFreshness("não é data").state).toBe("nodata");
  });

  it("respeita o intervalo configurado", () => {
    const ts = new Date(now - 90_000);
    expect(dataFreshness(ts, 60).state).toBe("stale");
    expect(dataFreshness(ts, 120).state).toBe("fresh");
  });
});

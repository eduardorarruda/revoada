// Modal de configuração dos limiares de saúde (semáforo) — admin. Edita os três
// limiares globais (CPU/RAM/Disco, warn/crit) e overrides por host. Sem esta UI,
// "configurável" só existiria via SQL. Autocontido; segue o estilo dos demais modais de admin.
import { useCallback, useEffect, useMemo, useState } from "react";
import { Button, Modal, useToast } from "../components";
import { listHostThresholds, saveHostThresholds, type HostThreshold } from "../api";
import { invalidateHostThresholds } from "../hooks/useHostThresholds";

type Metric = HostThreshold["metric"];

const METRICS: Metric[] = ["cpu", "mem", "disk"];
const METRIC_LABEL: Record<Metric, string> = { cpu: "CPU", mem: "RAM", disk: "Disco" };

// Defaults embutidos (§4.1 do plano): disco é mais rígido (75/90) que CPU/RAM (70/90).
const DEFAULTS: Record<Metric, { warn: number; crit: number }> = {
  cpu: { warn: 70, crit: 90 },
  mem: { warn: 70, crit: 90 },
  disk: { warn: 75, crit: 90 },
};

// Estado editável de um par warn/crit — mantido como string para permitir campos
// temporariamente vazios enquanto o admin digita.
interface Pair {
  warn: string;
  crit: string;
}
interface OverrideRow extends Pair {
  id: number;
  hostname: string;
  metric: Metric;
}

function errMsg(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

// Valida um par warn/crit: ambos inteiros e 0 ≤ warn < crit ≤ 100.
function validatePair(p: Pair, ctx: string): string | null {
  const warn = Number(p.warn);
  const crit = Number(p.crit);
  if (p.warn.trim() === "" || p.crit.trim() === "" || !Number.isFinite(warn) || !Number.isFinite(crit)) {
    return `${ctx}: preencha Alerta e Crítico com números.`;
  }
  if (warn < 0 || crit > 100) return `${ctx}: os valores devem ficar entre 0 e 100.`;
  if (warn >= crit) return `${ctx}: o Alerta (${warn}) deve ser menor que o Crítico (${crit}).`;
  return null;
}

let nextId = 1;

export function HealthThresholdsModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const toast = useToast();
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [globals, setGlobals] = useState<Record<Metric, Pair>>(() => defaultGlobals());
  const [overrides, setOverrides] = useState<OverrideRow[]>([]);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const r = await listHostThresholds();
      const g = defaultGlobals();
      const ov: OverrideRow[] = [];
      for (const t of r.thresholds) {
        if (t.hostname === "") {
          g[t.metric] = { warn: String(t.warn), crit: String(t.crit) };
        } else {
          ov.push({ id: nextId++, hostname: t.hostname, metric: t.metric, warn: String(t.warn), crit: String(t.crit) });
        }
      }
      setGlobals(g);
      setOverrides(ov);
    } catch (e) {
      toast.error(`Erro ao carregar limiares: ${errMsg(e)}`);
    } finally {
      setLoading(false);
    }
  }, [toast]);

  useEffect(() => {
    if (!open) return;
    void load();
  }, [open, load]);

  const setGlobal = (m: Metric, field: keyof Pair, value: string) =>
    setGlobals((prev) => ({ ...prev, [m]: { ...prev[m], [field]: value } }));

  const setOverride = (id: number, patch: Partial<OverrideRow>) =>
    setOverrides((prev) => prev.map((o) => (o.id === id ? { ...o, ...patch } : o)));

  const addOverride = () =>
    setOverrides((prev) => [...prev, { id: nextId++, hostname: "", metric: "cpu", warn: "70", crit: "90" }]);

  const removeOverride = (id: number) => setOverrides((prev) => prev.filter((o) => o.id !== id));

  const error = useMemo<string | null>(() => {
    for (const m of METRICS) {
      const err = validatePair(globals[m], `Global ${METRIC_LABEL[m]}`);
      if (err) return err;
    }
    const seen = new Set<string>();
    for (const o of overrides) {
      const host = o.hostname.trim();
      if (!host) return "Todos os overrides precisam de um host.";
      const key = `${host}|${o.metric}`;
      if (seen.has(key)) return `Override duplicado para ${host} / ${METRIC_LABEL[o.metric]}.`;
      seen.add(key);
      const err = validatePair(o, `Override ${host} / ${METRIC_LABEL[o.metric]}`);
      if (err) return err;
    }
    return null;
  }, [globals, overrides]);

  async function onSave() {
    if (error) {
      toast.error(error);
      return;
    }
    const payload: HostThreshold[] = [
      ...METRICS.map((m) => ({ hostname: "", metric: m, warn: Number(globals[m].warn), crit: Number(globals[m].crit) })),
      ...overrides.map((o) => ({ hostname: o.hostname.trim(), metric: o.metric, warn: Number(o.warn), crit: Number(o.crit) })),
    ];
    setSaving(true);
    try {
      await saveHostThresholds(payload);
      // Os gauges e barras dos dashboards leem estes limiares de um cache em módulo;
      // sem invalidar, continuariam coloridos pelo limiar antigo até recarregar a página.
      invalidateHostThresholds();
      toast.success("Limiares salvos. Os gauges e o Mural passam a usar os novos valores.");
      onClose();
    } catch (e) {
      toast.error(`Erro ao salvar: ${errMsg(e)}`);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Limiares de saúde"
      wide
      footer={
        <>
          <Button onClick={onClose} disabled={saving}>Cancelar</Button>
          <Button variant="primary" onClick={() => void onSave()} disabled={saving || loading || error !== null}>
            {saving ? "Salvando…" : "Salvar"}
          </Button>
        </>
      }
    >
      <p style={{ color: "var(--text-2)", fontSize: "var(--fs-13)", marginTop: 0 }}>
        O semáforo do Mural de Saúde compara o uso de cada recurso com estes limiares (em %):{" "}
        <strong style={{ color: "var(--ok)" }}>Saudável</strong> abaixo do Alerta,{" "}
        <strong style={{ color: "var(--warn)" }}>Alerta</strong> entre Alerta e Crítico e{" "}
        <strong style={{ color: "var(--crit)" }}>Crítico</strong> acima do Crítico. O estado do card é o{" "}
        <strong>pior</strong> entre os recursos e os alertas ativos. Regra: 0 ≤ Alerta &lt; Crítico ≤ 100.
      </p>

      {loading ? (
        <p style={{ color: "var(--text-2)" }}>Carregando…</p>
      ) : (
        <>
          {/* Limiares globais */}
          <h3 style={{ fontSize: "var(--fs-14)", margin: "var(--sp-3) 0 var(--sp-2)" }}>Limiares globais</h3>
          <p style={{ color: "var(--text-3)", fontSize: "var(--fs-12)", marginTop: 0 }}>
            Aplicados a todos os hosts que não têm um override próprio abaixo.
          </p>
          <div style={{ overflowX: "auto" }}>
            <table className="dtable" style={{ width: "100%" }}>
              <thead>
                <tr>
                  <th>Recurso</th>
                  <th>Alerta (%)</th>
                  <th>Crítico (%)</th>
                </tr>
              </thead>
              <tbody>
                {METRICS.map((m) => (
                  <tr key={m}>
                    <td>{METRIC_LABEL[m]}</td>
                    <td>
                      <PctInput
                        value={globals[m].warn}
                        onChange={(v) => setGlobal(m, "warn", v)}
                        label={`Alerta global ${METRIC_LABEL[m]}`}
                      />
                    </td>
                    <td>
                      <PctInput
                        value={globals[m].crit}
                        onChange={(v) => setGlobal(m, "crit", v)}
                        label={`Crítico global ${METRIC_LABEL[m]}`}
                      />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          {/* Overrides por host */}
          <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginTop: "var(--sp-4)" }}>
            <h3 style={{ fontSize: "var(--fs-14)", margin: 0 }}>Overrides por host</h3>
            <Button onClick={addOverride}>Adicionar override</Button>
          </div>
          <p style={{ color: "var(--text-3)", fontSize: "var(--fs-12)", marginTop: "var(--sp-1)" }}>
            Um limiar específico para um host e recurso, que substitui o global correspondente.
          </p>

          {overrides.length === 0 ? (
            <p style={{ color: "var(--text-2)", fontSize: "var(--fs-13)" }}>
              Nenhum override. Todos os hosts usam os limiares globais.
            </p>
          ) : (
            <div style={{ overflowX: "auto" }}>
              <table className="dtable" style={{ width: "100%" }}>
                <thead>
                  <tr>
                    <th>Host</th>
                    <th>Recurso</th>
                    <th>Alerta (%)</th>
                    <th>Crítico (%)</th>
                    <th style={{ textAlign: "right" }}>Ações</th>
                  </tr>
                </thead>
                <tbody>
                  {overrides.map((o) => (
                    <tr key={o.id}>
                      <td>
                        <input
                          className="field"
                          placeholder="ex.: app-prod-01"
                          value={o.hostname}
                          onChange={(e) => setOverride(o.id, { hostname: e.target.value })}
                          aria-label="Host do override"
                          style={{ minWidth: 160 }}
                        />
                      </td>
                      <td>
                        <select
                          className="field"
                          value={o.metric}
                          onChange={(e) => setOverride(o.id, { metric: e.target.value as Metric })}
                          aria-label="Recurso do override"
                        >
                          {METRICS.map((m) => (
                            <option key={m} value={m}>
                              {METRIC_LABEL[m]}
                            </option>
                          ))}
                        </select>
                      </td>
                      <td>
                        <PctInput value={o.warn} onChange={(v) => setOverride(o.id, { warn: v })} label="Alerta do override" />
                      </td>
                      <td>
                        <PctInput value={o.crit} onChange={(v) => setOverride(o.id, { crit: v })} label="Crítico do override" />
                      </td>
                      <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                        <Button variant="ghost" onClick={() => removeOverride(o.id)}>
                          Remover
                        </Button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {error && (
            <p role="alert" style={{ color: "var(--crit)", fontSize: "var(--fs-13)", marginTop: "var(--sp-3)", fontWeight: 600 }}>
              {error}
            </p>
          )}
        </>
      )}
    </Modal>
  );
}

function PctInput({ value, onChange, label }: { value: string; onChange: (v: string) => void; label: string }) {
  return (
    <input
      className="field tabular"
      type="number"
      min={0}
      max={100}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      aria-label={label}
      style={{ width: 90 }}
    />
  );
}

function defaultGlobals(): Record<Metric, Pair> {
  return {
    cpu: { warn: String(DEFAULTS.cpu.warn), crit: String(DEFAULTS.cpu.crit) },
    mem: { warn: String(DEFAULTS.mem.warn), crit: String(DEFAULTS.mem.crit) },
    disk: { warn: String(DEFAULTS.disk.warn), crit: String(DEFAULTS.disk.crit) },
  };
}

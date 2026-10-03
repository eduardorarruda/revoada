// Painel lateral de correlação (P5.4): de qualquer ponto/série → traces, logs e
// deploys daquele instante/serviço, sem trocar de tela. Ouve o evento revoada:correlate.
import { useEffect, useState } from "react";
import { Badge, Button } from "./index";
import { correlate, getTrace, searchLogs, type CorrelationData, type CorrelationRequest, type LogRow, type TraceSpan } from "../api";
import { formatTime } from "../format";

const SEV_CRIT = new Set(["ERROR", "FATAL"]);
// Horário de Brasília (formatTime), não o do navegador: o drawer correlaciona
// logs/traces com o instante clicado no gráfico, e os dois têm de bater.
function fmt(ms: number): string {
  return formatTime(ms);
}

export function CorrelationDrawer() {
  const [req, setReq] = useState<CorrelationRequest | null>(null);
  const [data, setData] = useState<CorrelationData | null>(null);
  const [traceLogs, setTraceLogs] = useState<LogRow[] | null>(null);
  const [spans, setSpans] = useState<TraceSpan[] | null>(null);

  useEffect(() => {
    const onOpen = (e: Event) => {
      const detail = (e as CustomEvent<CorrelationRequest>).detail;
      setReq(detail);
      setData(null);
      setTraceLogs(null);
      setSpans(null);
    };
    window.addEventListener("revoada:correlate", onOpen);
    return () => window.removeEventListener("revoada:correlate", onOpen);
  }, []);

  useEffect(() => {
    if (!req) return;
    if (req.traceId) {
      // correlação exata por trace_id: logs + spans daquele trace.
      searchLogs({ q: "", limit: 200 }).catch(() => []); // warm
      Promise.all([searchLogsByTrace(req.traceId), getTrace(req.traceId).catch(() => [])]).then(([l, s]) => {
        setTraceLogs(l);
        setSpans(s);
      });
    }
    correlate({ service: req.service, host: req.host, ts: req.tsMs }).then(setData).catch(() => setData(null));
  }, [req]);

  if (!req) return null;

  return (
    <div style={{ position: "fixed", top: 0, right: 0, bottom: 0, width: 460, maxWidth: "92vw", background: "var(--bg-1)", borderLeft: "1px solid var(--border)", boxShadow: "-8px 0 24px rgba(0,0,0,.35)", zIndex: 60, overflow: "auto", padding: "var(--sp-4)" }}>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: "var(--sp-2)" }}>
        <strong style={{ fontSize: "var(--fs-16)" }}>
          Correlação {req.service ? `· ${req.service}` : req.host ? `· ${req.host}` : req.traceId ? "· trace" : ""}
        </strong>
        <Button variant="ghost" onClick={() => setReq(null)}>fechar ✕</Button>
      </div>
      {req.tsMs && (
        <div style={{ color: "var(--text-3)", fontSize: "var(--fs-12)", marginBottom: "var(--sp-3)" }}>
          instante {fmt(req.tsMs)} (janela ±2 min)
          {req.host ? ` · só deste servidor` : ""}
        </div>
      )}

      {/* trace exato (via ⛓ do log) */}
      {req.traceId && spans && (
        <Section title={`Trace ${req.traceId.slice(0, 12)}… (${spans.length} spans)`}>
          {spans.map((s) => (
            <Line key={s.span_id} left={<Badge state={s.status_code === "ERROR" ? "crit" : "ok"}>{s.service}</Badge>} body={`${s.name} · ${Math.round(s.duration_ms)}ms`} />
          ))}
        </Section>
      )}
      {req.traceId && traceLogs && (
        <Section title={`Logs deste trace (${traceLogs.length})`}>
          {traceLogs.map((l, i) => (
            <Line key={i} left={<Badge state={SEV_CRIT.has(l.severity) ? "crit" : "info"}>{l.severity}</Badge>} body={l.body} />
          ))}
          {traceLogs.length === 0 && <Empty />}
        </Section>
      )}

      {!data && !req.traceId && <p style={{ color: "var(--text-3)" }}>Carregando…</p>}
      {data && (
        <>
          {data.deploys.length > 0 && (
            <Section title="Deploys no período">
              {data.deploys.map((d, i) => (
                <Line key={i} left={<Badge state="warn">deploy</Badge>} body={`${d.title}, ${fmt(d.t)}`} />
              ))}
            </Section>
          )}
          <Section title={`Traces mais lentos (${data.traces.length})`}>
            {data.traces.map((t) => (
              <Line
                key={t.trace_id}
                left={<Badge state={t.has_error ? "crit" : "ok"}>{Math.round(t.duration_ms)}ms</Badge>}
                body={`${t.root_service} ${t.root_name}`}
                onClick={() => window.dispatchEvent(new CustomEvent("revoada:correlate", { detail: { traceId: t.trace_id } }))}
              />
            ))}
            {data.traces.length === 0 && <Empty />}
          </Section>
          <Section title={`Logs (${data.logs.length}, erros primeiro)`}>
            {data.logs.map((l, i) => (
              <Line
                key={i}
                left={<Badge state={SEV_CRIT.has(l.severity) ? "crit" : "info"}>{l.severity}</Badge>}
                body={l.body}
                hint={l.trace_id ? "⛓" : undefined}
                onClick={l.trace_id ? () => window.dispatchEvent(new CustomEvent("revoada:correlate", { detail: { traceId: l.trace_id } })) : undefined}
              />
            ))}
            {data.logs.length === 0 && <Empty />}
          </Section>
        </>
      )}
    </div>
  );
}

async function searchLogsByTrace(traceId: string): Promise<LogRow[]> {
  const { api } = await import("../api");
  const r = await api<{ rows: Record<string, unknown>[] }>(`/api/logs/search?trace_id=${encodeURIComponent(traceId)}&limit=200`);
  return (r.rows ?? []).map((x) => ({
    t: Number(x.t), service: String(x.service ?? ""), severity: String(x.severity ?? ""),
    severity_num: Number(x.severity_num ?? 0), body: String(x.body ?? ""),
    labels: (x.labels as Record<string, string>) ?? {}, trace_id: String(x.trace_id ?? ""), span_id: String(x.span_id ?? ""),
  }));
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div style={{ marginBottom: "var(--sp-4)" }}>
      <div style={{ fontSize: "var(--fs-12)", color: "var(--text-3)", textTransform: "uppercase", letterSpacing: 0.5, marginBottom: "var(--sp-1)" }}>{title}</div>
      {children}
    </div>
  );
}
function Line({ left, body, hint, onClick }: { left: React.ReactNode; body: string; hint?: string; onClick?: () => void }) {
  return (
    <div onClick={onClick} style={{ display: "flex", gap: "var(--sp-2)", alignItems: "baseline", padding: "3px 0", borderBottom: "1px solid var(--border)", cursor: onClick ? "pointer" : "default", fontSize: "var(--fs-12)" }}>
      <span style={{ flexShrink: 0 }}>{left}</span>
      <span style={{ flex: 1, fontFamily: "var(--font-mono)", wordBreak: "break-all" }}>{body}</span>
      {hint && <span style={{ color: "var(--accent)" }}>{hint}</span>}
    </div>
  );
}
function Empty() {
  return <div style={{ color: "var(--text-3)", fontSize: "var(--fs-12)" }}>nada no período.</div>;
}

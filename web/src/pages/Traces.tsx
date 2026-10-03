// Traces (P5.3 + UX.11): busca de traces, waterfall e service map — com ajuda
// contextual (waterfall/sampling) e estados vazios didáticos. A mecânica de
// waterfall, service map e correlação é preservada; a camada de polish é aditiva.
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Waypoints, FileText, Info, Link2 } from "lucide-react";
import {
  Badge,
  Button,
  Card,
  ConfirmDialog,
  EmptyState,
  HelpPanel,
  InfoTip,
  PageHeader,
  Skeleton,
  Tabs,
  useToast,
  type State,
} from "../components";
import { help } from "../help";
import { getRole, getTrace, listHosts, logPatterns, logServices, logSources, logsForTrace, openCorrelation, purgeTraces, searchTraces, serviceMap, spansSaoParciais, type HostDetail, type LogPattern, type LogRow, type ServiceEdge, type TraceSpan, type TraceSummary } from "../api";
import { useHostNames } from "../hooks/useHostNames";
import { fmtRelAbs, formatTime } from "../format";
import { PhpTracerModal } from "./PhpTracerModal";

// UX-13: ordenação real da lista de traces por coluna. Os cabeçalhos parecem
// clicáveis (cursor:pointer) — agora ordenam de verdade, com aria-sort.
type SortKey = "status" | "root_service" | "root_name" | "duration_ms" | "spans" | "start_ms";
const TRACE_COLS: { key: SortKey; label: string; numeric: boolean; align?: "num" }[] = [
  { key: "status", label: "Status", numeric: true },
  { key: "root_service", label: "Raiz", numeric: false },
  { key: "root_name", label: "Operação", numeric: false },
  { key: "duration_ms", label: "Duração", numeric: true, align: "num" },
  { key: "spans", label: "Spans", numeric: true, align: "num" },
  { key: "start_ms", label: "Início", numeric: true },
];
function traceSortVal(t: TraceSummary, key: SortKey): number | string {
  switch (key) {
    case "status":
      return t.has_error ? 1 : 0;
    case "root_service":
      return t.root_service ?? "";
    case "root_name":
      return t.root_name ?? "";
    case "duration_ms":
      return t.duration_ms;
    case "spans":
      return t.spans;
    case "start_ms":
      return t.start_ms;
  }
}

// PARCIAL_AJUDA é o texto único do aviso de trace incompleto: lista, cascata e a
// ajuda contextual dizem a MESMA coisa, porque a consequência é sempre a mesma.
//
// O gateway marca com revoada.trace_partial='1' o trace que virou "manter" DEPOIS de já
// ter descartado spans. Não dá para recuperar o que foi jogado fora, então os dois
// números que a tela mostra ficam sem garantia: a raiz pode ser um span filho
// promovido (o servidor usa argMin(name, ts) sobre o que sobrou) e a duração é o que
// dá para medir com os spans restantes — um piso, nunca o tempo total.
const PARCIAL_AJUDA = help.fields["traces.partial"];

// Selo de trace parcial. Usa o estado "warn" (o mesmo de "precisa de atenção" no
// resto do painel): não é erro do serviço monitorado, é um limite do que medimos.
function ParcialBadge() {
  return (
    <span title={PARCIAL_AJUDA}>
      <Badge state="warn">parcial</Badge>
    </span>
  );
}

// severidade → estado visual do Badge (mesma escala usada na tela de Logs).
const SEV_STATE: Record<string, State> = { TRACE: "neutral", DEBUG: "neutral", INFO: "info", WARN: "warn", NOTICE: "info", ERROR: "crit", FATAL: "crit" };
function sevState(s: string): State {
  return SEV_STATE[s?.toUpperCase()] ?? "neutral";
}
// Horário de Brasília + milissegundos (a cascata do trace se lê no milissegundo).
function fmtLogTime(ms: number): string {
  return formatTime(ms) + "." + String(ms % 1000).padStart(3, "0");
}

// severity_num (escala OTLP) → rótulo textual. Reaproveita a mesma escala de
// severidade da tela de Logs (TRACE 1-4, DEBUG 5-8, INFO 9-12, WARN 13-16,
// ERROR 17-20, FATAL 21-24). Cor vem do sevState acima (cor semântica só p/ estado).
function sevNumLabel(n: number): string {
  if (n >= 21) return "FATAL";
  if (n >= 17) return "ERROR";
  if (n >= 13) return "WARN";
  if (n >= 9) return "INFO";
  if (n >= 5) return "DEBUG";
  return "TRACE";
}

// Rótulos amigáveis das fontes de log (mesmos da tela de Logs). Fontes
// desconhecidas caem no fallback com o próprio nome cru.
const SOURCE_LABEL: Record<string, string> = {
  journald: "Sistema (journald)",
  docker: "Containers (Docker)",
  syslog: "Syslog",
  kernel: "Kernel (dmesg)",
  file: "Arquivo",
};
function sourceLabel(s: string): string {
  return SOURCE_LABEL[s] ?? s;
}

// Cadência da atualização em tempo real da lista de traces (ms). 5s equilibra
// "aparecer sozinho" com carga: cada tick é uma busca leve (LIMIT 100) e pausa
// com a aba oculta ou quando um trace/mapa está aberto.
const LIVE_MS = 5000;

// Janelas de tempo do filtro da visão de erros. `secs` volta no `from` (epoch s).
const ERR_WINDOWS: { id: string; label: string; secs: number }[] = [
  { id: "15m", label: "últimos 15 min", secs: 900 },
  { id: "1h", label: "última 1 hora", secs: 3600 },
  { id: "6h", label: "últimas 6 horas", secs: 21600 },
  { id: "24h", label: "últimas 24 horas", secs: 86400 },
];

// Heurística "linha de frame" do stack trace: linhas de quadro de pilha em vários
// runtimes (Java/JS "  at ...", Python 'File "..."', PHP "#0 ...", "Caused by:",
// reticências de truncamento). Recebem destaque discreto (cor mais suave).
function isFrameLine(line: string): boolean {
  return (
    /^\s+at\s/.test(line) ||
    /^\s*File\s+"/.test(line) ||
    /^\s*#\d+\s/.test(line) ||
    /^\s*Caused by:/.test(line) ||
    /^\s*\.\.\./.test(line) ||
    /^\s+from\s/.test(line)
  );
}

// cor categórica estável por serviço (hsl derivado do nome — sem hex hardcoded).
function colorFor(service: string): string {
  let h = 0;
  for (const c of service) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return `hsl(${h % 360}, 55%, 60%)`;
}

// Monta as seções do HelpPanel a partir de help.pages.traces (padrão das telas),
// acrescentando as explicações-chave de waterfall e de amostragem (sampling).
function tracesHelpSections(): { heading: string; body: ReactNode }[] {
  const p = help.pages.traces;
  const sections: { heading: string; body: ReactNode }[] = [
    { heading: "O que é esta tela", body: p.what },
    {
      heading: "Como usar",
      body: (
        <ol className="help-section__how">
          {p.how.map((h, i) => (
            <li key={i}>{h}</li>
          ))}
        </ol>
      ),
    },
    { heading: "Duas visões: spans distribuídos × erros dos logs", body: help.fields["traces.spansVsErrors"] },
    { heading: "Erros & stack traces (a partir dos logs)", body: help.fields["traces.errorsFromLogs"] },
    { heading: "A cascata (waterfall)", body: help.fields["traces.waterfall"] },
    { heading: "Traces parciais (o selo \"parcial\")", body: help.fields["traces.partial"] },
    {
      heading: "Amostragem (por que não vejo 100%?)",
      body: (
        <>
          <p>{help.fields["traces.sampling"]}</p>
          <p>{help.concepts["tail-sampling"].body}</p>
        </>
      ),
    },
  ];
  if (p.faq) {
    sections.push({
      heading: "Perguntas frequentes",
      body: (
        <div className="help-faq">
          {p.faq.map((f, i) => (
            <div key={i}>
              <p className="help-faq__q">{f.q}</p>
              <p className="help-faq__a">{f.a}</p>
            </div>
          ))}
        </div>
      ),
    });
  }
  return sections;
}

// Selo "ao vivo": sinaliza que a lista se atualiza sozinha. Estático (sem animação)
// por acessibilidade — o texto já comunica; o title traz a cadência.
function LiveDot() {
  return (
    <span
      title={`Atualiza sozinho a cada ${LIVE_MS / 1000} segundos`}
      style={{ display: "inline-flex", alignItems: "center", gap: 5, color: "var(--text-3)", fontSize: "var(--fs-12)", fontWeight: 400 }}
    >
      <span aria-hidden="true" style={{ width: 7, height: 7, borderRadius: "50%", background: "var(--ok)", display: "inline-block" }} />
      ao vivo
    </span>
  );
}

// Nota de amostragem sempre visível perto da lista: responde "por que não vejo
// 100% dos traces?" sem precisar abrir a ajuda. Detalhe completo no InfoTip.
function SamplingNote() {
  return (
    <div
      role="note"
      style={{
        display: "flex",
        alignItems: "flex-start",
        gap: "var(--sp-2)",
        padding: "var(--sp-2) var(--sp-3)",
        borderRadius: 8,
        background: "var(--bg-2)",
        border: "1px solid var(--border)",
        color: "var(--text-2)",
        fontSize: "var(--fs-14)",
        margin: "var(--sp-3) 0",
      }}
    >
      <span aria-hidden="true"><Info size={16} /></span>
      <span>
        Você não vê 100% dos traces de propósito: o Revoada usa <strong>tail sampling</strong>, guarda os
        que têm erro ou passam de 1&nbsp;segundo e cerca de 20% dos demais, para economizar armazenamento. A
        decisão é tomada quando o trace aparece; se ele só ficar interessante depois, spans já descartados não
        voltam e o trace entra marcado como <strong>parcial</strong>, nele a raiz pode estar errada e a
        duração é um piso.{" "}
        <InfoTip title="Amostragem (tail sampling)" text={help.concepts["tail-sampling"].body} />
      </span>
    </div>
  );
}

export function Traces() {
  // Visão ativa: spans OTLP distribuídos (real, exige instrumentação) ou stack
  // traces de erro extraídos dos logs (sem instrumentação). Preserva 100% a
  // mecânica de spans; a visão de erros é aditiva.
  const [mode, setMode] = useState<"spans" | "errors">("spans");
  const [service, setService] = useState("");
  const [status, setStatus] = useState("");
  const [minDur, setMinDur] = useState("");
  const [host, setHost] = useState("");
  const [hosts, setHosts] = useState<HostDetail[]>([]);
  const [traces, setTraces] = useState<TraceSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [loaded, setLoaded] = useState(false);
  const [selected, setSelected] = useState<string | null>(null);
  const [showMap, setShowMap] = useState(false);
  const [helpOpen, setHelpOpen] = useState(false);
  // Botão "Instrumentar PHP (cPanel)" só para admin — a lista de chaves de
  // ingestão que o modal consome é admin-only.
  const isAdmin = getRole() === "admin";
  const [phpOpen, setPhpOpen] = useState(false);
  const [confirmingPurge, setConfirmingPurge] = useState(false);
  const [purging, setPurging] = useState(false);
  const toast = useToast();
  // Ordenação padrão: MAIS RECENTES primeiro (o trace novo aparece no topo, casando
  // com a atualização em tempo real). O usuário ainda pode reordenar por duração etc.
  const [sort, setSort] = useState<{ key: SortKey; dir: "asc" | "desc" }>({ key: "start_ms", dir: "desc" });
  const { hostLabel } = useHostNames();
  // Fixo no mount (não recalcular a cada render): `from` entra nas deps de `run` e
  // do efeito com debounce — se mudasse a cada segundo, dispararia refetch contínuo
  // não intencional. Mesma abordagem da tela de Logs.
  const [from] = useState(() => String(Math.floor(Date.now() / 1000) - 3600));

  const toggleSort = (key: SortKey, numeric: boolean) =>
    setSort((s) => (s.key === key ? { key, dir: s.dir === "asc" ? "desc" : "asc" } : { key, dir: numeric ? "desc" : "asc" }));

  const sortedTraces = useMemo(() => {
    const arr = [...traces];
    const { key, dir } = sort;
    arr.sort((a, b) => {
      const va = traceSortVal(a, key);
      const vb = traceSortVal(b, key);
      const c = typeof va === "number" && typeof vb === "number" ? va - vb : String(va).localeCompare(String(vb), "pt-BR");
      return dir === "asc" ? c : -c;
    });
    return arr;
  }, [traces, sort]);

  const run = useCallback(async () => {
    setLoading(true);
    // Janela deslizante de 1h recomputada a cada busca: no modo "ao vivo" o
    // período é sempre [agora-1h, agora], não a hora fixa em que a tela abriu.
    const fromRolling = String(Math.floor(Date.now() / 1000) - 3600);
    const rows = await searchTraces({ service, status, min_duration: minDur, host, from: fromRolling }).catch(() => []);
    setTraces(rows);
    setLoading(false);
    setLoaded(true);
  }, [service, status, minDur, host]);

  useEffect(() => {
    listHosts().then((r) => setHosts(r.hosts)).catch(() => {});
  }, []);

  // Reage a mudança de filtro com um debounce curto (bom ao digitar o serviço).
  useEffect(() => {
    const t = setTimeout(run, 250);
    return () => clearTimeout(t);
  }, [run]);

  // Atualização em tempo real: re-busca a lista a cada LIVE_MS para que traces
  // novos apareçam sozinhos, sem recarregar a página. Só quando a LISTA está
  // visível (não dentro de um waterfall/service map/aba de erros) e a aba está em
  // foco. O ref garante que o timer sempre chame o `run` com os filtros atuais
  // sem re-assinar o intervalo a cada tecla digitada.
  const runRef = useRef(run);
  runRef.current = run;
  useEffect(() => {
    if (mode !== "spans" || selected || showMap) return;
    const id = setInterval(() => {
      if (document.visibilityState !== "hidden") runRef.current();
    }, LIVE_MS);
    return () => clearInterval(id);
  }, [mode, selected, showMap]);

  const filtersActive = service.trim() !== "" || minDur.trim() !== "" || status !== "" || host !== "";
  const clearFilters = () => {
    setService("");
    setMinDur("");
    setStatus("");
    setHost("");
  };

  const purge = async () => {
    setPurging(true);
    try {
      const r = await purgeTraces();
      toast.success(`Traces apagados (${r.deleted_rows.toLocaleString("pt-BR")} spans removidos).`);
      setConfirmingPurge(false);
      setSelected(null);
      await run();
    } catch {
      toast.error("Não foi possível apagar os traces.");
    } finally {
      setPurging(false);
    }
  };

  return (
    <div style={{ padding: "var(--sp-5)", maxWidth: 1200, margin: "0 auto" }}>
      <PageHeader
        title="Traces"
        subtitle="Siga uma requisição por todos os serviços, ou veja os erros e stack traces dos logs"
        onHelp={() => setHelpOpen(true)}
        actions={
          <div style={{ display: "flex", gap: "var(--sp-2)", flexWrap: "wrap" }}>
            {isAdmin && (
              <Button variant="ghost" onClick={() => setPhpOpen(true)}>
                Instrumentar PHP (cPanel)
              </Button>
            )}
            {mode === "spans" && (
              <Button variant={showMap ? "primary" : "ghost"} onClick={() => setShowMap((v) => !v)}>
                {showMap ? "ver lista" : "service map"}
              </Button>
            )}
            {isAdmin && (
              <Button className="btn--danger" onClick={() => setConfirmingPurge(true)} disabled={purging}>
                Apagar traces
              </Button>
            )}
          </div>
        }
      />

      <div style={{ margin: "var(--sp-3) 0" }}>
        <Tabs
          tabs={[
            { id: "spans", label: "Distribuído (spans)" },
            { id: "errors", label: "Erros & stack traces" },
          ]}
          active={mode}
          onChange={(id) => setMode(id as "spans" | "errors")}
        />
      </div>

      {mode === "errors" ? (
        <ErrorStackTraces hosts={hosts} hostLabel={hostLabel} />
      ) : (
        <>
      {!showMap && !selected && (
        <div style={{ display: "flex", gap: "var(--sp-2)", flexWrap: "wrap", margin: "var(--sp-3) 0" }}>
          <input className="field" placeholder="serviço (ex: payment)" value={service} onChange={(e) => setService(e.target.value)} />
          <select className="field" value={host} onChange={(e) => setHost(e.target.value)} aria-label="Servidor">
            <option value="">todos os servidores</option>
            {hosts.map((h) => (
              <option key={h.hostname} value={h.hostname}>{hostLabel(h.hostname)}</option>
            ))}
          </select>
          <label style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
            <input
              className="field"
              type="number"
              min={0}
              inputMode="numeric"
              placeholder="duração mín"
              aria-label="Duração mínima em milissegundos"
              value={minDur}
              onChange={(e) => setMinDur(e.target.value)}
              style={{ width: 120 }}
            />
            <span style={{ color: "var(--text-3)", fontSize: "var(--fs-12)" }}>ms</span>
          </label>
          <select className="field" value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">qualquer status</option>
            <option value="error">só com erro</option>
          </select>
        </div>
      )}

      {showMap ? (
        <ServiceMapView from={from} />
      ) : selected ? (
        <Waterfall traceId={selected} onClose={() => setSelected(null)} />
      ) : (
        <>
          <SamplingNote />
          <Card
            title={
              loading && !loaded ? (
                "Buscando traces…"
              ) : (
                <span style={{ display: "inline-flex", alignItems: "center", gap: "var(--sp-2)" }}>
                  {`${traces.length} trace(s)`}
                  <LiveDot />
                </span>
              )
            }
          >
            {loading && !loaded ? (
              <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
                {[0, 1, 2, 3, 4].map((i) => (
                  <Skeleton key={i} height={22} />
                ))}
              </div>
            ) : traces.length === 0 ? (
              filtersActive ? (
                <EmptyState
                  icon={<Waypoints size={40} aria-hidden="true" />}
                  title="Nenhum trace com esses filtros no período"
                  body={
                    <>
                      Nada bate com os filtros na última hora. Lembre que a amostragem (tail sampling) guarda
                      100% dos traces com erro ou lentos, mas só ~20% dos normais, então parte dos traces comuns
                      simplesmente não foi guardada.
                    </>
                  }
                  steps={[
                    "Afrouxe os filtros (menos duração mínima, qualquer status)",
                    'Para ver os problemas, use o filtro "só com erro"',
                    "Confirme que o serviço buscado está mesmo instrumentado",
                  ]}
                  action={{ label: "Limpar filtros", onClick: clearFilters }}
                />
              ) : (
                <EmptyState
                  icon={<Waypoints size={40} aria-hidden="true" />}
                  title="Nenhum trace no período"
                  body={
                    <>
                      Não chegou nenhum trace na última hora. Isso costuma significar que seus serviços ainda não
                      estão <strong>instrumentados</strong>: para aparecerem aqui, eles precisam exportar telemetria
                      em OTLP para o gateway do Revoada. Se você já instrumentou, pode ser só ausência de tráfego no
                      período, ou o efeito da amostragem, que descarta parte dos traces normais.
                    </>
                  }
                  steps={[
                    "Instrumente o serviço com OpenTelemetry (OTLP)",
                    "Aponte o exportador para o gateway do Revoada (HTTP ou gRPC)",
                    "Gere tráfego e volte aqui, traces com erro ou lentos aparecem primeiro",
                  ]}
                  action={{ label: "Abrir o Guia (como instrumentar via OTLP)", onClick: () => (window.location.hash = "#/help") }}
                />
              )
            ) : (
              <div className="table-scroll">
                <table className="ptable tabular">
                  <thead>
                    <tr>
                      {TRACE_COLS.map((col) => {
                        const activeCol = sort.key === col.key;
                        const ariaSort = activeCol ? (sort.dir === "asc" ? "ascending" : "descending") : "none";
                        return (
                          <th
                            key={col.key}
                            aria-sort={ariaSort}
                            tabIndex={0}
                            title={`Ordenar por ${col.label}`}
                            onClick={() => toggleSort(col.key, col.numeric)}
                            onKeyDown={(e) => {
                              if (e.key === "Enter" || e.key === " ") {
                                e.preventDefault();
                                toggleSort(col.key, col.numeric);
                              }
                            }}
                          >
                            {col.label}
                            <span aria-hidden="true" style={{ marginLeft: 4, color: activeCol ? "var(--accent)" : "var(--text-3)" }}>
                              {activeCol ? (sort.dir === "asc" ? "▲" : "▼") : "↕"}
                            </span>
                          </th>
                        );
                      })}
                    </tr>
                  </thead>
                  <tbody>
                    {sortedTraces.map((t) => {
                      const start = fmtRelAbs(t.start_ms);
                      return (
                        <tr
                          key={t.trace_id}
                          role="button"
                          tabIndex={0}
                          aria-label={`Abrir trace ${t.root_service} ${t.root_name}`}
                          style={{ cursor: "pointer" }}
                          onClick={() => setSelected(t.trace_id)}
                          onKeyDown={(e) => {
                            if (e.key === "Enter" || e.key === " ") {
                              e.preventDefault();
                              setSelected(t.trace_id);
                            }
                          }}
                        >
                          <td>
                            <span style={{ display: "inline-flex", gap: 4, flexWrap: "wrap" }}>
                              <Badge state={t.has_error ? "crit" : "ok"}>{t.has_error ? "erro" : "ok"}</Badge>
                              {t.partial && <ParcialBadge />}
                            </span>
                          </td>
                          <td>{t.root_service}</td>
                          <td style={{ fontFamily: "var(--font-mono)", fontSize: "var(--fs-12)" }}>
                            {t.root_name}
                            {t.partial && (
                              <span style={{ color: "var(--warn)" }} title={PARCIAL_AJUDA}>
                                {" "}
                                ?
                              </span>
                            )}
                          </td>
                          {/* "≥" e não "=": num trace parcial o total medido é um piso. */}
                          <td className="num" title={t.partial ? PARCIAL_AJUDA : undefined}>
                            {t.partial ? "≥ " : ""}
                            {Math.round(t.duration_ms)} ms
                          </td>
                          <td className="num">{t.spans}</td>
                          <td style={{ color: "var(--text-3)", fontSize: "var(--fs-12)" }} title={start.abs}>{start.rel}</td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
        </>
      )}
        </>
      )}

      <HelpPanel open={helpOpen} onClose={() => setHelpOpen(false)} title={help.pages.traces.title} sections={tracesHelpSections()} />

      {isAdmin && <PhpTracerModal open={phpOpen} onClose={() => setPhpOpen(false)} />}
      <ConfirmDialog
        open={confirmingPurge}
        verb="Apagar"
        target="todos os traces"
        consequences="Remove em definitivo TODOS os dados de trace (spans) de todos os servidores. Não pode ser desfeito. Os logs e as métricas não são afetados; novos traces voltam a aparecer conforme os serviços os enviam."
        danger
        onCancel={() => setConfirmingPurge(false)}
        onConfirm={purge}
      />
    </div>
  );
}

// depth de cada span a partir do parent_span_id.
function depths(spans: TraceSpan[]): Record<string, number> {
  const byId: Record<string, TraceSpan> = {};
  spans.forEach((s) => (byId[s.span_id] = s));
  const memo: Record<string, number> = {};
  const d = (id: string, guard = 0): number => {
    const s = byId[id];
    if (!s || !s.parent_span_id || !byId[s.parent_span_id] || guard > 50) return 0;
    if (memo[id] != null) return memo[id];
    return (memo[id] = 1 + d(s.parent_span_id, guard + 1));
  };
  const out: Record<string, number> = {};
  spans.forEach((s) => (out[s.span_id] = d(s.span_id)));
  return out;
}

// Contrato fixo dos labels de exception gravados pelo gateway num span que falhou.
function spanException(s: TraceSpan): { type: string; message: string; stacktrace: string; events: string } | null {
  const type = s.labels["exception.type"];
  const message = s.labels["exception.message"];
  if (!type && !message) return null;
  return {
    type: type ?? "",
    message: message ?? "",
    stacktrace: s.labels["exception.stacktrace"] ?? "",
    events: s.labels["span.events"] ?? "",
  };
}

function Waterfall({ traceId, onClose }: { traceId: string; onClose: () => void }) {
  const [spans, setSpans] = useState<TraceSpan[]>([]);
  const [loading, setLoading] = useState(true);
  const [selectedSpanId, setSelectedSpanId] = useState<string | null>(null);
  const [showLogs, setShowLogs] = useState(false);
  const [logs, setLogs] = useState<LogRow[] | null>(null);
  const [logsLoading, setLogsLoading] = useState(false);
  useEffect(() => {
    setLoading(true);
    setSelectedSpanId(null);
    setShowLogs(false);
    setLogs(null);
    getTrace(traceId)
      .then(setSpans)
      .catch(() => setSpans([]))
      .finally(() => setLoading(false));
  }, [traceId]);

  const toggleLogs = () => {
    const next = !showLogs;
    setShowLogs(next);
    if (next && logs === null && !logsLoading) {
      setLogsLoading(true);
      logsForTrace(traceId)
        .then(setLogs)
        .catch(() => setLogs([]))
        .finally(() => setLogsLoading(false));
    }
  };

  if (loading) {
    return (
      <Card title="Waterfall">
        <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
          {[0, 1, 2, 3, 4, 5].map((i) => (
            <Skeleton key={i} height={18} />
          ))}
        </div>
      </Card>
    );
  }
  if (spans.length === 0) {
    return (
      <Card title="Waterfall">
        <Button variant="ghost" onClick={onClose}>← voltar</Button>
        <EmptyState
          icon={<Waypoints size={40} aria-hidden="true" />}
          title="Trace indisponível"
          body="Não foi possível carregar os spans deste trace. Ele pode ter expirado ou sido descartado pela amostragem entre a busca e a abertura."
          action={{ label: "Voltar à lista", onClick: onClose }}
        />
      </Card>
    );
  }
  const t0 = Math.min(...spans.map((s) => s.start_ms));
  const total = Math.max(...spans.map((s) => s.start_ms + s.duration_ms)) - t0 || 1;
  const dep = depths(spans);
  const selected = spans.find((s) => s.span_id === selectedSpanId) ?? null;
  // A cascata sabe dizer sozinha se o trace é parcial: os labels de cada span vêm
  // em /api/traces/{id}. Aqui não dependemos de campo novo no backend.
  const parcial = spansSaoParciais(spans);

  return (
    <Card title={`Waterfall, ${spans.length} spans · ${parcial ? "≥ " : ""}${Math.round(total)} ms`}>
      {parcial && (
        // Faixa no topo, antes das barras: quem abre a cascata está prestes a
        // concluir "o tempo foi gasto aqui" a partir de um desenho que está
        // incompleto. O aviso precisa vir ANTES do desenho.
        <div
          role="note"
          style={{
            display: "flex",
            alignItems: "flex-start",
            gap: "var(--sp-2)",
            padding: "var(--sp-2) var(--sp-3)",
            borderRadius: 8,
            background: "var(--bg-2)",
            border: "1px solid var(--warn)",
            color: "var(--text-2)",
            fontSize: "var(--fs-13)",
            marginBottom: "var(--sp-2)",
          }}
        >
          <span aria-hidden="true"><Info size={16} /></span>
          <span>
            <strong>Este trace está incompleto.</strong> Parte dos spans foi descartada pela amostragem
            antes de o trace virar interessante, e não há como recuperá-los. Por isso: o span mais antigo
            aqui <strong>pode não ser a raiz de verdade</strong> e os{" "}
            <strong>{Math.round(total)}&nbsp;ms</strong> são um <strong>piso</strong>, o tempo real da
            operação é igual ou maior. Para comparar latência, prefira um trace sem esta marca.
          </span>
        </div>
      )}
      <div style={{ display: "flex", alignItems: "center", gap: "var(--sp-2)", marginBottom: "var(--sp-2)", flexWrap: "wrap" }}>
        <Button variant="ghost" onClick={onClose}>← voltar</Button>
        <Button variant={showLogs ? "primary" : "ghost"} onClick={toggleLogs}>
          <FileText size={14} style={{ verticalAlign: "-2px" }} /> Logs deste trace{logs !== null ? ` (${logs.length})` : ""}
        </Button>
        <Button variant="ghost" onClick={() => openCorrelation({ service: spans[0]?.service, tsMs: t0 })}>
          <Link2 size={14} style={{ verticalAlign: "-2px" }} /> correlacionar (logs + deploys)
        </Button>
        <span style={{ display: "inline-flex", alignItems: "center", gap: 4, color: "var(--text-3)", fontSize: "var(--fs-12)" }}>
          clique numa barra para ver o detalhe do span; o comprimento é a duração
          <InfoTip title="Cascata (waterfall)" text={help.fields["traces.waterfall"]} />
        </span>
      </div>

      {showLogs && <TraceLogsPanel loading={logsLoading} logs={logs} />}

      <div style={{ display: "flex", flexDirection: "column", gap: 3 }}>
        {spans.map((s) => {
          const left = ((s.start_ms - t0) / total) * 100;
          const width = Math.max(0.5, (s.duration_ms / total) * 100);
          const err = s.status_code === "ERROR";
          const hasExc = spanException(s) !== null;
          const active = s.span_id === selectedSpanId;
          return (
            <div
              key={s.span_id}
              onClick={() => setSelectedSpanId(active ? null : s.span_id)}
              style={{
                display: "flex",
                alignItems: "center",
                gap: "var(--sp-2)",
                fontSize: "var(--fs-12)",
                cursor: "pointer",
                borderRadius: 3,
                background: active ? "var(--bg-3)" : "transparent",
                padding: "1px 2px",
              }}
            >
              <div style={{ width: 200, whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis", paddingLeft: (dep[s.span_id] || 0) * 14 }}>
                <span style={{ color: colorFor(s.service) }}>●</span> {s.service} <span style={{ color: "var(--text-3)" }}>{s.name}</span>
                {hasExc && <span title="Este span tem uma exception" style={{ color: "var(--crit)", marginLeft: 4 }} aria-label="tem exception">⚠</span>}
              </div>
              <div style={{ flex: 1, position: "relative", height: 18, background: "var(--bg-2)", borderRadius: 3 }}>
                <div
                  title={`${s.name}, ${Math.round(s.duration_ms)}ms${err ? " (ERRO: " + s.status_msg + ")" : ""}`}
                  style={{ position: "absolute", left: `${left}%`, width: `${width}%`, top: 0, bottom: 0, background: err ? "var(--crit)" : colorFor(s.service), borderRadius: 3, opacity: 0.9 }}
                />
              </div>
              <div className="num" style={{ width: 64, textAlign: "right", color: err ? "var(--crit)" : "var(--text-2)" }}>{Math.round(s.duration_ms)}ms</div>
            </div>
          );
        })}
      </div>

      {selected && <SpanDetail span={selected} onClose={() => setSelectedSpanId(null)} />}
    </Card>
  );
}

// Painel dos logs correlacionados ao trace aberto (correlação exata por trace_id).
function TraceLogsPanel({ loading, logs }: { loading: boolean; logs: LogRow[] | null }) {
  return (
    <div
      style={{
        margin: "var(--sp-2) 0 var(--sp-3)",
        border: "1px solid var(--border)",
        borderRadius: "var(--radius-sm)",
        background: "var(--bg-2)",
        padding: "var(--sp-3)",
      }}
    >
      <div style={{ display: "flex", alignItems: "center", gap: 6, marginBottom: "var(--sp-2)" }}>
        <strong style={{ fontSize: "var(--fs-14)" }}>Logs deste trace</strong>
        <span style={{ color: "var(--text-3)", fontSize: "var(--fs-12)" }}>correlação exata por trace_id</span>
      </div>
      {loading ? (
        <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} height={16} />
          ))}
        </div>
      ) : !logs || logs.length === 0 ? (
        <p style={{ color: "var(--text-3)", fontSize: "var(--fs-12)", margin: 0 }}>
          Nenhum log com este trace_id. O serviço pode não estar enviando o trace_id nos logs, ou eles já
          expiraram pelo TTL.
        </p>
      ) : (
        <div className="table-scroll">
          <div style={{ fontFamily: "var(--font-mono)", fontSize: "var(--fs-12)", maxHeight: 320, overflowY: "auto", minWidth: 560 }}>
            {logs.map((r, i) => (
              <div key={i} style={{ display: "flex", gap: "var(--sp-2)", padding: "2px 4px", borderBottom: "1px solid var(--border)" }}>
                <span style={{ color: "var(--text-3)", whiteSpace: "nowrap" }}>{fmtLogTime(r.t)}</span>
                <Badge state={sevState(r.severity)}>{r.severity}</Badge>
                <span style={{ color: "var(--text-2)", whiteSpace: "nowrap" }}>{r.service}</span>
                <span style={{ flex: 1, wordBreak: "break-all" }}>{r.body}</span>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

// Detalhe de um span selecionado no waterfall. Quando o span carrega os labels
// de exception (contrato fixo do gateway), destaca tipo + mensagem em erro e
// mostra o stacktrace recolhível. Spans sem esses labels não quebram.
function SpanDetail({ span, onClose }: { span: TraceSpan; onClose: () => void }) {
  const [stackOpen, setStackOpen] = useState(false);
  const exc = spanException(span);
  const err = span.status_code === "ERROR";
  return (
    <div
      style={{
        marginTop: "var(--sp-3)",
        border: "1px solid var(--border)",
        borderRadius: "var(--radius-sm)",
        background: "var(--bg-2)",
        padding: "var(--sp-3)",
      }}
    >
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "flex-start", gap: "var(--sp-2)" }}>
        <div>
          <div style={{ display: "flex", alignItems: "center", gap: "var(--sp-2)", flexWrap: "wrap" }}>
            <span style={{ color: colorFor(span.service) }}>●</span>
            <strong>{span.service}</strong>
            <span style={{ fontFamily: "var(--font-mono)", fontSize: "var(--fs-12)", color: "var(--text-2)" }}>{span.name}</span>
            <Badge state={err ? "crit" : "ok"}>{err ? "erro" : "ok"}</Badge>
          </div>
          <div style={{ color: "var(--text-3)", fontSize: "var(--fs-12)", marginTop: 2 }}>
            {span.kind && <>tipo: {span.kind} · </>}duração: {Math.round(span.duration_ms)} ms
            {err && span.status_msg && <> · {span.status_msg}</>}
          </div>
        </div>
        <Button variant="ghost" onClick={onClose}>fechar</Button>
      </div>

      {exc && (
        <div
          role="alert"
          style={{
            marginTop: "var(--sp-3)",
            border: "1px solid var(--crit)",
            borderRadius: "var(--radius-sm)",
            background: "var(--crit-bg)",
            padding: "var(--sp-3)",
          }}
        >
          <div style={{ display: "flex", alignItems: "center", gap: 6, color: "var(--crit)", fontWeight: 600 }}>
            <span aria-hidden="true">⚠</span>
            Exception
            {exc.events && (
              <span style={{ color: "var(--text-3)", fontWeight: 400, fontSize: "var(--fs-12)" }}>
                · {exc.events} evento(s) no span
              </span>
            )}
          </div>
          {exc.type && (
            <div style={{ fontFamily: "var(--font-mono)", fontSize: "var(--fs-12)", marginTop: "var(--sp-2)", color: "var(--text-1)" }}>
              {exc.type}
            </div>
          )}
          {exc.message && (
            <div style={{ fontSize: "var(--fs-14)", marginTop: 2, color: "var(--text-1)", wordBreak: "break-word" }}>
              {exc.message}
            </div>
          )}
          {exc.stacktrace && (
            <div style={{ marginTop: "var(--sp-2)" }}>
              <Button variant="ghost" onClick={() => setStackOpen((o) => !o)}>
                {stackOpen ? "▾ ocultar stacktrace" : "▸ ver stacktrace"}
              </Button>
              {stackOpen && (
                <pre
                  style={{
                    marginTop: "var(--sp-2)",
                    fontFamily: "var(--font-mono)",
                    fontSize: "var(--fs-12)",
                    color: "var(--text-2)",
                    background: "var(--bg-1)",
                    border: "1px solid var(--border)",
                    borderRadius: "var(--radius-sm)",
                    padding: "var(--sp-2)",
                    overflowX: "auto",
                    whiteSpace: "pre",
                    maxHeight: 320,
                  }}
                >
                  {exc.stacktrace}
                </pre>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function ServiceMapView({ from }: { from: string }) {
  const [edges, setEdges] = useState<ServiceEdge[]>([]);
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    setLoading(true);
    serviceMap(from)
      .then(setEdges)
      .catch(() => setEdges([]))
      .finally(() => setLoading(false));
  }, [from]);

  if (loading) {
    return (
      <Card title="Service map (derivado dos spans)">
        <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} height={22} />
          ))}
        </div>
      </Card>
    );
  }

  if (edges.length === 0) {
    return (
      <Card title="Service map (derivado dos spans)">
        <EmptyState
          icon={<Waypoints size={40} aria-hidden="true" />}
          title="Sem arestas no período"
          body={
            <>
              O mapa de serviços é montado a partir dos spans dos traces. Sem traces na última hora, não há
              chamadas entre serviços para desenhar. Instrumente seus serviços com OTLP para que as ligações
              apareçam aqui.
            </>
          }
          action={{ label: "Abrir o Guia (como instrumentar via OTLP)", onClick: () => (window.location.hash = "#/help") }}
        />
      </Card>
    );
  }

  // layout simples: serviços únicos em coluna, arestas listadas.
  const nodes = Array.from(new Set(edges.flatMap((e) => [e.src, e.dst])));
  return (
    <Card title="Service map (derivado dos spans)">
      <div style={{ display: "flex", gap: "var(--sp-2)", flexWrap: "wrap", marginBottom: "var(--sp-3)" }}>
        {nodes.map((n) => (
          <span key={n} style={{ padding: "4px 10px", borderRadius: 16, background: "var(--bg-2)", border: `2px solid ${colorFor(n)}`, fontSize: "var(--fs-14)" }}>
            {n}
          </span>
        ))}
      </div>
      <div className="table-scroll">
        <table className="ptable tabular">
          <thead>
            <tr>
              <th>Origem</th>
              <th>Destino</th>
              <th>Chamadas</th>
              <th>Erros</th>
              <th>Latência média</th>
            </tr>
          </thead>
          <tbody>
            {edges.map((e, i) => (
              <tr key={i}>
                <td><span style={{ color: colorFor(e.src) }}>●</span> {e.src}</td>
                <td><span style={{ color: colorFor(e.dst) }}>●</span> {e.dst}</td>
                <td className="num">{e.calls}</td>
                <td className="num" style={{ color: e.errors > 0 ? "var(--crit)" : "inherit" }}>{e.errors}</td>
                <td className="num">{e.avg_ms} ms</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

// Visão "Erros & stack traces": lista os stack traces de erro agrupados a partir
// dos LOGS (reaproveita GET /api/logs/patterns com severity=error). Não exige
// instrumentação — o agente já costura o traceback multiline num único log e o
// classifica como erro; aqui os erros idênticos são agrupados por assinatura.
function ErrorStackTraces({ hosts, hostLabel }: { hosts: HostDetail[]; hostLabel: (h: string) => string }) {
  const [host, setHost] = useState("");
  const [service, setService] = useState("");
  const [source, setSource] = useState("");
  const [win, setWin] = useState("1h");
  const [services, setServices] = useState<string[]>([]);
  const [sources, setSources] = useState<string[]>([]);
  const [patterns, setPatterns] = useState<LogPattern[]>([]);
  const [loading, setLoading] = useState(true);
  const [loaded, setLoaded] = useState(false);
  const [expanded, setExpanded] = useState<Set<number>>(new Set());

  useEffect(() => {
    logServices().then((r) => setServices(r.services)).catch(() => {});
    logSources().then((r) => setSources(r.sources)).catch(() => {});
  }, []);

  const run = useCallback(async () => {
    const secs = ERR_WINDOWS.find((w) => w.id === win)?.secs ?? 3600;
    const from = String(Math.floor(Date.now() / 1000) - secs);
    setLoading(true);
    // severity fixado em "error": só stack traces / erros. Os demais filtros
    // (servidor/serviço/fonte/janela) são combináveis via o mesmo LogQuery.
    const rows = await logPatterns({ severity: "error", host, service, source, from }).catch(() => []);
    rows.sort((a, b) => b.c - a.c); // mais frequentes primeiro
    setPatterns(rows);
    setExpanded(new Set());
    setLoading(false);
    setLoaded(true);
  }, [host, service, source, win]);

  useEffect(() => {
    const t = setTimeout(run, 250);
    return () => clearTimeout(t);
  }, [run]);

  const toggle = (i: number) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(i)) next.delete(i);
      else next.add(i);
      return next;
    });

  const filtersActive = host !== "" || service !== "" || source !== "";
  const clearFilters = () => {
    setHost("");
    setService("");
    setSource("");
  };

  return (
    <>
      {/* Nota explicativa (explicativo por padrão): esta visão vem dos LOGS. */}
      <div
        role="note"
        style={{
          display: "flex",
          alignItems: "flex-start",
          gap: "var(--sp-2)",
          padding: "var(--sp-2) var(--sp-3)",
          borderRadius: 8,
          background: "var(--bg-2)",
          border: "1px solid var(--border)",
          color: "var(--text-2)",
          fontSize: "var(--fs-14)",
          margin: "var(--sp-3) 0",
        }}
      >
        <span aria-hidden="true"><Info size={16} /></span>
        <span>
          Esta visão <strong>não exige instrumentação</strong>: os stack traces são extraídos dos{" "}
          <strong>logs</strong>, o agente costura o traceback multiline num único registro e o classifica
          como erro, e aqui os erros idênticos são agrupados por assinatura (números, UUID e hex viram
          curingas). Já a aba <em>Distribuído (spans)</em> mostra traces distribuídos reais, que dependem de
          instrumentação com OpenTelemetry (OTLP), observação de futuro, fora do escopo imediato.
        </span>
      </div>

      {/* Filtros: servidor / serviço / fonte / janela (severidade é sempre erro). */}
      <div style={{ display: "flex", gap: "var(--sp-2)", flexWrap: "wrap", margin: "var(--sp-3) 0" }}>
        <select className="field" value={host} onChange={(e) => setHost(e.target.value)} aria-label="Servidor">
          <option value="">todos os servidores</option>
          {hosts.map((h) => (
            <option key={h.hostname} value={h.hostname}>{hostLabel(h.hostname)}</option>
          ))}
        </select>
        <select className="field" value={service} onChange={(e) => setService(e.target.value)} aria-label="Serviço">
          <option value="">todos os serviços</option>
          {services.map((s) => (
            <option key={s} value={s}>{s}</option>
          ))}
        </select>
        <select className="field" value={source} onChange={(e) => setSource(e.target.value)} aria-label="Fonte">
          <option value="">todas as fontes</option>
          {sources.map((s) => (
            <option key={s} value={s}>{sourceLabel(s)}</option>
          ))}
        </select>
        <select className="field" value={win} onChange={(e) => setWin(e.target.value)} aria-label="Janela de tempo">
          {ERR_WINDOWS.map((w) => (
            <option key={w.id} value={w.id}>{w.label}</option>
          ))}
        </select>
      </div>

      <Card title={loading && !loaded ? "Buscando erros…" : `${patterns.length} grupo(s) de erro`}>
        {loading && !loaded ? (
          <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
            {[0, 1, 2, 3, 4].map((i) => (
              <Skeleton key={i} height={22} />
            ))}
          </div>
        ) : patterns.length === 0 ? (
          <EmptyState
            icon={<Waypoints size={40} aria-hidden="true" />}
            title="Nenhum stack trace de erro encontrado"
            body={
              filtersActive ? (
                <>
                  Nenhum log de nível <strong>erro</strong> bateu com estes filtros na janela selecionada.
                  Isso pode ser uma boa notícia, ou os filtros estão restritos demais.
                </>
              ) : (
                <>
                  Nenhum log de nível <strong>erro</strong> na janela selecionada. Esta visão agrupa os stack
                  traces que o agente capturou dos logs; sem erros no período, não há nada para mostrar. Se você
                  esperava ver algo, amplie a janela de tempo ou confira se o serviço está enviando logs.
                </>
              )
            }
            steps={[
              "Amplie a janela de tempo (ex.: últimas 24 horas)",
              "Remova filtros de servidor, serviço ou fonte para abrir o escopo",
              "Confirme na tela de Logs se o serviço está enviando logs de erro",
            ]}
            action={filtersActive ? { label: "Limpar filtros", onClick: clearFilters } : undefined}
          />
        ) : (
          <div style={{ display: "flex", flexDirection: "column" }}>
            {patterns.map((p, i) => {
              const sig = p.pattern.split("\n")[0];
              const sev = sevNumLabel(p.sev);
              const open = expanded.has(i);
              return (
                <div key={i} style={{ borderBottom: "1px solid var(--border)" }}>
                  <div
                    role="button"
                    tabIndex={0}
                    aria-expanded={open}
                    aria-label={`${open ? "Recolher" : "Expandir"} stack trace: ${sig}`}
                    onClick={() => toggle(i)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" || e.key === " ") {
                        e.preventDefault();
                        toggle(i);
                      }
                    }}
                    style={{
                      display: "flex",
                      alignItems: "center",
                      gap: "var(--sp-2)",
                      padding: "var(--sp-2) 2px",
                      cursor: "pointer",
                    }}
                  >
                    <span aria-hidden="true" style={{ width: 14, color: "var(--text-3)", flexShrink: 0 }}>
                      {open ? "▾" : "▸"}
                    </span>
                    <Badge state={sevState(sev)}>{sev}</Badge>
                    <span
                      className="num"
                      title={`${p.c} ocorrência(s)`}
                      style={{ width: 72, textAlign: "right", color: "var(--text-2)", flexShrink: 0 }}
                    >
                      {p.c}×
                    </span>
                    <span
                      title={p.pattern}
                      style={{
                        flex: 1,
                        minWidth: 0,
                        fontFamily: "var(--font-mono)",
                        fontSize: "var(--fs-12)",
                        color: "var(--text-1)",
                        whiteSpace: "nowrap",
                        overflow: "hidden",
                        textOverflow: "ellipsis",
                      }}
                    >
                      {sig}
                    </span>
                  </div>
                  {open && <StackTrace sample={p.sample} />}
                </div>
              );
            })}
          </div>
        )}
      </Card>
    </>
  );
}

// Renderiza o stack trace completo do `sample` preservando as quebras de linha
// (white-space: pre-wrap) em monospace legível, com destaque discreto (cor mais
// suave) nas linhas de frame ("  at ...", 'File "..."', "#0 ...").
function StackTrace({ sample }: { sample: string }) {
  const lines = sample.split("\n");
  return (
    <div
      style={{
        margin: "0 0 var(--sp-3)",
        fontFamily: "var(--font-mono)",
        fontSize: "var(--fs-12)",
        lineHeight: 1.5,
        color: "var(--text-1)",
        background: "var(--bg-1)",
        border: "1px solid var(--border)",
        borderRadius: "var(--radius-sm)",
        padding: "var(--sp-3)",
        overflowX: "auto",
        maxHeight: 420,
        overflowY: "auto",
      }}
    >
      {lines.map((line, i) => (
        <div
          key={i}
          style={{
            whiteSpace: "pre-wrap",
            wordBreak: "break-word",
            color: isFrameLine(line) ? "var(--text-3)" : "var(--text-1)",
          }}
        >
          {line === "" ? " " : line}
        </div>
      ))}
    </div>
  );
}

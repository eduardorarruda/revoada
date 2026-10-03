// Logs (UX.11): busca, filtros, histograma, padrões, contexto e live tail.
// A mecânica de busca/histograma/live tail é preservada; esta camada adiciona
// ajuda contextual, chips de filtro, exemplos clicáveis e polimento responsivo.
import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { ChevronDown, ChevronRight, Search } from "lucide-react";
import {
  Badge,
  Button,
  Card,
  Checkbox,
  ConfirmDialog,
  EmptyState,
  FormField,
  FreshnessIndicator,
  HelpPanel,
  InfoTip,
  Modal,
  PageHeader,
  ServerContainerFilter,
  Skeleton,
  Tabs,
  useToast,
  IconButton,
  ActionIcons,
  type Freshness,
  type State,
} from "../components";
import { fmtRelAbs, formatDateTime, formatTime } from "../format";
import { getRole } from "../api";
import { help } from "../help";
import {
  createLogMetric,
  deleteLogMetric,
  listLogMetrics,
  openCorrelation,
  logContext,
  logHistogram,
  logPatterns,
  logServices,
  logSources,
  openLogTail,
  searchLogs,
  type LogBucket,
  type LogMetric,
  type LogPattern,
  type LogRow,
} from "../api";
import { useHostNames } from "../hooks/useHostNames";
import { seriesPalette } from "../panels/chart";

const SEV_STATE: Record<string, State> = { TRACE: "neutral", DEBUG: "neutral", INFO: "info", WARN: "warn", NOTICE: "info", ERROR: "crit", FATAL: "crit" };
function sevState(s: string): State {
  return SEV_STATE[s?.toUpperCase()] ?? "neutral";
}
// Timestamp padronizado (UX-25): relativo na tela + absoluto no title (hover).
function LogTime({ ms }: { ms: number }) {
  const { rel, abs } = fmtRelAbs(ms);
  return (
    <span style={{ color: "var(--text-3)", whiteSpace: "nowrap" }} title={abs}>
      {rel}
    </span>
  );
}

// SEV_UNKNOWN: filtro "só o que NÃO tem nível".
//
// POR QUE ELE EXISTE: o backend traduz o filtro de severidade em
// `severity_num >= N` (server/internal/logs/query.go), e linha sem nível declarado
// tem `severity_num = 0`. Ou seja: QUALQUER opção diferente de "qualquer nível"
// exclui todo log UNKNOWN, sem dizer isso em lugar nenhum. Medido no dev: de 75
// linhas, 71 são UNKNOWN — quem filtrava "INFO+" para tirar ruído apagava 95% dos
// logs e concluía que o servidor tinha ficado quieto.
//
// A API não sabe filtrar "só severity_num = 0" (só sabe o piso), então esta opção
// não manda `severity` e o recorte acontece aqui, sobre as linhas recebidas.
export const SEV_UNKNOWN = "unknown";

const SEV_LABEL: Record<string, string> = {
  info: "INFO+",
  warn: "WARN+",
  error: "ERROR+",
  [SEV_UNKNOWN]: "não classificado (UNKNOWN)",
};

// Aviso exibido junto do seletor quando um piso de nível está ativo: o corte é por
// severity_num, e o que não tem nível fica de fora do resultado.
const SEV_AVISO_PISO =
  "Pisos de nível (INFO+, WARN+, ERROR+) excluem as linhas sem nível declarado (UNKNOWN), na maioria dos servidores elas são o grosso dos logs. Para vê-las, use \"não classificado\".";

/**
 * filterUnknown recorta as linhas sem nível (severity_num = 0) quando o filtro
 * "não classificado" está ativo. Nos demais casos devolve a lista intacta — o
 * recorte por piso é do backend.
 */
export function filterUnknown(rows: LogRow[], severity: string): LogRow[] {
  if (severity !== SEV_UNKNOWN) return rows;
  return rows.filter((r) => (r.severity_num ?? 0) === 0);
}

// Rótulos amigáveis das fontes de log do servidor (label `source`). Fontes
// desconhecidas caem no fallback e aparecem com o próprio nome cru.
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

// Chip enxuto (servidor/container) na linha de log: não encolhe e trunca com
// reticências para não empurrar a mensagem nem gerar scroll horizontal.
const LINE_CHIP: CSSProperties = {
  flex: "0 0 auto",
  fontSize: "var(--fs-12)",
  padding: "0 6px",
  borderRadius: 4,
  border: "1px solid var(--border)",
  background: "var(--bg-3)",
  color: "var(--text-2)",
  whiteSpace: "nowrap",
  maxWidth: 200,
  overflow: "hidden",
  textOverflow: "ellipsis",
};

// Uma linha do par chave/valor no detalhe expansível.
function DetailRow({ k, v }: { k: string; v: string }) {
  return (
    <>
      <span style={{ color: "var(--text-3)", whiteSpace: "nowrap" }}>{k}</span>
      <span style={{ minWidth: 0, wordBreak: "break-word" }}>{v}</span>
    </>
  );
}

// LogLine — uma linha de log com as colunas essenciais (hora, nível, servidor,
// container e mensagem) e um detalhe expansível inline por linha (labels, trace,
// span, fonte e ações). O `min-width:0` na mensagem faz o texto quebrar em vez de
// gerar scroll horizontal; o detalhe é inline (não um modal que quebra o layout).
function LogLine({
  r,
  hostDisplay,
  onContext,
}: {
  r: LogRow;
  hostDisplay: (h: string) => string;
  onContext?: (r: LogRow) => void;
}) {
  const [open, setOpen] = useState(false);
  const labels = r.labels ?? {};
  const host = labels.host ?? "";
  const container = labels.container ?? "";
  const source = labels.source ?? "";
  // labels restantes (fora dos já exibidos em colunas) para o detalhe.
  const extra = Object.entries(labels).filter(([k]) => k !== "host" && k !== "container" && k !== "source");
  const toggle = () => setOpen((o) => !o);
  return (
    // `content-visibility: auto` faz o navegador PULAR layout e pintura das linhas
    // fora da janela de rolagem. A lista chega a 1.000 linhas (o teto de memória em
    // `setRows(... .slice(-1000))`, que está certo) e todas iam para o DOM pintadas.
    //
    // Por que não a virtualização por janela de `pages/containers.tsx`: aquela depende
    // de ALTURA DE LINHA FIXA (ROW_H) para calcular o índice pelo scrollTop. Uma linha
    // de log não tem altura fixa — a mensagem quebra em várias linhas (`pre-wrap`) e o
    // detalhe expande inline ao clicar. Fixar a altura truncaria a mensagem, que é o
    // conteúdo da tela. `content-visibility` dá a mesma economia de renderização sem
    // mexer no layout, e `contain-intrinsic-size: auto` faz o navegador lembrar a
    // altura real já medida, então a barra de rolagem não pula.
    <div
      style={{
        borderBottom: "1px solid var(--border)",
        contentVisibility: "auto",
        containIntrinsicSize: "auto 24px",
      }}
    >
      <div
        role="button"
        tabIndex={0}
        aria-expanded={open}
        aria-label={`${open ? "Recolher" : "Expandir"} detalhes do log${r.service ? ` de ${r.service}` : ""}`}
        onClick={toggle}
        onKeyDown={(e) => {
          if (e.target !== e.currentTarget) return;
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            toggle();
          }
        }}
        style={{ display: "flex", alignItems: "baseline", gap: "var(--sp-2)", padding: "3px 4px", cursor: "pointer" }}
      >
        <span aria-hidden style={{ flex: "0 0 auto", color: "var(--text-3)", alignSelf: "center", display: "inline-flex" }}>
          {open ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
        </span>
        <LogTime ms={r.t} />
        <Badge state={sevState(r.severity)}>{r.severity}</Badge>
        {host && (
          <span title={`Servidor: ${hostDisplay(host)}`} style={{ flex: "0 0 auto", color: "var(--text-2)", whiteSpace: "nowrap" }}>
            {hostDisplay(host)}
          </span>
        )}
        {container && (
          <span title={`Container: ${container}`} style={LINE_CHIP}>
            {container}
          </span>
        )}
        <span style={{ flex: 1, minWidth: 0, wordBreak: "break-word", whiteSpace: "pre-wrap" }}>{r.body}</span>
      </div>
      {open && (
        <div style={{ padding: "var(--sp-2) var(--sp-3) var(--sp-3) 26px", background: "var(--bg-1)" }}>
          <div style={{ display: "grid", gridTemplateColumns: "auto minmax(0, 1fr)", gap: "2px var(--sp-3)", alignItems: "baseline" }}>
            {r.service && <DetailRow k="Serviço" v={r.service} />}
            {host && <DetailRow k="Servidor" v={hostDisplay(host)} />}
            {container && <DetailRow k="Container" v={container} />}
            {source && <DetailRow k="Fonte" v={sourceLabel(source)} />}
            {r.trace_id && <DetailRow k="Trace" v={r.trace_id} />}
            {r.span_id && <DetailRow k="Span" v={r.span_id} />}
            {extra.map(([k, v]) => (
              <DetailRow key={k} k={k} v={v} />
            ))}
          </div>
          {(onContext || r.trace_id) && (
            <div className="row" style={{ gap: "var(--sp-2)", marginTop: "var(--sp-2)", flexWrap: "wrap" }}>
              {onContext && (
                <Button variant="ghost" onClick={() => onContext(r)}>
                  Ver contexto
                </Button>
              )}
              {r.trace_id && (
                <Button variant="ghost" onClick={() => openCorrelation({ traceId: r.trace_id })}>
                  Correlacionar trace
                </Button>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// Exemplos realistas para este sistema: cada um é um termo/expressão que
// costuma aparecer no corpo dos logs. Clicar preenche a busca e a executa.
const EXAMPLES: { q: string; hint: string }[] = [
  { q: "error", hint: "qualquer linha de erro" },
  { q: "timeout", hint: "esperas que estouraram o tempo" },
  { q: "status=500", hint: "respostas HTTP com falha do servidor" },
  { q: "latency>1s", hint: "operações lentas (acima de 1s)" },
  { q: "connection refused", hint: "conexões recusadas" },
  { q: "panic", hint: "quedas/panics de serviços" },
];

// Seções do HelpPanel a partir de help.pages.logs (padrão das telas da Fase 3).
function logsHelpSections(): { heading: string; body: ReactNode }[] {
  const p = help.pages.logs;
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
  // Seção fixa desta tela (não vem do dicionário): quem busca por um dado vazado
  // precisa saber o que a busca deixa para trás. O termo digitado ia parar no
  // registro de consultas do banco (3 dias), fora do alcance do expurgo — hoje já
  // não vai. Dizer isso aqui é o que separa "procurei o CPF" de "espalhei o CPF".
  sections.push({
    heading: "Privacidade da busca",
    body: (
      <p>
        O termo que você digita não fica gravado no registro de consultas do banco de dados: procurar por um dado
        sensível (token, CPF, chave) não cria uma nova cópia dele. Para <strong>apagar</strong> as linhas
        encontradas, use “Apagar logs deste servidor”, na tela do servidor, lá dá para recortar por servidor, por
        janela de datas e pelo trecho do conteúdo.
      </p>
    ),
  });
  return sections;
}

// `initialHost` pré-seleciona o servidor a partir do ?host= da URL (ex.: link
// "Ver logs" da tela de Infraestrutura). Vazio = todos os servidores.
export function Logs({ initialHost = "" }: { initialHost?: string } = {}) {
  const toast = useToast();
  const [q, setQ] = useState("");
  const [service, setService] = useState("");
  const [severity, setSeverity] = useState("");
  const [host, setHost] = useState(initialHost);
  const [container, setContainer] = useState("");
  const [source, setSource] = useState("");
  const [services, setServices] = useState<string[]>([]);
  const [sources, setSources] = useState<string[]>([]);
  const { hostLabel } = useHostNames();
  const [rows, setRows] = useState<LogRow[]>([]);
  const [buckets, setBuckets] = useState<LogBucket[]>([]);
  const [step, setStep] = useState(60);
  // Detalhe de uma coluna do histograma (drill-down por janela de tempo). Guarda o
  // início da janela e os totais agregados (soma de todos os servidores da coluna).
  const [bucket, setBucket] = useState<{ t: number; total: number; errors: number; rows: LogRow[]; loading: boolean } | null>(null);
  const [tab, setTab] = useState<"logs" | "patterns">("logs");
  const [patterns, setPatterns] = useState<LogPattern[]>([]);
  const [live, setLive] = useState(false);
  const [context, setContext] = useState<{ service: string; rows: LogRow[] } | null>(null);
  const [loading, setLoading] = useState(true);
  const [searched, setSearched] = useState(false);
  const [helpOpen, setHelpOpen] = useState(false);
  const canEdit = getRole() === "admin";
  // janela fixa de 1h a partir da carga da página (estável entre renders — senão
  // o efeito do live tail reabriria o WebSocket a cada render).
  const [from] = useState(() => String(Math.floor(Date.now() / 1000) - 3600));

  // Estado do live tail para o FreshnessIndicator (live/stale/reconnecting).
  const [tailState, setTailState] = useState<Freshness>("reconnecting");
  const [tailAge, setTailAge] = useState(0);
  const lastRowAtRef = useRef<number>(0);
  const receivedRef = useRef(false);

  // Métricas derivadas de logs + modal de "salvar como métrica".
  const [metrics, setMetrics] = useState<LogMetric[]>([]);
  const [metricModal, setMetricModal] = useState(false);
  const [metricName, setMetricName] = useState("");
  const [metricToDelete, setMetricToDelete] = useState<LogMetric | null>(null);

  // O filtro enviado à API. "não classificado" NÃO vira parâmetro `severity` (o
  // backend só sabe aplicar piso `severity_num >= N`): pedimos tudo e recortamos as
  // linhas sem nível no cliente, com filterUnknown.
  const filter = useCallback(
    () => ({
      q,
      service,
      severity: severity === SEV_UNKNOWN ? "" : severity,
      host,
      container,
      source,
      from,
      limit: 500,
    }),
    [q, service, severity, host, container, source, from],
  );

  const runSearch = useCallback(async () => {
    const f = filter();
    setLoading(true);
    try {
      const [r, h] = await Promise.all([searchLogs(f).catch(() => []), logHistogram(f).catch(() => ({ buckets: [], step: 60 }))]);
      setRows(filterUnknown(r, severity));
      setBuckets(h.buckets);
      setStep(h.step);
      if (tab === "patterns") setPatterns(await logPatterns(f).catch(() => []));
    } finally {
      setLoading(false);
      setSearched(true);
    }
  }, [filter, tab, severity]);

  useEffect(() => {
    logServices().then((r) => setServices(r.services)).catch(() => {});
    logSources().then((r) => setSources(r.sources)).catch(() => {});
  }, []);

  const reloadMetrics = useCallback(async () => {
    const r = await listLogMetrics().catch(() => ({ log_metrics: [] as LogMetric[] }));
    setMetrics(r.log_metrics);
  }, []);
  useEffect(() => {
    reloadMetrics();
  }, [reloadMetrics]);

  // busca com debounce ao mudar filtros (quando não está em live tail).
  useEffect(() => {
    if (live) return;
    const t = setTimeout(runSearch, 250);
    return () => clearTimeout(t);
  }, [runSearch, live]);

  // live tail
  const tailRef = useRef<(() => void) | null>(null);
  useEffect(() => {
    if (!live) {
      tailRef.current?.();
      tailRef.current = null;
      return;
    }
    setRows([]);
    receivedRef.current = false;
    lastRowAtRef.current = Date.now();
    setTailState("reconnecting");
    setTailAge(0);
    tailRef.current = openLogTail(filter(), (newRows) => {
      lastRowAtRef.current = Date.now();
      receivedRef.current = true;
      setRows((prev) => [...prev, ...newRows].slice(-1000));
    });
    return () => {
      tailRef.current?.();
      tailRef.current = null;
    };
  }, [live, q, service, severity, host, container, source, filter]);

  // Atualiza o indicador de frescor do tail: sem linhas há >10s vira "stale".
  useEffect(() => {
    if (!live) return;
    const t = setInterval(() => {
      const age = Math.floor((Date.now() - lastRowAtRef.current) / 1000);
      setTailAge(age);
      setTailState(receivedRef.current ? (age > 10 ? "stale" : "live") : "reconnecting");
    }, 1000);
    return () => clearInterval(t);
  }, [live]);

  // Pivota os buckets (t, host) em colunas empilhadas: uma entrada por instante,
  // com o total, os erros e um segmento por servidor. `hostList` alimenta a legenda
  // e o mapa de cores; `max` é o maior total de coluna (escala vertical das barras).
  const hist = useMemo(() => {
    const times: number[] = [];
    const seen = new Set<number>();
    const hostSet = new Set<string>();
    const byTime = new Map<number, { total: number; errors: number; segs: Map<string, { c: number; errors: number }> }>();
    const hostTotals = new Map<string, number>();
    for (const b of buckets) {
      if (!seen.has(b.t)) {
        seen.add(b.t);
        times.push(b.t);
      }
      hostSet.add(b.host);
      let e = byTime.get(b.t);
      if (!e) {
        e = { total: 0, errors: 0, segs: new Map() };
        byTime.set(b.t, e);
      }
      e.total += b.c;
      e.errors += b.errors;
      const s = e.segs.get(b.host) ?? { c: 0, errors: 0 };
      s.c += b.c;
      s.errors += b.errors;
      e.segs.set(b.host, s);
      hostTotals.set(b.host, (hostTotals.get(b.host) ?? 0) + b.c);
    }
    times.sort((a, b) => a - b);
    const hostList = [...hostSet].sort((a, b) => (hostTotals.get(b) ?? 0) - (hostTotals.get(a) ?? 0) || a.localeCompare(b));
    const palette = seriesPalette();
    const colorOf = new Map<string, string>();
    hostList.forEach((h, i) => colorOf.set(h, palette[i % palette.length] || "var(--accent)"));
    const max = Math.max(1, ...times.map((t) => byTime.get(t)?.total ?? 0));
    return { times, hostList, colorOf, max, byTime, hostTotals };
  }, [buckets]);

  const hostDisplay = useCallback((h: string) => (h ? hostLabel(h) : "(sem servidor)"), [hostLabel]);

  // Executa um exemplo: preenche a busca (a busca roda pelo efeito de debounce).
  const runExample = (text: string) => {
    setLive(false);
    setQ(text);
  };

  const submitMetric = async () => {
    const name = metricName.trim();
    if (!name) return;
    const metric_name = "logs." + (name.toLowerCase().replace(/[^a-z0-9]+/g, "_").replace(/^_|_$/g, "") || "custom");
    try {
      await createLogMetric({ name, metric_name, service, severity_min: severity ? sevNum(severity) : 0, query: q });
      toast.success(`Métrica "${name}" criada. Em segundos ela vira uma série consultável e alertável.`);
      setMetricName("");
      await reloadMetrics();
    } catch (e) {
      console.error("createLogMetric falhou", e);
      toast.error("Não foi possível criar a métrica. Verifique os campos e tente novamente.");
    }
  };

  const confirmDeleteMetric = async () => {
    const m = metricToDelete;
    if (!m) return;
    setMetricToDelete(null);
    try {
      await deleteLogMetric(m.id);
      toast.success(`Métrica "${m.name}" removida.`);
      await reloadMetrics();
    } catch (e) {
      console.error("deleteLogMetric falhou", e);
      toast.error("Não foi possível remover a métrica. Tente novamente em instantes.");
    }
  };

  const openContext = async (row: LogRow) => {
    const ctx = await logContext(row.service, row.t, 15).catch(() => []);
    setContext({ service: row.service, rows: ctx });
  };

  // Drill-down: abre as linhas que compõem uma coluna do histograma. Reaplica os
  // filtros ativos e restringe a janela ao intervalo [t, t+step). `total`/`errors`
  // são os agregados da coluna (soma de todos os servidores) para o cabeçalho.
  const openBucket = async (t: number, total: number, errors: number) => {
    setBucket({ t, total, errors, rows: [], loading: true });
    const rowsIn = await searchLogs({ ...filter(), from: String(t), to: String(t + step), limit: 500 }).catch(() => []);
    setBucket((prev) => (prev && prev.t === t ? { ...prev, rows: rowsIn, loading: false } : prev));
  };

  // Chips legíveis dos filtros ativos (removíveis).
  const chips: { label: string; onRemove: () => void }[] = [];
  if (q) chips.push({ label: `busca: ${q}`, onRemove: () => setQ("") });
  if (service) chips.push({ label: `serviço: ${service}`, onRemove: () => setService("") });
  if (host) chips.push({ label: `servidor: ${hostLabel(host)}`, onRemove: () => { setHost(""); setContainer(""); } });
  if (container) chips.push({ label: `container: ${container}`, onRemove: () => setContainer("") });
  if (source) chips.push({ label: `fonte: ${sourceLabel(source)}`, onRemove: () => setSource("") });
  if (severity) chips.push({ label: `nível: ${SEV_LABEL[severity] ?? severity}`, onRemove: () => setSeverity("") });

  const showEmpty = !loading && searched && !live && rows.length === 0;

  return (
    <div className="page">
      <PageHeader
        title="Logs"
        subtitle="Busque, agrupe e acompanhe logs em tempo real"
        onHelp={() => setHelpOpen(true)}
        actions={
          canEdit && (
            <Button variant="ghost" onClick={() => setMetricModal(true)}>
              Salvar como métrica
            </Button>
          )
        }
      />

      {/* Filtros de busca */}
      <Card>
        <div className="filtros">
          <div>
            <FormField
              label="Buscar no corpo do log"
              help={
                <>
                  {help.fields["logs.query"]}
                  <br />
                  Exemplos: <code>timeout</code>, <code>status=500</code>, <code>latency&gt;1s</code>. Combine com os filtros de serviço e severidade.
                </>
              }
            >
              <input className="field" placeholder="Buscar no corpo do log…" value={q} onChange={(e) => setQ(e.target.value)} style={{ width: "100%" }} />
            </FormField>
          </div>
          <div className="filtros__largo">
          <FormField label="Servidor, container e serviço" help="Filtre por servidor (e, dentro dele, por container) e por serviço, tudo num só lugar. Escolha um servidor para liberar o filtro por container daquele servidor.">
            <ServerContainerFilter
              host={host}
              container={container}
              onChange={(next) => {
                setHost(next.host);
                setContainer(next.container);
              }}
              services={services}
              service={service}
              onServiceChange={setService}
            />
          </FormField>
          </div>
          <FormField label="Fonte" help={help.fields["logs.source"]}>
            <select className="field" value={source} onChange={(e) => setSource(e.target.value)}>
              <option value="">Todas as fontes</option>
              {sources.map((s) => (
                <option key={s} value={s}>{sourceLabel(s)}</option>
              ))}
            </select>
          </FormField>
          <FormField label="Severidade" help={help.fields["logs.severity"]}>
            <select className="field" value={severity} onChange={(e) => setSeverity(e.target.value)}>
              <option value="">qualquer nível</option>
              <option value="info">INFO+</option>
              <option value="warn">WARN+</option>
              <option value="error">ERROR+</option>
              <option value={SEV_UNKNOWN}>não classificado</option>
            </select>
            {severity !== "" && severity !== SEV_UNKNOWN && (
              <p className="hint" style={{ marginTop: "var(--sp-1)" }}>
                {SEV_AVISO_PISO}
              </p>
            )}
          </FormField>
          <FormField label="Ao vivo" help={help.fields["logs.liveTail"]}>
            <div className="row" style={{ gap: "var(--sp-2)", minHeight: 34 }}>
              <Checkbox checked={live} onChange={setLive} label="live tail" />
              {live && <FreshnessIndicator status={tailState} ageSeconds={tailState === "live" ? tailAge : undefined} />}
            </div>
          </FormField>
        </div>

        {/* Exemplos clicáveis */}
        <div className="row" style={{ gap: "var(--sp-2)", marginTop: "var(--sp-3)", alignItems: "center" }}>
          <span style={{ color: "var(--text-3)", fontSize: "var(--fs-12)" }}>Exemplos:</span>
          {EXAMPLES.map((ex) => (
            <button
              key={ex.q}
              type="button"
              className="btn btn--ghost"
              title={ex.hint}
              onClick={() => runExample(ex.q)}
              style={{ fontFamily: "var(--font-mono)", fontSize: "var(--fs-12)" }}
            >
              {ex.q}
            </button>
          ))}
        </div>

        {/* Chips de filtro ativos */}
        {chips.length > 0 && (
          <div className="row" style={{ gap: "var(--sp-2)", marginTop: "var(--sp-2)", alignItems: "center" }}>
            <span style={{ color: "var(--text-3)", fontSize: "var(--fs-12)" }}>Filtros:</span>
            {chips.map((c) => (
              <button
                key={c.label}
                type="button"
                onClick={c.onRemove}
                aria-label={`Remover filtro ${c.label}`}
                style={{
                  display: "inline-flex",
                  alignItems: "center",
                  gap: 6,
                  fontSize: "var(--fs-12)",
                  padding: "2px 8px",
                  borderRadius: 999,
                  border: "1px solid var(--border)",
                  background: "var(--bg-3)",
                  color: "var(--text-1)",
                  cursor: "pointer",
                }}
              >
                {c.label} <span aria-hidden="true">✕</span>
              </button>
            ))}
          </div>
        )}
      </Card>

      {/* histograma por servidor — cada coluna é uma janela de tempo; dentro dela as
          contagens são empilhadas por servidor (host), com cor da legenda. A coluna
          toda captura o hover/clique e abre as linhas daquela janela de tempo. */}
      <Card title="Volume no tempo por servidor">
        <div style={{ display: "flex", alignItems: "stretch", gap: 2, height: 60 }}>
          {hist.times.length === 0 && <span style={{ color: "var(--text-3)", fontSize: "var(--fs-12)" }}>sem dados na janela</span>}
          {hist.times.map((t) => {
            const e = hist.byTime.get(t)!;
            const when = formatDateTime(t * 1000);
            const linhas = `${e.total} ${e.total === 1 ? "linha" : "linhas"}`;
            const erros = e.errors > 0 ? ` · ${e.errors} ${e.errors === 1 ? "erro" : "erros"}` : "";
            const clickable = e.total > 0;
            // segmentos na ordem da legenda; o 1º da legenda fica na base da pilha.
            const segs = hist.hostList
              .map((h) => ({ h, seg: e.segs.get(h) }))
              .filter((x): x is { h: string; seg: { c: number; errors: number } } => !!x.seg && x.seg.c > 0);
            return (
              <div
                key={t}
                role={clickable ? "button" : undefined}
                tabIndex={clickable ? 0 : undefined}
                aria-label={clickable ? `Ver as ${linhas}${erros} de ${when}` : undefined}
                title={
                  clickable
                    ? `${when} · ${linhas}${erros}\n${segs.map((s) => `${hostDisplay(s.h)}: ${s.seg.c}`).join("\n")}\nclique para ver as linhas`
                    : `${when} · ${linhas}${erros}`
                }
                onClick={clickable ? () => openBucket(t, e.total, e.errors) : undefined}
                onKeyDown={
                  clickable
                    ? (ev) => {
                        if (ev.key === "Enter" || ev.key === " ") {
                          ev.preventDefault();
                          openBucket(t, e.total, e.errors);
                        }
                      }
                    : undefined
                }
                style={{ flex: 1, minWidth: 2, display: "flex", flexDirection: "column", justifyContent: "flex-end", cursor: clickable ? "pointer" : "default" }}
              >
                {segs.map((s) => (
                  <span
                    key={s.h}
                    aria-hidden
                    style={{
                      display: "block",
                      width: "100%",
                      height: `max(2px, ${(s.seg.c / hist.max) * 100}%)`,
                      background: hist.colorOf.get(s.h),
                      opacity: 0.85,
                      pointerEvents: "none",
                    }}
                  />
                ))}
              </div>
            );
          })}
        </div>
        {hist.hostList.length > 0 && (
          <div className="row" style={{ gap: "var(--sp-3)", marginTop: "var(--sp-2)", flexWrap: "wrap", alignItems: "center" }}>
            {hist.hostList.map((h) => (
              <span key={h} style={{ display: "inline-flex", alignItems: "center", gap: 6, fontSize: "var(--fs-12)", color: "var(--text-2)" }}>
                <span aria-hidden style={{ width: 10, height: 10, borderRadius: 2, background: hist.colorOf.get(h), flex: "0 0 auto" }} />
                {hostDisplay(h)}
                <span style={{ color: "var(--text-3)" }}>· {hist.hostTotals.get(h) ?? 0}</span>
              </span>
            ))}
          </div>
        )}
        {hist.times.length > 0 && (
          <p style={{ color: "var(--text-3)", fontSize: "var(--fs-12)", marginTop: "var(--sp-2)" }}>
            Dica: cada cor é um servidor. Clique numa coluna para ver as linhas daquele intervalo.
          </p>
        )}
      </Card>

      <div style={{ margin: "var(--sp-3) 0" }}>
        <Tabs
          tabs={[
            { id: "logs", label: `Linhas (${rows.length})` },
            { id: "patterns", label: "Padrões" },
          ]}
          active={tab}
          onChange={(id) => {
            const next = id as "logs" | "patterns";
            setTab(next);
            if (next === "patterns") {
              logPatterns(filter())
                .then(setPatterns)
                .catch(() => setPatterns([]));
            }
          }}
        />
      </div>

      {tab === "logs" ? (
        <Card>
          {loading && rows.length === 0 ? (
            <div className="stack" style={{ gap: "var(--sp-2)" }}>
              {Array.from({ length: 6 }).map((_, i) => (
                <Skeleton key={i} height={16} />
              ))}
            </div>
          ) : showEmpty ? (
            <EmptyState
              icon={<Search size={32} strokeWidth={1.5} />}
              title="Nenhuma linha para esta busca"
              body="Nenhum log bateu com os filtros atuais na janela de tempo (última 1 hora)."
              steps={[
                "Amplie a busca: apague termos ou remova um filtro clicando no ✕ do chip.",
                "Confira a severidade, talvez esteja filtrando por um nível alto demais.",
                "Verifique se o serviço realmente envia logs; logs antigos podem já ter expirado pelo TTL.",
              ]}
              action={chips.length > 0 ? { label: "Limpar filtros", onClick: () => { setQ(""); setService(""); setSeverity(""); setHost(""); setSource(""); } } : undefined}
            />
          ) : (
            <div style={{ fontFamily: "var(--font-mono)", fontSize: "var(--fs-12)", maxHeight: 520, overflowY: "auto" }}>
              {rows.map((r, i) => (
                <LogLine key={i} r={r} hostDisplay={hostDisplay} onContext={openContext} />
              ))}
            </div>
          )}
        </Card>
      ) : (
        <Card title="Padrões (linhas semelhantes agrupadas)">
          <div className="table-scroll">
            <table className="ptable tabular">
              <thead>
                <tr>
                  <th>Contagem</th>
                  <th>Padrão</th>
                </tr>
              </thead>
              <tbody>
                {patterns.map((p, i) => (
                  <tr key={i}>
                    <td className="num">{p.c}</td>
                    <td style={{ fontFamily: "var(--font-mono)", fontSize: "var(--fs-12)" }}>{p.pattern}</td>
                  </tr>
                ))}
                {patterns.length === 0 && (
                  <tr>
                    <td colSpan={2} style={{ color: "var(--text-3)" }}>Sem padrões.</td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      {/* Modal: salvar como métrica + gestão das métricas de logs */}
      <Modal
        open={metricModal}
        onClose={() => setMetricModal(false)}
        title="Salvar busca como métrica"
        footer={
          <div className="row row--end">
            <Button variant="ghost" onClick={() => setMetricModal(false)}>Fechar</Button>
            <Button variant="primary" onClick={submitMetric} disabled={!metricName.trim()}>
              Criar métrica
            </Button>
          </div>
        }
      >
        <p style={{ color: "var(--text-2)", marginBottom: "var(--sp-3)", display: "flex", alignItems: "center", gap: 6 }}>
          Cria uma métrica que conta, ao longo do tempo, quantos logs batem com esta busca, para gráficar e alertar.
          <InfoTip text={help.fields["logs.saveMetric"]} title="Salvar como métrica" />
        </p>
        <FormField
          label="Nome da métrica"
          hint={`Filtros da métrica: serviço "${service || "todos"}", nível "${severity ? SEV_LABEL[severity] : "qualquer"}", busca "${q || "(vazia)"}".`}
        >
          <input
            className="field"
            placeholder={q ? `Logs: ${q}` : "Ex.: Erros de pagamento"}
            value={metricName}
            onChange={(e) => setMetricName(e.target.value)}
            style={{ width: "100%" }}
          />
        </FormField>

        <h4 style={{ marginTop: "var(--sp-4)" }}>Métricas de logs existentes</h4>
        {metrics.length === 0 ? (
          <p style={{ color: "var(--text-3)", fontSize: "var(--fs-14)" }}>Nenhuma métrica de log criada ainda.</p>
        ) : (
          <div className="stack" style={{ gap: "var(--sp-2)", marginTop: "var(--sp-2)" }}>
            {metrics.map((m) => (
              <div key={m.id} className="row" style={{ justifyContent: "space-between", borderBottom: "1px solid var(--border)", paddingBottom: "var(--sp-2)" }}>
                <div>
                  <div style={{ fontWeight: 600 }}>{m.name}</div>
                  <div style={{ color: "var(--text-3)", fontSize: "var(--fs-12)", fontFamily: "var(--font-mono)" }}>
                    {m.metric_name}{m.query ? ` · busca: ${m.query}` : ""}{m.service ? ` · serviço: ${m.service}` : ""}
                  </div>
                </div>
                <IconButton icon={ActionIcons.delete} label="Apagar" onClick={() => setMetricToDelete(m)} />
              </div>
            ))}
          </div>
        )}
      </Modal>

      <ConfirmDialog
        open={metricToDelete != null}
        onCancel={() => setMetricToDelete(null)}
        onConfirm={confirmDeleteMetric}
        verb="Apagar"
        target={`a métrica "${metricToDelete?.name ?? ""}"`}
        consequences="A série derivada deixa de ser coletada. Alertas que dependam dela param de disparar. Os dados já coletados seguem sujeitos ao TTL."
        danger
      />

      {/* Drill-down de uma coluna do histograma: linhas da janela de tempo clicada.
          Sem scroll aninhado — o próprio corpo do Modal rola (um único scroll
          vertical); as linhas quebram texto (min-width:0), sem scroll horizontal. */}
      <Modal
        open={bucket != null}
        onClose={() => setBucket(null)}
        wide
        title={
          bucket
            ? `Logs de ${formatTime(bucket.t * 1000)}–${formatTime((bucket.t + step) * 1000)}`
            : "Logs do intervalo"
        }
        footer={
          <div className="row row--end">
            <Button variant="ghost" onClick={() => setBucket(null)}>Fechar</Button>
          </div>
        }
      >
        {bucket && (
          <>
            <p style={{ color: "var(--text-2)", fontSize: "var(--fs-14)", marginBottom: "var(--sp-2)" }}>
              {bucket.total} {bucket.total === 1 ? "linha" : "linhas"} nesta janela
              {bucket.errors > 0 ? ` · ${bucket.errors} ${bucket.errors === 1 ? "erro" : "erros"}` : ""}
              {chips.length > 0 ? " (com os filtros atuais aplicados)" : ""}. Clique numa linha para ver os detalhes.
            </p>
            {bucket.loading ? (
              <div className="stack" style={{ gap: "var(--sp-2)" }}>
                {Array.from({ length: 6 }).map((_, i) => (
                  <Skeleton key={i} height={16} />
                ))}
              </div>
            ) : bucket.rows.length === 0 ? (
              <p style={{ color: "var(--text-3)", fontSize: "var(--fs-14)" }}>
                Nenhuma linha retornada para este intervalo. As linhas podem ter expirado pelo TTL desde a contagem do histograma.
              </p>
            ) : (
              <div style={{ fontFamily: "var(--font-mono)", fontSize: "var(--fs-12)" }}>
                {bucket.rows.map((r, i) => (
                  <LogLine key={i} r={r} hostDisplay={hostDisplay} onContext={openContext} />
                ))}
              </div>
            )}
          </>
        )}
      </Modal>

      <HelpPanel open={helpOpen} onClose={() => setHelpOpen(false)} title={help.pages.logs.title} sections={logsHelpSections()} />

      {context && (
        <div
          onClick={() => setContext(null)}
          style={{ position: "fixed", inset: 0, background: "rgba(0,0,0,.6)", display: "flex", alignItems: "center", justifyContent: "center", zIndex: 150 }}
        >
          <div onClick={(e) => e.stopPropagation()} style={{ background: "var(--bg-1)", border: "1px solid var(--border)", borderRadius: 8, padding: "var(--sp-4)", maxWidth: 900, width: "90%", maxHeight: "80vh", overflow: "auto" }}>
            <div style={{ display: "flex", justifyContent: "space-between", marginBottom: "var(--sp-2)" }}>
              <strong>Contexto, {context.service}</strong>
              <Button variant="ghost" onClick={() => setContext(null)}>fechar</Button>
            </div>
            <div style={{ fontFamily: "var(--font-mono)", fontSize: "var(--fs-12)" }}>
              {context.rows.map((r, i) => (
                <LogLine key={i} r={r} hostDisplay={hostDisplay} />
              ))}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

function sevNum(text: string): number {
  return { info: 9, warn: 13, error: 17 }[text] ?? 0;
}

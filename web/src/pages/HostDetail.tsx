// Visão do servidor (detalhe): página de um host individual (#/hosts/<hostname>).
// Abas Overview/CPU/RAM/Discos/Rede/Containers/Processos/Alertas, seletor de janela
// no topo (1h..30d) que controla from/to/step das séries. Cada aba sem dado disponível
// no backend mostra um EmptyState honesto (não inventa série/métrica). Reaproveita
// TimeSeriesPanel (com unidade do dicionário), ContainersBlock e os formatadores.
import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { ArrowLeft, Boxes, Cpu, ServerCrash, TriangleAlert } from "lucide-react";
import {
  Badge,
  Card,
  EmptyState,
  LastSeen,
  PageHeader,
  Skeleton,
  Tabs,
  type State,
} from "../components";
import {
  hostMetrics,
  listAlertRules,
  listAlerts,
  listHosts,
  queryMetric,
  type AlertEvent,
  type AlertRule,
  type HostDetail as HostDetailModel,
  type HostMetrics,
  type QueryResponse,
} from "../api";
import { TimeSeriesPanel, type PanelState, type TimeSeriesData } from "../panels";
import { buildSeriesLabels } from "../panels/seriesLabels";
import { cpuSerieDoHost } from "./cpuSerie";
import { metricMeta } from "../metrics/dict";
import { panelStateFor } from "../metrics/emptiness";
import { aggSuffix, panelFootnote, unitForAgg } from "../metrics/resolution";
import { useHostNames } from "../hooks/useHostNames";
import { usePolling } from "../hooks/usePolling";
import { fmtRelAbs, formatMetric, formatValue, mensagemDeErro, SEV_LABEL, sevRank } from "../format";
import { ContainersBlock } from "./containers";
import { Kpi, KpiGrid } from "../components/Kpi";
import { resumoDoServidor } from "./hostResumo";
import { ServicosDoServidor } from "./ServicosDescobertos";

const MUTED_12 = { fontSize: "var(--fs-12)", color: "var(--text-3)" } as const;

// Janelas de tempo pré-definidas (mesmo padrão do Explore, estendido até 30 dias).
// step derivado da janela, sempre >= 60s — janelas longas caem em rollups no backend.
const WINDOWS: { id: string; label: string; seconds: number }[] = [
  { id: "1h", label: "Última 1 hora", seconds: 60 * 60 },
  { id: "6h", label: "Últimas 6 horas", seconds: 6 * 60 * 60 },
  { id: "24h", label: "Últimas 24 horas", seconds: 24 * 60 * 60 },
  { id: "7d", label: "Últimos 7 dias", seconds: 7 * 24 * 60 * 60 },
  { id: "30d", label: "Últimos 30 dias", seconds: 30 * 24 * 60 * 60 },
];

type TabId = "overview" | "cpu" | "ram" | "disks" | "net" | "containers" | "procs" | "alerts";

const TABS: { id: TabId; label: string }[] = [
  { id: "overview", label: "Visão geral" },
  { id: "cpu", label: "CPU" },
  { id: "ram", label: "RAM" },
  { id: "disks", label: "Discos" },
  { id: "net", label: "Rede" },
  { id: "containers", label: "Containers" },
  { id: "procs", label: "Processos" },
  { id: "alerts", label: "Alertas" },
];

// Uptime legível: 3d 4h / 5h / 12m — a partir dos segundos do inventário.
function formatUptime(secs: number): string {
  if (!secs || secs <= 0) return "—";
  const d = Math.floor(secs / 86400);
  const h = Math.floor((secs % 86400) / 3600);
  const m = Math.floor((secs % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

// Estado (Badge) por severidade do alerta.
function sevState(sev: string): State {
  switch (sev?.toLowerCase()) {
    case "critical":
    case "crit":
      return "crit";
    case "warning":
    case "warn":
      return "warn";
    default:
      return "info";
  }
}

// `initialWindow` (#/hosts/<host>?janela=24h) abre numa janela escolhida por quem
// linkou, como o replay de Agentes de IA ("Ver host nesta hora"). Id desconhecido = 6h.
export function HostDetail({ hostname, initialWindow = "" }: { hostname: string; initialWindow?: string }) {
  const { hostLabel } = useHostNames();
  const [tab, setTab] = useState<TabId>("overview");
  const [windowId, setWindowId] = useState(() => (WINDOWS.some((w) => w.id === initialWindow) ? initialWindow : "6h"));
  const win = WINDOWS.find((w) => w.id === windowId) ?? WINDOWS[1];

  // Inventário do host (dados "de placa": SO, kernel, CPU, uptime…).
  const [detail, setDetail] = useState<HostDetailModel | null | undefined>(undefined);
  // Métricas ao vivo do host (CPU atual, discos, rede, containers).
  const [live, setLive] = useState<HostMetrics | null>(null);

  // Troca de host → volta ao esqueleto enquanto o novo inventário carrega.
  useEffect(() => {
    setDetail(undefined);
  }, [hostname]);

  // Inventário REPOLADO (não só no load): o selo "no ar"/"sem sinal" e o frescor
  // "atualizado há X" liam `up`/`last_seen` uma única vez e congelavam (mostrava
  // "no ar" com "sem dado há 46 min"). Erro transitório mantém o último dado — só o
  // load inicial (prev === undefined) resolve para null ("não encontrado").
  const loadDetail = useCallback(() => {
    // listHosts(search) filtra por termo; casamos pelo hostname técnico exato.
    listHosts(hostname)
      .then((r) => setDetail(r.hosts.find((h) => h.hostname === hostname) ?? null))
      .catch(() => setDetail((prev) => (prev === undefined ? null : prev)));
  }, [hostname]);
  usePolling(loadDetail, 15000, [loadDetail]);

  const loadLive = useCallback(() => {
    hostMetrics()
      .then((r) => setLive(r.hosts.find((m) => m.host === hostname) ?? null))
      .catch(() => {
        /* mantém o último snapshot; o campo `up` sinaliza frescor */
      });
  }, [hostname]);
  usePolling(loadLive, 5000, [loadLive]);

  const label = detail?.display_name?.trim() || hostLabel(hostname) || hostname;

  return (
    <div className="page">
      <a
        href="#/hosts"
        style={{
          display: "inline-flex",
          alignItems: "center",
          gap: "var(--sp-1)",
          fontSize: "var(--fs-12)",
          color: "var(--text-2)",
          textDecoration: "none",
          marginBottom: "var(--sp-2)",
        }}
      >
        <ArrowLeft size={14} /> Voltar aos servidores
      </a>

      <PageHeader
        title={label}
        subtitle={label !== hostname ? `Hostname técnico: ${hostname}` : "Visão detalhada do servidor"}
        actions={
          <label style={{ display: "flex", alignItems: "center", gap: "var(--sp-2)", fontSize: "var(--fs-12)", color: "var(--text-2)" }}>
            Intervalo
            <select
              className="field"
              value={windowId}
              onChange={(e) => setWindowId(e.target.value)}
              aria-label="Intervalo de tempo das séries"
              style={{ minWidth: 170 }}
            >
              {WINDOWS.map((w) => (
                <option key={w.id} value={w.id}>
                  {w.label}
                </option>
              ))}
            </select>
          </label>
        }
      />

      {detail === null ? (
        <EmptyState
          icon={<ServerCrash size={32} strokeWidth={1.5} />}
          title="Servidor não encontrado"
          body={
            <>
              Nenhum servidor com o hostname <code>{hostname}</code> no inventário. Ele pode ter sido
              removido ou nunca reportou métricas.
            </>
          }
        />
      ) : (
        <>
          <div style={{ marginBottom: "var(--sp-4)" }}>
            <Badge state={detail?.up ? "ok" : "crit"}>{detail?.up ? "no ar" : "sem sinal"}</Badge>{" "}
            <span style={MUTED_12}>
              <LastSeen ts={detail?.last_seen} />
            </span>
          </div>

          {/* O estado da máquina AGORA, antes de qualquer gráfico: a resposta que a
              pessoa veio buscar fica no topo, e os gráficos explicam o porquê. */}
          <KpiGrid>
            {live
              ? resumoDoServidor(live).map((k) => (
                  <Kpi key={k.rotulo} rotulo={k.rotulo} valor={k.valor} sub={k.sub} tone={k.tone} />
                ))
              : ["CPU", "RAM", "Disco mais cheio", "Rede, recebendo", "Processos", "No ar há"].map((r) => (
                  <Kpi key={r} rotulo={r} valor={null} loading />
                ))}
          </KpiGrid>

          <Tabs tabs={TABS} active={tab} onChange={(id) => setTab(id as TabId)} />

          <div style={{ marginTop: "var(--sp-4)" }}>
            {tab === "overview" && (
              <OverviewTab hostname={hostname} windowSeconds={win.seconds} detail={detail} live={live} />
            )}
            {tab === "cpu" && (
              <div className="stack">
                {/* O gráfico de cima é a CPU EXIGIDA (consumo + roubo) — o mesmo número
                    do cartão. Os dois abaixo abrem a conta: sem eles, um servidor
                    estrangulado pelo provedor é indistinguível de um servidor com
                    software pesado, e a ação que cada caso pede é oposta. Só aparecem
                    quando o agente publica as parcelas (0.8.1+). */}
                <MetricChart metric={cpuSerieDoHost(detail?.agent_version)} host={hostname} windowSeconds={win.seconds} />
                {cpuSerieDoHost(detail?.agent_version) === "system.cpu.utilization.total" && (
                  <>
                    <MetricChart metric="system.cpu.utilization" host={hostname} windowSeconds={win.seconds} />
                    <MetricChart metric="system.cpu.steal" host={hostname} windowSeconds={win.seconds} />
                  </>
                )}
              </div>
            )}
            {tab === "ram" && (
              <div className="stack">
                <MetricChart metric="system.memory.utilization" host={hostname} windowSeconds={win.seconds} />
                <MetricChart metric="system.memory.used" host={hostname} windowSeconds={win.seconds} />
              </div>
            )}
            {tab === "disks" && (
              <div className="stack">
                <MetricChart metric="system.filesystem.utilization" host={hostname} windowSeconds={win.seconds} />
              </div>
            )}
            {tab === "net" && (
              // Nomes técnicos EXATOS do agente (`bytes_recv`/`bytes_sent`). Os nomes
              // `.receive`/`.transmit` usados antes nunca existiram no armazenamento:
              // a consulta voltava vazia e a aba dizia "sem dados" — indistinguível de
              // "sem tráfego" — desde sempre.
              //
              // agg="rate": bytes_recv/bytes_sent são CONTADORES ACUMULADOS desde o boot
              // — só sobem. Com a média padrão a linha subia igual em repouso e numa
              // rajada: no mesmo passo de 60 s a coluna plotada não distinguia 13.511 de
              // 156.502 bytes/s. `rate` mede quanto o contador andou por segundo (o
              // backend já protege reset de contador com greatest(…,0)) e a unidade
              // vira bytes/s sozinha via unitForAgg. É o que o painel de fábrica
              // (dashboards/starter.go) e a lista de servidores já faziam.
              <div className="stack">
                <MetricChart metric="system.network.io.bytes_recv" host={hostname} windowSeconds={win.seconds} agg="rate" />
                <MetricChart metric="system.network.io.bytes_sent" host={hostname} windowSeconds={win.seconds} agg="rate" />
              </div>
            )}
            {tab === "containers" && <ContainersTab live={live} />}
            {tab === "procs" && (
              <div className="stack">
                <MetricChart metric="system.processes.count" host={hostname} windowSeconds={win.seconds} />
                <EmptyState
                  icon={<Cpu size={32} strokeWidth={1.5} />}
                  title="Lista por processo não é coletada"
                  body="O agente coleta o número total de processos (gráfico acima), mas não a lista individual por nome, uso de CPU ou memória. Só o que o backend fornece aparece aqui."
                />
              </div>
            )}
            {tab === "alerts" && <AlertsTab hostname={hostname} />}
          </div>
        </>
      )}
    </div>
  );
}

// Aba Overview: gráficos empilhados (CPU/RAM/Disco/Rede) + painel lateral fixo com
// as informações "de placa" do servidor e o uso atual de CPU (ao vivo).
function OverviewTab({
  hostname,
  windowSeconds,
  detail,
  live,
}: {
  hostname: string;
  windowSeconds: number;
  detail: HostDetailModel | null | undefined;
  live: HostMetrics | null;
}) {
  return (
    <div style={{ display: "flex", gap: "var(--sp-4)", alignItems: "flex-start", flexWrap: "wrap" }}>
      <div className="stack" style={{ flex: "1 1 480px", minWidth: 0 }}>
        <MetricChart metric={cpuSerieDoHost(detail?.agent_version)} host={hostname} windowSeconds={windowSeconds} />
        <MetricChart metric="system.memory.utilization" host={hostname} windowSeconds={windowSeconds} />
        <MetricChart metric="system.filesystem.utilization" host={hostname} windowSeconds={windowSeconds} />
        {/* Rede é contador acumulado: sem agg="rate" a linha só sobe e não distingue
            repouso de rajada (ver a aba Rede abaixo). */}
        <MetricChart metric="system.network.io.bytes_recv" host={hostname} windowSeconds={windowSeconds} agg="rate" />
      </div>
      <div className="stack" style={{ flex: "0 1 320px", minWidth: 260 }}>
        <InfoPanel detail={detail} live={live} hostname={hostname} />
        <ServicosDoServidor hostname={hostname} />
      </div>
    </div>
  );
}

// Painel lateral "Informações do servidor". Só mostra o que o backend fornece; campos
// vazios são omitidos (nunca inventa valor).
function InfoPanel({
  detail,
  live,
  hostname,
}: {
  detail: HostDetailModel | null | undefined;
  live: HostMetrics | null;
  hostname: string;
}) {
  if (detail === undefined) {
    return (
      <Card title="Informações do servidor">
        <Skeleton height={18} />
        <div style={{ marginTop: "var(--sp-2)" }}>
          <Skeleton height={18} width="80%" />
        </div>
      </Card>
    );
  }

  const uptimeSecs = live?.up ? live.uptime_secs : detail?.uptime_secs ?? 0;
  // `nodata` não pode virar "0,0%": é o único consumidor de HealthMetric que ficava
  // de fora, e escreveria "Uso atual de CPU: 0,0%" num servidor que ninguém mediu.
  const currentCpu = live?.up && live.cpu.state !== "nodata" ? formatValue(live.cpu.pct, "percent") : null;

  const rows: { label: string; value: ReactNode }[] = [];
  const push = (label: string, value: string | number | undefined | null) => {
    if (value == null || value === "" || value === 0) return;
    rows.push({ label, value: String(value) });
  };
  rows.push({ label: "Hostname", value: <code>{hostname}</code> });
  push("SO", detail?.os);
  push("Kernel", detail?.kernel);
  push("Arquitetura", detail?.arch);
  push("Modelo de CPU", detail?.cpu_model);
  push("Núcleos", detail?.cpu_cores);
  if (uptimeSecs > 0) rows.push({ label: "Uptime", value: formatUptime(uptimeSecs) });
  push("IPs", detail?.ips);
  push("Versão do agente", detail?.agent_version);
  if (currentCpu) rows.push({ label: "Uso atual de CPU", value: currentCpu });

  return (
    <Card title="Informações do servidor">
      <dl style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)", margin: 0 }}>
        {rows.map((r) => (
          <div key={r.label} style={{ display: "flex", justifyContent: "space-between", gap: "var(--sp-2)" }}>
            <dt style={MUTED_12}>{r.label}</dt>
            <dd style={{ margin: 0, fontSize: "var(--fs-14)", color: "var(--text-1)", textAlign: "right", wordBreak: "break-word" }}>
              {r.value}
            </dd>
          </div>
        ))}
      </dl>
      {!live?.up && (
        <p style={{ ...MUTED_12, marginTop: "var(--sp-3)", marginBottom: 0, fontStyle: "italic" }}>
          Sem métricas ao vivo recentes, o uso atual de CPU aparece quando o servidor volta a reportar.
        </p>
      )}
    </Card>
  );
}

// Gráfico de uma métrica filtrada por host, no intervalo escolhido. Reconsulta quando
// muda métrica/host/janela. Estado loading/empty/error propagado ao TimeSeriesPanel.
function MetricChart({
  metric,
  host,
  windowSeconds,
  title,
  description,
  agg = "avg",
}: {
  metric: string;
  host: string;
  windowSeconds: number;
  title?: string;
  description?: string;
  agg?: string;
}) {
  const { hostLabel } = useHostNames();
  const [resp, setResp] = useState<QueryResponse | null>(null);
  const [state, setState] = useState<PanelState>("loading");
  const [err, setErr] = useState<string>();
  const meta = metricMeta(metric);
  // Mesmo cálculo de passo usado na consulta — o rodapé precisa dizer a resolução
  // REAL do que está desenhado, não uma estimativa.
  const step = Math.max(60, Math.round(windowSeconds / 500));

  const load = useCallback(() => {
    const to = new Date();
    const from = new Date(to.getTime() - windowSeconds * 1000);
    const step = Math.max(60, Math.round(windowSeconds / 500));
    setState("loading");
    queryMetric({
      metric,
      filters: { host },
      from: from.toISOString(),
      to: to.toISOString(),
      step,
      agg,
    })
      .then((r) => {
        setResp(r);
        // O vazio é decidido pelo CONTEÚDO das séries, não por `ts.length`: a Query API
        // devolve grade densa (um balde por passo, mesmo sem amostra), então `ts` nunca
        // vem vazio e "Sem dados no período." nunca mais apareceria — o operador via um
        // gráfico em branco, que se lê como "nada aconteceu". Ver metrics/emptiness.ts.
        setState(panelStateFor(r.series));
        setErr(undefined);
      })
      .catch((e) => {
        console.error("queryMetric falhou", e);
        setResp(null);
        // A explicação que o SERVIDOR escreveu é acionável ("400 from/to inválidos
        // (RFC3339, to>from)"); trocá-la por "Tente outro intervalo" jogava fora a
        // única pista do que fazer. Mesmo tratamento de DashboardView e Explore.
        setErr(mensagemDeErro(e, "Não foi possível carregar a série. Tente outro intervalo."));
        setState("error");
      });
  }, [metric, host, windowSeconds, agg]);

  useEffect(() => {
    load();
  }, [load]);

  const data = useMemo<TimeSeriesData>(() => {
    if (!resp) return { ts: [], series: [] };
    const labels = buildSeriesLabels(resp.series, meta.label, hostLabel);
    return {
      ts: resp.ts,
      series: resp.series.map((s, i) => ({ label: labels[i], values: s.values })),
    };
  }, [resp, meta.label, hostLabel]);

  return (
    <TimeSeriesPanel
      title={(title ?? meta.label) + aggSuffix(agg)}
      description={description ?? (meta.description ? `${meta.description}, nome técnico: ${metric}` : metric)}
      state={state}
      error={err}
      // A unidade depende da AGREGAÇÃO: `rate` sobre um contador de bytes é bytes/s.
      // Sem isto o eixo do gráfico de rede diria "32 KB" onde o valor é "32 KB/s".
      unit={unitForAgg(meta.unit, agg)}
      correlateHost={host}
      // Agregação + resolução efetiva: `table` vem da resposta da própria consulta,
      // então a nota acompanha a troca automática de rollup ao mudar o intervalo.
      note={panelFootnote({ agg, table: resp?.table, step, aggEfetivo: resp?.agg_efetivo })}
      data={data}
    />
  );
}

// Aba Containers: containers ao vivo do host (ContainersBlock aberto por padrão).
function ContainersTab({ live }: { live: HostMetrics | null }) {
  const containers = live?.containers ?? [];
  if (!live?.up) {
    return (
      <EmptyState
        icon={<Boxes size={32} strokeWidth={1.5} />}
        title="Sem métricas ao vivo recentes"
        body="Os containers deste servidor aparecem quando o agente volta a reportar."
      />
    );
  }
  if (containers.length === 0) {
    return (
      <EmptyState
        icon={<Boxes size={32} strokeWidth={1.5} />}
        title="Nenhum container neste servidor"
        body="O agente não encontrou containers Docker em execução aqui. Assim que houver, eles aparecem nesta aba automaticamente."
      />
    );
  }
  return (
    <Card title="Containers">
      <ContainersBlock containers={containers} defaultOpen />
    </Card>
  );
}

// Valor observado do alerta COM unidade e o limite que ele passou. Sem a regra
// (apagada, ou lista ainda carregando) cai no número puro — mas com unidade "none",
// nunca num "92,54" que o operador precisaria adivinhar se é % ou GB.
function alertValueText(a: AlertEvent, rule: AlertRule | null): string {
  if (!rule) return formatValue(a.value, "none");
  const valor = formatMetric(rule.metric, a.value);
  const limite = formatMetric(rule.metric, rule.threshold);
  return `${valor}, limite: ${limite}`;
}

// Aba Alertas: alertas ativos cujo labels.host casa com o hostname (piores primeiro).
function AlertsTab({ hostname }: { hostname: string }) {
  const [alerts, setAlerts] = useState<AlertEvent[] | null>(null);
  const [rules, setRules] = useState<Map<number, AlertRule>>(new Map());
  const [err, setErr] = useState(false);

  useEffect(() => {
    setAlerts(null);
    setErr(false);
    listAlerts(true)
      .then((r) => setAlerts(r.alerts.filter((a) => a.labels?.host === hostname)))
      .catch(() => setErr(true));
  }, [hostname]);

  // As regras trazem a MÉTRICA de cada alerta — é dela que sai a unidade do valor
  // ("92,5%" em vez de "92,54") e o limite ultrapassado. Falha aqui não quebra a
  // aba: sem regra, o valor sai sem unidade, como antes.
  useEffect(() => {
    listAlertRules()
      .then((r) => setRules(new Map(r.rules.map((x) => [x.id, x]))))
      .catch(() => setRules(new Map()));
  }, []);

  if (err) {
    return <p style={{ color: "var(--crit)", margin: 0 }}>Não foi possível carregar os alertas.</p>;
  }
  if (alerts === null) {
    return (
      <div className="stack">
        <Skeleton height={20} />
        <Skeleton height={20} width="80%" />
      </div>
    );
  }
  if (alerts.length === 0) {
    return (
      <EmptyState
        icon={<TriangleAlert size={32} strokeWidth={1.5} />}
        title="Nenhum alerta ativo"
        body="Este servidor não tem alertas disparados no momento. Quando uma regra dispara para ele, o alerta aparece aqui."
      />
    );
  }

  const sorted = [...alerts].sort((a, b) => sevRank(b.severity) - sevRank(a.severity));
  return (
    <Card title={`Alertas ativos (${sorted.length})`}>
      <ul style={{ listStyle: "none", padding: 0, margin: 0 }}>
        {sorted.map((a) => (
          <li key={a.id} style={{ padding: "var(--sp-2) 0", borderBottom: "1px solid var(--border)" }}>
            <div style={{ display: "flex", alignItems: "center", gap: "var(--sp-2)", flexWrap: "wrap" }}>
              <Badge state={sevState(a.severity)}>{SEV_LABEL[a.severity?.toLowerCase()] ?? a.severity}</Badge>
              <strong style={{ fontSize: "var(--fs-14)" }}>{a.rule_name}</strong>
              {a.flapping && <span style={MUTED_12}>(instável)</span>}
              <span style={{ ...MUTED_12, marginLeft: "auto" }} title={fmtRelAbs(a.started_at).abs}>
                desde {fmtRelAbs(a.started_at).rel}
              </span>
            </div>
            <div style={MUTED_12}>
              valor{" "}
              <span className="tabular" style={{ color: "var(--text-1)", fontWeight: 600 }}>
                {alertValueText(a, rules.get(a.rule_id) ?? null)}
              </span>
              {a.acked_by ? ` · reconhecido por ${a.acked_by}` : ""}
            </div>
          </li>
        ))}
      </ul>
    </Card>
  );
}

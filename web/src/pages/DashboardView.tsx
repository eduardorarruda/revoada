// Visualização de dashboard: carrega o modelo e renderiza cada painel com dados
// REAIS da Query API. Grade por gridPos, seletor de período, variáveis.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button, Card, IconButton, ActionIcons, PageHeader, Skeleton, EmptyState } from "../components";
import { StatPanel, TimeSeriesPanel, GaugePanel, BarGaugePanel, TablePanel, type TimeSeriesData } from "../panels";
import { buildSeriesLabels } from "../panels/seriesLabels";
import { getDashboard, getRole, hostMetrics, listHosts, queryMetric, type ContainerStatus, type Dashboard, type DashboardModel, type HostDetail, type QueryResponse } from "../api";
import { useHostNames } from "../hooks/useHostNames";
import { useHostThresholds } from "../hooks/useHostThresholds";
import { metricMeta } from "../metrics/dict";
import { panelStateFor } from "../metrics/emptiness";
import { displayValue, lastPoint, panelFreshness, pointFreshness, staleAfterSeconds, type LastPoint } from "../metrics/lastPoint";
import { panelFootnote, unitForAgg } from "../metrics/resolution";
import { thresholdForMetric } from "../metrics/thresholds";
import { fmtRelAbs, mensagemDeErro } from "../format";
import { usePolling } from "../hooks/usePolling";
import { LiveClient, type LiveStatus } from "../live";
import { ContainersBlock } from "./containers";

const RANGES = [
  { label: "15m", seconds: 900 },
  { label: "1h", seconds: 3600 },
  { label: "6h", seconds: 21600 },
  { label: "24h", seconds: 86400 },
  { label: "7d", seconds: 604800 },
  { label: "30d", seconds: 2592000 },
  { label: "90d", seconds: 7776000 },
  { label: "1 ano", seconds: 31536000 },
];

// stepFor escolhe a resolução (segundos por ponto) pela janela. Alinhado ao
// pickTable do backend: janelas > 7d caem em metrics_1h (rollup horário, retенção
// 2 anos), 24h–7d em metrics_1m, e o restante no bruto. Passos maiores em janelas
// longas evitam milhares de pontos por série (ex.: 1 ano ≈ 8.760 pontos a 1h).
function stepFor(seconds: number): number {
  if (seconds <= 86400) return 60; // até 24h: 1 min
  if (seconds <= 604800) return 300; // até 7d: 5 min (metrics_1m)
  if (seconds <= 7776000) return 3600; // até 90d: 1 h (metrics_1h)
  return 21600; // acima de 90d: 6 h
}

// HOST_VAR é o sentinela gravado nos filtros do dashboard genérico ("Visão do Host").
// O visualizador troca este valor pelo servidor escolhido no seletor antes de
// consultar a Query API (o backend faz match literal de label, não expande $host).
const HOST_VAR = "$host";

// usesHostVar detecta o dashboard genérico: algum painel filtra host por "$host".
function usesHostVar(model: DashboardModel): boolean {
  return model.panels.some((p) => p.query.filters && Object.values(p.query.filters).includes(HOST_VAR));
}

// firstLiteralHost extrai o servidor de um dashboard por-host (uid "host-…"): o
// filtro host dos painéis carrega o hostname literal (não "$host"). Devolve null
// quando não há host fixo (dashboard genérico ou sem filtro host).
function firstLiteralHost(model: DashboardModel): string | null {
  for (const p of model.panels) {
    const h = p.query.filters?.host;
    if (h && h !== HOST_VAR) return h;
  }
  return null;
}

// resolveFilters substitui "$host" pelo servidor selecionado. Devolve o próprio
// objeto quando não há nada a trocar (estabilidade referencial para os efeitos).
function resolveFilters(filters: Record<string, string> | undefined, host: string): Record<string, string> | undefined {
  if (!filters) return filters;
  let changed = false;
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(filters)) {
    if (v === HOST_VAR) {
      out[k] = host;
      changed = true;
    } else {
      out[k] = v;
    }
  }
  return changed ? out : filters;
}

export function DashboardView({ uid, initialHost = "" }: { uid: string; initialHost?: string }) {
  const [dash, setDash] = useState<Dashboard | null>(null);
  const [rangeSec, setRangeSec] = useState(21600);
  const [error, setError] = useState(false);
  // Servidor escolhido no dashboard genérico (variável $host). Vem pré-selecionado
  // pela URL (?var-host=<host>, usado nos deep links das notificações).
  const [host, setHost] = useState(initialHost);
  const [hosts, setHosts] = useState<HostDetail[]>([]);
  // Só editor/admin veem os atalhos de edição (viewer não gerencia dashboards).
  const canEdit = getRole() === "admin";

  const load = useCallback(() => {
    setError(false);
    setDash(null);
    getDashboard(uid)
      .then(setDash)
      .catch(() => setError(true));
  }, [uid]);

  useEffect(() => {
    load();
  }, [load]);

  // Dashboard genérico ("Visão do Host"): tem painéis com filtro host = "$host".
  const needsHost = useMemo(() => (dash ? usesHostVar(dash.model) : false), [dash]);
  // Servidor efetivo do quadro de Containers: o escolhido no seletor (genérico) ou
  // o hostname fixo do dashboard por-host (uid "host-…").
  const effectiveHost = needsHost ? host : dash ? firstLiteralHost(dash.model) : null;

  // Carrega a lista de servidores só quando o dashboard precisa do seletor.
  useEffect(() => {
    if (!needsHost) return;
    let alive = true;
    listHosts()
      .then((r) => {
        if (alive) setHosts(r.hosts ?? []);
      })
      .catch(() => {
        /* seletor fica vazio; o usuário ainda vê a mensagem de escolha */
      });
    return () => {
      alive = false;
    };
  }, [needsHost]);

  // Se a URL trouxe um host, reflete a troca de rota (deep link para outro servidor).
  useEffect(() => {
    if (initialHost) setHost(initialHost);
  }, [initialHost]);

  if (error) {
    return (
      <div style={{ padding: "var(--sp-5)", maxWidth: 1400, margin: "0 auto" }}>
        <EmptyState
          title="Não foi possível carregar este dashboard"
          body="Houve um erro ao buscar este dashboard. Verifique a conexão e tente novamente."
          action={{ label: "Tentar novamente", onClick: load }}
        />
      </div>
    );
  }

  if (!dash) {
    // Esqueleto: cabeçalho + grade de painéis (evita salto de layout no carregamento).
    return (
      <div style={{ padding: "var(--sp-5)", maxWidth: 1400, margin: "0 auto" }}>
        <div style={{ marginBottom: "var(--sp-4)" }}>
          <Skeleton height={32} width={280} />
          <div style={{ marginTop: "var(--sp-2)" }}>
            <Skeleton height={14} width={160} />
          </div>
        </div>
        <div style={{ display: "grid", gridTemplateColumns: "repeat(24, 1fr)", gap: "var(--sp-4)" }}>
          {[0, 1, 2, 3, 4, 5].map((i) => (
            <div key={i} style={{ gridColumn: "span 8" }}>
              <Skeleton height={160} />
            </div>
          ))}
        </div>
      </div>
    );
  }

  return (
    <div style={{ padding: "var(--sp-5)", maxWidth: 1400, margin: "0 auto" }}>
      <PageHeader
        title={dash.title}
        subtitle={`${dash.folder} · v${dash.version}`}
        actions={
          <>
            {needsHost && (
              <select
                aria-label="Servidor"
                value={host}
                onChange={(e) => setHost(e.target.value)}
                style={{
                  background: "var(--surface-2)",
                  color: "var(--text-1)",
                  border: "1px solid var(--border)",
                  borderRadius: "var(--radius-2)",
                  padding: "var(--sp-1) var(--sp-2)",
                  marginRight: "var(--sp-2)",
                }}
              >
                <option value="">Selecione o servidor…</option>
                {hosts.map((h) => (
                  <option key={h.hostname} value={h.hostname}>
                    {h.display_name && h.display_name.trim() !== "" ? h.display_name : h.hostname}
                    {h.up ? "" : " (offline)"}
                  </option>
                ))}
              </select>
            )}
            {RANGES.map((r) => (
              <Button key={r.label} variant={rangeSec === r.seconds ? "primary" : "ghost"} onClick={() => setRangeSec(r.seconds)}>
                {r.label}
              </Button>
            ))}
            {canEdit && (
              <>
                <IconButton
                  icon={ActionIcons.history}
                  label="Histórico de versões"
                  onClick={() => (window.location.hash = `/d/${uid}/versions`)}
                />
                <IconButton
                  icon={ActionIcons.edit}
                  label="Editar este dashboard"
                  variant="primary"
                  onClick={() => (window.location.hash = `/d/${uid}/edit`)}
                />
              </>
            )}
          </>
        }
      />
      {needsHost && !host ? (
        <EmptyState
          title="Selecione um servidor"
          body="Este é o painel genérico “Visão do Host”. Escolha um servidor no seletor acima para ver CPU, memória, disco, swap, rede e uptime dele."
        />
      ) : (
        <>
          <Grid model={dash.model} rangeSec={rangeSec} host={host} />
          {effectiveHost && (
            <div style={{ marginTop: "var(--sp-4)" }}>
              <HostContainersCard host={effectiveHost} />
            </div>
          )}
        </>
      )}
    </div>
  );
}

// Quadro "Containers" da Visão do Host: lista os containers em execução no servidor
// e quanto cada um usa (CPU%/memória) + estado. Reusa /api/host-metrics (mesma fonte
// da Infraestrutura) e filtra pelo host do dashboard — sem endpoint novo.
function HostContainersCard({ host }: { host: string }) {
  const [containers, setContainers] = useState<ContainerStatus[] | null>(null);
  // reportando: o host está mandando métricas? Lista vazia com o servidor CALADO não
  // é "nenhum container" — é "não sei". Dizer a primeira coisa é afirmar sobre a
  // máquina do cliente um fato que ninguém mediu.
  const [reportando, setReportando] = useState(true);
  const [error, setError] = useState(false);

  const load = useCallback(() => {
    hostMetrics()
      .then((r) => {
        const found = r.hosts.find((h) => h.host === host);
        setContainers(found?.containers ?? []);
        setReportando(found?.up ?? false);
        setError(false);
      })
      .catch(() => setError(true));
  }, [host]);
  // Poll leve (5s) para o uso acompanhar o resto do dashboard ao vivo.
  usePolling(load, 5000, [load]);

  return (
    <Card title="Containers">
      {error && containers === null ? (
        <p style={{ color: "var(--text-3)", margin: 0, fontSize: "var(--fs-14)" }}>
          Não foi possível carregar os containers deste servidor.
        </p>
      ) : containers === null ? (
        <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
          <Skeleton height={20} />
          <Skeleton height={20} width="80%" />
        </div>
      ) : containers.length === 0 ? (
        <p style={{ color: "var(--text-3)", margin: 0, fontSize: "var(--fs-14)" }}>
          {reportando
            ? "Nenhum container em execução neste servidor."
            : "Sem dado recente: este servidor parou de reportar, então não dá para dizer o que está rodando nele."}
        </p>
      ) : (
        <ContainersBlock containers={containers} />
      )}
    </Card>
  );
}

// Rótulos enxutos: mostra só o que distingue as séries (ver panels/seriesLabels).
function toTimeSeries(
  resp: QueryResponse,
  fallbackTitle: string,
  hostLabel: (h: string) => string,
): TimeSeriesData {
  const labels = buildSeriesLabels(resp.series, fallbackTitle, hostLabel);
  return {
    ts: resp.ts,
    series: resp.series.map((s, i) => ({
      label: labels[i],
      values: s.values,
    })),
  };
}

function Grid({ model, rangeSec, host }: { model: DashboardModel; rangeSec: number; host: string }) {
  const sorted = useMemo(
    () => [...model.panels].sort((a, b) => a.gridPos.y - b.gridPos.y || a.gridPos.x - b.gridPos.x),
    [model.panels],
  );

  // Um cliente WS por dashboard, criado de forma síncrona (antes dos efeitos dos filhos,
  // que rodam antes dos do pai — daí NÃO usar useEffect para instanciar).
  const [client] = useState(() => new LiveClient());
  const [liveStatus, setLiveStatus] = useState<LiveStatus>("reconnecting");
  useEffect(() => {
    const off = client.onStatus(setLiveStatus);
    client.connect();
    return () => {
      off();
      client.close();
    };
  }, [client]);
  useEffect(() => {
    client.setSubscriptions(
      sorted.map((p) => ({
        panelId: p.id,
        metric: p.query.metric,
        filters: resolveFilters(p.query.filters, host),
        group_by: p.query.group_by,
        agg: p.query.agg,
        step: stepFor(rangeSec),
        // Janela cheia: o server re-consulta `now-window` a cada update e o cliente
        // SUBSTITUI a série. Encolher aqui faria o gráfico de 30d/90d/1a colapsar para
        // a janela menor no 1º tick ao vivo. Janelas longas usam step grande sobre os
        // rollups (metrics_1h), então a reconsulta é barata (centenas de pontos).
        window: rangeSec,
      })),
    );
  }, [client, sorted, rangeSec, host]);

  return (
    <div style={{ display: "grid", gridTemplateColumns: "repeat(24, 1fr)", gap: "var(--sp-4)" }}>
      {sorted.map((p) => (
        <div key={p.id} style={{ gridColumn: `span ${Math.min(24, p.gridPos.w)}` }}>
          <PanelLoader panel={p} rangeSec={rangeSec} host={host} live={client} liveStatus={liveStatus} />
        </div>
      ))}
    </div>
  );
}

function PanelLoader({
  panel,
  rangeSec,
  host,
  live,
  liveStatus,
}: {
  panel: DashboardModel["panels"][number];
  rangeSec: number;
  host: string;
  live: LiveClient;
  liveStatus: LiveStatus;
}) {
  const [data, setData] = useState<TimeSeriesData | null>(null);
  const [state, setState] = useState<"loading" | "ok" | "empty" | "error">("loading");
  const [err, setErr] = useState<string>();
  const [ageSeconds, setAgeSeconds] = useState<number | undefined>();
  // Resolução efetiva devolvida pela Query API (bruto / rollup 1m / rollup 1h). A
  // API sempre mandou este campo; nenhum componente o lia, então a troca silenciosa
  // de resolução em janelas longas nunca chegava à tela.
  const [table, setTable] = useState<string | undefined>();
  const lastUpdate = useRef<number>(0);
  const { hostLabel } = useHostNames();
  const thresholds = useHostThresholds();
  const filters = resolveFilters(panel.query.filters, host);
  const step = stepFor(rangeSec);

  // carga inicial (histórico completo do período)
  useEffect(() => {
    let alive = true;
    setState("loading");
    const to = new Date();
    const from = new Date(to.getTime() - rangeSec * 1000);
    queryMetric({
      metric: panel.query.metric,
      filters,
      group_by: panel.query.group_by,
      from: from.toISOString(),
      to: to.toISOString(),
      step: stepFor(rangeSec),
      agg: panel.query.agg,
    })
      .then((resp) => {
        if (!alive) return;
        setData(toTimeSeries(resp, panel.title, hostLabel));
        setTable(resp.table);
        // Vazio pelo CONTEÚDO das séries, não por `ts.length`: a Query API devolve
        // grade densa (um balde por passo, mesmo sem amostra), então `ts` nunca vem
        // vazio e "Sem dados no período." tinha virado inalcançável — um painel de
        // host mudo desenhava um gráfico em branco. Ver metrics/emptiness.ts.
        setState(panelStateFor(resp.series));
      })
      .catch((e) => {
        if (!alive) return;
        setErr(mensagemDeErro(e, "Não foi possível carregar este painel."));
        setState("error");
      });
    return () => {
      alive = false;
    };
    // Dep em `host` (não `filters`): filters é objeto novo a cada render e re-dispararia.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [panel, rangeSec, host, hostLabel]);

  // atualizações ao vivo (WS)
  useEffect(() => {
    const off = live.onUpdate(panel.id, (resp, at) => {
      lastUpdate.current = at;
      setData(toTimeSeries(resp, panel.title, hostLabel));
      setTable(resp.table);
      // Mesmo critério da carga inicial: grade densa não é dado (ver emptiness.ts).
      setState(panelStateFor(resp.series));
    });
    return off;
  }, [panel.id, live, panel.title, hostLabel]);

  // frescor: idade desde o último update
  useEffect(() => {
    const t = setInterval(() => {
      if (lastUpdate.current) setAgeSeconds(Math.max(0, Math.floor(Date.now() / 1000 - lastUpdate.current)));
    }, 1000);
    return () => clearInterval(t);
  }, []);

  // A unidade depende da agregação: `rate` sobre bytes é bytes/s (ver unitForAgg).
  const unit = unitForAgg(metricMeta(panel.query.metric).unit, panel.query.agg);
  // Host efetivo do painel: o filtro já resolvido ($host trocado pelo servidor
  // escolhido). É ele que decide se existe override de limiar.
  const panelHost = filters?.host;
  const threshold = thresholdForMetric(panel.query.metric, panelHost, thresholds);

  // Último ponto de cada série COM o instante dele; `displayValue` só devolve
  // número quando o ponto ainda vale como atual. Antes era `?? 0`: host morto virava
  // "Processos: 0" e gauge de CPU em 0% verde.
  const ts = data?.ts ?? [];
  const pontos: { label: string; p: LastPoint }[] = (data?.series ?? []).map((s) => ({
    label: s.label,
    p: lastPoint(ts, s.values),
  }));
  const primeiro: LastPoint = pontos[0]?.p ?? { value: null, ts: null };
  const valorAtual = displayValue(primeiro, step);
  const stale = staleAfterSeconds(step);
  const tsMs = primeiro.ts != null ? primeiro.ts * 1000 : null;

  // Selo de frescor: pior entre a saúde da conexão e a idade do próprio dado
  // (ver metrics/lastPoint.ts — regra pura, com teste).
  const fresh = panelFreshness({
    connected: liveStatus !== "reconnecting",
    messageAgeSeconds: ageSeconds,
    point: primeiro,
    step,
  });

  const base = {
    title: panel.title,
    description: panel.description,
    state,
    error: err,
    freshness: fresh,
    unit,
    note: panelFootnote({ agg: panel.query.agg, table, step, aggEfetivo: data?.agg_efetivo }),
  };

  // stat/gauge mostram UM número — não têm como representar N séries. Com group_by
  // (ex.: uma série por host) o painel passa a ter várias séries; mostrar só series[0]
  // esconderia todos os outros hosts silenciosamente. Decisão de MENOR risco: quando há
  // mais de uma série, caímos para barras horizontais (uma por host/série, com o rótulo
  // amigável já resolvido), sem perder dado. Com uma única série, o número grande fica.
  const multiSeries = pontos.length > 1;
  const barItems = pontos.map((x) => ({ label: x.label, value: displayValue(x.p, step) }));

  // Cada tipo de painel é renderizado de forma fiel (o editor oferece os 5).
  switch (panel.type) {
    case "stat":
    case "gauge":
      if (multiSeries) {
        return <BarGaugePanel {...base} items={barItems} threshold={threshold ?? undefined} />;
      }
      return panel.type === "gauge" ? (
        <GaugePanel
          {...base}
          value={valorAtual}
          valueTs={tsMs}
          staleAfterSeconds={stale}
          threshold={threshold ?? undefined}
        />
      ) : (
        <StatPanel {...base} value={valorAtual} valueTs={tsMs} staleAfterSeconds={stale} />
      );
    case "bargauge":
      return <BarGaugePanel {...base} items={barItems} threshold={threshold ?? undefined} />;
    case "table":
      // A coluna "Atualizado" existe para o valor nunca ser lido como "de agora":
      // ela diz, ao lado do número, de quando ele é.
      return (
        <TablePanel
          {...base}
          columns={[
            { key: "serie", label: "Série" },
            { key: "valor", label: "Último valor", numeric: true, unit },
            { key: "quando", label: "Atualizado" },
          ]}
          rows={pontos.map((x) => {
            const f = pointFreshness(x.p, step);
            return {
              serie: x.label,
              valor: x.p.value,
              quando: f.state === "nodata" ? "sem dado" : fmtRelAbs(x.p.ts! * 1000).rel,
            };
          })}
        />
      );
    default:
      return <TimeSeriesPanel {...base} data={data ?? { ts: [], series: [] }} />;
  }
}

// Explore (UX.12): query builder amigável — selects de métrica/agregação/
// agrupamento/janela, sem digitar nada à mão. O resultado vira gráfico e pode ser
// salvo como painel de dashboard. Preserva o mecanismo de query/render original
// (queryMetric + TimeSeriesPanel), só troca os inputs crus por selects com ajuda.
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Compass } from "lucide-react";
import {
  Button,
  Card,
  EmptyState,
  FormField,
  HelpPanel,
  PageHeader,
  useToast,
} from "../components";
import { SaveToDashboardModal, type SavePanel } from "../components/SaveToDashboardModal";
import { TimeSeriesPanel, type PanelState, type TimeSeriesData } from "../panels";
import { buildSeriesLabels } from "../panels/seriesLabels";
import { listHosts, listMetrics, queryMetric, type HostDetail, type QueryResponse } from "../api";
import { useHostNames } from "../hooks/useHostNames";
import { useHostContainers } from "../hooks/useHostContainers";
import { aggBloqueada, metricMeta, metricLabel } from "../metrics/dict";
import { panelStateFor } from "../metrics/emptiness";
import { unitForAgg } from "../metrics/resolution";
import { aggSuffix, panelFootnote } from "../metrics/resolution";
import { help } from "../help";
import { mensagemDeErro } from "../format";

// Opções de agregação (como combinar vários pontos num só valor).
const AGGS: { id: string; label: string }[] = [
  { id: "avg", label: "Média (avg)" },
  { id: "max", label: "Máximo (max)" },
  { id: "min", label: "Mínimo (min)" },
  { id: "sum", label: "Soma (sum)" },
  { id: "last", label: "Último (last)" },
  // Para contadores acumulados (bytes de rede): quanto o contador andou no
  // intervalo, por segundo. Sem ela, o gráfico de rede é uma reta subindo.
  { id: "rate", label: "Taxa (rate)" },
];

function aggLabelDe(id: string): string {
  return AGGS.find((a) => a.id === id)?.label ?? id;
}

// Janelas de tempo pré-definidas (evita o usuário digitar datas).
const WINDOWS: { id: string; label: string; seconds: number }[] = [
  { id: "15m", label: "Últimos 15 minutos", seconds: 15 * 60 },
  { id: "1h", label: "Última 1 hora", seconds: 60 * 60 },
  { id: "6h", label: "Últimas 6 horas", seconds: 6 * 60 * 60 },
  { id: "24h", label: "Últimas 24 horas", seconds: 24 * 60 * 60 },
  { id: "7d", label: "Últimos 7 dias", seconds: 7 * 24 * 60 * 60 },
];

// Seções do HelpPanel a partir de help.pages.explore (mesmo padrão das outras telas).
function exploreHelpSections(): { heading: string; body: ReactNode }[] {
  const p = help.pages.explore;
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
  return sections;
}

export function Explore() {
  const toast = useToast();
  const { hostLabel } = useHostNames();

  const [metrics, setMetrics] = useState<string[]>([]);
  const [metric, setMetric] = useState("");
  const [agg, setAgg] = useState("avg");
  const [groupBy, setGroupBy] = useState("");
  const [windowId, setWindowId] = useState("6h");
  // Servidor (host) selecionado: "" = todos. A lista vem do inventário completo
  // (listHosts), assim mostramos TODOS os servidores, com nome amigável.
  const [hosts, setHosts] = useState<HostDetail[]>([]);
  const [host, setHost] = useState("");
  const [container, setContainer] = useState("");
  const { containersOf } = useHostContainers();

  const [resp, setResp] = useState<QueryResponse | null>(null);
  // Parâmetros da consulta EXECUTADA (não os dos selects, que o usuário pode mexer
  // sem clicar em Executar). É o que o título e o rodapé descrevem — se dissessem o
  // valor do select, o gráfico de média apareceria rotulado como "máximo".
  const [executed, setExecuted] = useState<{ agg: string; step: number } | null>(null);
  const [hasRun, setHasRun] = useState(false);
  const [state, setState] = useState<PanelState>("ok");
  const [err, setErr] = useState<string>();

  const [helpOpen, setHelpOpen] = useState(false);
  const [saveOpen, setSaveOpen] = useState(false);

  // Carrega a lista de métricas coletadas e pré-seleciona a primeira.
  useEffect(() => {
    listMetrics()
      .then((r) => setMetrics(r.metrics))
      .catch(() => {
        setMetrics([]);
        toast.error("Não foi possível carregar a lista de métricas.");
      });
  }, [toast]);

  useEffect(() => {
    if (metrics.length && !metric) setMetric(metrics[0]);
  }, [metrics, metric]);

  // Inventário de servidores para o seletor "Servidor" (todos, com nome amigável).
  useEffect(() => {
    listHosts()
      .then((r) => setHosts(r.hosts ?? []))
      .catch(() => setHosts([]));
  }, []);

  // Opções do seletor ordenadas pelo nome amigável (rótulo), value = hostname técnico.
  const hostOptions = useMemo(
    () =>
      hosts
        .map((h) => ({ hostname: h.hostname, label: hostLabel(h.hostname) }))
        .sort((a, b) => a.label.localeCompare(b.label, "pt-BR")),
    [hosts, hostLabel],
  );

  // Opções de "agrupar por": os rótulos presentes no último resultado.
  const groupByOptions = useMemo(() => {
    const keys = new Set<string>();
    resp?.series.forEach((s) => Object.keys(s.labels).forEach((k) => keys.add(k)));
    return Array.from(keys).sort();
  }, [resp]);

  // Transforma a resposta bruta em dados do gráfico, aplicando o agrupamento
  // escolhido apenas na rotulagem (sem re-consultar o backend).
  const data = useMemo<TimeSeriesData>(() => {
    if (!resp) return { ts: [], series: [] };
    const labels = buildSeriesLabels(resp.series, resp.metric, hostLabel, groupBy || undefined);
    return {
      ts: resp.ts,
      series: resp.series.map((s, i) => ({
        label: labels[i],
        values: s.values,
      })),
    };
  }, [resp, groupBy, hostLabel]);

  async function run() {
    if (!metric) return;
    const win = WINDOWS.find((w) => w.id === windowId) ?? WINDOWS[2];
    setHasRun(true);
    setState("loading");
    setErr(undefined);
    try {
      const to = new Date();
      const from = new Date(to.getTime() - win.seconds * 1000);
      const step = Math.max(60, Math.round(win.seconds / 500));
      const qf: Record<string, string> = {};
      if (host) qf.host = host;
      if (container) qf.container = container;
      const r = await queryMetric({
        metric,
        filters: Object.keys(qf).length ? qf : undefined,
        from: from.toISOString(),
        to: to.toISOString(),
        step,
        agg,
      });
      setResp(r);
      setExecuted({ agg, step });
      // Vazio pelo CONTEÚDO, não por `ts.length`: com a grade densa da Query API o
      // eixo do tempo sempre vem cheio, e uma consulta sem nenhuma série (métrica
      // inexistente, filtro que não casa) entrava em "ok" e desenhava um gráfico em
      // branco em vez de "Sem dados no período." Ver metrics/emptiness.ts.
      setState(panelStateFor(r.series));
    } catch (e) {
      console.error("queryMetric falhou", e);
      setResp(null);
      const motivo = mensagemDeErro(e, "Não foi possível executar a consulta. Verifique a conexão e tente novamente.");
      setErr(motivo);
      setState("error");
      toast.error(motivo);
    }
  }

  // Auto-executa a métrica default no mount (respeitando um debounce curto), para
  // a tela não nascer vazia e para popular as opções de "Agrupar por". Roda 1× só.
  const didAutoRun = useRef(false);
  useEffect(() => {
    if (!metric || didAutoRun.current) return;
    didAutoRun.current = true;
    const t = setTimeout(() => {
      void run();
    }, 250);
    return () => clearTimeout(t);
    // Roda uma vez só (didAutoRun): incluir `run` re-dispararia a cada render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [metric]);

  // Painel construído a partir da consulta atual, no formato de DashboardModel.panels.
  const savePanel: SavePanel = {
    id: 1,
    type: "timeseries",
    title: metric || "Consulta",
    gridPos: { x: 0, y: 0, w: 12, h: 8 },
    query: { metric, agg, ...(host ? { filters: { host } } : {}) },
  };

  return (
    <div className="page">
      <PageHeader
        title="Explore"
        subtitle="Monte consultas ad-hoc sem digitar nada à mão"
        onHelp={() => setHelpOpen(true)}
        actions={
          <Button variant="primary" onClick={() => setSaveOpen(true)} disabled={!hasRun || !metric}>
            Salvar em dashboard
          </Button>
        }
      />

      <div className="stack">
        <Card title="Consulta">
          <div className="grid-3">
            <FormField label="Métrica" help={help.fields["explore.metric"]}>
              <select className="field" value={metric} onChange={(e) => setMetric(e.target.value)}>
                {metrics.length === 0 && <option value="">Nenhuma métrica coletada ainda</option>}
                {metrics.map((m) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ))}
              </select>
            </FormField>

            <FormField
              label="Servidor"
              hint={
                hostOptions.length === 0
                  ? "Nenhum servidor no inventário ainda."
                  : "Deixe em “Todos” para somar/agregar todos os servidores."
              }
            >
              <select
                className="field"
                value={host}
                onChange={(e) => {
                  setHost(e.target.value);
                  setContainer(""); // troca de servidor zera o container escolhido
                }}
              >
                <option value="">Todos os servidores</option>
                {hostOptions.map((h) => (
                  <option key={h.hostname} value={h.hostname}>
                    {h.label}
                  </option>
                ))}
              </select>
            </FormField>

            <FormField
              label="Container"
              hint={!host ? "Escolha um servidor para filtrar por container." : undefined}
            >
              <select
                className="field"
                value={container}
                disabled={!host || containersOf(host).length === 0}
                onChange={(e) => setContainer(e.target.value)}
              >
                <option value="">Todos os containers</option>
                {containersOf(host).map((c) => (
                  <option key={c} value={c}>
                    {c}
                  </option>
                ))}
              </select>
            </FormField>

            {/* Agregação sem significado para a métrica sai DESABILITADA com o
                motivo: somar um contador acumulado (container.restarts) empilha o
                mesmo passado a cada balde e devolve um número que parece certo. */}
            <FormField label="Agregação" help={help.fields["explore.agg"]}>
              <select className="field" value={agg} onChange={(e) => setAgg(e.target.value)}>
                {AGGS.map((a) => {
                  const motivo = aggBloqueada(metric, a.id);
                  return (
                    <option key={a.id} value={a.id} disabled={motivo !== null} title={motivo ?? undefined}>
                      {motivo ? `${a.label}, não se aplica` : a.label}
                    </option>
                  );
                })}
              </select>
              {aggBloqueada(metric, agg) && (
                <p className="hint" style={{ color: "var(--warn)" }}>
                  {aggLabelDe(agg)} não vale para esta métrica: {aggBloqueada(metric, agg)}
                </p>
              )}
            </FormField>

            <FormField
              label="Agrupar por"
              help={help.fields["explore.groupBy"]}
              hint={
                groupByOptions.length === 0
                  ? "Os rótulos aparecem aqui depois da primeira consulta."
                  : undefined
              }
            >
              <select className="field" value={groupBy} onChange={(e) => setGroupBy(e.target.value)}>
                <option value="">Todas as séries</option>
                {groupByOptions.map((k) => (
                  <option key={k} value={k}>
                    {k}
                  </option>
                ))}
              </select>
            </FormField>

            <FormField label="Janela" help={help.fields["explore.window"]}>
              <select className="field" value={windowId} onChange={(e) => setWindowId(e.target.value)}>
                {WINDOWS.map((w) => (
                  <option key={w.id} value={w.id}>
                    {w.label}
                  </option>
                ))}
              </select>
            </FormField>
          </div>

          <div className="row">
            <Button variant="primary" onClick={run} disabled={!metric || state === "loading"}>
              {state === "loading" ? "Executando…" : "Executar"}
            </Button>
          </div>
        </Card>

        <Card title="Resultado">
          {!hasRun ? (
            <EmptyState
              icon={<Compass size={40} aria-hidden="true" />}
              title="Escolha uma métrica para começar"
              body="Monte a consulta nos seletores acima e clique em Executar para ver o gráfico. Nada de digitar consulta à mão."
              steps={[
                "Escolha a métrica que quer investigar.",
                "Ajuste a agregação, o agrupamento e a janela de tempo.",
                "Clique em Executar e, se gostar, salve o resultado num dashboard.",
              ]}
            />
          ) : (
            <TimeSeriesPanel
              // O sufixo de agregação no título evita a leitura errada mais comum:
              // ler o PICO (máximo) como se fosse o comportamento típico.
              title={metric ? metricLabel(metric) + aggSuffix(executed?.agg ?? agg) : "—"}
              description={metric ? `${metricMeta(metric).description || help.fields["explore.metric"]}, nome técnico: ${metric}` : help.fields["explore.metric"]}
              state={state}
              error={err}
              unit={metric ? unitForAgg(metricMeta(metric).unit, agg) : undefined}
              note={panelFootnote({ agg: executed?.agg ?? agg, table: resp?.table, step: executed?.step, aggEfetivo: resp?.agg_efetivo })}
              data={data}
            />
          )}
        </Card>
      </div>

      <HelpPanel
        open={helpOpen}
        onClose={() => setHelpOpen(false)}
        title={help.pages.explore.title}
        sections={exploreHelpSections()}
      />

      <SaveToDashboardModal open={saveOpen} onClose={() => setSaveOpen(false)} panel={savePanel} />
    </div>
  );
}

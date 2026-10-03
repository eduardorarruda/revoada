// Editor de dashboard (UX.5) — editor por FORMULÁRIO (não drag-drop, deliberado):
// metadados + lista de painéis com reordenação por botões + modal de painel com
// preview AO VIVO usando o componente real de panels/ e a Query API.
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
  Button,
  Card,
  Badge,
  Skeleton,
  FormField,
  Modal,
  ConfirmDialog,
  EmptyState,
  PageHeader,
  HelpPanel,
  AdvancedSection,
  useToast,
  IconButton,
  ActionIcons,
} from "../components";
import {
  StatPanel,
  TimeSeriesPanel,
  GaugePanel,
  BarGaugePanel,
  TablePanel,
  type TimeSeriesData,
  type PanelState,
} from "../panels";
import { buildSeriesLabels } from "../panels/seriesLabels";
import { useHostNames } from "../hooks/useHostNames";
import { useHostThresholds } from "../hooks/useHostThresholds";
import { aggBloqueada, metricMeta, type Unit } from "../metrics/dict";
import { panelStateFor } from "../metrics/emptiness";
import { displayValue, lastPoint, panelFreshness, staleAfterSeconds } from "../metrics/lastPoint";
import { mensagemDeErro } from "../format";
import { panelFootnote, unitForAgg } from "../metrics/resolution";
import { thresholdForMetric, type Threshold } from "../metrics/thresholds";
import {
  getDashboard,
  updateDashboard,
  listMetrics,
  labelValues,
  listHosts,
  queryMetric,
  type Dashboard,
  type DashboardModel,
  type QueryResponse,
} from "../api";
import { help } from "../help";
import {
  inferScope,
  filterValue,
  setFilter,
  isContainerMetric,
  DEFAULT_CONTAINER_METRIC,
  CONTAINER_LABEL_METRIC,
  type FilterRow,
  type PanelScope,
} from "./dashboardScope";

type Panel = DashboardModel["panels"][number];

// Tipos de painel oferecidos: os do registro panels/ que são dirigidos por uma
// métrica e têm preview fiel a partir da Query API.
const PANEL_TYPES: { value: string; label: string }[] = [
  { value: "timeseries", label: "Série temporal (linha)" },
  { value: "stat", label: "Número grande" },
  { value: "gauge", label: "Medidor (gauge)" },
  { value: "bargauge", label: "Barras horizontais" },
  { value: "table", label: "Tabela" },
];

const AGGS: { value: string; label: string }[] = [
  { value: "avg", label: "Média" },
  { value: "max", label: "Máximo" },
  { value: "min", label: "Mínimo" },
  { value: "sum", label: "Soma" },
  { value: "last", label: "Último" },
  { value: "rate", label: "Taxa" },
];

// Largura em colunas numa grade de 24; altura em linhas da grade.
const WIDTHS: { value: number; label: string }[] = [
  { value: 8, label: "Pequeno (1/3)" },
  { value: 12, label: "Médio (1/2)" },
  { value: 24, label: "Grande (largura toda)" },
];
const HEIGHTS: { value: number; label: string }[] = [
  { value: 6, label: "Baixo" },
  { value: 8, label: "Médio" },
  { value: 12, label: "Alto" },
];

// Janela fixa do preview (6h) — dá o suficiente para o usuário ver a forma do dado.
const PREVIEW_WINDOW = 21600;
const PREVIEW_STEP = 60;

interface PanelDraft {
  type: string;
  title: string;
  description: string;
  metric: string;
  agg: string;
  filters: FilterRow[];
  // groupBy: chaves de label para agrupar as séries (ex.: ["host"] = uma série por
  // servidor). Vazio mantém o comportamento antigo (série por combinação de labels).
  groupBy: string[];
  w: number;
  h: number;
}

function emptyDraft(): PanelDraft {
  return { type: "timeseries", title: "", description: "", metric: "", agg: "avg", filters: [], groupBy: [], w: 12, h: 8 };
}

function draftFromPanel(p: Panel): PanelDraft {
  return {
    type: p.type,
    title: p.title,
    description: p.description ?? "",
    metric: p.query.metric,
    agg: p.query.agg ?? "avg",
    filters: Object.entries(p.query.filters ?? {}).map(([label, value]) => ({ label, value })),
    groupBy: p.query.group_by ?? [],
    w: p.gridPos.w || 12,
    h: p.gridPos.h || 8,
  };
}

// Converte as linhas de filtro (ignorando as incompletas) num Record.
function filtersToRecord(rows: FilterRow[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const r of rows) if (r.label.trim() && r.value.trim()) out[r.label.trim()] = r.value.trim();
  return out;
}

// --- Adaptadores QueryResponse → props de cada painel (mesma lógica do DashboardView) ---
// Rótulos enxutos: mostra só o que distingue as séries (ver panels/seriesLabels).
function toTimeSeries(resp: QueryResponse, fallback: string, hostLabel: (h: string) => string): TimeSeriesData {
  const labels = buildSeriesLabels(resp.series, fallback, hostLabel);
  return {
    ts: resp.ts,
    series: resp.series.map((s, i) => ({
      label: labels[i],
      values: s.values,
    })),
  };
}

// Renderiza o painel REAL conforme o tipo, reaproveitando os componentes de panels/.
//
// A prévia está sob o MESMO contrato de frescor do painel publicado (era a única tela
// fora dele): o número só aparece enquanto o ponto ainda vale como atual — ver
// metrics/lastPoint.ts. Antes ela lia `lastPoint(...).value` direto, então quem
// editava um dashboard via o último valor conhecido sem nenhuma indicação de idade,
// podendo ser de horas atrás — e ajustava o painel achando que a métrica estava viva.
function PanelPreview({
  type,
  data,
  base,
}: {
  type: string;
  data: TimeSeriesData;
  base: { title: string; description?: string; state: PanelState; error?: string; unit?: Unit; note?: string; threshold?: Threshold };
}) {
  const pontos = data.series.map((s) => ({ label: s.label, p: lastPoint(data.ts, s.values) }));
  const primeiro = pontos[0]?.p ?? { value: null, ts: null };
  const valorAtual = displayValue(primeiro, PREVIEW_STEP);
  const tsMs = primeiro.ts != null ? primeiro.ts * 1000 : null;
  const stale = staleAfterSeconds(PREVIEW_STEP);
  const items = pontos.map((x) => ({ label: x.label, value: displayValue(x.p, PREVIEW_STEP) }));
  // Selo de frescor da prévia: não há WebSocket aqui (é uma consulta única), então a
  // conexão conta como boa e o que decide é a idade do próprio dado. Só aparece com
  // dado na mão — em "carregando"/"vazio" não há idade sobre a qual falar.
  const comFrescor = {
    ...base,
    freshness: base.state === "ok" ? panelFreshness({ connected: true, point: primeiro, step: PREVIEW_STEP }) : undefined,
  };
  switch (type) {
    case "stat":
      return <StatPanel {...comFrescor} value={valorAtual} valueTs={tsMs} staleAfterSeconds={stale} />;
    case "gauge":
      return <GaugePanel {...comFrescor} value={valorAtual} valueTs={tsMs} staleAfterSeconds={stale} />;
    case "bargauge":
      return <BarGaugePanel {...comFrescor} items={items} />;
    case "table":
      return (
        <TablePanel
          {...comFrescor}
          columns={[
            { key: "serie", label: "Série" },
            { key: "valor", label: "Último valor", numeric: true, unit: base.unit },
          ]}
          rows={items.map((it) => ({ serie: it.label, valor: it.value }))}
        />
      );
    default:
      return <TimeSeriesPanel {...comFrescor} data={data} />;
  }
}

// Seções do HelpPanel a partir de help.pages.dashboards (padrão das telas da Fase 3).
function dashHelpSections(): { heading: string; body: ReactNode }[] {
  const p = help.pages.dashboards;
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
  if (p.faq)
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
  return sections;
}

function go(hash: string): void {
  window.location.hash = hash;
}

function typeLabel(type: string): string {
  return PANEL_TYPES.find((t) => t.value === type)?.label ?? type;
}

// --- Editor de uma linha de filtro: rótulo (texto) + valor (select via labelValues) ---
function FilterRowEditor({
  metric,
  row,
  onChange,
  onRemove,
}: {
  metric: string;
  row: FilterRow;
  onChange: (r: FilterRow) => void;
  onRemove: () => void;
}) {
  const [values, setValues] = useState<string[]>([]);
  const label = row.label.trim();

  useEffect(() => {
    if (!metric || !label) {
      setValues([]);
      return;
    }
    let alive = true;
    labelValues(metric, label)
      .then((r) => alive && setValues(r.values))
      .catch(() => alive && setValues([]));
    return () => {
      alive = false;
    };
  }, [metric, label]);

  return (
    <div className="row" style={{ alignItems: "flex-end" }}>
      <input
        className="field"
        placeholder="rótulo (ex.: host)"
        value={row.label}
        onChange={(e) => onChange({ ...row, label: e.target.value })}
        aria-label="Rótulo do filtro"
      />
      {values.length > 0 ? (
        <select
          className="field"
          value={row.value}
          onChange={(e) => onChange({ ...row, value: e.target.value })}
          aria-label="Valor do filtro"
        >
          <option value="">Selecione…</option>
          {values.map((v) => (
            <option key={v} value={v}>
              {v}
            </option>
          ))}
        </select>
      ) : (
        <input
          className="field"
          placeholder="valor"
          value={row.value}
          onChange={(e) => onChange({ ...row, value: e.target.value })}
          aria-label="Valor do filtro"
        />
      )}
      <Button variant="ghost" onClick={onRemove} aria-label="Remover filtro">
        remover
      </Button>
    </div>
  );
}

// --- Modal de criação/edição de painel, com preview ao vivo ---
function PanelModal({
  open,
  metrics,
  initial,
  onCancel,
  onSubmit,
}: {
  open: boolean;
  metrics: string[];
  initial: PanelDraft;
  onCancel: () => void;
  onSubmit: (d: PanelDraft) => void;
}) {
  const [draft, setDraft] = useState<PanelDraft>(initial);
  const [data, setData] = useState<TimeSeriesData>({ ts: [], series: [] });
  // Resolução efetiva da prévia (campo `table` da Query API) — a nota de rodapé do
  // painel publicado sai daqui também.
  const [table, setTable] = useState<string | undefined>();
  const [state, setState] = useState<PanelState>("empty");
  const [err, setErr] = useState<string>();
  const { hostLabel } = useHostNames();
  const thresholds = useHostThresholds();

  // Escopo do painel (servidor inteiro por padrão; container opcional).
  const [scope, setScope] = useState<PanelScope>("server");
  const [hosts, setHosts] = useState<string[]>([]);
  // null = carregando; [] = nenhum container reportando (estado vazio explicativo).
  const [containers, setContainers] = useState<string[] | null>(null);

  // Reinicia o rascunho toda vez que o modal (re)abre e infere o escopo dos filtros.
  useEffect(() => {
    if (open) {
      setDraft(initial);
      setScope(inferScope(initial.filters));
    }
  }, [open, initial]);

  // Lista de servidores para o seletor "Servidor" do escopo "Servidor inteiro".
  useEffect(() => {
    if (!open) return;
    let alive = true;
    listHosts()
      .then((r) => alive && setHosts(r.hosts.map((h) => h.hostname)))
      .catch(() => alive && setHosts([]));
    return () => {
      alive = false;
    };
  }, [open]);

  // Containers existentes (só busca quando o escopo é "Container específico").
  useEffect(() => {
    if (!open || scope !== "container") return;
    let alive = true;
    setContainers(null);
    labelValues(CONTAINER_LABEL_METRIC, "container")
      .then((r) => alive && setContainers(r.values))
      .catch(() => alive && setContainers([]));
    return () => {
      alive = false;
    };
  }, [open, scope]);

  const set = <K extends keyof PanelDraft>(k: K, v: PanelDraft[K]) => setDraft((d) => ({ ...d, [k]: v }));

  // Troca de escopo: ajusta filtros (e a métrica, no caso de container) por baixo dos panos.
  const chooseScope = (next: PanelScope) => {
    setScope(next);
    setDraft((d) => {
      if (next === "server") {
        // Remove o recorte de container; mantém servidor e métrica atuais.
        return { ...d, filters: setFilter(d.filters, "container", "") };
      }
      // Container: o recorte passa a ser o container; garante uma métrica de container.
      return {
        ...d,
        filters: setFilter(d.filters, "host", ""),
        metric: isContainerMetric(d.metric) ? d.metric : DEFAULT_CONTAINER_METRIC,
      };
    });
  };

  const chooseHost = (host: string) => setDraft((d) => ({ ...d, filters: setFilter(d.filters, "host", host) }));

  const chooseContainer = (container: string) =>
    setDraft((d) => ({
      ...d,
      filters: setFilter(d.filters, "container", container),
      metric: isContainerMetric(d.metric) ? d.metric : DEFAULT_CONTAINER_METRIC,
    }));

  // Garante que a métrica atual do rascunho sempre exista como opção do select,
  // mesmo quando o escopo de container assume `container.cpu.utilization` e essa
  // métrica ainda não veio na lista de coletadas.
  const metricOptions = useMemo(
    () => (draft.metric && !metrics.includes(draft.metric) ? [draft.metric, ...metrics] : metrics),
    [metrics, draft.metric],
  );

  const filterRecord = useMemo(() => filtersToRecord(draft.filters), [draft.filters]);

  // Preview ao vivo: consulta a métrica quando os campos relevantes mudam (debounce).
  useEffect(() => {
    if (!open) return;
    if (!draft.metric) {
      setState("empty");
      setData({ ts: [], series: [] });
      return;
    }
    setState("loading");
    // `alive` no escopo do EFEITO (não do setTimeout): o cleanup abaixo o zera, então
    // uma query lenta da métrica anterior não sobrescreve o preview da nova (a resposta
    // fora de ordem era descartada só se o timeout não tivesse disparado — corrigido).
    let alive = true;
    const handle = setTimeout(() => {
      const to = new Date();
      const from = new Date(to.getTime() - PREVIEW_WINDOW * 1000);
      queryMetric({
        metric: draft.metric,
        filters: filterRecord,
        group_by: draft.groupBy.length ? draft.groupBy : undefined,
        from: from.toISOString(),
        to: to.toISOString(),
        step: PREVIEW_STEP,
        agg: draft.agg,
      })
        .then((resp) => {
          if (!alive) return;
          setData(toTimeSeries(resp, draft.title || draft.metric, hostLabel));
          setTable(resp.table);
          // Vazio pelo CONTEÚDO das séries, não por `ts.length`: a Query API devolve
          // grade densa e `ts` nunca vem vazio — a prévia de uma métrica sem dado
          // desenhava um gráfico em branco em vez de "Sem dados no período.", e o
          // autor do dashboard salvava um painel que ele nunca viu funcionar.
          setState(panelStateFor(resp.series));
        })
        .catch((e) => {
          if (!alive) return;
          // A explicação escrita pelo servidor é acionável; `String(e)` cru não é.
          setErr(mensagemDeErro(e, "Não foi possível consultar esta métrica."));
          setState("error");
        });
    }, 400);
    return () => {
      alive = false;
      clearTimeout(handle);
    };
  }, [open, draft.metric, draft.agg, draft.title, draft.groupBy, filterRecord, hostLabel]);

  const valid = draft.title.trim() !== "" && draft.metric !== "";

  return (
    <Modal
      open={open}
      onClose={onCancel}
      wide
      title={initial.title ? "Editar painel" : "Adicionar painel"}
      footer={
        <div className="row row--end">
          <Button onClick={onCancel}>Cancelar</Button>
          <Button variant="primary" disabled={!valid} onClick={() => valid && onSubmit(draft)}>
            {initial.title ? "Salvar painel" : "Adicionar painel"}
          </Button>
        </div>
      }
    >
      <div className="row" style={{ alignItems: "flex-start", flexWrap: "wrap" }}>
        {/* Coluna do formulário */}
        <div className="stack" style={{ flex: "1 1 320px", minWidth: 0 }}>
          <FormField label="Escopo do painel" help={help.fields["panel.scope"]}>
            <div className="stack" style={{ gap: "var(--sp-2)" }}>
              <label className="row" style={{ alignItems: "flex-start", gap: "var(--sp-2)", cursor: "pointer" }}>
                <input
                  type="radio"
                  name="panel-scope"
                  checked={scope === "server"}
                  onChange={() => chooseScope("server")}
                  aria-label="Servidor inteiro"
                />
                <span>
                  Servidor inteiro <span style={{ color: "var(--text-3)" }}>(padrão)</span>
                  <br />
                  <span style={{ color: "var(--text-3)", fontSize: "var(--fs-12)" }}>
                    Mede o servidor como um todo. Escolha um servidor abaixo ou deixe em "Todos".
                  </span>
                </span>
              </label>
              <label className="row" style={{ alignItems: "flex-start", gap: "var(--sp-2)", cursor: "pointer" }}>
                <input
                  type="radio"
                  name="panel-scope"
                  checked={scope === "container"}
                  onChange={() => chooseScope("container")}
                  aria-label="Container específico"
                />
                <span>
                  Container específico
                  <br />
                  <span style={{ color: "var(--text-3)", fontSize: "var(--fs-12)" }}>
                    Foca num container Docker (CPU, memória, reinícios).
                  </span>
                </span>
              </label>
            </div>
          </FormField>

          {scope === "server" && (
            <FormField
              label="Servidor"
              help="Opcional. Restringe o painel a um servidor. Deixe em 'Todos os servidores' para agregar todos os que reportam a métrica."
            >
              <select
                className="field"
                value={filterValue(draft.filters, "host")}
                onChange={(e) => chooseHost(e.target.value)}
                aria-label="Servidor"
              >
                <option value="">Todos os servidores</option>
                {hosts.map((h) => (
                  <option key={h} value={h}>
                    {hostLabel(h)}
                  </option>
                ))}
              </select>
            </FormField>
          )}

          {scope === "container" && (
            <FormField
              label="Container"
              help="Escolha o container Docker que este painel deve medir. Deixe em 'Todos os containers' para somar todos. Serviços descobertos como MySQL ou Nginx ainda não aparecem aqui: eles só terão métricas dedicadas quando houver exporters (etapa futura)."
            >
              {containers === null ? (
                <p style={{ color: "var(--text-3)", margin: 0 }}>Carregando containers…</p>
              ) : containers.length === 0 ? (
                <div
                  className="stack"
                  style={{
                    gap: "var(--sp-1)",
                    padding: "var(--sp-3)",
                    border: "1px dashed var(--border)",
                    borderRadius: "var(--radius)",
                  }}
                >
                  <strong>Nenhum container reportando</strong>
                  <span style={{ color: "var(--text-3)", fontSize: "var(--fs-12)" }}>
                    Nenhum servidor está enviando métricas de container Docker no momento. Use "Servidor
                    inteiro" ou instale/ligue o agente num host com Docker. Serviços como MySQL ou Nginx
                    dependem de exporters e chegam numa etapa futura.
                  </span>
                </div>
              ) : (
                <select
                  className="field"
                  value={filterValue(draft.filters, "container")}
                  onChange={(e) => chooseContainer(e.target.value)}
                  aria-label="Container"
                >
                  <option value="">Todos os containers</option>
                  {containers.map((c) => (
                    <option key={c} value={c}>
                      {c}
                    </option>
                  ))}
                </select>
              )}
            </FormField>
          )}

          <FormField label="Tipo de painel" help={help.fields["panel.type"]} required>
            <select className="field" value={draft.type} onChange={(e) => set("type", e.target.value)}>
              {PANEL_TYPES.map((t) => (
                <option key={t.value} value={t.value}>
                  {t.label}
                </option>
              ))}
            </select>
          </FormField>

          <FormField label="Título" help={help.fields["panel.title"]} required>
            <input className="field" value={draft.title} onChange={(e) => set("title", e.target.value)} />
          </FormField>

          <FormField label="Métrica" help={help.fields["panel.metric"]} required>
            <select className="field" value={draft.metric} onChange={(e) => set("metric", e.target.value)}>
              <option value="">Escolha uma métrica…</option>
              {metricOptions.map((m) => (
                <option key={m} value={m}>
                  {m}
                </option>
              ))}
            </select>
          </FormField>

          {/* Mesma regra do /explore: agregação sem significado para a métrica sai
              desabilitada com o motivo (ex.: somar container.restarts, um contador
              acumulado, empilha o mesmo passado a cada balde). */}
          <FormField label="Agregação" help={help.fields["panel.agg"]}>
            <select className="field" value={draft.agg} onChange={(e) => set("agg", e.target.value)}>
              {AGGS.map((a) => {
                const motivo = aggBloqueada(draft.metric, a.value);
                return (
                  <option key={a.value} value={a.value} disabled={motivo !== null} title={motivo ?? undefined}>
                    {motivo ? `${a.label}, não se aplica` : a.label}
                  </option>
                );
              })}
            </select>
            {aggBloqueada(draft.metric, draft.agg) && (
              <p className="hint" style={{ color: "var(--warn)" }}>
                {aggBloqueada(draft.metric, draft.agg)}
              </p>
            )}
          </FormField>

          <FormField
            label="Agrupar por"
            help="Colapsa as séries por dimensão. Marque 'host' para ver UMA série por servidor num só painel, ideal para comparar CPU/memória de vários servidores. Sem marcar, cada combinação de rótulos (ex.: disco por ponto de montagem, CPU por core) vira uma série."
          >
            <label className="row" style={{ alignItems: "center", gap: "var(--sp-2)", cursor: "pointer" }}>
              <input
                type="checkbox"
                checked={draft.groupBy.includes("host")}
                onChange={(e) =>
                  set(
                    "groupBy",
                    e.target.checked
                      ? [...draft.groupBy.filter((k) => k !== "host"), "host"]
                      : draft.groupBy.filter((k) => k !== "host"),
                  )
                }
                aria-label="Agrupar por host"
              />
              <span>Uma série por servidor (agrupar por host)</span>
            </label>
          </FormField>

          <div className="row" style={{ flexWrap: "nowrap" }}>
            <FormField label="Largura" help={help.fields["panel.size"]}>
              <select className="field" value={draft.w} onChange={(e) => set("w", Number(e.target.value))}>
                {WIDTHS.map((w) => (
                  <option key={w.value} value={w.value}>
                    {w.label}
                  </option>
                ))}
              </select>
            </FormField>
            <FormField label="Altura" help={help.fields["panel.size"]}>
              <select className="field" value={draft.h} onChange={(e) => set("h", Number(e.target.value))}>
                {HEIGHTS.map((h) => (
                  <option key={h.value} value={h.value}>
                    {h.label}
                  </option>
                ))}
              </select>
            </FormField>
          </div>

          <AdvancedSection label="Filtros e descrição (opcional)">
            <div className="stack">
              <FormField label="Descrição" help="Explique o que este painel mostra e qual é o valor normal. Aparece no ícone de ajuda do painel.">
                <input
                  className="field"
                  value={draft.description}
                  onChange={(e) => set("description", e.target.value)}
                  placeholder="ex.: Uso de CPU por host. Normal < 70%."
                />
              </FormField>

              <FormField label="Filtros" help={help.fields["panel.filters"]}>
                <div className="stack">
                  {draft.filters.length === 0 && (
                    <p style={{ color: "var(--text-3)", margin: 0 }}>
                      Sem filtros: mostra todas as séries desta métrica.
                    </p>
                  )}
                  {draft.filters.map((row, i) => (
                    <FilterRowEditor
                      key={i}
                      metric={draft.metric}
                      row={row}
                      onChange={(r) =>
                        set(
                          "filters",
                          draft.filters.map((x, j) => (j === i ? r : x)),
                        )
                      }
                      onRemove={() =>
                        set(
                          "filters",
                          draft.filters.filter((_, j) => j !== i),
                        )
                      }
                    />
                  ))}
                  <div>
                    <Button onClick={() => set("filters", [...draft.filters, { label: "", value: "" }])}>
                      + Adicionar filtro
                    </Button>
                  </div>
                </div>
              </FormField>
            </div>
          </AdvancedSection>
        </div>

        {/* Coluna do preview ao vivo */}
        <div className="stack" style={{ flex: "1 1 320px", minWidth: 0, width: "100%" }}>
          <span style={{ color: "var(--text-3)", fontSize: "var(--fs-12)" }}>
            Prévia ao vivo (últimas 6h), dados reais da métrica escolhida.
          </span>
          <div style={{ width: "100%" }}>
            <PanelPreview
              type={draft.type}
              data={data}
              base={{
                title: draft.title || "Sem título",
                description: draft.description || undefined,
                state: draft.metric ? state : "empty",
                error: err,
                // A prévia usa a MESMA unidade, limiar e nota do painel publicado —
                // o que o autor vê aqui é o que o operador verá depois.
                unit: draft.metric ? unitForAgg(metricMeta(draft.metric).unit, draft.agg) : undefined,
                threshold: thresholdForMetric(draft.metric, filterRecord.host, thresholds) ?? undefined,
                note: panelFootnote({ agg: draft.agg, table, step: PREVIEW_STEP }),
              }}
            />
          </div>
        </div>
      </div>
    </Modal>
  );
}

export function DashboardEdit({ uid }: { uid: string }) {
  const toast = useToast();
  const [loading, setLoading] = useState(true);
  const [notFound, setNotFound] = useState(false);
  const [title, setTitle] = useState("");
  const [folder, setFolder] = useState("");
  const [panels, setPanels] = useState<Panel[]>([]);
  const [metrics, setMetrics] = useState<string[]>([]);
  const [helpOpen, setHelpOpen] = useState(false);
  const [saving, setSaving] = useState(false);

  // Estado do modal de painel: índice editado (null = criando) e rascunho inicial.
  const [modalOpen, setModalOpen] = useState(false);
  const [editIndex, setEditIndex] = useState<number | null>(null);
  const [modalInitial, setModalInitial] = useState<PanelDraft>(emptyDraft());

  // Confirmação de remoção.
  const [removeIndex, setRemoveIndex] = useState<number | null>(null);

  const modelRef = useRef<DashboardModel | null>(null);

  useEffect(() => {
    let alive = true;
    setLoading(true);
    setNotFound(false);
    getDashboard(uid)
      .then((d: Dashboard) => {
        if (!alive) return;
        modelRef.current = d.model;
        setTitle(d.title);
        setFolder(d.folder);
        setPanels(d.model.panels);
        setLoading(false);
      })
      .catch((e) => {
        if (!alive) return;
        if (String(e).includes("404")) setNotFound(true);
        else toast.error("Não foi possível carregar o dashboard.");
        setLoading(false);
      });
    listMetrics()
      .then((r) => alive && setMetrics(r.metrics))
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, [uid, toast]);

  const move = useCallback((i: number, dir: -1 | 1) => {
    setPanels((ps) => {
      const j = i + dir;
      if (j < 0 || j >= ps.length) return ps;
      const copy = [...ps];
      [copy[i], copy[j]] = [copy[j], copy[i]];
      return copy;
    });
  }, []);

  const openCreate = () => {
    setEditIndex(null);
    setModalInitial(emptyDraft());
    setModalOpen(true);
  };
  const openEdit = (i: number) => {
    setEditIndex(i);
    setModalInitial(draftFromPanel(panels[i]));
    setModalOpen(true);
  };

  // Converte o rascunho em Panel (id/gridPos são recalculados no save).
  const draftToPanel = (d: PanelDraft, existing?: Panel): Panel => ({
    id: existing?.id ?? 0,
    type: d.type,
    title: d.title.trim(),
    description: d.description.trim() || undefined,
    gridPos: { x: 0, y: existing?.gridPos.y ?? 0, w: d.w, h: d.h },
    query: {
      metric: d.metric,
      filters: Object.keys(filtersToRecord(d.filters)).length ? filtersToRecord(d.filters) : undefined,
      group_by: d.groupBy.length ? d.groupBy : undefined,
      agg: d.agg,
    },
  });

  const submitPanel = (d: PanelDraft) => {
    setPanels((ps) => {
      if (editIndex == null) return [...ps, draftToPanel(d)];
      return ps.map((p, i) => (i === editIndex ? draftToPanel(d, p) : p));
    });
    setModalOpen(false);
  };

  const confirmRemove = () => {
    if (removeIndex == null) return;
    setPanels((ps) => ps.filter((_, i) => i !== removeIndex));
    setRemoveIndex(null);
  };

  const save = async () => {
    if (!modelRef.current) return;
    if (!title.trim()) {
      toast.error("Dê um título ao dashboard antes de salvar.");
      return;
    }
    // Empacota: ids incrementais e y crescente (mantém a ordem do array; a largura
    // real vem do w com auto-flow na grade de 24 colunas do DashboardView).
    const packed: Panel[] = panels.map((p, i) => ({
      ...p,
      id: i + 1,
      gridPos: { ...p.gridPos, x: 0, y: i },
    }));
    const model: DashboardModel = { ...modelRef.current, title: title.trim(), panels: packed };
    setSaving(true);
    try {
      const res = await updateDashboard(uid, { title: title.trim(), folder: folder.trim() || undefined, model });
      toast.success(`Dashboard salvo (v${res.version})`);
      go(`/d/${uid}`);
    } catch {
      toast.error("Não foi possível salvar o dashboard.");
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return (
      <div className="page">
        <PageHeader title="Editar dashboard" subtitle="Carregando…" />
        <div className="stack">
          <Skeleton height={48} />
          <Skeleton height={120} />
          <Skeleton height={120} />
        </div>
      </div>
    );
  }

  if (notFound) {
    return (
      <div className="page">
        <PageHeader title="Editar dashboard" />
        <EmptyState
          title="Dashboard não encontrado"
          body="Ele pode ter sido apagado ou o endereço está errado."
          action={{ label: "Voltar para os dashboards", onClick: () => go("/dashboards") }}
        />
      </div>
    );
  }

  return (
    <div className="page">
      <PageHeader
        title={`Editar: ${title}`}
        subtitle="Monte os painéis deste dashboard"
        onHelp={() => setHelpOpen(true)}
        actions={
          <>
            <Button onClick={() => go(`/d/${uid}/versions`)}>Ver histórico</Button>
            <Button variant="primary" disabled={saving} onClick={save}>
              {saving ? "Salvando…" : "Salvar"}
            </Button>
          </>
        }
      />

      {/* Metadados */}
      <Card title="Dados do dashboard">
        <div className="stack">
          <FormField label="Título" help={help.fields["dashboard.title"]} required>
            <input className="field" value={title} onChange={(e) => setTitle(e.target.value)} />
          </FormField>
          <FormField label="Pasta" help={help.fields["dashboard.folder"]}>
            <input
              className="field"
              value={folder}
              onChange={(e) => setFolder(e.target.value)}
              placeholder="ex.: Infra"
            />
          </FormField>
        </div>
      </Card>

      {/* Lista de painéis */}
      <div className="row" style={{ justifyContent: "space-between", marginTop: "var(--sp-5)" }}>
        <h2 style={{ margin: 0, fontSize: "var(--fs-20)" }}>Painéis ({panels.length})</h2>
        <Button variant="primary" onClick={openCreate}>
          + Adicionar painel
        </Button>
      </div>

      {panels.length === 0 ? (
        <EmptyState
          icon="▦"
          title="Nenhum painel ainda"
          body="Um dashboard é feito de painéis: cada um mostra uma métrica num formato (linha, número, tabela…)."
          steps={[
            "Clique em \"Adicionar painel\".",
            "Escolha o tipo, a métrica e a agregação.",
            "Veja a prévia com dados reais e confirme.",
          ]}
          action={{ label: "Adicionar painel", onClick: openCreate }}
        />
      ) : (
        <div className="stack" style={{ marginTop: "var(--sp-3)" }}>
          {panels.map((p, i) => (
            <Card key={i}>
              <div className="row" style={{ justifyContent: "space-between", alignItems: "center" }}>
                <div className="stack" style={{ gap: "var(--sp-1)", minWidth: 0 }}>
                  <div className="row" style={{ alignItems: "center", gap: "var(--sp-2)" }}>
                    <Badge state="info">{typeLabel(p.type)}</Badge>
                    <strong>{p.title || "(sem título)"}</strong>
                  </div>
                  <span style={{ color: "var(--text-3)", fontSize: "var(--fs-12)" }}>
                    métrica: {p.query.metric || "—"}
                    {p.query.agg ? ` · ${p.query.agg}` : ""}
                  </span>
                </div>
                <div className="row" style={{ flexWrap: "nowrap" }}>
                  <Button variant="ghost" aria-label="Subir painel" disabled={i === 0} onClick={() => move(i, -1)}>
                    ↑
                  </Button>
                  <Button
                    variant="ghost"
                    aria-label="Descer painel"
                    disabled={i === panels.length - 1}
                    onClick={() => move(i, 1)}
                  >
                    ↓
                  </Button>
                  <IconButton icon={ActionIcons.edit} label="Editar painel" onClick={() => openEdit(i)} />
                  <IconButton
                    icon={ActionIcons.delete}
                    label="Remover painel"
                    onClick={() => setRemoveIndex(i)}
                  />
                </div>
              </div>
            </Card>
          ))}
        </div>
      )}

      <PanelModal
        open={modalOpen}
        metrics={metrics}
        initial={modalInitial}
        onCancel={() => setModalOpen(false)}
        onSubmit={submitPanel}
      />

      <ConfirmDialog
        open={removeIndex != null}
        verb="Remover"
        target={removeIndex != null ? panels[removeIndex]?.title || "painel" : "painel"}
        consequences="O painel sai deste dashboard. A remoção só é gravada quando você salvar."
        danger
        onCancel={() => setRemoveIndex(null)}
        onConfirm={confirmRemove}
      />

      <HelpPanel
        open={helpOpen}
        onClose={() => setHelpOpen(false)}
        title={help.pages.dashboards.title}
        sections={dashHelpSections()}
      />
    </div>
  );
}

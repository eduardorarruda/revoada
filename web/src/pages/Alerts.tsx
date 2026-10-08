// Alertas (UX.9): abas Ativos·Histórico·Regras, wizard de 3 passos para
// criar/editar regra (com preview retroativo) e interruptor liga/desliga na lista de
// regras.
import { useCallback, useEffect, useState, type ReactNode } from "react";
import { HEARTBEAT_METRIC } from "./tv/valorAlerta";
import { Bell, Layers } from "lucide-react";
import { usePolling } from "../hooks/usePolling";
import {
  AdvancedSection,
  Badge,
  Button,
  Card,
  Checkbox,
  ConfirmDialog,
  DataTable,
  EmptyState,
  FormField,
  HelpPanel,
  Modal,
  PageHeader,
  Radio,
  Skeleton,
  Tabs,
  useToast,
  IconButton,
  ActionIcons,
  InfoTip,
  type State,
} from "../components";
import {
  ackAlert,
  createAlertRule,
  deleteAlertRule,
  getRole,
  ignoreContainer,
  labelValues,
  listAlertRules,
  listAlerts,
  listChannels,
  listHosts,
  listIgnoredContainers,
  listMetrics,
  previewAlertRule,
  resolveAlert,
  unignoreContainer,
  updateAlertRule,
  type AlertEvent,
  type IgnoredContainer,
  type AlertRule,
  type HostDetail,
  type NotificationChannel,
  type NewAlertRule,
} from "../api";
import { useHostNames } from "../hooks/useHostNames";
import { ContainersIgnorados, podeIgnorar } from "./alerts/ContainersIgnorados";
import { ModelosIA } from "./alerts/ModelosIA";
import { help } from "../help";
import { fmtRelAbs, formatMetric, formatValue, sevRank } from "../format";
import { metricLabel } from "../metrics/dict";
import { aggLabel } from "../metrics/resolution";

type ToastApi = ReturnType<typeof useToast>;
type TabId = "ativos" | "historico" | "regras";

// A regra sai da tela para a API já com `enabled` — o PUT persiste esse campo e o
// POST o ignora (default do banco). Mandamos sempre o payload completo (incl. filtros
// e escalonamento) para não perder nada do que o backend guarda.
type RulePayload = NewAlertRule & { enabled: boolean };

const SEVERITY_STATE: Record<string, State> = { info: "info", warning: "warn", critical: "crit" };
const SEVERITY_LABEL: Record<string, string> = { info: "aviso", warning: "alta", critical: "crítica" };
const OPERATORS = [">", ">=", "<", "<="] as const;
const AGGS = ["avg", "max", "min", "sum"] as const;

// Estilos utilitários (texto secundário e monoespaçado) via variáveis do tema.
const MUTED = { color: "var(--text-3)" } as const;
const MONO = { fontFamily: "var(--font-mono)", fontSize: "var(--fs-12)" } as const;
// Trunca com reticências dentro da largura da célula (nunca estoura a coluna).
const ELLIPSIS = {
  display: "block",
  maxWidth: 260,
  overflow: "hidden",
  textOverflow: "ellipsis",
  whiteSpace: "nowrap",
} as const;

// Detecta a regra "container caído" (metric container.running com comparador de queda).
function isContainerDownRule(r: { metric: string; condition_op: string }): boolean {
  return r.metric === "container.running" && r.condition_op.startsWith("<");
}

// HEARTBEAT_METRIC (em tv/valorAlerta.ts, compartilhado com a TV) é o nome reservado
// da regra de fábrica "servidor parou de reportar": o valor comparado é a IDADE, em
// segundos, do último sinal do agente.

// isHeartbeatRule identifica a regra de ausência. Ela é de fábrica e não pode ser
// desligada nem apagada: sem ela, um agente morto cala TODAS as outras regras (um
// valor ausente nunca é "> 90"), e a ausência só apareceria como pintura de tela.
function isHeartbeatRule(r: { metric: string }): boolean {
  return r.metric === HEARTBEAT_METRIC;
}

// MIN_AVALIACOES_CONSECUTIVAS espelha o piso do avaliador (minConsecutiveEvals em
// server/internal/alerting/evaluator.go): o número de avaliações consecutivas
// violando exigidas para o alerta ABRIR (e para fechar).
const MIN_AVALIACOES_CONSECUTIVAS = 2;

/**
 * pisoSegundos: o tempo mínimo REAL até um alerta abrir ou fechar.
 *
 * O erro que isto corrige: a tela multiplicava o piso pelo passo do LOOP de avaliação
 * (30 s) e prometia "confirma em 2 avaliações (~1 min)". Mas o avaliador não conta
 * tique de relógio — ele conta BALDE NOVO (`lastBucket` em evaluator.go: um balde já
 * avaliado é descartado, "não é uma segunda avaliação"). E balde novo só aparece uma
 * vez por `window_seconds`. Na regra padrão, de janela de 5 min, o mínimo real é
 * ~10 min, não ~1 min. O operador esperava o aviso em ~6 min, ele chegava em ~10-11,
 * e na dúvida concluía que o alerta não funciona — o pior desfecho possível para um
 * sistema de alerta.
 */
// Janela da regra de fábrica "container caído" criada pelo assistente. Nomeada para o
// texto do modal e o payload não se descolarem (era 300 escrito em dois lugares).
const JANELA_CONTAINER_S = 300;

function pisoSegundos(windowSeconds: number): number {
  const janela = windowSeconds > 0 ? windowSeconds : 300;
  return MIN_AVALIACOES_CONSECUTIVAS * janela;
}

/**
 * forEfetivo: o tempo que o alerta REALMENTE leva para abrir. O `for` configurado não
 * consegue ser menor que o piso — pedir 60 s numa regra de janela de 5 min continua
 * levando ~10 min, porque as duas avaliações precisam de dois baldes.
 */
function forEfetivo(forSeconds: number, windowSeconds: number): number {
  return Math.max(forSeconds, pisoSegundos(windowSeconds));
}

// splitCsv separa uma lista "a, b ,c" (ex.: o filtro `container` multivalorado) em
// itens limpos. Vazio/undefined → []. Espelha o splitContainers do backend.
function splitCsv(v: string | undefined): string[] {
  if (!v) return [];
  return v
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
}

// containerFilters traduz o escopo escolhido no modal em filtros da regra.
//
// A distinção que importa: "todos" grava filtro VAZIO, e é isso que faz a regra
// render uma série por container — inclusive os criados depois. Enumerar a lista
// inteira dá o mesmo resultado HOJE e congela o conjunto amanhã; foi esse engano
// que obrigava a reabrir a regra a cada container novo.
export function containerFilters(scope: "todos" | "escolhidos", selected: string[]): Record<string, string> {
  if (scope === "todos") return {};
  const limpos = selected.map((c) => c.trim()).filter(Boolean);
  return limpos.length > 0 ? { container: limpos.join(",") } : {};
}

// Constrói as seções do HelpPanel a partir de help.pages.alerts (padrão das telas).
function alertsHelpSections(): { heading: string; body: ReactNode }[] {
  const p = help.pages.alerts;
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

// "por 5m" — traduz for_seconds para leitura humana.
function humanFor(seconds: number): string {
  if (seconds <= 0) return "imediato";
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
  return `${Math.round(seconds / 3600)}h`;
}

// Duração legível em PT-BR para a JANELA de agregação ("5 min", "1 h").
function humanWindow(seconds: number): string {
  if (!seconds || seconds <= 0) return "";
  if (seconds < 60) return `${seconds} s`;
  if (seconds < 3600) return `${Math.round(seconds / 60)} min`;
  return `${Math.round(seconds / 3600)} h`;
}

/**
 * Resumo da condição em linguagem clara, COM a janela de agregação.
 *
 * Antes saía "avg(system.cpu.utilization) > 90 por imediato": escondia justamente o
 * `window_seconds`. Uma regra com janela de 5 min e `for_seconds = 0` NÃO dispara no
 * primeiro segundo acima de 90 — ela dispara quando a MÉDIA dos últimos 5 minutos
 * passa de 90, o que é bem diferente. Agora a frase diz as duas coisas:
 *
 *   "média de 5 min de CPU (%) acima de 90% — confirma em 2 avaliações (~10 min)"
 *
 * E "imediato" saiu do vocabulário: nenhuma regra dispara mais na primeira avaliação.
 * O avaliador aplica um piso de 2 avaliações consecutivas para abrir E para fechar,
 * mesmo com `for` = 0 (server/internal/alerting/evaluator.go, minConsecutiveEvals).
 * Dizer "imediato" na tela era prometer um comportamento que o motor não tem.
 *
 * O tempo mostrado sai de `forEfetivo`: mesmo um `for` configurado abaixo do piso não
 * acelera nada, porque as duas avaliações exigem dois baldes da janela (ver pisoSegundos).
 */
function conditionSummary(r: AlertRule): string {
  if (isHeartbeatRule(r)) return heartbeatSummary(r);
  const janela = humanWindow(r.window_seconds);
  const agregacao = janela ? `${aggLabel(r.agg)} de ${janela}` : aggLabel(r.agg);
  const limite = formatMetric(r.metric, r.threshold);
  const piso = pisoSegundos(r.window_seconds);
  const disparo =
    r.for_seconds > piso
      ? `por ${humanFor(r.for_seconds)}`
      : `confirma em ${MIN_AVALIACOES_CONSECUTIVAS} avaliações (~${humanWindow(piso)})`;
  return `${agregacao} de ${metricLabel(r.metric)} ${opPhrase(r.condition_op)} ${limite}, ${disparo}`;
}

// heartbeatSummary descreve a regra de ausência em português. O "limite" dela não é um
// valor de métrica: é o tempo de silêncio tolerado antes de o painel avisar.
function heartbeatSummary(r: AlertRule): string {
  return `sem nenhum sinal do agente por mais de ${humanWindow(r.threshold)}, o servidor parou de reportar`;
}

// scopeSummary descreve a quais servidores a regra se aplica, usando o nome amigável
// quando existe. Sem hosts = global ("Todos").
function scopeSummary(r: AlertRule, hostLabel: (h: string) => string): string {
  const hs = r.hosts ?? [];
  if (hs.length === 0) return "Todos";
  if (hs.length <= 2) return hs.map(hostLabel).join(", ");
  return `${hostLabel(hs[0])} +${hs.length - 1}`;
}

// Comparador técnico (>, <=, …) traduzido para linguagem clara, usado na descrição
// do limite ("limite: acima de 80%").
function opPhrase(op: string): string {
  switch (op) {
    case ">":
      return "acima de";
    case ">=":
      return "a partir de";
    case "<":
      return "abaixo de";
    case "<=":
      return "no máximo";
    default:
      return op;
  }
}

// Coluna "Onde": informação estruturada e legível (servidor, container, métrica) com
// nome amigável, mais os labels brutos recolhidos em "Detalhes técnicos". Nada estoura
// a largura — cada linha trunca com reticências e mostra o valor completo no hover.
function AlertWhere({
  a,
  rule,
  hostLabel,
}: {
  a: AlertEvent;
  rule: AlertRule | null;
  hostLabel: (h: string) => string;
}) {
  const labels = a.labels ?? {};
  const entries = Object.entries(labels);
  const host = labels.host;
  const container = labels.container;
  const metricName = rule?.metric ?? labels.metric ?? "";
  const anyStructured = host || container || metricName;

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 2, minWidth: 0 }}>
      {host && (
        <span style={ELLIPSIS} title={hostLabel(host)}>
          <span style={MUTED}>Servidor: </span>
          {hostLabel(host)}
        </span>
      )}
      {container && (
        <span style={ELLIPSIS} title={container}>
          <span style={MUTED}>Container: </span>
          {container}
        </span>
      )}
      {metricName && (
        <span style={ELLIPSIS} title={metricName}>
          <span style={MUTED}>Métrica: </span>
          {metricLabel(metricName)}
        </span>
      )}
      {!anyStructured && entries.length === 0 && <span style={MUTED}>—</span>}
      {entries.length > 0 && (
        <details style={{ marginTop: 2 }}>
          <summary style={{ ...MUTED, cursor: "pointer", fontSize: "var(--fs-12)" }}>
            Detalhes técnicos
          </summary>
          <div style={{ ...MONO, ...MUTED, marginTop: 2, wordBreak: "break-word" }}>
            {entries.map(([k, v]) => (
              <div key={k}>
                {k}={v}
              </div>
            ))}
          </div>
        </details>
      )}
    </div>
  );
}

// Coluna "Valor": valor observado com unidade + condição em linguagem clara. Para a
// regra de "container caído" descreve o estado esperado (e o código de saída, quando há).
function valueSummary(a: AlertEvent, rule: AlertRule | null): string {
  // Regra de ausência: o "valor" é a idade do último sinal em segundos. Lido como
  // número puro ("420") não diz nada; como tempo ("7 min sem sinal") diz tudo.
  if (rule && isHeartbeatRule(rule)) {
    return `${humanWindow(Math.round(a.value))} sem sinal, tolerado: ${humanWindow(rule.threshold)}`;
  }
  if (rule && isContainerDownRule(rule)) {
    const code = a.labels?.exit_code;
    const suffix = code && code !== "0" ? ` (saiu com código ${code})` : "";
    return `Container parado, esperado: em execução${suffix}`;
  }
  // Regra apagada: sem ela não há métrica e, portanto, nem unidade nem limite. Ainda
  // assim o número passa pelo formatador (vírgula decimal, 2 casas) em vez de sair
  // cru como "92.54718281828" — e a tela diz por que falta o resto.
  if (!rule) return `${formatValue(a.value, "none")}, regra apagada, sem limite para comparar`;
  const value = formatMetric(rule.metric, a.value);
  const limit = formatMetric(rule.metric, rule.threshold);
  return `${value}, limite: ${opPhrase(rule.condition_op)} ${limit}`;
}

// Interruptor acessível (role=switch) — sem CSS novo, só variáveis do tema.
function Switch({ checked, onChange, label }: { checked: boolean; onChange: () => void; label: string }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      onClick={onChange}
      style={{
        width: 40,
        height: 22,
        padding: 0,
        borderRadius: 999,
        border: "1px solid var(--border)",
        background: checked ? "var(--accent)" : "var(--bg-3)",
        position: "relative",
        cursor: "pointer",
        flexShrink: 0,
        transition: "background .15s",
      }}
    >
      <span
        aria-hidden="true"
        style={{
          position: "absolute",
          top: 2,
          left: checked ? 20 : 2,
          width: 16,
          height: 16,
          borderRadius: "50%",
          background: "var(--bg-0)",
          transition: "left .15s",
        }}
      />
    </button>
  );
}

// canaisExistentes descarta IDs de canais que não existem mais.
//
// Apagar um canal deixava o ID dele dentro das regras para sempre. A regra dizia
// "1 canal(is)" com nenhuma caixa marcada — não havia como desmarcar um canal que
// sumiu — e, pior, uma regra que só apontasse para fantasmas parecia configurada e
// não avisava ninguém. O servidor limpa na exclusão e no boot; aqui a tela para de
// contar fantasma na hora, e o próximo "Salvar" grava a lista já limpa.
export function canaisExistentes(ids: number[] | null | undefined, canais: { id: number }[]): number[] {
  if (!ids || ids.length === 0) return [];
  // Sem a lista de canais carregada ainda, não dá para saber quem é fantasma:
  // devolver o que veio é melhor do que piscar "todos" e assustar.
  if (canais.length === 0) return ids;
  return ids.filter((id) => canais.some((c) => c.id === id));
}

export function Alerts() {
  const toast = useToast();
  const canEdit = getRole() === "admin";

  const [tab, setTab] = useState<TabId>("ativos");
  const [helpOpen, setHelpOpen] = useState(false);

  const [active, setActive] = useState<AlertEvent[] | null>(null);
  const [toResolve, setToResolve] = useState<AlertEvent | null>(null);
  const [toIgnore, setToIgnore] = useState<AlertEvent | null>(null);
  const [ignored, setIgnored] = useState<IgnoredContainer[] | null>(null);
  const [history, setHistory] = useState<AlertEvent[] | null>(null);
  const [host, setHost] = useState("");
  const [hosts, setHosts] = useState<HostDetail[]>([]);
  const { hostLabel } = useHostNames();
  const [rules, setRules] = useState<AlertRule[] | null>(null);
  // Os canais entram aqui só para a coluna "Canais" contar o que EXISTE. Uma regra
  // podia guardar o ID de um canal já apagado: a lista dizia "1 canal(is)" e o
  // formulário não tinha caixa nenhuma marcada, porque o canal não existe mais.
  const [channels, setChannels] = useState<NotificationChannel[]>([]);

  // Modais / diálogos.
  const [modelosIA, setModelosIA] = useState(false);
  const [wizard, setWizard] = useState<
    { mode: "new"; preset?: Partial<WizardState> } | { mode: "edit"; rule: AlertRule } | null
  >(null);
  const [ruleDelete, setRuleDelete] = useState<AlertRule | null>(null);
  const [containerDown, setContainerDown] = useState<
    { mode: "new" } | { mode: "edit"; rule: AlertRule } | null
  >(null);

  const reload = useCallback(() => {
    listAlerts(true).then((r) => setActive(r.alerts)).catch(() => setActive([]));
    listAlerts(false).then((r) => setHistory(r.alerts)).catch(() => setHistory([]));
    listAlertRules().then((r) => setRules(r.rules)).catch(() => setRules([]));
    // Rota só de admin: para quem não é, nem pergunta.
    if (canEdit) listIgnoredContainers().then((r) => setIgnored(r.containers)).catch(() => setIgnored([]));
  }, [canEdit]);

  usePolling(reload, 15000, [reload]);

  useEffect(() => {
    listHosts().then((r) => setHosts(r.hosts)).catch(() => {});
    listChannels().then((r) => setChannels(r.channels)).catch(() => setChannels([]));
  }, []);

  // Filtro por servidor (cliente): alertas trazem labels['host'].
  const byHost = (a: AlertEvent) => !host || a.labels?.host === host;
  // UX-11 (aba Ativos): pior primeiro — severidade desc, desempate por início mais recente.
  const activeView =
    active === null
      ? null
      : active.filter(byHost).slice().sort((a, b) => {
          const sev = sevRank(b.severity) - sevRank(a.severity);
          if (sev !== 0) return sev;
          return new Date(b.started_at).getTime() - new Date(a.started_at).getTime();
        });
  const historyView = history === null ? null : history.filter(byHost);

  // --- Ações ---
  async function ack(a: AlertEvent) {
    try {
      const r = await ackAlert(a.id);
      toast.success(`Alerta "${a.rule_name}" reconhecido por ${r.acked_by}.`);
      reload();
    } catch {
      toast.error("Não foi possível reconhecer o alerta.");
    }
  }

  // Encerrar à mão serve para o alerta que não vai se resolver sozinho porque o
  // mundo mudou (container removido de propósito, servidor desativado). Não
  // silencia a regra: se a condição voltar a valer, um alerta NOVO dispara.
  async function confirmResolve() {
    const a = toResolve;
    if (!a) return;
    setToResolve(null);
    try {
      const r = await resolveAlert(a.id);
      toast.success(`Alerta "${a.rule_name}" marcado como resolvido por ${r.resolved_by}.`);
      reload();
    } catch {
      toast.error("Não foi possível marcar o alerta como resolvido.");
    }
  }

  // Container parado de propósito: o painel para de alertar sobre ele (em todas as
  // regras) e o alerta aberto fecha na hora. Nada muda no servidor.
  async function confirmIgnore() {
    const a = toIgnore;
    if (!a) return;
    setToIgnore(null);
    const ctr = a.labels.container;
    try {
      await ignoreContainer(a.labels.host, ctr);
      toast.success(`O container ${ctr} não gera mais alerta. Para voltar a vigiar: aba Regras › Containers ignorados.`);
      reload();
    } catch {
      toast.error(`Não foi possível ignorar o container ${ctr}.`);
    }
  }

  async function voltarAVigiar(c: IgnoredContainer) {
    try {
      await unignoreContainer(c.host, c.container);
      toast.success(`O container ${c.container} voltou a ser vigiado. Se seguir parado, o alerta abre em cerca de 1 minuto.`);
      reload();
    } catch {
      toast.error(`Não foi possível voltar a vigiar o container ${c.container}.`);
    }
  }

  // Da linha de um alerta (ativo/histórico) chega-se à regra que o originou, para
  // editá-la ou apagá-la sem precisar ir até a aba Regras.
  const ruleFor = (a: AlertEvent): AlertRule | null => rules?.find((r) => r.id === a.rule_id) ?? null;
  // Edição unificada: regra de container caído abre o ContainerDownModal (simples);
  // as demais seguem no RuleWizard (técnico).
  function openEditRule(r: AlertRule) {
    if (isContainerDownRule(r)) setContainerDown({ mode: "edit", rule: r });
    else setWizard({ mode: "edit", rule: r });
  }
  function editRuleOf(a: AlertEvent) {
    const r = ruleFor(a);
    if (r) openEditRule(r);
    else toast.error("Regra não encontrada, pode já ter sido apagada.");
  }
  function deleteRuleOf(a: AlertEvent) {
    const r = ruleFor(a);
    if (!r) {
      toast.error("Regra não encontrada, pode já ter sido apagada.");
      return;
    }
    if (isHeartbeatRule(r)) {
      toast.error("A regra \"servidor parou de reportar\" é de fábrica e não pode ser apagada, sem ela, um servidor mudo não avisa ninguém.");
      return;
    }
    setRuleDelete(r);
  }

  async function toggleRule(r: AlertRule) {
    const next = !r.enabled;
    // Otimista: reflete na hora e reverte se a API falhar.
    setRules((rs) => rs?.map((x) => (x.id === r.id ? { ...x, enabled: next } : x)) ?? null);
    const payload: RulePayload = {
      name: r.name,
      metric: r.metric,
      filters: r.filters ?? {},
      hosts: r.hosts ?? [], // preserva o escopo de servidores — sem isto, ligar/desligar torna a regra global
      agg: r.agg,
      condition_op: r.condition_op,
      threshold: r.threshold,
      window_seconds: r.window_seconds,
      for_seconds: r.for_seconds,
      severity: r.severity,
      runbook: r.runbook,
      escalation_policy_id: r.escalation_policy_id,
      channel_ids: r.channel_ids ?? [], // preserva a seleção de canais ao ligar/desligar
      enabled: next,
    };
    try {
      await updateAlertRule(r.id, payload);
      toast.success(`Regra "${r.name}" ${next ? "ativada" : "desativada"}.`);
    } catch {
      setRules((rs) => rs?.map((x) => (x.id === r.id ? { ...x, enabled: r.enabled } : x)) ?? null);
      toast.error("Não foi possível mudar o estado da regra.");
    }
  }

  async function confirmDeleteRule() {
    const r = ruleDelete;
    if (!r) return;
    setRuleDelete(null);
    try {
      await deleteAlertRule(r.id);
      toast.success(`Regra "${r.name}" apagada.`);
      reload();
    } catch {
      toast.error("Não foi possível apagar a regra.");
    }
  }

  const activeCount = activeView?.length ?? 0;

  return (
    <div className="page">
      <PageHeader
        title="Alertas"
        subtitle="Regras que vigiam suas métricas e te avisam"
        onHelp={() => setHelpOpen(true)}
        actions={
          <Badge state={activeCount ? "crit" : "ok"}>
            {activeCount} ativo{activeCount === 1 ? "" : "s"}
          </Badge>
        }
      />

      <Tabs
        active={tab}
        onChange={(id) => setTab(id as TabId)}
        tabs={[
          { id: "ativos", label: "Ativos" },
          { id: "historico", label: "Histórico" },
          { id: "regras", label: "Regras" },
        ]}
      />

      {(tab === "ativos" || tab === "historico") && hosts.length > 0 && (
        <div className="row" style={{ margin: "var(--sp-2) 0", alignItems: "center", gap: "var(--sp-2)" }}>
          <span style={MUTED}>Servidor:</span>
          <select className="field" value={host} onChange={(e) => setHost(e.target.value)} aria-label="Filtrar por servidor" style={{ maxWidth: 260 }}>
            <option value="">todos os servidores</option>
            {hosts.map((h) => (
              <option key={h.hostname} value={h.hostname}>{hostLabel(h.hostname)}</option>
            ))}
          </select>
        </div>
      )}

      {tab === "ativos" && (
        <Card>
          <TableOrSkeleton data={activeView}>
            {(rows) => (
              <DataTable<AlertEvent>
                rows={rows}
                keyFn={(a) => a.id}
                columns={[
                  {
                    key: "severity",
                    label: "Severidade",
                    render: (a) => (
                      <Badge state={SEVERITY_STATE[a.severity] ?? "neutral"}>
                        {SEVERITY_LABEL[a.severity] ?? a.severity}
                      </Badge>
                    ),
                  },
                  { key: "rule_name", label: "Regra" },
                  {
                    key: "labels",
                    label: "Onde",
                    hideOnMobile: true,
                    render: (a) => <AlertWhere a={a} rule={ruleFor(a)} hostLabel={hostLabel} />,
                  },
                  { key: "value", label: "Valor", render: (a) => <span>{valueSummary(a, ruleFor(a))}</span> },
                  {
                    key: "since",
                    label: "Desde",
                    render: (a) => (
                      <span style={MUTED} title={fmtRelAbs(a.started_at).abs}>
                        {fmtRelAbs(a.started_at).rel}
                        {a.flapping && (
                          <>
                            {" "}
                            <Badge state="warn">oscilante</Badge>
                          </>
                        )}
                      </span>
                    ),
                  },
                ]}
                rowActions={
                  canEdit
                    ? (a) => (
                        // Quatro ações em ícone, na mesma linha. Em texto, "Reconhecer"
                        // e "Marcar como resolvido" ocupavam duas linhas por alerta e
                        // faziam a coluna Ações ficar mais larga que a de Valor. O
                        // rótulo não sumiu: cada IconButton leva `label`, que vira
                        // aria-label (leitor de tela) e title (dica no hover).
                        <div className="row" style={{ gap: "var(--sp-1)", flexWrap: "nowrap" }}>
                          {a.acked_by ? (
                            <span
                              className="alerta-ack"
                              title={`Reconhecido por ${a.acked_by}`}
                              aria-label={`Reconhecido por ${a.acked_by}`}
                            >
                              <ActionIcons.ack size={16} aria-hidden={true} />
                            </span>
                          ) : (
                            <IconButton
                              icon={ActionIcons.ack}
                              label="Reconhecer, avisa aos outros que alguém já está olhando"
                              onClick={() => ack(a)}
                            />
                          )}
                          <IconButton
                            icon={ActionIcons.resolve}
                            label="Marcar como resolvido, encerra o alerta"
                            onClick={() => setToResolve(a)}
                          />
                          {podeIgnorar(a.labels) && (
                            <IconButton
                              icon={ActionIcons.ignore}
                              label={`Ignorar o container ${a.labels.container}, para de alertar sobre ele`}
                              onClick={() => setToIgnore(a)}
                            />
                          )}
                          <IconButton icon={ActionIcons.edit} label="Editar regra" onClick={() => editRuleOf(a)} />
                          <IconButton
                            icon={ActionIcons.delete}
                            label="Apagar regra"
                            onClick={() => deleteRuleOf(a)}
                          />
                        </div>
                      )
                    : undefined
                }
                empty={
                  <EmptyState
                    icon="✓"
                    title="Nenhum alerta ativo ✓"
                    body="Tudo tranquilo por aqui. Quando uma regra disparar, o alerta aparece nesta lista com opção de reconhecer."
                  />
                }
              />
            )}
          </TableOrSkeleton>
        </Card>
      )}

      {tab === "historico" && (
        <Card>
          <TableOrSkeleton data={historyView}>
            {(rows) => (
              <DataTable<AlertEvent>
                rows={rows}
                keyFn={(a) => a.id}
                columns={[
                  {
                    key: "severity",
                    label: "Severidade",
                    render: (a) => (
                      <Badge state={SEVERITY_STATE[a.severity] ?? "neutral"}>
                        {SEVERITY_LABEL[a.severity] ?? a.severity}
                      </Badge>
                    ),
                  },
                  {
                    key: "state",
                    label: "Estado",
                    render: (a) => (
                      <span className="row" style={{ gap: "var(--sp-1)", alignItems: "center", flexWrap: "wrap" }}>
                        <Badge state={a.state === "firing" ? "crit" : "ok"}>
                          {a.state === "firing" ? "disparado" : "resolvido"}
                        </Badge>
                        {/* Encerrado à mão ≠ problema que passou: quem lê o histórico
                            precisa distinguir os dois, e saber de quem foi a decisão. */}
                        {a.resolved_by && (
                          <span style={{ ...MUTED, fontSize: "var(--fs-12)" }}>à mão, por {a.resolved_by}</span>
                        )}
                      </span>
                    ),
                  },
                  { key: "rule_name", label: "Regra" },
                  {
                    key: "labels",
                    label: "Onde",
                    hideOnMobile: true,
                    render: (a) => <AlertWhere a={a} rule={ruleFor(a)} hostLabel={hostLabel} />,
                  },
                  { key: "value", label: "Valor", render: (a) => <span>{valueSummary(a, ruleFor(a))}</span> },
                  {
                    key: "started_at",
                    label: "Início",
                    render: (a) => (
                      <span style={MUTED} title={fmtRelAbs(a.started_at).abs}>
                        {fmtRelAbs(a.started_at).rel}
                      </span>
                    ),
                  },
                ]}
                // Histórico é registro do que aconteceu: SEM ações.
                //
                // Havia aqui "Editar regra" e "Apagar regra" — dois botões que não
                // agiam sobre a linha em que estavam. A linha é um disparo passado; os
                // botões mexiam na REGRA que o originou, que continua viva e valendo
                // para o futuro. Apagar a regra a partir de um evento de uma semana
                // atrás destrói a vigilância de hoje sem que a tela diga isso em
                // lugar nenhum. Editar e apagar regra é na aba Regras, onde o objeto
                // que está sendo alterado é o que está escrito na linha.
                empty={
                  <EmptyState
                    icon={<Layers size={32} strokeWidth={1.5} />}
                    title="Histórico vazio"
                    body="Ainda não houve disparos registrados. Assim que uma regra disparar e depois resolver, o evento fica guardado aqui."
                  />
                }
              />
            )}
          </TableOrSkeleton>
        </Card>
      )}

      {tab === "regras" && (
        <Card>
          {canEdit && (
            <div className="row row--end" style={{ marginBottom: "var(--sp-3)", gap: "var(--sp-2)" }}>
              <Button
                onClick={() => setContainerDown({ mode: "new" })}
                title="Avisa quando um container do Docker cair (e quando voltar). Você escolhe um ou mais containers e o servidor."
              >
                + Container caído
              </Button>
              <Button
                onClick={() => setModelosIA(true)}
                title="Modelos prontos para agentes de IA: gasto por hora, erros, latência, loop e modelo sem preço."
              >
                + Agentes de IA
              </Button>
              <Button variant="primary" onClick={() => setWizard({ mode: "new" })}>
                Nova regra
              </Button>
            </div>
          )}
          <ModelosIA
            aberto={modelosIA}
            onFechar={() => setModelosIA(false)}
            onEscolher={(preset) => {
              setModelosIA(false);
              setWizard({ mode: "new", preset });
            }}
          />
          <TableOrSkeleton data={rules}>
            {(rows) => (
              <DataTable<AlertRule>
                rows={rows}
                keyFn={(r) => r.id}
                columns={[
                  {
                    key: "name",
                    label: "Nome",
                    render: (r) => (
                      <span style={{ opacity: r.enabled ? 1 : 0.5, display: "inline-flex", gap: "var(--sp-2)", alignItems: "center" }}>
                        {r.name}
                        {isHeartbeatRule(r) && (
                          <span title="Regra de fábrica: avisa quando um servidor para de reportar. Não pode ser desligada nem apagada, sem ela, um agente morto silencia todas as outras regras.">
                            <Badge state="info">de fábrica</Badge>
                          </span>
                        )}
                        {!r.enabled && <Badge state="neutral">desativada</Badge>}
                      </span>
                    ),
                  },
                  {
                    key: "condition",
                    label: "Condição",
                    render: (r) => (
                      // Não é mais expressão técnica (era MONO): virou frase em PT-BR.
                      <span style={{ opacity: r.enabled ? 1 : 0.5 }} title={`${r.agg}(${r.metric}) ${r.condition_op} ${r.threshold}`}>
                        {conditionSummary(r)}
                      </span>
                    ),
                  },
                  {
                    key: "scope",
                    label: "Servidores",
                    hideOnMobile: true,
                    render: (r) => (
                      <span style={{ ...MUTED, opacity: r.enabled ? 1 : 0.5 }}>{scopeSummary(r, hostLabel)}</span>
                    ),
                  },
                  {
                    key: "severity",
                    label: "Severidade",
                    render: (r) => (
                      <span style={{ opacity: r.enabled ? 1 : 0.5 }}>
                        <Badge state={SEVERITY_STATE[r.severity] ?? "neutral"}>
                          {SEVERITY_LABEL[r.severity] ?? r.severity}
                        </Badge>
                      </span>
                    ),
                  },
                  {
                    key: "channels",
                    label: "Canais",
                    hideOnMobile: true,
                    render: (r) => {
                      const escolhidos = canaisExistentes(r.channel_ids, channels);
                      return (
                        <span style={MUTED}>
                          {escolhidos.length > 0 ? `${escolhidos.length} canal(is)` : "todos"}
                        </span>
                      );
                    },
                  },
                  {
                    key: "enabled",
                    label: "Ativa",
                    render: (r) =>
                      // A regra de ausência não tem interruptor: ela é o piso do
                      // monitoramento. Oferecer o botão e depois o servidor recusar
                      // (409) seria pior do que não oferecer.
                      canEdit && !isHeartbeatRule(r) ? (
                        <Switch checked={r.enabled} onChange={() => toggleRule(r)} label={`Ativar regra ${r.name}`} />
                      ) : (
                        <Badge state={r.enabled ? "ok" : "neutral"}>{r.enabled ? "sim" : "não"}</Badge>
                      ),
                  },
                ]}
                rowActions={
                  canEdit
                    ? (r) => (
                        <div className="row" style={{ gap: "var(--sp-2)" }}>
                          <IconButton
                            icon={ActionIcons.edit}
                            label="Editar"
                            onClick={() => openEditRule(r)}
                          />
                          {/* Apagar a regra de fábrica só adiaria: o próximo boot a
                              recria, e o intervalo fica sem cobertura. Editar (limite,
                              canais, severidade) continua liberado. */}
                          {!isHeartbeatRule(r) && (
                            <IconButton icon={ActionIcons.delete} label="Apagar" onClick={() => setRuleDelete(r)} />
                          )}
                        </div>
                      )
                    : undefined
                }
                empty={
                  <EmptyState
                    icon={<Bell size={32} strokeWidth={1.5} />}
                    title="Nenhuma regra ainda"
                    body="Uma regra vigia uma métrica e dispara um alerta quando ela passa de um limite pelo tempo que você definir."
                    steps={[
                      "Clique em Nova regra",
                      "Passo 1: escolha o que vigiar (métrica e filtros)",
                      "Passo 2: defina o limite e teste com o preview",
                      "Passo 3: escolha severidade e roteamento",
                    ]}
                    action={canEdit ? { label: "Nova regra", onClick: () => setWizard({ mode: "new" }) } : undefined}
                  />
                }
              />
            )}
          </TableOrSkeleton>
        </Card>
      )}

      {tab === "regras" && canEdit && (
        <ContainersIgnorados lista={ignored} hostLabel={hostLabel} onVoltar={voltarAVigiar} />
      )}

      {/* Wizard criar/editar regra */}
      {wizard && (
        <RuleWizard
          editing={wizard.mode === "edit" ? wizard.rule : null}
          preset={wizard.mode === "new" ? wizard.preset : undefined}
          toast={toast}
          onClose={() => setWizard(null)}
          onSaved={() => {
            setWizard(null);
            reload();
          }}
        />
      )}

      {/* Atalho "Container caído": escolha simples de container/servidor/canais.
          Serve tanto para criar (mode "new") quanto para editar uma regra existente. */}
      {containerDown && (
        <ContainerDownModal
          editing={containerDown.mode === "edit" ? containerDown.rule : null}
          toast={toast}
          onClose={() => setContainerDown(null)}
          onSaved={() => {
            setContainerDown(null);
            reload();
          }}
        />
      )}

      <ConfirmDialog
        open={ruleDelete !== null}
        verb="Apagar"
        target={`a regra "${ruleDelete?.name ?? ""}"`}
        consequences="A regra para de vigiar e é removida em definitivo. Alertas já registrados permanecem no histórico."
        danger
        onCancel={() => setRuleDelete(null)}
        onConfirm={confirmDeleteRule}
      />

      <ConfirmDialog
        open={toResolve !== null}
        verb="Marcar como resolvido"
        target={`o alerta "${toResolve?.rule_name ?? ""}"`}
        consequences="O alerta sai da lista de ativos e vai para o histórico com o seu nome. A regra continua vigiando: se a condição voltar a valer, um alerta novo dispara."
        onCancel={() => setToResolve(null)}
        onConfirm={confirmResolve}
      />

      <ConfirmDialog
        open={toIgnore !== null}
        verb="Ignorar"
        target={`o container ${toIgnore?.labels.container ?? ""} em ${toIgnore ? hostLabel(toIgnore.labels.host) : ""}`}
        consequences="O painel para de alertar sobre este container em todas as regras, o alerta atual é encerrado e ele sai da TV. Nada muda no servidor: o container continua como está. Para voltar a vigiar, use a aba Regras › Containers ignorados."
        onCancel={() => setToIgnore(null)}
        onConfirm={confirmIgnore}
      />

      <HelpPanel open={helpOpen} onClose={() => setHelpOpen(false)} title={help.pages.alerts.title} sections={alertsHelpSections()} />
    </div>
  );
}

// Enquanto data===null mostra skeleton; senão entrega as linhas ao filho.
function TableOrSkeleton<T>({ data, children }: { data: T[] | null; children: (rows: T[]) => ReactNode }) {
  if (data === null) {
    return (
      <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
        {[0, 1, 2, 3].map((i) => (
          <Skeleton key={i} height={40} />
        ))}
      </div>
    );
  }
  return <>{children(data)}</>;
}


// --- Wizard de regra (3 passos) ---

type Filter = { label: string; value: string };
type WizardState = {
  name: string;
  metric: string;
  filters: Filter[];
  hosts: string[];
  condition_op: string;
  threshold: number;
  for_seconds: number;
  severity: string;
  enabled: boolean;
  agg: string;
  window_seconds: number;
  runbook: string;
  escalation_policy_id: number | null;
  channel_ids: number[];
};

function fromRule(r: AlertRule): WizardState {
  return {
    name: r.name,
    metric: r.metric,
    filters: Object.entries(r.filters ?? {}).map(([label, value]) => ({ label, value })),
    hosts: r.hosts ?? [],
    condition_op: r.condition_op,
    threshold: r.threshold,
    for_seconds: r.for_seconds,
    severity: r.severity,
    enabled: r.enabled,
    agg: r.agg,
    window_seconds: r.window_seconds,
    runbook: r.runbook,
    escalation_policy_id: r.escalation_policy_id,
    channel_ids: r.channel_ids ?? [],
  };
}

const EMPTY_WIZARD: WizardState = {
  name: "",
  metric: "",
  filters: [],
  hosts: [],
  condition_op: ">",
  threshold: 0,
  // 0 = "use o piso do avaliador" (2 avaliações consecutivas, ~1 min), não "dispare
  // no primeiro instante". O default de fábrica era 0 com a segunda leitura, e por
  // isso uma amostra solta abria alerta; o piso corrigiu o significado do 0 sem
  // exigir migração das regras que já existem.
  for_seconds: 0,
  severity: "warning",
  enabled: true,
  agg: "avg",
  window_seconds: 300,
  runbook: "",
  escalation_policy_id: null,
  channel_ids: [],
};

// ContainerDownModal: atalho SIMPLES para "avisar quando um container cair". O usuário
// escolhe só o quê vigiar — todos os containers ou uma lista específica, e um servidor
// (ou todos) — e os canais. "Todos" grava filtro VAZIO: o backend agrupa por conjunto de
// labels e rende uma série por container, inclusive os que aparecerem depois. Uma lista
// específica vira um filtro `container` separado por vírgula, que o backend expande em
// uma consulta por container. A regra por trás usa container.running
// com mínimo < 1 na janela: cai → dispara, volta → resolve (o motor de alertas notifica
// as duas transições). Severidade crítica. Não expõe métrica/agregação/limiar; cria uma
// regra de alerta normal.
function ContainerDownModal({
  editing,
  toast,
  onClose,
  onSaved,
}: {
  editing: AlertRule | null;
  toast: ToastApi;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [hosts, setHosts] = useState<HostDetail[]>([]);
  const [containers, setContainers] = useState<string[]>([]);
  const [channels, setChannels] = useState<NotificationChannel[]>([]);
  // Em edição, pré-preenche a partir da regra existente (host, containers, canais). O
  // filtro `container` guarda 1+ containers separados por vírgula — parseia para a lista.
  const [host, setHost] = useState(editing?.hosts?.[0] ?? "");
  const [selectedContainers, setSelectedContainers] = useState<string[]>(() => splitCsv(editing?.filters?.container));
  // "todos" = filtro vazio, que acompanha os containers novos sozinho. Enumerar a lista
  // inteira PARECE a mesma coisa mas congela o conjunto do dia — era o que obrigava a
  // reabrir a regra a cada container criado.
  const [scope, setScope] = useState<"todos" | "escolhidos">(
    splitCsv(editing?.filters?.container).length > 0 ? "escolhidos" : "todos",
  );
  const [channelIds, setChannelIds] = useState<number[]>(editing?.channel_ids ?? []);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    listHosts().then((r) => setHosts(r.hosts ?? [])).catch(() => setHosts([]));
    labelValues("container.running", "container").then((r) => setContainers(r.values ?? [])).catch(() => setContainers([]));
    listChannels().then((r) => setChannels(r.channels)).catch(() => setChannels([]));
  }, []);

  const toggleChannel = (id: number) =>
    setChannelIds((prev) => (prev.includes(id) ? prev.filter((c) => c !== id) : [...prev, id]));
  const toggleContainer = (c: string) =>
    setSelectedContainers((prev) => (prev.includes(c) ? prev.filter((x) => x !== c) : [...prev, c]));
  // Lista mostrada: containers reportados + os já selecionados que não reportam mais
  // (não some da tela um container que a regra em edição vigia mas que está parado).
  const allContainers = Array.from(new Set([...selectedContainers, ...containers].filter(Boolean))).sort();

  async function save() {
    setBusy(true);
    const filters = containerFilters(scope, selectedContainers);
    const escolhidos = splitCsv(filters.container);
    // Nome legível: base "Container caído", com o container quando é um só, "N containers"
    // quando vários, e "@ host" quando restrito a um servidor. Todos + global → só a base.
    let name = "Container caído";
    if (escolhidos.length === 1) name += `: ${escolhidos[0]}`;
    else if (escolhidos.length > 1) name += `: ${escolhidos.length} containers`;
    if (host) name += ` @ ${host}`;
    const payload: RulePayload = {
      name,
      metric: "container.running",
      filters,
      hosts: host ? [host] : [],
      agg: "min",
      condition_op: "<",
      threshold: 1,
      window_seconds: JANELA_CONTAINER_S,
      // 0 aqui NÃO é "avisa no primeiro instante": o avaliador exige 2 avaliações
      // consecutivas com o container fora antes de abrir, e outras 2 com ele no ar
      // antes de encerrar — e cada avaliação precisa de um BALDE NOVO da janela, ou
      // seja ~2×window_seconds (~10 min com a janela padrão), não ~1 min. É o que
      // impede um container em crash-loop de render uma mensagem a cada reinício.
      for_seconds: 0,
      severity: "critical",
      runbook: "",
      escalation_policy_id: null,
      channel_ids: channelIds,
      enabled: editing?.enabled ?? true,
    };
    try {
      if (editing) {
        await updateAlertRule(editing.id, payload);
        toast.success(`Aviso de "container caído" salvo${host ? ` para ${host}` : ""}.`);
      } else {
        await createAlertRule(payload);
        toast.success(`Aviso de "container caído" criado${host ? ` para ${host}` : ""}.`);
      }
      onSaved();
    } catch {
      toast.error(editing ? "Não foi possível salvar o aviso." : "Não foi possível criar o aviso.");
      setBusy(false);
    }
  }

  return (
    <Modal
      open
      onClose={onClose}
      title={editing ? "Editar aviso de container caído" : "Avisar quando um container cair"}
      footer={
        <>
          <Button onClick={onClose}>Cancelar</Button>
          <Button variant="primary" onClick={save} disabled={busy}>
            {busy ? "Salvando…" : editing ? "Salvar alterações" : "Criar aviso"}
          </Button>
        </>
      }
    >
      <p style={{ ...MUTED, marginTop: 0 }}>
        Cria um alerta que avisa <strong>quando o container cair</strong> (parar de rodar) e de novo <strong>quando
        voltar</strong>. Você só escolhe o que vigiar, sem configurar métricas nem limiares.
      </p>
      {/* Dizer o atraso é melhor que a surpresa: sem esta linha, quem derruba um
          container para testar acha que o aviso não funcionou. A regra criada aqui usa
          janela de JANELA_CONTAINER_S, e cada avaliação exige um balde novo dessa
          janela — por isso a espera é de ~10 min, não de ~1 min como a tela dizia. */}
      <p style={{ ...MUTED, marginTop: 0 }}>
        O aviso sai depois de <strong>{MIN_AVALIACOES_CONSECUTIVAS} verificações seguidas</strong> com o container
        fora (cerca de {humanWindow(pisoSegundos(JANELA_CONTAINER_S))}), e a recuperação depois de{" "}
        {MIN_AVALIACOES_CONSECUTIVAS} com ele no ar. É essa espera que evita uma mensagem a cada reinício de um
        container em ciclo de queda.
      </p>

      <FormField label="O que vigiar">
        <div className="stack" style={{ gap: "var(--sp-1)" }}>
          <Radio
            name="escopo-container"
            checked={scope === "todos"}
            onChange={() => setScope("todos")}
            alignTop
            label={
              <>
                <strong>Todos os containers</strong>
                <div style={{ ...MUTED, fontSize: "var(--fs-12)" }}>
                  Vale também para containers criados depois, a regra acompanha sozinha, sem
                  precisar reabrir este cadastro.
                </div>
              </>
            }
          />
          <Radio
            name="escopo-container"
            checked={scope === "escolhidos"}
            onChange={() => setScope("escolhidos")}
            alignTop
            label={
              <>
                <strong>Só os containers que eu escolher</strong>
                <div style={{ ...MUTED, fontSize: "var(--fs-12)" }}>
                  A lista fica fixa: um container novo só entra se você voltar aqui e marcá-lo.
                </div>
              </>
            }
          />
        </div>
      </FormField>

      {scope === "escolhidos" && (
        <FormField label="Containers" hint={`${selectedContainers.length} de ${allContainers.length} marcado(s).`}>
          {allContainers.length === 0 ? (
            <span style={MUTED}>Nenhum container detectado ainda.</span>
          ) : (
            <div
              style={{
                display: "flex",
                flexDirection: "column",
                gap: "var(--sp-1)",
                maxHeight: 240,
                overflowY: "auto",
                padding: "var(--sp-2)",
                border: "1px solid var(--border)",
                borderRadius: "var(--radius)",
                background: "var(--bg-2)",
              }}
            >
              {allContainers.map((c) => (
                <Checkbox
                  key={c}
                  checked={selectedContainers.includes(c)}
                  onChange={() => toggleContainer(c)}
                  label={
                    <span className="row" style={{ gap: "var(--sp-2)", alignItems: "center" }}>
                      <span style={MONO}>{c}</span>
                      {!containers.includes(c) && <Badge state="neutral">não reporta agora</Badge>}
                    </span>
                  }
                />
              ))}
            </div>
          )}
        </FormField>
      )}

      <FormField label="Servidor" hint="Restrinja a um servidor ou vigie todos.">
        <select className="field" value={host} onChange={(e) => setHost(e.target.value)}>
          <option value="">Todos os servidores</option>
          {host && !hosts.some((h) => h.hostname === host) && <option value={host}>{host}</option>}
          {hosts.map((h) => (
            <option key={h.hostname} value={h.hostname}>
              {h.hostname}
            </option>
          ))}
        </select>
      </FormField>

      <FormField label="Canais de notificação" hint="Para onde avisar. Nenhum marcado = todos os canais ativos.">
        {channels.length === 0 ? (
          <span style={MUTED}>
            Nenhum canal cadastrado. Crie um em <a href="#/notify">Notificações</a> (o aviso vai a todos).
          </span>
        ) : (
          <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-1)" }}>
            {channels.map((c) => (
              <label key={c.id} className="row" style={{ gap: "var(--sp-2)", alignItems: "center" }}>
                <input type="checkbox" checked={channelIds.includes(c.id)} onChange={() => toggleChannel(c.id)} />
                <span>{c.name}</span>
                <Badge state="info">{c.type}</Badge>
                {!c.enabled && <span style={MUTED}>(desativado)</span>}
              </label>
            ))}
          </div>
        )}
      </FormField>
    </Modal>
  );
}

function toPayload(s: WizardState, canais: { id: number }[]): RulePayload {
  const filters: Record<string, string> = {};
  for (const f of s.filters) {
    const k = f.label.trim();
    if (k) filters[k] = f.value.trim();
  }
  return {
    name: s.name.trim(),
    metric: s.metric.trim(),
    filters,
    hosts: s.hosts.filter((h) => h.trim() !== ""),
    agg: s.agg,
    condition_op: s.condition_op,
    threshold: s.threshold,
    window_seconds: s.window_seconds,
    for_seconds: s.for_seconds,
    severity: s.severity,
    runbook: s.runbook.trim(),
    escalation_policy_id: s.escalation_policy_id,
    // Salvar limpa os fantasmas: só vai o que a pessoa consegue ver e desmarcar.
    channel_ids: canaisExistentes(s.channel_ids, canais),
    enabled: s.enabled,
  };
}

function RuleWizard({
  editing,
  preset,
  toast,
  onClose,
  onSaved,
}: {
  editing: AlertRule | null;
  preset?: Partial<WizardState>;
  toast: ToastApi;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [step, setStep] = useState(0);
  const [s, setS] = useState<WizardState>(
    editing ? fromRule(editing) : { ...EMPTY_WIZARD, ...preset },
  );
  const [metrics, setMetrics] = useState<string[]>([]);
  const [hosts, setHosts] = useState<HostDetail[]>([]);
  const [channels, setChannels] = useState<NotificationChannel[]>([]);
  const [preview, setPreview] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    listMetrics().then((r) => setMetrics(r.metrics)).catch(() => setMetrics([]));
    listHosts().then((r) => setHosts(r.hosts ?? [])).catch(() => setHosts([]));
    listChannels().then((r) => setChannels(r.channels)).catch(() => setChannels([]));
  }, []);

  const set = (patch: Partial<WizardState>) => setS((prev) => ({ ...prev, ...patch }));
  const toggleChannel = (id: number) =>
    set({ channel_ids: s.channel_ids.includes(id) ? s.channel_ids.filter((c) => c !== id) : [...s.channel_ids, id] });
  const canProceed1 = s.name.trim() !== "" && s.metric.trim() !== "";

  async function doPreview() {
    setPreview("calculando…");
    try {
      const r = await previewAlertRule(toPayload(s, channels), 7);
      setPreview(`Teria disparado ${r.fires} ${r.fires === 1 ? "vez" : "vezes"} nos últimos ${r.days} dias.`);
    } catch {
      setPreview(null);
      toast.error("Não foi possível calcular o preview.");
    }
  }

  async function submit() {
    setBusy(true);
    try {
      if (editing) {
        await updateAlertRule(editing.id, toPayload(s, channels));
        toast.success(`Regra "${s.name.trim()}" salva.`);
      } else {
        await createAlertRule(toPayload(s, channels));
        toast.success(`Regra "${s.name.trim()}" criada.`);
      }
      onSaved();
    } catch {
      setBusy(false);
      toast.error(editing ? "Não foi possível salvar a regra." : "Não foi possível criar a regra.");
    }
  }

  const steps = ["O quê", "Quando", "Quem"];
  const metricOptions = Array.from(new Set([s.metric, ...metrics].filter(Boolean)));

  return (
    <Modal
      open
      onClose={onClose}
      wide
      title={editing ? `Editar regra: ${editing.name}` : "Nova regra"}
      footer={
        <div className="row" style={{ justifyContent: "space-between", width: "100%" }}>
          <Button onClick={onClose}>Cancelar</Button>
          <div className="row" style={{ gap: "var(--sp-2)" }}>
            {step > 0 && (
              <Button onClick={() => setStep((n) => n - 1)}>Anterior</Button>
            )}
            {step < 2 ? (
              <Button variant="primary" disabled={step === 0 && !canProceed1} onClick={() => setStep((n) => n + 1)}>
                Próximo
              </Button>
            ) : (
              <Button variant="primary" disabled={busy || !canProceed1} onClick={submit}>
                {busy ? "Salvando…" : editing ? "Salvar alterações" : "Criar regra"}
              </Button>
            )}
          </div>
        </div>
      }
    >
      {/* Regra de fábrica: o que dá para mexer e o que não dá, dito antes de a pessoa
          tentar. O servidor recusa desligá-la (409) — avisar aqui evita a surpresa. */}
      {editing && isHeartbeatRule(editing) && (
        <div
          style={{
            marginBottom: "var(--sp-4)",
            padding: "var(--sp-3)",
            borderRadius: "var(--radius)",
            background: "var(--bg-2)",
            border: "1px solid var(--border)",
          }}
        >
          <strong>Regra de fábrica.</strong>{" "}
          <span style={MUTED}>
            Avisa quando um servidor para de reportar, é o único alerta que funciona com o agente
            morto, porque todos os outros comparam um valor que deixou de existir. Você pode ajustar o
            tempo tolerado (o campo <em>Limite</em>, em segundos), a severidade e os canais; desligar ou
            apagar, não.
          </span>
        </div>
      )}

      {/* Stepper */}
      <ol
        className="row"
        style={{ gap: "var(--sp-2)", marginBottom: "var(--sp-4)", listStyle: "none", padding: 0, flexWrap: "wrap" }}
      >
        {steps.map((label, i) => (
          <li
            key={label}
            style={{
              display: "flex",
              alignItems: "center",
              gap: "var(--sp-2)",
              color: i === step ? "var(--text-1)" : "var(--text-3)",
              fontWeight: i === step ? 600 : 400,
            }}
          >
            <span
              style={{
                width: 22,
                height: 22,
                borderRadius: "50%",
                display: "inline-flex",
                alignItems: "center",
                justifyContent: "center",
                fontSize: "var(--fs-12)",
                background: i === step ? "var(--accent)" : "var(--bg-3)",
                color: i === step ? "var(--bg-0)" : "var(--text-2)",
              }}
            >
              {i + 1}
            </span>
            {label}
            {i < steps.length - 1 && <span style={{ color: "var(--text-3)" }}>›</span>}
          </li>
        ))}
      </ol>

      {step === 0 && (
        <>
          <FormField label="Nome da regra" required hint="Um nome que o time reconheça, ex.: “CPU alta em produção”.">
            <input className="field" value={s.name} onChange={(e) => set({ name: e.target.value })} placeholder="CPU alta em produção" autoFocus />
          </FormField>
          <FormField label="Métrica" help={help.fields["alert.metric"]} required>
            <input
              className="field"
              list="wizard-metrics"
              value={s.metric}
              onChange={(e) => set({ metric: e.target.value })}
              placeholder="system.cpu.utilization"
            />
            {/* Sugestões com rótulo amigável (o valor preenchido é o nome técnico). */}
            <datalist id="wizard-metrics">
              {metricOptions.map((m) => (
                <option key={m} value={m} label={metricLabel(m)} />
              ))}
            </datalist>
            {s.metric.trim() !== "" && (
              // Mostra o rótulo amigável da métrica escolhida; nome técnico fica no tooltip.
              <div className="row" style={{ ...MUTED, gap: "var(--sp-1)", alignItems: "center", marginTop: "var(--sp-1)" }}>
                <span title={s.metric}>{metricLabel(s.metric)}</span>
                <InfoTip title="Nome técnico" text={<span style={MONO}>{s.metric}</span>} />
              </div>
            )}
          </FormField>
          <FormField label="Servidores" help={help.fields["alert.scope"]}>
            <ServerScope hosts={hosts} selected={s.hosts} onChange={(hosts) => set({ hosts })} />
          </FormField>
          <FormField label="Filtros" help={help.fields["alert.filters"]} hint="Opcional. Restringe ainda mais por qualquer rótulo (ex.: ambiente, disco). Combina com os servidores acima.">
            <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
              {s.filters.map((f, i) => (
                <FilterRow
                  key={i}
                  metric={s.metric}
                  filter={f}
                  onChange={(nf) => set({ filters: s.filters.map((x, j) => (j === i ? nf : x)) })}
                  onRemove={() => set({ filters: s.filters.filter((_, j) => j !== i) })}
                />
              ))}
              <div>
                <Button variant="ghost" onClick={() => set({ filters: [...s.filters, { label: "", value: "" }] })}>
                  + Adicionar filtro
                </Button>
              </div>
            </div>
          </FormField>
        </>
      )}

      {step === 1 && (
        <>
          <div className="grid-2">
            <FormField label="Operador" help={help.fields["alert.operator"]}>
              <select className="field" value={s.condition_op} onChange={(e) => set({ condition_op: e.target.value })}>
                {OPERATORS.map((op) => (
                  <option key={op} value={op}>
                    {op}
                  </option>
                ))}
              </select>
            </FormField>
            <FormField label="Limite" help={help.fields["alert.threshold"]}>
              <input className="field" type="number" value={s.threshold} onChange={(e) => set({ threshold: Number(e.target.value) })} />
            </FormField>
          </div>
          <FormField
            label="Por quanto tempo (segundos)"
            help={help.fields["alert.for"]}
            hint={`0 usa o piso de segurança: ${MIN_AVALIACOES_CONSECUTIVAS} avaliações consecutivas violando (~${humanWindow(pisoSegundos(s.window_seconds))} com a janela de ${humanWindow(s.window_seconds || JANELA_CONTAINER_S)}), nenhuma regra dispara na primeira.`}
          >
            <input className="field" type="number" min={0} value={s.for_seconds} onChange={(e) => set({ for_seconds: Number(e.target.value) })} />
          </FormField>

          {/* O NÚMERO EFETIVO, não o pedido. Cada avaliação consecutiva exige um BALDE
              NOVO da janela, e balde novo só aparece uma vez por window_seconds: pedir
              60 s numa regra de janela de 5 min continua levando ~10 min. Dizer isso
              aqui evita a conclusão de que "o alerta não funciona" quando ele apenas
              chegou no prazo que o motor sempre teve. */}
          <p style={{ ...MUTED, marginTop: "var(--sp-2)" }}>
            Na prática este alerta abre em <strong>~{humanWindow(forEfetivo(s.for_seconds, s.window_seconds))}</strong>
            {s.for_seconds > 0 && s.for_seconds < pisoSegundos(s.window_seconds) && (
              <>, o valor pedido ({humanWindow(s.for_seconds)}) não acelera, porque cada avaliação precisa de um
                intervalo novo de {humanWindow(s.window_seconds)}</>
            )}
            .
          </p>

          {/* O piso vale para os dois sentidos e é o que mata o vaivém. Um valor que
              passeia em volta do limite (91/89) abriria e fecharia o alerta sem parar;
              explicar isso aqui evita que alguém "conserte" o ruído inflando o limiar. */}
          <p style={{ ...MUTED, marginTop: "var(--sp-2)" }}>
            Para <strong>encerrar</strong> o alerta, o painel também exige {MIN_AVALIACOES_CONSECUTIVAS} avaliações
            consecutivas abaixo do limite, mesma espera. Assim um valor que fica indo e voltando em torno do limite
            não vira uma enxurrada de avisos de disparo e recuperação.
          </p>

          <div
            style={{
              marginTop: "var(--sp-3)",
              padding: "var(--sp-3)",
              borderRadius: "var(--radius)",
              background: "var(--bg-2)",
              border: "1px solid var(--border)",
            }}
          >
            <div className="row" style={{ gap: "var(--sp-3)", alignItems: "center", flexWrap: "wrap" }}>
              <Button onClick={doPreview} disabled={!s.metric.trim()}>
                Prever disparos (7 dias)
              </Button>
              {preview && <strong>{preview}</strong>}
            </div>
            <p style={{ ...MUTED, marginTop: "var(--sp-2)" }}>
              {help.fields["alert.preview"]}
            </p>
          </div>
        </>
      )}

      {step === 2 && (
        <>
          <FormField label="Severidade" help={help.fields["alert.severity"]}>
            <select className="field" value={s.severity} onChange={(e) => set({ severity: e.target.value })}>
              <option value="info">aviso (info)</option>
              <option value="warning">alta (warning)</option>
              <option value="critical">crítica (critical)</option>
            </select>
          </FormField>

          <FormField
            label="Canais de notificação"
            help={help.fields["alert.channels"]}
            hint="Para onde enviar quando disparar. Nenhum marcado = todos os canais ativos."
          >
            {channels.length === 0 ? (
              <span style={MUTED}>
                Nenhum canal cadastrado ainda. Crie um em <a href="#/notify">Notificações</a> (o alerta então vai a todos).
              </span>
            ) : (
              <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-1)" }}>
                {channels.map((c) => (
                  <label key={c.id} className="row" style={{ gap: "var(--sp-2)", alignItems: "center" }}>
                    <input type="checkbox" checked={s.channel_ids.includes(c.id)} onChange={() => toggleChannel(c.id)} />
                    <span>{c.name}</span>
                    <Badge state="info">{c.type}</Badge>
                    {!c.enabled && <span style={MUTED}>(desativado)</span>}
                  </label>
                ))}
              </div>
            )}
          </FormField>

          <FormField label="Regra ativa" help={help.fields["alert.enabled"]}>
            <label className="row" style={{ gap: "var(--sp-2)", alignItems: "center" }}>
              <input type="checkbox" checked={s.enabled} onChange={(e) => set({ enabled: e.target.checked })} />
              <span style={MUTED}>Quando desativada, a regra para de vigiar e de disparar (sem apagá-la).</span>
            </label>
          </FormField>

          <AdvancedSection label="Opções avançadas">
            <div className="grid-2">
              <FormField label="Agregação" hint="Como resumir a métrica na janela: média, máximo, mínimo ou soma.">
                <select className="field" value={s.agg} onChange={(e) => set({ agg: e.target.value })}>
                  {AGGS.map((a) => (
                    <option key={a} value={a}>
                      {a}
                    </option>
                  ))}
                </select>
              </FormField>
              <FormField label="Janela (segundos)" hint="Intervalo usado para a agregação da métrica. Padrão: 300 (5 min).">
                <input className="field" type="number" min={1} value={s.window_seconds} onChange={(e) => set({ window_seconds: Number(e.target.value) })} />
              </FormField>
            </div>
            <FormField label="Runbook (URL)" hint="Link opcional para o procedimento de resposta a este alerta.">
              <input className="field" value={s.runbook} onChange={(e) => set({ runbook: e.target.value })} placeholder="https://wiki/runbook" />
            </FormField>
          </AdvancedSection>
        </>
      )}
    </Modal>
  );
}

// Escopo por servidor: escolhe entre "todos" (global) e "específicos" (checklist do
// inventário). Sem nenhum servidor marcado a regra é global — vale para todo servidor
// que reporta a métrica. Marcando um ou mais, a regra passa a vigiar só aqueles.
function ServerScope({
  hosts,
  selected,
  onChange,
}: {
  hosts: HostDetail[];
  selected: string[];
  onChange: (hosts: string[]) => void;
}) {
  // `specific` é local (intenção da UI): permite abrir a checklist mesmo antes de marcar
  // o primeiro servidor. O que vale de fato é `selected` — vazio significa global.
  const [specific, setSpecific] = useState(selected.length > 0);
  const toggle = (hostname: string) =>
    onChange(selected.includes(hostname) ? selected.filter((h) => h !== hostname) : [...selected, hostname]);
  const label = (h: HostDetail) => (h.display_name && h.display_name.trim() !== "" ? h.display_name.trim() : h.hostname);

  // Hosts selecionados que não estão (mais) no inventário — ex.: servidor removido ou
  // regra escopada por API. Mostramos mesmo assim, para o usuário poder vê-los e
  // desmarcá-los em vez de perdê-los silenciosamente.
  const known = new Set(hosts.map((h) => h.hostname));
  const orphans = selected.filter((h) => !known.has(h));

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
      <label className="row" style={{ gap: "var(--sp-2)", alignItems: "flex-start" }}>
        <input
          type="radio"
          name="scope-mode"
          checked={!specific}
          onChange={() => {
            setSpecific(false);
            onChange([]);
          }}
          style={{ marginTop: 3 }}
        />
        <span>
          Todos os servidores<span style={MUTED}>, a regra vale para qualquer servidor que reporte a métrica (global).</span>
        </span>
      </label>
      <label className="row" style={{ gap: "var(--sp-2)", alignItems: "flex-start" }}>
        <input type="radio" name="scope-mode" checked={specific} onChange={() => setSpecific(true)} style={{ marginTop: 3 }} />
        <span>
          Servidores específicos<span style={MUTED}>, escolha um ou mais servidores abaixo.</span>
        </span>
      </label>

      {specific && (
        <div
          style={{
            display: "flex",
            flexDirection: "column",
            gap: "var(--sp-1)",
            marginLeft: "var(--sp-5)",
            padding: "var(--sp-2)",
            maxHeight: 240,
            overflowY: "auto",
            border: "1px solid var(--border)",
            borderRadius: "var(--radius)",
            background: "var(--bg-2)",
          }}
        >
          {hosts.length === 0 && orphans.length === 0 ? (
            <span style={MUTED}>Nenhum servidor no inventário ainda.</span>
          ) : (
            hosts.map((h) => (
              <label key={h.hostname} className="row" style={{ gap: "var(--sp-2)", alignItems: "center" }}>
                <input type="checkbox" checked={selected.includes(h.hostname)} onChange={() => toggle(h.hostname)} />
                <span>{label(h)}</span>
                {label(h) !== h.hostname && <span style={{ ...MONO, ...MUTED }}>{h.hostname}</span>}
                {!h.up && <Badge state="neutral">offline</Badge>}
              </label>
            ))
          )}
          {orphans.map((h) => (
            <label key={h} className="row" style={{ gap: "var(--sp-2)", alignItems: "center" }}>
              <input type="checkbox" checked onChange={() => toggle(h)} />
              <span style={MONO}>{h}</span>
              <Badge state="neutral">fora do inventário</Badge>
            </label>
          ))}
          {selected.length === 0 && hosts.length > 0 && (
            <span style={{ ...MUTED, marginTop: "var(--sp-1)" }}>
              Marque ao menos um servidor, sem nenhum marcado, a regra fica global.
            </span>
          )}
        </div>
      )}
    </div>
  );
}

// Linha de filtro: chave + valor, com sugestões de valor via labelValues.
function FilterRow({
  metric,
  filter,
  onChange,
  onRemove,
}: {
  metric: string;
  filter: Filter;
  onChange: (f: Filter) => void;
  onRemove: () => void;
}) {
  const [values, setValues] = useState<string[]>([]);
  useEffect(() => {
    const label = filter.label.trim();
    if (!metric.trim() || !label) {
      setValues([]);
      return;
    }
    labelValues(metric, label).then((r) => setValues(r.values)).catch(() => setValues([]));
  }, [metric, filter.label]);

  const listId = `fv-${filter.label}`;
  return (
    <div className="row" style={{ gap: "var(--sp-2)", alignItems: "center", flexWrap: "wrap" }}>
      <input
        className="field"
        style={{ maxWidth: 180 }}
        placeholder="rótulo (ex.: host)"
        value={filter.label}
        onChange={(e) => onChange({ ...filter, label: e.target.value })}
      />
      <span style={MUTED}>=</span>
      <input
        className="field"
        style={{ maxWidth: 200 }}
        list={listId}
        placeholder="valor (ex.: web-01)"
        value={filter.value}
        onChange={(e) => onChange({ ...filter, value: e.target.value })}
      />
      <datalist id={listId}>
        {values.map((v) => (
          <option key={v} value={v} />
        ))}
      </datalist>
      <Button variant="ghost" onClick={onRemove} aria-label="Remover filtro">
        ✕
      </Button>
    </div>
  );
}


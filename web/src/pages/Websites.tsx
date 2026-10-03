// Websites & Jornadas (UX.10): checks sintéticos (estado, uptime, timing decomposto
// DNS→TLS→TTFB, certificado) e builder de jornadas multi-passo por formulário — o
// JSON cru vira modo avançado, não mais o caminho principal.
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Compass, Globe, Map as MapIcon } from "lucide-react";
import { usePolling } from "../hooks/usePolling";
import {
  ActionIcons,
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
  IconButton,
  InfoTip,
  Modal,
  PageHeader,
  Skeleton,
  Tabs,
  useToast,
  type State,
} from "../components";
import {
  createJourney,
  createSiteCheck,
  deleteJourney,
  deleteSiteCheck,
  getRole,
  listChannels,
  listJourneys,
  listSiteChecks,
  siteCheckHistory,
  siteOverview,
  siteProbes,
  updateSiteCheck,
  type Journey,
  type JourneyStep,
  type NotificationChannel,
  type ProbeResult,
  type SiteCheck,
  type SiteCheckResult,
} from "../api";
import { help } from "../help";
import { fmtRelAbs, formatUptimePercent, formatValue } from "../format";

// =========================================================================
// CONTRATO COM A API (server/internal/sitecheck/handlers.go)
// =========================================================================
//
// O uptime deixou de ser um número solto. Antes, `uptime_day` vinha 100 mesmo
// quando NÃO houve sondagem no período (o backend fazia `if total == 0 return
// 100`), e a tela não tinha como distinguir "ficou no ar" de "não olhamos" —
// medido no dev: 20 sondagens em 24 h (6,9% do esperado pelo tier) publicadas
// como "100,00%". Agora vem a COBERTURA junto e `percent` é `null` quando a
// medição não autoriza afirmar nada.
//
// `src/api.ts` é de outra frente e ainda declara `uptime_day: number`; até ser
// atualizado, os tipos abaixo descrevem o contrato real e a conversão acontece
// num ponto só (asCheck).
interface UptimeStat {
  percent: number | null; // null = sem dados suficientes (NUNCA assuma 100)
  coverage: number; // % do período efetivamente sondado
  samples: number; // sondagens observadas
  expected: number; // sondagens esperadas pelo intervalo do check
  window_full: boolean; // o histórico cobre a janela inteira
}
interface PageCount {
  total: number;
  down: number;
  degraded: number;
}
interface Consensus {
  configured: number; // sondas designadas no check
  reporting: number; // designadas com reporte fresco
  silent: string[] | null;
  source: "central" | "consenso" | "sem_consenso" | string;
}
type Check = Omit<SiteCheck, "uptime_day" | "uptime_month"> & {
  uptime: UptimeStat;
  uptime_30d: UptimeStat;
  consensus: Consensus;
  pages?: PageCount; // só em kind='sitemap' (que não tem uptime próprio)
  timeout_ms: number;
};

const UPTIME_VAZIO: UptimeStat = { percent: null, coverage: 0, samples: 0, expected: 0, window_full: false };

// TIMEOUT_PADRAO_MS espelha sitecheck.DefaultTimeout no servidor. A API manda o
// valor em vigor em cada linha (`timeout_ms`); esta constante só serve ao
// formulário, que fala do check antes de ele existir.
const TIMEOUT_PADRAO_MS = 20000;

// asCheck normaliza a linha vinda da API. Um campo ausente NUNCA vira 100%: vira
// "sem dados", que é a verdade quando não medimos.
function asCheck(raw: unknown): Check {
  const c = raw as Check;
  return {
    ...c,
    uptime: c.uptime ?? UPTIME_VAZIO,
    uptime_30d: c.uptime_30d ?? UPTIME_VAZIO,
    consensus: c.consensus ?? { configured: 0, reporting: 0, silent: null, source: "central" },
  };
}

// Vocabulário único de diagnósticos (server/internal/sitecheck/diagnosis.go e a
// cópia espelhada em agent/internal/probe/diagnosis.go). A tela mostra o resultado
// da sonda central e o das sondas remotas lado a lado; sem esta tradução o usuário
// via "tls" de um lado e "tls_error" do outro e tinha de adivinhar que era a mesma
// coisa.
const DIAG_LABEL: Record<string, string> = {
  dns_error: "o nome do site não resolve (DNS)",
  connect_refused: "conexão recusada (porta fechada)",
  connect_timeout: "sem resposta dentro do limite",
  sem_resposta: "conectou, mas a página não veio no limite",
  connect_error: "a conexão caiu ou não há rota",
  tls: "falha no certificado/HTTPS",
  http_5xx: "o servidor respondeu erro (5xx)",
  http_status: "status diferente do esperado",
  keyword_missing: "a palavra-chave não está na página",
  keyword_indeterminado: "página grande demais para conferir a palavra-chave",
  slow: "respondeu acima da latência máxima",
  bad_url: "URL inválida",
  bloqueado_pelo_painel: "destino recusado pelo Painel (endereço interno)",
  unclassified: "falha não classificada",
};

// diagLabel traduz um diagnóstico para uma frase. `timeoutMs` deixa o limite
// explícito ("sem resposta em 20 s") — era uma constante embutida no código, que
// não aparecia em lugar nenhum da tela: um site que responde em 21 s era publicado
// como fora do ar sem que nada dissesse que o corte era nosso.
//
// LIMITE CONHECIDO: o número vem do check de HOJE, e nem `site_check_results` nem
// `probe_results` guardam a régua que valeu em cada sondagem. Uma falha registrada
// quando o corte era 15 s é rotulada agora como "sem resposta em 20 s". Enquanto a
// coluna não existir, leia a frase como "o corte atual", não como o histórico.
function diagLabel(diag: string, timeoutMs?: number): string {
  if (!diag) return "";
  if (diag === "connect_timeout" && timeoutMs && timeoutMs > 0) {
    return `sem resposta em ${Math.round(timeoutMs / 1000)} s`;
  }
  if (diag === "sem_resposta" && timeoutMs && timeoutMs > 0) {
    return `conectou, mas a página não veio em ${Math.round(timeoutMs / 1000)} s`;
  }
  // Diagnósticos compostos (consenso multi-sonda) já vêm em português do backend.
  return DIAG_LABEL[diag] ?? diag;
}

const STATE_BADGE: Record<string, State> = { UP: "ok", DEGRADADO: "warn", SUSPEITO: "warn", DOWN: "crit" };
const CHANNEL_TYPE_LABEL: Record<string, string> = {
  smtp: "e-mail",
  webhook: "webhook",
  telegram: "Telegram",
  whatsapp: "WhatsApp",
};
const TIER_LABEL: Record<string, string> = {
  critico: "crítico · 1 min · confirma em 30 s",
  padrao: "padrão · 5 min · confirma em ~1 min",
  basico: "básico · 10 min · confirma em ~1,5 min",
};

const MUTED = { color: "var(--text-3)" } as const;
const SUBTLE = { fontSize: "var(--fs-12)", color: "var(--text-2)" } as const;
const MONO = { fontFamily: "var(--font-mono)", fontSize: "var(--fs-12)" } as const;

// Monta as seções do HelpPanel a partir de help.pages.websites (padrão das telas).
function websitesHelpSections(): { heading: string; body: ReactNode }[] {
  const p = help.pages.websites;
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

// Explicação reutilizável das fases de timing (usada no InfoTip de check.timing).
const timingHelp = (
  <>
    {help.fields["check.timing"]}
    <ul style={{ margin: "var(--sp-2) 0 0", paddingLeft: "var(--sp-4)" }}>
      <li><strong>DNS</strong>: tempo para achar o endereço do servidor.</li>
      <li><strong>Conn</strong>: abrir a conexão TCP até o servidor.</li>
      <li><strong>TLS</strong>: negociar a conexão segura (HTTPS).</li>
      <li><strong>TTFB</strong>: espera até o primeiro byte da resposta.</li>
    </ul>
  </>
);

// Sparkline de latência recente. `limit` (max_latency_ms do check) vira uma linha
// de referência tracejada. O número com unidade (última latência) sai na própria
// coluna, então `showLast` controla se o mini-gráfico também repete o valor ao lado
// (evita duplicar quando a célula já mostra o número em destaque).
function Sparkline({ points, limit, showLast = true }: { points: number[]; limit?: number; showLast?: boolean }) {
  if (points.length < 2) return <span style={MUTED}>—</span>;
  const last = points[points.length - 1];
  const hasLimit = typeof limit === "number" && limit > 0;
  const w = 90;
  const h = 22;
  const max = Math.max(...points, hasLimit ? limit : 0, 1);
  const step = w / (points.length - 1);
  const y = (v: number) => (h - (v / max) * h).toFixed(1);
  const d = points.map((p, i) => `${i === 0 ? "M" : "L"}${(i * step).toFixed(1)},${y(p)}`).join(" ");
  const over = hasLimit && last > (limit as number);
  const title =
    `Última latência ${Math.round(last)} ms` +
    (hasLimit ? ` · limite ${Math.round(limit as number)} ms${over ? " (acima do limite)" : ""}` : "");
  return (
    <span style={{ display: "inline-flex", alignItems: "center", gap: "var(--sp-1)" }} title={title}>
      <svg width={w} height={h} style={{ verticalAlign: "middle" }} role="img" aria-label={title}>
        {hasLimit && (limit as number) <= max && (
          <line x1="0" y1={y(limit as number)} x2={w} y2={y(limit as number)} stroke="var(--warn)" strokeWidth="1" strokeDasharray="3 2" opacity="0.7" />
        )}
        <path d={d} fill="none" stroke={over ? "var(--crit)" : "var(--accent)"} strokeWidth="1.5" />
      </svg>
      {showLast && (
        <span className="tabular" style={{ fontSize: "var(--fs-12)", color: over ? "var(--crit)" : "var(--text-2)" }}>
          {Math.round(last)} ms
        </span>
      )}
    </span>
  );
}

function certState(days: number): State {
  return days <= 7 ? "crit" : days <= 30 ? "warn" : "ok";
}

// coberturaTexto descreve, em uma frase, o quanto do período foi medido.
function coberturaTexto(u: UptimeStat): string {
  if (u.expected <= 0) return "nenhuma sondagem registrada no período";
  return `${u.samples} de ${u.expected} sondagens previstas (${Math.round(u.coverage)}% do período)`;
}

// UptimeText mostra o uptime SÓ quando a cobertura autoriza afirmá-lo. Sem
// cobertura, diz "sem dados" — nunca 100%, que era exatamente o que o backend
// devolvia para períodos em que não houve sondagem nenhuma. Janela sem sondagem é
// DESCONHECIDA, não "no ar".
function UptimeText({ u, label }: { u: UptimeStat; label?: string }) {
  if (u.percent == null) {
    return (
      <span
        style={{ color: "var(--text-3)", fontSize: "var(--fs-12)" }}
        title={`Sem dados suficientes para afirmar disponibilidade: ${coberturaTexto(u)}.`}
      >
        sem dados{label ? ` · ${label}` : ""}
      </span>
    );
  }
  return (
    <span className="num" title={`Medido em ${coberturaTexto(u)}.`}>
      {formatUptimePercent(u.percent)}
      {label ? <span style={{ color: "var(--text-3)" }}> {label}</span> : null}
    </span>
  );
}

// PagesText substitui o uptime nos checks kind='sitemap'. O check-pai nunca sonda
// (quem sonda são as páginas-filhas), mas passava pelo cálculo de uptime e caía no
// "sem sondagem = 100%": um site inteiro fora do ar exibia "Uptime 24h 100,00%" ao
// lado do estado DOWN.
function PagesText({ p }: { p: PageCount }) {
  if (!p || p.total === 0) {
    return <span style={{ color: "var(--text-3)", fontSize: "var(--fs-12)" }}>descobrindo páginas…</span>;
  }
  const up = p.total - p.down - p.degraded;
  return (
    <span className="num" title="O sitemap não tem uptime próprio: quem mede são as páginas descobertas.">
      {up}/{p.total} <span style={{ color: "var(--text-3)" }}>no ar</span>
    </span>
  );
}

// OriginText diz de onde veio a medição. Um check sem sondas designadas é de
// ORIGEM ÚNICA e nada na tela dizia isso; um check com sondas designadas que
// pararam de reportar precisa aparecer como "sem consenso", e não como se a
// medição seguisse normal (era aí que a proteção contra falso positivo se
// desligava sozinha, justamente quando a malha estava instável).
function OriginText({ c }: { c: Check }) {
  const k = c.consensus;
  if (!k || k.configured === 0) {
    return (
      <span style={SUBTLE} title="Este check é medido de um ponto só. Uma falha local do Painel vira 'site fora do ar'.">
        medido de 1 ponto (central)
      </span>
    );
  }
  const semConsenso = k.reporting === 0;
  const mudas = (k.silent ?? []).join(", ");
  return (
    <span
      style={{ ...SUBTLE, color: semConsenso ? "var(--warn)" : "var(--text-2)" }}
      title={
        semConsenso
          ? `Nenhuma sonda designada reportou nos últimos 2 minutos (${mudas}). A decisão de estado está suspensa até haver consenso.`
          : `Central + ${k.reporting} de ${k.configured} sondas designadas reportando.${mudas ? ` Caladas: ${mudas}.` : ""}`
      }
    >
      {k.configured} sonda{k.configured === 1 ? "" : "s"}, {k.reporting} reportando
      {semConsenso ? " · sem consenso" : ""}
    </span>
  );
}

type WebTab = "overview" | "checks" | "jornadas";

export function Websites() {
  const [tab, setTab] = useState<WebTab>("overview");
  const [helpOpen, setHelpOpen] = useState(false);
  const canEdit = getRole() === "admin";

  return (
    <div className="page">
      <PageHeader
        title="Websites & Jornadas"
        subtitle="Monitore se seus sites estão no ar e fluxos de várias etapas"
        onHelp={() => setHelpOpen(true)}
      />

      <Tabs
        active={tab}
        onChange={(id) => setTab(id as WebTab)}
        tabs={[
          { id: "overview", label: "Visão geral" },
          { id: "checks", label: "Checks" },
          { id: "jornadas", label: "Jornadas" },
        ]}
      />

      {tab === "overview" && <OverviewPanel onManage={() => setTab("checks")} canEdit={canEdit} />}
      {tab === "checks" && <ChecksPanel canEdit={canEdit} />}
      {tab === "jornadas" && <JourneysPanel canEdit={canEdit} />}

      <HelpPanel
        open={helpOpen}
        onClose={() => setHelpOpen(false)}
        title={help.pages.websites.title}
        sections={websitesHelpSections()}
      />
    </div>
  );
}

// =========================================================================
// VISÃO GERAL (dashboard)
// =========================================================================

// cor de destaque por estado, só via tokens.
const STATE_ACCENT: Record<string, string> = {
  UP: "var(--ok)",
  DEGRADADO: "var(--warn)",
  SUSPEITO: "var(--warn)",
  DOWN: "var(--crit)",
};
const TINT_BG: Record<State, string> = {
  ok: "var(--ok-bg)",
  warn: "var(--warn-bg)",
  crit: "var(--crit-bg)",
  info: "var(--bg-2)",
  neutral: "var(--bg-2)",
};
const TINT_FG: Record<State, string> = {
  ok: "var(--ok)",
  warn: "var(--warn)",
  crit: "var(--crit)",
  info: "var(--text-1)",
  neutral: "var(--text-1)",
};

// há quanto tempo (pt-BR compacto) desde um instante ISO.
function timeSince(iso: string | null): string {
  if (!iso) return "";
  const secs = Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 1000));
  if (secs < 60) return `há ${secs}s`;
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `há ${mins} min`;
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return `há ${hrs}h${String(mins % 60).padStart(2, "0")}`;
  const days = Math.floor(hrs / 24);
  return `há ${days}d`;
}

// última latência conhecida (ms), do overview (last_latency) ou da sparkline; null se nenhuma.
function lastLatency(c: Check): number | null {
  if (typeof c.last_latency === "number" && c.last_latency > 0) return c.last_latency;
  const s = c.sparkline;
  return s && s.length > 0 ? s[s.length - 1] : null;
}

// páginas "folha" (as que de fato têm métrica): páginas avulsas + páginas de sitemap;
// exclui os checks kind='sitemap', que são agregadores e contariam em dobro.
function leafChecks(checks: Check[]): Check[] {
  return checks.filter((c) => c.kind !== "sitemap");
}

function groupChecks(checks: Check[]): [string, Check[]][] {
  const map = new Map<string, Check[]>();
  for (const c of checks) {
    const g = c.group_name || "Geral";
    if (!map.has(g)) map.set(g, []);
    map.get(g)!.push(c);
  }
  return [...map.entries()];
}

function StatBox({ label, value, sub, tone }: { label: string; value: ReactNode; sub?: string; tone?: "ok" | "crit" | "warn" }) {
  const toneClass = tone === "crit" ? " stat-card__value--crit" : tone === "warn" ? " stat-card__value--warn" : "";
  return (
    <div className="stat-card" style={tone === "ok" ? { borderColor: "var(--ok)" } : undefined}>
      <span className="stat-card__label">{label}</span>
      <span className={`stat-card__value tabular${toneClass}`} style={tone === "ok" ? { color: "var(--ok)" } : undefined}>
        {value}
      </span>
      {sub && <span className="stat-card__sub">{sub}</span>}
    </div>
  );
}

// Tile de um site na grade de status (barra de acento à esquerda + métricas).
function SiteTile({ c }: { c: Check }) {
  const st = STATE_BADGE[c.state] ?? "neutral";
  const accent = STATE_ACCENT[c.state] ?? "var(--text-3)";
  const lat = lastLatency(c);
  return (
    <div
      style={{
        display: "flex",
        gap: "var(--sp-3)",
        padding: "var(--sp-3)",
        background: "var(--bg-2)",
        border: "1px solid var(--border)",
        borderLeft: `3px solid ${accent}`,
        borderRadius: "var(--radius)",
        minWidth: 0,
      }}
    >
      <div style={{ flex: "1 1 auto", minWidth: 0 }}>
        <div style={{ display: "flex", alignItems: "center", gap: "var(--sp-2)", marginBottom: 2 }}>
          <Badge state={st}>{c.state}</Badge>
          <strong style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{c.name}</strong>
        </div>
        <div style={{ ...SUBTLE, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{c.url}</div>
        <div style={{ display: "flex", alignItems: "center", gap: "var(--sp-3)", marginTop: "var(--sp-2)", flexWrap: "wrap" }}>
          {c.kind === "sitemap" ? (
            <span className="tabular" style={SUBTLE}>
              <PagesText p={c.pages as PageCount} />
            </span>
          ) : (
            <span className="tabular" style={SUBTLE}>
              uptime <UptimeText u={c.uptime} label="24h" /> · <UptimeText u={c.uptime_30d} label="30d" />
            </span>
          )}
          {lat != null && (
            <span className="tabular" style={SUBTLE}>
              <strong style={{ color: "var(--text-1)" }}>{Math.round(lat)}</strong> ms
            </span>
          )}
        </div>
        <div style={{ marginTop: 4 }}>
          <OriginText c={c} />
        </div>
        {c.state === "DOWN" && c.down_since && (
          <div style={{ ...SUBTLE, color: "var(--crit)", marginTop: 4 }}>
            fora do ar {timeSince(c.down_since)}
            {c.last_diagnosis ? `, ${diagLabel(c.last_diagnosis, c.timeout_ms)}` : ""}
          </div>
        )}
        {c.state !== "DOWN" && c.state !== "UP" && c.last_diagnosis && (
          <div style={{ ...SUBTLE, color: "var(--warn)", marginTop: 4 }}>{diagLabel(c.last_diagnosis, c.timeout_ms)}</div>
        )}
      </div>
    </div>
  );
}

// SiteGroup renderiza um grupo de páginas com resumo (no ar / fora) e grade de
// tiles. Grupos grandes (ex.: um sitemap com dezenas de páginas) começam mostrando
// só as páginas com problema, com um botão para expandir todas.
function SiteGroup({ group, list }: { group: string; list: Check[] }) {
  const COLLAPSE_AT = 12;
  const problems = list.filter((c) => c.state !== "UP");
  const [expanded, setExpanded] = useState(false);
  const big = list.length > COLLAPSE_AT;
  const shown = big && !expanded ? (problems.length > 0 ? problems : list.slice(0, COLLAPSE_AT)) : list;
  const down = list.filter((c) => c.state === "DOWN").length;
  const degraded = list.filter((c) => c.state === "DEGRADADO" || c.state === "SUSPEITO").length;

  return (
    <Card title={group}>
      <div style={{ ...SUBTLE, marginBottom: "var(--sp-2)" }}>
        {list.length} página{list.length === 1 ? "" : "s"} · {list.length - down - degraded} no ar
        {down > 0 ? ` · ${down} fora` : ""}
        {degraded > 0 ? ` · ${degraded} degradada${degraded === 1 ? "" : "s"}` : ""}
      </div>
      <div style={{ display: "grid", gap: "var(--sp-3)", gridTemplateColumns: "repeat(auto-fill, minmax(260px, 1fr))" }}>
        {shown.map((c) => (
          <SiteTile key={c.id} c={c} />
        ))}
      </div>
      {big && (
        <div className="row" style={{ marginTop: "var(--sp-3)" }}>
          <Button variant="ghost" onClick={() => setExpanded((v) => !v)}>
            {expanded ? "Mostrar menos" : `Mostrar todas as ${list.length} páginas`}
          </Button>
          {!expanded && problems.length > 0 && (
            <span style={SUBTLE}>mostrando só as {problems.length} com problema</span>
          )}
        </div>
      )}
    </Card>
  );
}

function OverviewPanel({ onManage, canEdit }: { onManage: () => void; canEdit: boolean }) {
  const [checks, setChecks] = useState<Check[] | null>(null);

  const reload = useCallback(() => {
    siteOverview().then((r) => setChecks(r.checks.map(asCheck))).catch(() => setChecks([]));
  }, []);
  usePolling(reload, 15000, [reload]);

  // métricas contam as PÁGINAS (folhas), não os agregadores de sitemap.
  const leaves = useMemo(() => leafChecks(checks ?? []), [checks]);
  const stats = useMemo(() => {
    const list = leaves;
    const up = list.filter((c) => c.state === "UP").length;
    const down = list.filter((c) => c.state === "DOWN").length;
    const degraded = list.filter((c) => c.state === "DEGRADADO" || c.state === "SUSPEITO").length;
    // A média só considera as páginas com COBERTURA suficiente. Antes, cada página
    // sem medição entrava na média valendo 100% e puxava o "uptime médio" para
    // cima — o número da tela era, em parte, o silêncio das sondagens.
    const medidas = list.filter((c) => c.uptime.percent != null);
    const upt = medidas.length ? medidas.reduce((a, c) => a + (c.uptime.percent as number), 0) / medidas.length : null;
    const semDados = list.length - medidas.length;
    const lats = list.map(lastLatency).filter((v): v is number => v != null);
    const avgLat = lats.length ? lats.reduce((a, b) => a + b, 0) / lats.length : null;
    // Uptime médio NÃO arredonda para cima: formatUptimePercent usa piso com 2 casas,
    // senão 99,96% viraria "100%" e a tela afirmaria que nada caiu.
    return { total: list.length, up, down, degraded, upt, semDados, medidas: medidas.length, avgLat };
  }, [leaves]);

  if (checks === null) {
    return (
      <div className="stack">
        <div className="stat-grid">
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} height={92} />
          ))}
        </div>
        <Skeleton height={140} />
      </div>
    );
  }

  if (checks.length === 0) {
    return (
      <Card>
        <EmptyState
          icon={<Globe size={32} strokeWidth={1.5} />}
          title="Nenhum site monitorado ainda"
          body="Cadastre um check para começar a acompanhar aqui, em tempo real, se seus sites estão no ar, o uptime e a latência, e receber alerta quando algum cair."
          steps={canEdit ? ["Abra a aba Checks.", "Preencha a URL e salve.", "Volte aqui para ver o painel."] : undefined}
          action={canEdit ? { label: "Cadastrar um site", onClick: onManage } : undefined}
        />
      </Card>
    );
  }

  const problems = leaves.filter((c) => c.state !== "UP");
  const anyDown = problems.some((c) => c.state === "DOWN");
  const overall: State = problems.length === 0 ? "ok" : anyDown ? "crit" : "warn";
  const overallLabel =
    problems.length === 0
      ? "Todas as páginas no ar"
      : anyDown
        ? `${stats.down} página${stats.down === 1 ? "" : "s"} fora do ar`
        : `${stats.degraded} página${stats.degraded === 1 ? "" : "s"} com degradação`;

  const groups = groupChecks(leaves);

  return (
    <div className="stack">
      {/* Banner geral */}
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: "var(--sp-3)",
          padding: "var(--sp-4)",
          background: TINT_BG[overall],
          border: "1px solid var(--border)",
          borderRadius: "var(--radius)",
        }}
      >
        <Badge state={overall}>●</Badge>
        <span style={{ fontSize: "var(--fs-20)", color: TINT_FG[overall] }}>
          {overallLabel}
          {overall === "ok" ? " ✓" : ""}
        </span>
        <span style={{ marginLeft: "auto" }}>
          <Button variant="ghost" onClick={onManage}>
            Gerenciar checks
          </Button>
        </span>
      </div>

      {/* Cartões de estado */}
      <div className="stat-grid">
        <StatBox label="Monitorados" value={stats.total} />
        <StatBox label="No ar" value={stats.up} tone="ok" />
        <StatBox label="Fora do ar" value={stats.down} tone={stats.down > 0 ? "crit" : undefined} />
        <StatBox label="Degradados" value={stats.degraded} tone={stats.degraded > 0 ? "warn" : undefined} />
        <StatBox
          label="Uptime médio"
          value={stats.upt != null ? formatUptimePercent(stats.upt) : "sem dados"}
          sub={
            stats.upt != null
              ? `últimas 24h · ${stats.medidas} de ${stats.total} página(s) com medição suficiente`
              : "cobertura insuficiente nas últimas 24h"
          }
        />
        <StatBox label="Latência média" value={stats.avgLat != null ? Math.round(stats.avgLat) : "—"} sub="ms · última medição" />
      </div>

      {/* Precisam de atenção */}
      {problems.length > 0 && (
        <Card title={`Precisam de atenção (${problems.length})`}>
          <div className="stack">
            {problems
              .slice()
              .sort((a, b) => (a.state === "DOWN" ? -1 : 1) - (b.state === "DOWN" ? -1 : 1))
              .slice(0, 15)
              .map((c) => (
                <div
                  key={c.id}
                  style={{
                    display: "flex",
                    alignItems: "center",
                    gap: "var(--sp-3)",
                    flexWrap: "wrap",
                    padding: "var(--sp-2) var(--sp-3)",
                    background: c.state === "DOWN" ? "var(--crit-bg)" : "var(--warn-bg)",
                    border: "1px solid var(--border)",
                    borderRadius: "var(--radius-sm)",
                  }}
                >
                  <Badge state={STATE_BADGE[c.state] ?? "neutral"}>{c.state}</Badge>
                  <div style={{ flex: "1 1 200px", minWidth: 0 }}>
                    <strong>{c.name}</strong>
                    <div style={SUBTLE}>{c.url}</div>
                  </div>
                  <span style={{ ...SUBTLE, color: c.state === "DOWN" ? "var(--crit)" : "var(--warn)" }}>
                    {c.state === "DOWN" && c.down_since ? `fora ${timeSince(c.down_since)}` : ""}
                    {c.last_diagnosis ? `${c.state === "DOWN" && c.down_since ? " · " : ""}${diagLabel(c.last_diagnosis, c.timeout_ms)}` : ""}
                  </span>
                </div>
              ))}
            {problems.length > 15 && (
              <div style={SUBTLE}>+{problems.length - 15} outra(s) página(s) com problema, veja nos grupos abaixo.</div>
            )}
          </div>
        </Card>
      )}

      {/* Grade de status por grupo */}
      {groups.map(([group, list]) => (
        <SiteGroup key={group} group={group} list={list} />
      ))}
    </div>
  );
}

// =========================================================================
// CHECKS
// =========================================================================

function ChecksPanel({ canEdit }: { canEdit: boolean }) {
  const toast = useToast();
  const [checks, setChecks] = useState<Check[] | null>(null);
  const [selected, setSelected] = useState<Check | null>(null);
  const [editing, setEditing] = useState<Check | null>(null);
  const [confirmDel, setConfirmDel] = useState<Check | null>(null);
  const [zoom, setZoom] = useState<Check | null>(null);
  const formRef = useRef<HTMLDivElement>(null);

  const reload = useCallback(() => {
    listSiteChecks().then((r) => setChecks(r.checks.map(asCheck))).catch(() => setChecks([]));
  }, []);

  const startEdit = (c: Check) => {
    setEditing(c);
    // leva o formulário (abaixo da tabela) para a área visível ao entrar em edição.
    requestAnimationFrame(() => formRef.current?.scrollIntoView({ behavior: "smooth", block: "start" }));
  };
  usePolling(reload, 10000, [reload]);

  const doDelete = async () => {
    if (!confirmDel) return;
    const c = confirmDel;
    setConfirmDel(null);
    try {
      await deleteSiteCheck(c.id);
      if (selected?.id === c.id) setSelected(null);
      if (editing?.id === c.id) setEditing(null);
      toast.success(`Check "${c.name}" apagado.`);
      reload();
    } catch {
      toast.error("Não foi possível apagar o check.");
    }
  };

  return (
    <div className="stack">
      <Card title={checks ? `${checks.length} check(s)` : "Checks"}>
        {checks === null ? (
          <SkeletonRows />
        ) : (
          <DataTable<Check>
            rows={checks}
            keyFn={(c) => c.id}
            fit
            actionsWidth="112px"
            // Piso = larguras declaradas (616px) + ações (112px) + 170px de piso
            // para o "Site", que é a coluna que identifica a linha.
            //
            // Caiu de 1100px para 898px em dois passos: os dois uptimes viraram
            // uma coluna e o diagnóstico/origem foram para dentro do "Estado".
            // Medido no navegador: a largura útil da tabela no painel é 935px,
            // então agora ela CABE e a rolagem lateral sumiu. O piso continua
            // existindo para a janela estreita — sem ele o "Site" chegava a 8px e
            // a URL quebrava uma letra por linha; ali, rolar é o menor dos males.
            minWidth="898px"
            empty={
              <EmptyState
                icon={<Globe size={32} strokeWidth={1.5} />}
                title="Nenhum site monitorado ainda"
                body="Um check acessa a URL de tempos em tempos e mede se o site está no ar, quanto demora para responder e quando o certificado expira, o uptime% mostra a confiabilidade ao longo do dia e do mês."
                steps={
                  canEdit
                    ? ["Preencha a URL no formulário abaixo.", "Escolha o intervalo e, se quiser, as sondas.", "Salve e acompanhe o estado e o uptime aqui."]
                    : undefined
                }
              />
            }
            columns={[
              {
                key: "state",
                label: "Estado",
                width: "150px",
                // Três informações que respondem à MESMA pergunta ("o site está de
                // pé, e o quanto essa resposta é confiável?"), numa coluna só:
                // o selo, o diagnóstico e a origem da medição.
                //
                // Diagnóstico e origem eram colunas separadas. Somadas às outras
                // larguras fixas, a tabela pedia mais do que a largura útil do
                // painel (935px) e o container passava a rolar de lado — e o que
                // sai de vista numa tabela que rola é sempre a ponta direita, ou
                // seja, a coluna de Ações. Juntas aqui, sobram ~205px para o
                // "Site", que é o que identifica a linha.
                render: (c) => {
                  const diag = diagLabel(c.last_diagnosis, c.timeout_ms);
                  return (
                    <div>
                      <Badge state={STATE_BADGE[c.state] ?? "neutral"}>{c.state}</Badge>
                      {diag && (
                        <div style={{ ...SUBTLE, marginTop: 2 }} title={c.last_diagnosis || undefined}>
                          {diag}
                        </div>
                      )}
                      <div style={{ marginTop: 2 }}>
                        <OriginText c={c} />
                      </div>
                    </div>
                  );
                },
              },
              {
                key: "name",
                label: "Site",
                sortable: true,
                sortValue: (c) => c.name,
                render: (c) => (
                  <div>
                    {c.name}
                    {c.kind === "sitemap" && (
                      <span style={{ marginLeft: 6 }}>
                        <Badge state="info">sitemap</Badge>
                      </span>
                    )}
                    <div style={SUBTLE}>{c.url}</div>
                  </div>
                ),
              },
              {
                key: "group",
                label: "Grupo",
                width: "84px",
                hideOnMobile: true,
                sortable: true,
                sortValue: (c) => c.group_name || "",
                render: (c) => <span style={SUBTLE}>{c.group_name || "—"}</span>,
              },
              {
                key: "last_checked_at",
                label: "Último check",
                width: "108px",
                sortable: true,
                // ordena pelo instante real (0 = nunca checou fica no fim do "mais recente").
                sortValue: (c) => (c.last_checked_at ? new Date(c.last_checked_at).getTime() : 0),
                // Quando foi o último check e de quanto em quanto tempo ele roda são
                // a mesma pergunta (a cadência da medição): uma coluna só, com o
                // intervalo como linha de apoio.
                render: (c) => {
                  const tier = TIER_LABEL[c.tier] ?? c.tier;
                  const t = c.last_checked_at ? fmtRelAbs(c.last_checked_at) : null;
                  return (
                    <div>
                      {t ? (
                        <span style={SUBTLE} title={t.abs}>
                          {t.rel}
                        </span>
                      ) : (
                        <span style={MUTED}>—</span>
                      )}
                      <div style={{ ...SUBTLE, color: "var(--text-3)" }}>{tier}</div>
                    </div>
                  );
                },
              },
              {
                // Uptime de 24h e de 30d numa coluna só, empilhados.
                //
                // Eram duas colunas de 96px. Somadas às outras seis larguras fixas
                // e à de ações, a tabela precisava de mais espaço do que a tela
                // tinha e o container passou a rolar de lado — e uma tabela que
                // rola esconde justamente a coluna de ações, que fica no fim.
                // Empilhar é o mesmo padrão que Estado e Último check já usam
                // aqui: o número principal em cima, o de apoio embaixo. Devolve
                // 82px, que vão para a coluna "Site".
                key: "uptime_day",
                label: "Uptime 24h / 30d",
                width: "110px",
                align: "right",
                sortable: true,
                // "sem dados" ordena abaixo de qualquer percentual medido (-1), em vez
                // de fingir 100% e liderar a lista.
                sortValue: (c) => (c.kind === "sitemap" ? -1 : (c.uptime.percent ?? -1)),
                render: (c) =>
                  c.kind === "sitemap" ? (
                    <PagesText p={c.pages as PageCount} />
                  ) : (
                    <div>
                      <UptimeText u={c.uptime} />
                      <div style={{ ...SUBTLE, color: "var(--text-3)" }} title="Uptime dos últimos 30 dias">
                        <UptimeText u={c.uptime_30d} />
                      </div>
                    </div>
                  ),
              },
              {
                key: "latency",
                label: "Tempo de carregamento",
                width: "164px",
                // A ajuda explica a COLUNA, não a linha: no cabeçalho ela aparece
                // uma vez, em vez de repetir o mesmo ícone em todas as linhas (e
                // devolve ~20px por célula, que faltavam para o número caber).
                headerExtra: <InfoTip text={timingHelp} title="Timing DNS → TLS → TTFB" />,
                // número com unidade + mini-gráfico clicável (abre o gráfico ampliado).
                render: (c) => {
                  const lat = lastLatency(c);
                  const canZoom = (c.sparkline?.length ?? 0) >= 2;
                  return (
                    <span style={{ display: "inline-flex", alignItems: "center", gap: "var(--sp-2)" }}>
                      <span className="tabular" style={{ fontSize: "var(--fs-12)", color: "var(--text-1)" }}>
                        {lat != null ? `${Math.round(lat)} ms` : "—"}
                      </span>
                      <button
                        type="button"
                        className="spark-zoom"
                        onClick={() => canZoom && setZoom(c)}
                        disabled={!canZoom}
                        title={
                          !canZoom
                            ? "Sem dados suficientes para o gráfico"
                            : (c.max_latency_ms ?? 0) > 0
                              ? "Ampliar gráfico de tempo de carregamento"
                              : "Ampliar gráfico de tempo de carregamento (este site não tem limiar de lentidão: só é julgado por no ar × fora do ar)"
                        }
                        aria-label={`Ampliar gráfico de ${c.name}`}
                      >
                        <Sparkline points={c.sparkline ?? []} limit={c.max_latency_ms} showLast={false} />
                      </button>
                    </span>
                  );
                },
              },
            ]}
            rowActions={(c) => (
              <div className="row" style={{ gap: "var(--sp-1)" }}>
                {/* Ações por ícone (mesmo par ícone↔ação do resto do sistema). O
                    IconButton sempre leva aria-label + title, então o nome da ação
                    aparece no hover e é lido por leitor de tela — sem isso, ícone
                    sozinho vira adivinhação. */}
                <IconButton
                  icon={ActionIcons.history}
                  label={c.kind === "sitemap" ? "Ver páginas" : "Ver histórico"}
                  onClick={() => setSelected(c)}
                />
                {canEdit && (
                  <IconButton icon={ActionIcons.edit} label="Editar" onClick={() => startEdit(c)} />
                )}
                {canEdit && (
                  <IconButton
                    icon={ActionIcons.delete}
                    label="Apagar"
                    className="btn--icon-danger"
                    onClick={() => setConfirmDel(c)}
                  />
                )}
              </div>
            )}
          />
        )}
      </Card>

      {canEdit && (
        <div ref={formRef}>
          <CheckForm
            key={editing ? `edit-${editing.id}` : "new"}
            edit={editing}
            onSaved={() => {
              setEditing(null);
              reload();
            }}
            onCancelEdit={() => setEditing(null)}
          />
        </div>
      )}

      {selected &&
        (selected.kind === "sitemap" ? (
          <SitemapPages check={selected} onClose={() => setSelected(null)} />
        ) : (
          <CheckHistory check={selected} onClose={() => setSelected(null)} />
        ))}

      {zoom && <LatencyZoomModal check={zoom} onClose={() => setZoom(null)} />}

      <ConfirmDialog
        open={confirmDel !== null}
        onCancel={() => setConfirmDel(null)}
        onConfirm={doDelete}
        verb="Apagar"
        target={confirmDel ? confirmDel.url : "check"}
        consequences="O check para de rodar e o histórico de disponibilidade dele é descartado. Esta ação não pode ser desfeita."
        danger
      />
    </div>
  );
}

function SkeletonRows() {
  return (
    <div className="stack">
      {[0, 1, 2].map((i) => (
        <Skeleton key={i} height={28} />
      ))}
    </div>
  );
}

// SiteCheck expõe group_name/probe_locations no JSON da API, mas o tipo público não
// os declara; este alias opcional deixa lê-los na edição sem `any`.
interface CheckConfig {
  group_name?: string;
  probe_locations?: string[];
}

// CheckForm serve tanto para criar quanto para editar um check: quando `edit` é um
// SiteCheck, o formulário nasce pré-preenchido e salva via updateSiteCheck; quando
// é null, salva via createSiteCheck. O parent remonta o form (via key) ao trocar de
// alvo, então os valores iniciais abaixo bastam para reidratar os campos.
function CheckForm({ edit, onSaved, onCancelEdit }: { edit: Check | null; onSaved: () => void; onCancelEdit: () => void }) {
  const toast = useToast();
  const cfg = (edit ?? {}) as unknown as CheckConfig;
  const [kind, setKind] = useState(edit?.kind ?? "http");
  const [name, setName] = useState(edit?.name ?? "");
  const [url, setUrl] = useState(edit?.url ?? "");
  const [tier, setTier] = useState(edit?.tier ?? "padrao");
  const isSitemap = kind === "sitemap";
  const [expect, setExpect] = useState(edit?.expect_status ?? 200);
  const [keyword, setKeyword] = useState(edit?.keyword ?? "");
  const [maxLatency, setMaxLatency] = useState(edit?.max_latency_ms ?? 0);
  const [group, setGroup] = useState(cfg.group_name ?? "Geral");
  const [probes, setProbes] = useState((cfg.probe_locations ?? []).join(", "));
  const [channelIds, setChannelIds] = useState<number[]>(edit?.channel_ids ?? []);
  const [channels, setChannels] = useState<NotificationChannel[]>([]);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    listChannels().then((r) => setChannels(r.channels)).catch(() => setChannels([]));
  }, []);

  const submit = async () => {
    setBusy(true);
    try {
      const probe_locations = probes.split(",").map((s) => s.trim()).filter(Boolean);
      const payload = { name, url, tier, expect_status: expect, keyword, max_latency_ms: maxLatency, group_name: group, probe_locations, kind, channel_ids: channelIds };
      if (edit) {
        await updateSiteCheck(edit.id, payload);
        toast.success(`Check "${name}" atualizado.`);
      } else {
        await createSiteCheck(payload);
        setName("");
        setUrl("");
        setKeyword("");
        setProbes("");
        toast.success(`Check "${name}" criado.`);
      }
      onSaved();
    } catch {
      toast.error(edit ? "Não foi possível atualizar o check." : "Não foi possível criar o check.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title={edit ? `Editar check, ${edit.name}` : "Novo check"}>
      <div className="grid-2">
        <FormField label="Tipo de monitoramento">
          <select className="field" value={kind} onChange={(e) => setKind(e.target.value)}>
            <option value="http">Página única (uma URL)</option>
            <option value="sitemap">Sitemap (todas as páginas do site)</option>
          </select>
        </FormField>
        <FormField label="Nome" required>
          <input className="field" value={name} onChange={(e) => setName(e.target.value)} placeholder={isSitemap ? "Portal da empresa" : "Site institucional"} />
        </FormField>
        <FormField
          label={isSitemap ? "URL do sitemap" : "URL"}
          required
          help={isSitemap ? "Endereço do sitemap.xml. O Revoada busca e monitora automaticamente todas as páginas listadas (e re-sincroniza quando o sitemap muda)." : help.fields["check.url"]}
        >
          <input
            className="field"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder={isSitemap ? "https://www.exemplo.com.br/sitemap.xml" : "https://www.exemplo.com.br"}
          />
        </FormField>
        <FormField label="Intervalo de teste" help={help.fields["check.interval"]}>
          <select className="field" value={tier} onChange={(e) => setTier(e.target.value)}>
            <option value="critico">crítico (a cada 1 min; 2 falhas seguidas confirmam, ~30 s)</option>
            <option value="padrao">padrão (a cada 5 min; 3 falhas seguidas confirmam, ~1 min)</option>
            <option value="basico">básico (a cada 10 min; 4 falhas seguidas confirmam, ~1,5 min)</option>
          </select>
        </FormField>
        <FormField label="Status HTTP esperado" help={help.fields["check.expectStatus"]}>
          <input className="field" type="number" value={expect} onChange={(e) => setExpect(Number(e.target.value))} />
        </FormField>
        <FormField label="Grupo (status page)" help={help.fields["check.group"]}>
          <input className="field" value={group} onChange={(e) => setGroup(e.target.value)} placeholder="Geral" />
        </FormField>
        <FormField label="Sondas" help={help.fields["check.probes"]} hint="Separadas por vírgula. Vazio = só a central.">
          <input className="field" value={probes} onChange={(e) => setProbes(e.target.value)} placeholder="sp-saopaulo, us-east" />
        </FormField>
      </div>

      {isSitemap && (
        <div
          style={{
            display: "flex",
            gap: "var(--sp-2)",
            padding: "var(--sp-3)",
            marginTop: "var(--sp-2)",
            background: "var(--bg-2)",
            border: "1px solid var(--border)",
            borderRadius: "var(--radius)",
            fontSize: "var(--fs-14)",
            color: "var(--text-2)",
          }}
        >
          <span aria-hidden="true"><MapIcon size={16} /></span>
          <span>
            O Revoada vai buscar este sitemap, extrair <strong>todas as URLs</strong> e monitorar cada página no intervalo
            escolhido, com métricas individuais de estado, uptime e latência na aba <strong>Visão geral</strong>. A lista
            re-sincroniza sozinha quando o sitemap muda (até 500 páginas). Você recebe <strong>um alerta agregado</strong>{" "}
            (“N/M páginas fora do ar”), não um e-mail por página.
          </span>
        </div>
      )}

      <FormField
        label="Canais de notificação"
        hint="Quem é avisado quando este site cair, degradar ou o certificado estiver perto de expirar."
      >
        {channels.length === 0 ? (
          <p style={{ color: "var(--text-3)", margin: 0 }}>
            Nenhum canal cadastrado ainda. Crie um em <strong>Notificações → Canais</strong> (e-mail, WhatsApp, Telegram
            ou webhook) e ele aparecerá aqui.
          </p>
        ) : (
          <>
            <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-1)" }}>
              {channels.map((ch) => (
                <label key={ch.id} className="row" style={{ gap: "var(--sp-2)", alignItems: "center" }}>
                  <input
                    type="checkbox"
                    checked={channelIds.includes(ch.id)}
                    onChange={(e) =>
                      setChannelIds((prev) => (e.target.checked ? [...prev, ch.id] : prev.filter((x) => x !== ch.id)))
                    }
                  />
                  <span>{ch.name}</span>
                  <Badge state="neutral">{CHANNEL_TYPE_LABEL[ch.type] ?? ch.type}</Badge>
                  {!ch.enabled && <Badge state="warn">desativado</Badge>}
                </label>
              ))}
            </div>
            {channelIds.length === 0 && (
              <p style={{ color: "var(--text-3)", marginTop: "var(--sp-2)", marginBottom: 0 }}>
                Sem canais marcados, o alerta segue as <strong>Rotas de notificação</strong> (Notificações → Rotas).
                Marque um ou mais canais acima para avisar direto este site.
              </p>
            )}
          </>
        )}
      </FormField>

      <AdvancedSection label="Opções avançadas">
        <div className="grid-2">
          <FormField label="Palavra-chave esperada" hint="Se preenchida, a resposta precisa conter este texto.">
            <input className="field" value={keyword} onChange={(e) => setKeyword(e.target.value)} placeholder="opcional" />
          </FormField>
          <FormField
            label="Latência máx. (ms)"
            hint={
              maxLatency > 0
                ? "Acima disso o check é considerado LENTO. Compara com o tempo TOTAL (cabeçalho + página inteira)."
                : "Em 0 este site só é julgado por NO AR × FORA DO AR: uma página que leva 19 s para carregar aparece saudável, porque só o tempo limite da sondagem (20 s) a derruba. Preencha para o painel acusar lentidão."
            }
          >
            <input className="field" type="number" value={maxLatency} onChange={(e) => setMaxLatency(Number(e.target.value))} placeholder="0" />
          </FormField>
        </div>
        {/* Limite de resposta: era uma constante embutida no código, invisível na
            tela e no dicionário — um site que responde em 16 s era publicado como
            "fora do ar" sem que nada dissesse que o corte era nosso. Agora o número
            aparece; torná-lo editável por check depende de uma coluna nova em
            site_checks (ver relatório da frente). */}
        <div
          style={{
            display: "flex",
            gap: "var(--sp-2)",
            padding: "var(--sp-3)",
            marginTop: "var(--sp-2)",
            background: "var(--bg-2)",
            border: "1px solid var(--border)",
            borderRadius: "var(--radius)",
            fontSize: "var(--fs-14)",
            color: "var(--text-2)",
          }}
        >
          <span>
            <strong>Limite de resposta: {Math.round(TIMEOUT_PADRAO_MS / 1000)} s.</strong> Se a página não terminar de
            responder nesse tempo, a sondagem é encerrada e o diagnóstico fica{" "}
            <em>“sem resposta em {Math.round(TIMEOUT_PADRAO_MS / 1000)} s”</em>. É uma escolha do Revoada, não uma
            medição do seu site, sites legítimos que demoram mais que isso também aparecem como fora do ar.
          </span>
        </div>
      </AdvancedSection>

      <div className="row" style={{ marginTop: "var(--sp-3)" }}>
        <Button variant="primary" onClick={submit} disabled={busy || !name || !url}>
          {edit ? "Salvar alterações" : "Criar check"}
        </Button>
        {edit && (
          <Button variant="ghost" onClick={onCancelEdit} disabled={busy}>
            Cancelar
          </Button>
        )}
      </div>
    </Card>
  );
}

// SitemapPages mostra as páginas (filhos) de um check sitemap — o "histórico" de
// um sitemap não é uma timeline de sondagens (o pai não sonda), e sim o estado
// atual de cada página descoberta, com uptime e latência.
function SitemapPages({ check, onClose }: { check: Check; onClose: () => void }) {
  const [pages, setPages] = useState<Check[] | null>(null);
  const [q, setQ] = useState("");
  const [onlyProblems, setOnlyProblems] = useState(false);

  const reload = useCallback(() => {
    siteOverview()
      .then((r) => setPages(r.checks.map(asCheck).filter((c) => c.parent_id === check.id)))
      .catch(() => setPages([]));
  }, [check.id]);
  usePolling(reload, 15000, [reload]);

  const all = pages ?? [];
  const down = all.filter((c) => c.state === "DOWN").length;
  const degraded = all.filter((c) => c.state === "DEGRADADO" || c.state === "SUSPEITO").length;
  const term = q.trim().toLowerCase();
  const rows = all
    .filter((c) => (onlyProblems ? c.state !== "UP" : true))
    .filter((c) => (term ? (c.name + " " + c.url).toLowerCase().includes(term) : true))
    .slice()
    .sort((a, b) => (a.state === "UP" ? 1 : 0) - (b.state === "UP" ? 1 : 0) || a.name.localeCompare(b.name));

  return (
    <Modal open onClose={onClose} wide title={`Páginas do sitemap, ${check.name}`}>
      <div className="row" style={{ marginBottom: "var(--sp-3)", flexWrap: "wrap" }}>
        <Badge state={STATE_BADGE[check.state] ?? "neutral"}>{check.state}</Badge>
        <span style={SUBTLE}>
          {all.length} página{all.length === 1 ? "" : "s"} · {all.length - down - degraded} no ar
          {down > 0 ? ` · ${down} fora` : ""}
          {degraded > 0 ? ` · ${degraded} degradada${degraded === 1 ? "" : "s"}` : ""}
        </span>
        <span style={{ ...SUBTLE, wordBreak: "break-all" }}>{check.url}</span>
      </div>

      <div className="row" style={{ marginBottom: "var(--sp-2)", flexWrap: "wrap" }}>
        <input
          className="field"
          style={{ maxWidth: 280 }}
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="Filtrar por nome ou URL…"
        />
        <Checkbox checked={onlyProblems} onChange={setOnlyProblems} label="Só com problema" />
      </div>

      {pages === null ? (
        <SkeletonRows />
      ) : all.length === 0 ? (
        <div style={MUTED}>Ainda descobrindo as páginas do sitemap. Aguarde o próximo ciclo.</div>
      ) : (
        <DataTable<Check>
          rows={rows}
          keyFn={(c) => c.id}
          empty={<div style={MUTED}>Nenhuma página corresponde ao filtro.</div>}
          columns={[
            { key: "state", label: "Estado", render: (c) => <Badge state={STATE_BADGE[c.state] ?? "neutral"}>{c.state}</Badge> },
            {
              key: "page",
              label: "Página",
              render: (c) => (
                <div>
                  <a href={c.url} target="_blank" rel="noreferrer" style={{ color: "var(--accent)", textDecoration: "none" }}>
                    {c.name}
                  </a>
                  <div style={{ ...SUBTLE, overflowWrap: "anywhere" }}>{c.url}</div>
                </div>
              ),
            },
            { key: "uptime_day", label: "Uptime 24h", align: "right", render: (c) => <UptimeText u={c.uptime} /> },
            { key: "uptime_month", label: "Uptime 30d", align: "right", render: (c) => <UptimeText u={c.uptime_30d} /> },
            {
              key: "latency",
              label: "Latência",
              align: "right",
              render: (c) => {
                const lat = lastLatency(c);
                return lat != null ? `${Math.round(lat)} ms` : null;
              },
            },
            {
              key: "diag",
              label: "Diagnóstico",
              render: (c) => (
                <span style={SUBTLE} title={c.last_diagnosis || undefined}>
                  {c.state === "DOWN" && c.down_since ? `fora ${timeSince(c.down_since)} · ` : ""}
                  {diagLabel(c.last_diagnosis, c.timeout_ms) || (c.state === "UP" ? "—" : "")}
                </span>
              ),
            },
          ]}
        />
      )}
    </Modal>
  );
}

// localDayKey formata uma data como "AAAA-MM-DD" no fuso do navegador (o mesmo
// que o usuário vê), para casar o seletor de dia com os horários exibidos.
function localDayKey(d: Date): string {
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const dd = String(d.getDate()).padStart(2, "0");
  return `${y}-${m}-${dd}`;
}

// dayRange converte "AAAA-MM-DD" (dia local) nos limites [00:00, 24:00) em ISO/UTC
// para o backend filtrar exatamente aquele dia como o usuário o enxerga.
function dayRange(dayKey: string): { from: string; to: string } {
  const start = new Date(`${dayKey}T00:00:00`);
  const end = new Date(start);
  end.setDate(end.getDate() + 1);
  return { from: start.toISOString(), to: end.toISOString() };
}

function CheckHistory({ check, onClose }: { check: Check; onClose: () => void }) {
  const [history, setHistory] = useState<SiteCheckResult[] | null>(null);
  const [uptime, setUptime] = useState<{ day: UptimeStat; month: UptimeStat } | null>(null);
  const [probes, setProbes] = useState<ProbeResult[]>([]);
  // "" = sondagens recentes (padrão); "AAAA-MM-DD" = um dia específico.
  const [day, setDay] = useState<string>("");
  const [minTs, setMinTs] = useState<string | null>(null);
  const today = localDayKey(new Date());

  useEffect(() => {
    setHistory(null);
    siteCheckHistory(check.id, day ? dayRange(day) : undefined)
      .then((r) => {
        // O endpoint passou a devolver `uptime`/`uptime_30d` como objetos com
        // cobertura (antes eram dois números, e 100 significava tanto "no ar o dia
        // todo" quanto "não sondamos nada").
        const h = r as unknown as { uptime?: UptimeStat; uptime_30d?: UptimeStat };
        setHistory(r.history);
        setUptime({ day: h.uptime ?? UPTIME_VAZIO, month: h.uptime_30d ?? UPTIME_VAZIO });
        if (r.min_ts) setMinTs(r.min_ts);
      })
      .catch(() => setHistory([]));
  }, [check.id, day]);

  useEffect(() => {
    siteProbes(check.url).then((r) => setProbes(r.probes)).catch(() => setProbes([]));
  }, [check.url]);

  const cert = useMemo(() => history?.find((h) => h.cert_days_left > 0), [history]);

  return (
    <Modal open onClose={onClose} wide title={`Histórico, ${check.name}`}>
      <div className="row" style={{ marginBottom: "var(--sp-3)" }}>
        <Badge state={STATE_BADGE[check.state] ?? "neutral"}>{check.state}</Badge>
        {uptime && (
          <span style={SUBTLE}>
            Uptime <UptimeText u={uptime.day} label="24h" /> · <UptimeText u={uptime.month} label="30d" />
          </span>
        )}
        <OriginText c={check} />
        {cert && (
          <span style={{ display: "inline-flex", alignItems: "center", gap: "var(--sp-1)" }}>
            <Badge state={certState(cert.cert_days_left)}>certificado: {cert.cert_days_left} dia(s)</Badge>
            <InfoTip text={help.fields["check.cert"]} title="Expiração do certificado" />
          </span>
        )}
      </div>

      {probes.length > 0 && (
        <div style={{ marginBottom: "var(--sp-3)" }}>
          <div style={{ ...SUBTLE, textTransform: "uppercase", marginBottom: 4 }}>Sondas por região</div>
          <div className="row">
            {/* O diagnóstico da sonda remota passa pelo MESMO dicionário do da sonda
                central (vocabulário unificado): antes um lado dizia "tls" e o outro
                "tls_error" na mesma tela. */}
            {probes.map((p) => (
              <span key={p.location} style={{ display: "flex", alignItems: "center", gap: 6, padding: "3px 8px", border: "1px solid var(--border)", borderRadius: 8, fontSize: "var(--fs-12)" }}>
                <Badge state={p.up ? "ok" : "crit"}>{p.location}</Badge>
                {p.up ? formatValue(p.total_ms, "duration_ms") : diagLabel(p.diagnostic, check.timeout_ms) || "falha"}
              </span>
            ))}
          </div>
        </div>
      )}

      <div style={{ display: "flex", alignItems: "center", flexWrap: "wrap", gap: "var(--sp-2)", marginBottom: "var(--sp-2)" }}>
        <span style={{ ...SUBTLE, textTransform: "uppercase" }}>Tempo de resposta por fase (ms)</span>
        <InfoTip text={timingHelp} title="Timing DNS → TLS → TTFB" />
        <span style={{ flex: 1 }} />
        <label style={{ display: "inline-flex", alignItems: "center", gap: "var(--sp-1)", ...SUBTLE }}>
          Dia
          <input
            type="date"
            className="field"
            value={day}
            max={today}
            min={minTs ? localDayKey(new Date(minTs)) : undefined}
            onChange={(e) => setDay(e.target.value)}
            style={{ width: "auto" }}
            aria-label="Filtrar histórico por dia"
          />
        </label>
        {day ? (
          <Button variant="ghost" onClick={() => setDay("")}>
            recentes
          </Button>
        ) : (
          <span style={{ ...MUTED, fontSize: "var(--fs-12)" }}>últimas sondagens</span>
        )}
      </div>
      {history === null ? (
        <SkeletonRows />
      ) : (
        <DataTable<SiteCheckResult>
          rows={history}
          keyFn={(h) => h.ts}
          empty={
            <div style={MUTED}>
              {day
                // `day` é uma data pura (YYYY-MM-DD) escolhida no seletor: reordenar a
                // string evita a conversão de fuso, que poderia exibir o dia anterior.
                ? `Sem sondagens em ${day.split("-").reverse().join("/")}. Escolha outro dia ou volte para “recentes”.`
                : "Sem histórico ainda. Aguarde o próximo ciclo do check."}
            </div>
          }
          columns={[
            {
              key: "ts",
              label: "Data e hora",
              // Mostra a data/hora exata do check (visível, não só no hover) com o tempo
              // relativo logo abaixo em tom secundário — o usuário vê "quando" de fato.
              render: (h) => {
                const t = fmtRelAbs(h.ts);
                if (!t.abs) return <span style={MUTED}>—</span>;
                return (
                  <div style={{ display: "flex", flexDirection: "column", lineHeight: 1.3 }}>
                    <span className="tabular" style={{ color: "var(--text-1)" }}>
                      {t.abs}
                    </span>
                    <span style={{ ...MUTED, fontSize: "var(--fs-12)" }}>{t.rel}</span>
                  </div>
                );
              },
            },
            { key: "ok", label: "OK", render: (h) => <Badge state={h.ok ? "ok" : "crit"}>{h.ok ? "✓" : "✗"}</Badge> },
            // Duração com UNIDADE, sempre. A tabela imprimia o número cru em
            // milissegundos: "1768" sozinho não diz se é ms, s ou outra coisa, e o
            // cabeçalho não dizia. Passa pelo formatador único do painel, o mesmo
            // que o resto da tela usa — 1768 vira "1,8 s", 114 vira "114 ms".
            { key: "dns_ms", label: "DNS", align: "right", render: (h) => formatValue(h.dns_ms, "duration_ms") },
            { key: "connect_ms", label: "Conn", align: "right", render: (h) => formatValue(h.connect_ms, "duration_ms") },
            { key: "tls_ms", label: "TLS", align: "right", render: (h) => formatValue(h.tls_ms, "duration_ms") },
            { key: "ttfb_ms", label: "TTFB", align: "right", render: (h) => formatValue(h.ttfb_ms, "duration_ms") },
            { key: "total_ms", label: "Total", align: "right", render: (h) => formatValue(h.total_ms, "duration_ms") },
            { key: "diagnosis", label: "Diagnóstico", render: (h) => <span style={SUBTLE}>{h.diagnosis || "—"}</span> },
          ]}
        />
      )}
    </Modal>
  );
}

// LatencyZoomModal — abre o gráfico de "tempo de carregamento" ampliado, com a
// mesma série do mini-gráfico da tabela (latências recentes) + resumo numérico e
// a linha de limite. Serve para o usuário ler a tendência de perto.
function LatencyZoomModal({ check, onClose }: { check: Check; onClose: () => void }) {
  const points = useMemo(() => check.sparkline ?? [], [check.sparkline]);
  const limit = check.max_latency_ms;
  const hasLimit = typeof limit === "number" && limit > 0;

  const stats = useMemo(() => {
    if (points.length === 0) return null;
    const last = points[points.length - 1];
    const min = Math.min(...points);
    const max = Math.max(...points);
    const avg = points.reduce((a, b) => a + b, 0) / points.length;
    return { last, min, max, avg };
  }, [points]);

  // Geometria do gráfico (viewBox fixo; o SVG escala com a largura do modal).
  const W = 720;
  const H = 260;
  const padL = 48;
  const padR = 16;
  const padT = 16;
  const padB = 28;
  const innerW = W - padL - padR;
  const innerH = H - padT - padB;
  const yMax = Math.max(...points, hasLimit ? (limit as number) : 0, 1) * 1.1;
  const stepX = points.length > 1 ? innerW / (points.length - 1) : 0;
  const px = (i: number) => padL + i * stepX;
  const py = (v: number) => padT + innerH - (v / yMax) * innerH;
  const linePath = points.map((p, i) => `${i === 0 ? "M" : "L"}${px(i).toFixed(1)},${py(p).toFixed(1)}`).join(" ");
  const areaPath = points.length > 1 ? `${linePath} L${px(points.length - 1).toFixed(1)},${(padT + innerH).toFixed(1)} L${px(0).toFixed(1)},${(padT + innerH).toFixed(1)} Z` : "";
  const over = hasLimit && stats != null && stats.last > (limit as number);
  const gridVals = [0, yMax / 2, yMax];

  return (
    <Modal open onClose={onClose} wide title={`Tempo de carregamento, ${check.name}`}>
      <div style={{ ...SUBTLE, marginBottom: "var(--sp-3)" }}>
        {check.url} · {points.length} amostra(s) recente(s){hasLimit ? ` · limite ${Math.round(limit as number)} ms` : ""}
      </div>

      {stats == null ? (
        <div style={MUTED}>Sem dados suficientes para o gráfico. Aguarde o próximo ciclo do check.</div>
      ) : (
        <>
          <div style={{ display: "flex", flexWrap: "wrap", gap: "var(--sp-2)", marginBottom: "var(--sp-3)" }}>
            <ZoomStat label="Última" value={stats.last} accent={over ? "var(--crit)" : "var(--accent)"} />
            <ZoomStat label="Mínima" value={stats.min} />
            <ZoomStat label="Média" value={stats.avg} />
            <ZoomStat label="Máxima" value={stats.max} />
            {hasLimit && <ZoomStat label="Limite" value={limit as number} accent="var(--warn)" />}
          </div>

          <svg viewBox={`0 0 ${W} ${H}`} width="100%" style={{ height: "auto", display: "block" }} role="img" aria-label={`Gráfico de tempo de carregamento de ${check.name}`}>
            <defs>
              <linearGradient id="latfill" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor="var(--accent)" stopOpacity="0.28" />
                <stop offset="100%" stopColor="var(--accent)" stopOpacity="0" />
              </linearGradient>
            </defs>
            {/* grade horizontal + rótulos do eixo Y (ms) */}
            {gridVals.map((v, i) => (
              <g key={i}>
                <line x1={padL} y1={py(v)} x2={W - padR} y2={py(v)} stroke="var(--border)" strokeWidth="1" />
                <text x={padL - 8} y={py(v) + 4} textAnchor="end" fontSize="11" fill="var(--text-2)">
                  {Math.round(v)}
                </text>
              </g>
            ))}
            {/* linha de limite */}
            {hasLimit && (limit as number) <= yMax && (
              <line x1={padL} y1={py(limit as number)} x2={W - padR} y2={py(limit as number)} stroke="var(--warn)" strokeWidth="1.5" strokeDasharray="5 3" opacity="0.8" />
            )}
            {areaPath && <path d={areaPath} fill="url(#latfill)" />}
            <path d={linePath} fill="none" stroke={over ? "var(--crit)" : "var(--accent)"} strokeWidth="2" strokeLinejoin="round" />
            {/* ponto final destacado */}
            <circle cx={px(points.length - 1)} cy={py(stats.last)} r="4" fill={over ? "var(--crit)" : "var(--accent)"} />
            <text x={padL} y={H - 8} fontSize="11" fill="var(--text-2)">
              mais antigo
            </text>
            <text x={W - padR} y={H - 8} textAnchor="end" fontSize="11" fill="var(--text-2)">
              mais recente
            </text>
          </svg>
          <div style={{ ...MUTED, fontSize: "var(--fs-12)", marginTop: "var(--sp-2)" }}>
            Valores em milissegundos (ms). A linha tracejada é o limite configurado para o check. Para o histórico
            detalhado por fase (DNS/TLS/TTFB), use o botão “histórico”.
          </div>
        </>
      )}
    </Modal>
  );
}

// ZoomStat — cartãozinho de resumo (rótulo + valor em ms) do gráfico ampliado.
function ZoomStat({ label, value, accent }: { label: string; value: number; accent?: string }) {
  return (
    <div style={{ minWidth: 84, padding: "6px 10px", border: "1px solid var(--border)", borderRadius: 8, background: "var(--bg-1)" }}>
      <div style={{ ...MUTED, fontSize: "var(--fs-12)", textTransform: "uppercase" }}>{label}</div>
      <div className="tabular" style={{ fontSize: "var(--fs-16)", color: accent ?? "var(--text-1)" }}>
        {Math.round(value)} <span style={{ ...MUTED, fontSize: "var(--fs-12)" }}>ms</span>
      </div>
    </div>
  );
}

// =========================================================================
// JORNADAS
// =========================================================================

// Ações possíveis para um passo — mapeadas direto para o campo `method` do
// JourneyStep (o modelo é uma requisição HTTP por passo, com asserts opcionais).
const STEP_ACTIONS: { value: string; label: string; body: boolean }[] = [
  { value: "GET", label: "Abrir URL (GET)", body: false },
  { value: "POST", label: "Enviar formulário (POST)", body: true },
  { value: "PUT", label: "Atualizar (PUT)", body: true },
  { value: "DELETE", label: "Remover (DELETE)", body: false },
  { value: "HEAD", label: "Verificar cabeçalho (HEAD)", body: false },
];

interface FormPair {
  k: string;
  v: string;
}
// StepForm é a forma editável de um JourneyStep (form vira lista ordenável de pares,
// asserts viram string para caber nos inputs). Converte para JourneyStep ao salvar.
interface StepForm {
  name: string;
  method: string;
  url: string;
  form: FormPair[];
  assertStatus: string;
  assertContains: string;
}

function emptyStep(): StepForm {
  return { name: "", method: "GET", url: "", form: [], assertStatus: "", assertContains: "" };
}

function toJourneyStep(s: StepForm): JourneyStep {
  const step: JourneyStep = { name: s.name, method: s.method, url: s.url };
  const entries = s.form.filter((p) => p.k.trim());
  if (entries.length) step.form = Object.fromEntries(entries.map((p) => [p.k.trim(), p.v]));
  const st = Number(s.assertStatus);
  if (s.assertStatus.trim() && st > 0) step.assert_status = st;
  if (s.assertContains.trim()) step.assert_contains = s.assertContains;
  return step;
}

function fromJourneyStep(s: JourneyStep): StepForm {
  return {
    name: s.name ?? "",
    method: (s.method || "GET").toUpperCase(),
    url: s.url ?? "",
    form: s.form ? Object.entries(s.form).map(([k, v]) => ({ k, v: String(v) })) : [],
    assertStatus: s.assert_status ? String(s.assert_status) : "",
    assertContains: s.assert_contains ?? "",
  };
}

function stepsToJSON(steps: StepForm[]): string {
  return JSON.stringify(steps.map(toJourneyStep), null, 2);
}

const EXAMPLE_STEPS: StepForm[] = [
  { name: "abrir form", method: "GET", url: "https://lp.exemplo.com.br/contato", form: [], assertStatus: "", assertContains: "Fale conosco" },
  { name: "enviar", method: "POST", url: "https://lp.exemplo.com.br/enviar", form: [{ k: "email", v: "teste@exemplo.com.br" }], assertStatus: "", assertContains: "Recebemos" },
];

function JourneysPanel({ canEdit }: { canEdit: boolean }) {
  const toast = useToast();
  const [journeys, setJourneys] = useState<Journey[] | null>(null);
  const [confirmDel, setConfirmDel] = useState<Journey | null>(null);

  const reload = useCallback(() => {
    listJourneys().then((r) => setJourneys(r.journeys)).catch(() => setJourneys([]));
  }, []);
  usePolling(reload, 10000, [reload]);

  const doDelete = async () => {
    if (!confirmDel) return;
    const j = confirmDel;
    setConfirmDel(null);
    try {
      await deleteJourney(j.id);
      toast.success(`Jornada "${j.name}" apagada.`);
      reload();
    } catch {
      toast.error("Não foi possível apagar a jornada.");
    }
  };

  return (
    <div className="stack">
      <Card title={journeys ? `${journeys.length} jornada(s)` : "Jornadas"}>
        {journeys === null ? (
          <SkeletonRows />
        ) : (
          <DataTable<Journey>
            rows={journeys}
            keyFn={(j) => j.id}
            empty={
              <EmptyState
                icon={<Compass size={32} strokeWidth={1.5} />}
                title="Nenhuma jornada ainda"
                body="Uma jornada simula um usuário passo a passo (abrir uma página, enviar um formulário, conferir se o texto certo apareceu). Pega formulário quebrado e fluxo com falha que um check HTTP simples não detecta."
                steps={
                  canEdit
                    ? ["Dê um nome e um intervalo à jornada.", "Adicione passos e escolha a ação de cada um.", "Salve, a jornada roda sozinha no intervalo definido."]
                    : undefined
                }
              />
            }
            columns={[
              { key: "state", label: "Estado", render: (j) => <Badge state={STATE_BADGE[j.state] ?? "neutral"}>{j.state}</Badge> },
              { key: "name", label: "Nome" },
              { key: "steps", label: "Passos", render: (j) => <span className="num">{j.steps.length}</span> },
              { key: "interval", label: "Intervalo", render: (j) => <span className="num">{j.interval_seconds}s</span> },
              { key: "diag", label: "Diagnóstico", hideOnMobile: true, render: (j) => <span style={SUBTLE}>{j.last_diagnosis || "—"}</span> },
            ]}
            rowActions={
              canEdit
                ? (j) => (
                    <IconButton
                      icon={ActionIcons.delete}
                      label="Apagar"
                      className="btn--icon-danger"
                      onClick={() => setConfirmDel(j)}
                    />
                  )
                : undefined
            }
          />
        )}
      </Card>

      {canEdit && <JourneyBuilder onCreated={reload} />}

      <ConfirmDialog
        open={confirmDel !== null}
        onCancel={() => setConfirmDel(null)}
        onConfirm={doDelete}
        verb="Apagar"
        target={confirmDel ? confirmDel.name : "jornada"}
        consequences="A jornada para de rodar e é removida. Esta ação não pode ser desfeita."
        danger
      />
    </div>
  );
}

function JourneyBuilder({ onCreated }: { onCreated: () => void }) {
  const toast = useToast();
  const [name, setName] = useState("");
  const [intervalSec, setIntervalSec] = useState(300);
  const [steps, setSteps] = useState<StepForm[]>(EXAMPLE_STEPS);
  const [busy, setBusy] = useState(false);

  const setStep = (i: number, patch: Partial<StepForm>) =>
    setSteps((prev) => prev.map((s, idx) => (idx === i ? { ...s, ...patch } : s)));
  const addStep = () => setSteps((prev) => [...prev, emptyStep()]);
  const removeStep = (i: number) => setSteps((prev) => prev.filter((_, idx) => idx !== i));
  const move = (i: number, dir: -1 | 1) =>
    setSteps((prev) => {
      const j = i + dir;
      if (j < 0 || j >= prev.length) return prev;
      const next = [...prev];
      [next[i], next[j]] = [next[j], next[i]];
      return next;
    });

  const submit = async () => {
    const built = steps.map(toJourneyStep).filter((s) => s.url.trim());
    if (built.length === 0) {
      toast.error("Adicione ao menos um passo com URL.");
      return;
    }
    setBusy(true);
    try {
      await createJourney({ name, interval_seconds: intervalSec, steps: built });
      toast.success(`Jornada "${name}" criada.`);
      setName("");
      onCreated();
    } catch {
      toast.error("Não foi possível criar a jornada.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="Nova jornada">
      <div className="grid-2">
        <FormField label="Nome" required help={help.fields["journey.name"]}>
          <input className="field" value={name} onChange={(e) => setName(e.target.value)} placeholder="Contato LP" />
        </FormField>
        <FormField label="Intervalo (segundos)" help={help.fields["journey.interval"]}>
          <input className="field" type="number" min={30} value={intervalSec} onChange={(e) => setIntervalSec(Number(e.target.value))} />
        </FormField>
      </div>

      <div style={{ display: "flex", alignItems: "center", gap: "var(--sp-1)", margin: "var(--sp-3) 0 var(--sp-2)" }}>
        <strong style={{ fontSize: "var(--fs-14)" }}>Passos</strong>
        <InfoTip text={help.fields["journey.steps"]} title="Passos da jornada" />
      </div>

      <div className="stack">
        {steps.map((s, i) => {
          const action = STEP_ACTIONS.find((a) => a.value === s.method) ?? STEP_ACTIONS[0];
          return (
            <div key={i} style={{ border: "1px solid var(--border)", borderRadius: "var(--radius)", padding: "var(--sp-3)" }}>
              <div className="toolbar" style={{ marginBottom: "var(--sp-2)" }}>
                <strong style={{ fontSize: "var(--fs-14)" }}>Passo {i + 1}</strong>
                <div className="row">
                  <Button variant="ghost" onClick={() => move(i, -1)} disabled={i === 0} aria-label="Subir passo">
                    ↑
                  </Button>
                  <Button variant="ghost" onClick={() => move(i, 1)} disabled={i === steps.length - 1} aria-label="Descer passo">
                    ↓
                  </Button>
                  <Button variant="ghost" onClick={() => removeStep(i)} aria-label="Remover passo">
                    ✕
                  </Button>
                </div>
              </div>

              <div className="grid-2">
                <FormField label="Ação" help={help.fields["journey.step.action"]}>
                  <select className="field" value={s.method} onChange={(e) => setStep(i, { method: e.target.value })}>
                    {STEP_ACTIONS.map((a) => (
                      <option key={a.value} value={a.value}>
                        {a.label}
                      </option>
                    ))}
                  </select>
                </FormField>
                <FormField label="Nome do passo" hint="Rótulo para identificar o passo no diagnóstico.">
                  <input className="field" value={s.name} onChange={(e) => setStep(i, { name: e.target.value })} placeholder="abrir form" />
                </FormField>
                <FormField label="URL" required help={help.fields["check.url"]}>
                  <input className="field" value={s.url} onChange={(e) => setStep(i, { url: e.target.value })} placeholder="https://lp.exemplo.com.br/contato" />
                </FormField>
                <FormField label="Status esperado" hint="Opcional. Código HTTP que a resposta deve ter (ex.: 200).">
                  <input className="field" type="number" value={s.assertStatus} onChange={(e) => setStep(i, { assertStatus: e.target.value })} placeholder="opcional" />
                </FormField>
                <FormField label="Esperar texto" hint="Opcional. A resposta precisa conter este texto.">
                  <input className="field" value={s.assertContains} onChange={(e) => setStep(i, { assertContains: e.target.value })} placeholder="ex.: Recebemos sua mensagem" />
                </FormField>
              </div>

              {action.body && <FormFieldPairs pairs={s.form} onChange={(form) => setStep(i, { form })} />}
            </div>
          );
        })}
      </div>

      <div className="row" style={{ marginTop: "var(--sp-2)" }}>
        <Button onClick={addStep}>+ Adicionar passo</Button>
      </div>

      <JsonEditor steps={steps} onApply={setSteps} />

      <div className="row" style={{ marginTop: "var(--sp-3)" }}>
        <Button variant="primary" onClick={submit} disabled={busy || !name}>
          Criar jornada
        </Button>
      </div>
    </Card>
  );
}

// Editor dos campos de formulário (pares chave/valor) de um passo POST/PUT.
function FormFieldPairs({ pairs, onChange }: { pairs: FormPair[]; onChange: (p: FormPair[]) => void }) {
  const set = (i: number, patch: Partial<FormPair>) => onChange(pairs.map((p, idx) => (idx === i ? { ...p, ...patch } : p)));
  return (
    <div style={{ marginTop: "var(--sp-2)" }}>
      <div style={{ ...SUBTLE, marginBottom: "var(--sp-1)" }}>Campos do formulário enviados</div>
      <div className="stack">
        {pairs.map((p, i) => (
          <div key={i} className="row">
            <input className="field" style={{ maxWidth: 200 }} value={p.k} onChange={(e) => set(i, { k: e.target.value })} placeholder="campo (ex.: email)" />
            <input className="field" style={{ maxWidth: 260 }} value={p.v} onChange={(e) => set(i, { v: e.target.value })} placeholder="valor" />
            <Button variant="ghost" onClick={() => onChange(pairs.filter((_, idx) => idx !== i))} aria-label="Remover campo">
              ✕
            </Button>
          </div>
        ))}
      </div>
      <Button variant="ghost" onClick={() => onChange([...pairs, { k: "", v: "" }])} style={{ marginTop: "var(--sp-1)" }}>
        + campo
      </Button>
    </div>
  );
}

// Modo avançado: edita os passos como JSON cru, mantendo o antigo fluxo. Faz
// round-trip com o formulário — o form sincroniza o texto; ao aplicar, o texto
// volta para o formulário.
function JsonEditor({ steps, onApply }: { steps: StepForm[]; onApply: (s: StepForm[]) => void }) {
  const [raw, setRaw] = useState(() => stepsToJSON(steps));
  const [err, setErr] = useState<string | null>(null);
  const fromJson = useRef(false);

  // Form → JSON: quando os passos mudam pelo formulário, reflete no texto (a menos
  // que a própria mudança tenha vindo do JSON, para não atropelar o que se digita).
  useEffect(() => {
    if (fromJson.current) {
      fromJson.current = false;
      return;
    }
    setRaw(stepsToJSON(steps));
    setErr(null);
  }, [steps]);

  const apply = () => {
    try {
      const parsed: unknown = JSON.parse(raw);
      if (!Array.isArray(parsed) || parsed.length === 0) throw new Error("deve ser um array não-vazio de passos");
      fromJson.current = true;
      onApply((parsed as JourneyStep[]).map(fromJourneyStep));
      setErr(null);
    } catch (e) {
      setErr("JSON inválido: " + String(e));
    }
  };

  return (
    <AdvancedSection label="Editar como JSON">
      <p style={{ ...SUBTLE, marginTop: 0 }}>
        Para usuários avançados: edite ou cole os passos como JSON e clique em “Aplicar ao formulário”. O texto reflete o formulário automaticamente.
      </p>
      <textarea className="field" style={{ ...MONO, minHeight: 160 }} value={raw} onChange={(e) => setRaw(e.target.value)} />
      <div className="row" style={{ marginTop: "var(--sp-2)" }}>
        <Button onClick={apply}>Aplicar ao formulário</Button>
        {err && <span style={{ color: "var(--crit)", fontSize: "var(--fs-12)" }}>{err}</span>}
      </div>
    </AdvancedSection>
  );
}

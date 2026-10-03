// Início — a primeira tela, e a única que muita gente abre no dia.
//
// A ordem da página é a ordem das perguntas de quem chega:
//   1. "Está tudo de pé?"        → faixa de indicadores, antes de qualquer lista.
//   2. "Se não, o que quebrou?"  → o que precisa de atenção agora.
//   3. "Como está cada servidor?"→ a grade, com quem está em ordem recolhido.
//   4. "O que falta configurar?" → checklist, recolhido, só para admin.
//
// Antes, os números moravam ABAIXO da grade de servidores: numa frota de algumas
// dezenas de máquinas, "quantos alertas eu tenho agora" ficava a três rolagens de
// distância no celular. Passo 1 agora é literalmente a primeira coisa da página.
//
// Tudo aqui é calculado a partir do que o ambiente de fato reportou. Não existe
// número de exemplo, host de exemplo nem texto que finja saber algo sobre o
// ambiente — quando o dado não existe, a tela diz que não existe.
import { useCallback, useState, type ReactNode } from "react";
import { CheckCircle2, Circle } from "lucide-react";
import {
  Card,
  PageHeader,
  HelpPanel,
  Skeleton,
  Badge,
  Collapsible,
  FreshnessIndicator,
  type State,
} from "../components";
import { Kpi, KpiGrid } from "../components/Kpi";
import { Veredito } from "../components/Veredito";
import { usePolling } from "../hooks/usePolling";
import { useConexao } from "../hooks/useConexao";
import {
  listHosts,
  listAlerts,
  listSiteChecks,
  listDashboards,
  listChannels,
  listAlertRules,
  healthWall,
  isAdmin,
  type HostDetail,
  type AlertEvent,
  type SiteCheck,
  type HealthCard,
  type HealthMetric,
} from "../api";
import { help } from "../help";
import { SEV_LABEL, sevRank, fmtRelAbs, formatBytes, formatValue } from "../format";
import { useHostNames } from "../hooks/useHostNames";
import { healthFreshness } from "../metrics/lastPoint";
import {
  piorRecurso,
  quantosMedindo,
  recursosNoLimite,
  resumoFrota,
  vereditoDaFrota,
  type PiorRecurso,
  type Recurso,
} from "./homeResumo";

/** Quantos itens de "atenção agora" ficam à vista antes do bloco recolhível. */
const ATENCAO_VISIVEL = 5;

/**
 * Teto de servidores COM PROBLEMA desenhados de cara. Recolher problema parece
 * errado, mas quarenta máquinas sem sinal ao mesmo tempo quase nunca são
 * quarenta problemas: são um só (o gateway caiu, a rede caiu, o link caiu). Como
 * a lista já vem pior-primeiro, o teto preserva os críticos e recolhe a cauda —
 * e o resumo do bloco recolhido diz quantos e de que tipo ficaram lá dentro.
 */
const SERVIDORES_VISIVEIS = 12;

// Constrói as seções do HelpPanel a partir de uma entrada de help.pages.
function homeHelpSections() {
  const p = help.pages.home;
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

function isUp(state: string): boolean {
  return state.toUpperCase() === "UP";
}
function isDown(state: string): boolean {
  return state.toUpperCase() === "DOWN";
}

// Mapeia uma severidade crua (critical/warning/info/…) para o estado do Badge.
function sevToState(sev: string): State {
  switch (sev?.toLowerCase()) {
    case "critical":
    case "crit":
      return "crit";
    case "warning":
    case "warn":
      return "warn";
    case "info":
      return "info";
    default:
      return "neutral";
  }
}

// --- Resumo denso de servidores ---------------------------------------------
// Só problemas coloram (warn/crit); estado normal fica calmo (cor do texto).
const SRV_METRIC_COLOR: Record<HealthMetric["state"], string> = {
  ok: "--text-2",
  warn: "--warn",
  crit: "--crit",
  nodata: "--text-3",
};

// Valor "seco" por recurso: CPU em %, RAM/Disco em bytes usados (cai no % se o
// snapshot não trouxe bytes).
function srvMetricValue(kind: "cpu" | "mem" | "disk", m: HealthMetric): string {
  // Não medida é "—", não 0%: o zero pintava de verde um recurso que ninguém viu.
  // "Velha" também é "—": o frescor é POR RECURSO, e um coletor pode calar enquanto
  // os outros seguem — o host continua "no ar" e este número repetiria o último valor
  // com a cor do limiar. Mesmo critério dos painéis (~3× o passo real da série).
  if (healthFreshness(m).state !== "fresh") return "—";
  if (kind !== "cpu" && m.used_bytes != null && m.total_bytes != null && m.total_bytes > 0) {
    return formatBytes(m.used_bytes);
  }
  // Mesmo formatador do resto do painel: Math.round transformava 99,5% em "100%",
  // afirmando saturação total num recurso que ainda tinha folga.
  return formatValue(m.pct, "percent");
}

// Detalhe no tooltip: usado / total · %.
function srvMetricTitle(m: HealthMetric): string | undefined {
  const f = healthFreshness(m);
  if (f.state === "nodata") return "Não medido nesta janela";
  if (f.state === "stale") {
    const idade = m.ts != null ? fmtRelAbs(m.ts * 1000).rel : "";
    return `Este coletor parou de reportar${idade ? `, último valor ${idade} (${formatValue(m.pct, "percent")})` : ""}`;
  }
  if (m.used_bytes != null && m.total_bytes != null && m.total_bytes > 0) {
    return `${formatBytes(m.used_bytes)} / ${formatBytes(m.total_bytes)} · ${formatValue(m.pct, "percent")}`;
  }
  return undefined;
}

// srvMetricColor: recurso velho perde a cor do semáforo (cinza de "não sei"), senão
// um pico crítico já passado seguiria vermelho no tile.
function srvMetricColor(m: HealthMetric): string {
  return `var(${SRV_METRIC_COLOR[healthFreshness(m).state === "fresh" ? m.state : "nodata"]})`;
}

// Tile compacto de um servidor: status online/offline + nome, e a linha
// CPU/RAM/Disco. Clica para abrir a Visão do Servidor. Offline (sem sinal) fica
// esmaecido e sem métricas (nada de valor velho parecendo atual).
function SrvTile({ card, label }: { card: HealthCard; label: string }) {
  const online = card.up && card.state !== "nosignal";
  return (
    <a
      className={`srv-tile${online ? "" : " srv-tile--off"}`}
      href={`#/hosts/${encodeURIComponent(card.host)}`}
      title={card.host}
    >
      <div className="srv-tile__head">
        <span className={`srv-tile__dot srv-tile__dot--${online ? "on" : "off"}`} aria-hidden />
        <span className="srv-tile__status">{online ? "Ativo" : "Inativo"}</span>
        <span className="srv-tile__name">{label}</span>
      </div>
      {online ? (
        <div className="srv-tile__metrics tabular">
          <span style={{ color: srvMetricColor(card.cpu) }} title={srvMetricTitle(card.cpu)}>
            CPU {srvMetricValue("cpu", card.cpu)}
          </span>
          <span style={{ color: srvMetricColor(card.mem) }} title={srvMetricTitle(card.mem)}>
            RAM {srvMetricValue("mem", card.mem)}
          </span>
          <span style={{ color: srvMetricColor(card.disk) }} title={srvMetricTitle(card.disk)}>
            Disco {srvMetricValue("disk", card.disk)}
          </span>
        </div>
      ) : (
        <div className="srv-tile__metrics srv-tile__metrics--off">sem dados recentes</div>
      )}
    </a>
  );
}

// --- Indicador de recurso da faixa do topo -----------------------------------
// Mostra o PIOR servidor daquele recurso agora. Por que o pior e não a média: a
// média de uma frota esconde exatamente o caso que importa — quatro máquinas
// ociosas e uma com o disco em 98% dão uma média confortável de 30%.
const RECURSO_ROTULO: Record<Recurso, string> = { cpu: "Maior CPU", mem: "Maior RAM", disk: "Maior disco" };
const RECURSO_NOME: Record<Recurso, string> = { cpu: "CPU", mem: "RAM", disk: "Disco" };

function KpiRecurso({
  recurso,
  cards,
  loading,
  hostLabel,
}: {
  recurso: Recurso;
  cards: HealthCard[] | null;
  loading: boolean;
  hostLabel: (h: string) => string;
}) {
  const pior: PiorRecurso | null = piorRecurso(cards, recurso);
  const medindo = quantosMedindo(cards, recurso);

  if (!loading && pior === null) {
    return (
      <Kpi
        rotulo={RECURSO_ROTULO[recurso]}
        valor="—"
        sub="nenhuma medição recente"
        title="Nenhum servidor reportou este recurso dentro da janela de validade. Um número aqui seria invenção."
      />
    );
  }

  const detalhe =
    pior && pior.usedBytes != null && pior.totalBytes != null && pior.totalBytes > 0
      ? `${formatBytes(pior.usedBytes)} de ${formatBytes(pior.totalBytes)} em ${hostLabel(pior.host)} · pior entre ${medindo} servidor${medindo === 1 ? "" : "es"} medindo agora`
      : pior
        ? `${formatValue(pior.pct, "percent")} em ${hostLabel(pior.host)} · pior entre ${medindo} servidor${medindo === 1 ? "" : "es"} medindo agora`
        : undefined;

  return (
    <Kpi
      rotulo={RECURSO_ROTULO[recurso]}
      valor={pior ? formatValue(pior.pct, "percent") : "—"}
      sub={pior ? hostLabel(pior.host) : undefined}
      tone={pior && pior.state !== "ok" ? pior.state : undefined}
      href={pior ? `#/hosts/${encodeURIComponent(pior.host)}` : undefined}
      title={detalhe}
      loading={loading}
    />
  );
}

export function Home() {
  const { hostLabel } = useHostNames();
  const conexao = useConexao();
  const [helpOpen, setHelpOpen] = useState(false);
  const [loading, setLoading] = useState(true);

  const [hosts, setHosts] = useState<HostDetail[] | null>(null);
  const [servers, setServers] = useState<HealthCard[] | null>(null);
  const [alerts, setAlerts] = useState<AlertEvent[] | null>(null);
  const [sites, setSites] = useState<SiteCheck[] | null>(null);
  const [hasDashboard, setHasDashboard] = useState<boolean | null>(null);
  const [hasChannel, setHasChannel] = useState<boolean | null>(null);
  const [hasRule, setHasRule] = useState<boolean | null>(null);
  // Rastreia, por fonte crítica, se o ÚLTIMO fetch falhou. Sem isto, uma falha de
  // rede vira "Tudo tranquilo ✓" (dado desconhecido apresentado como saúde).
  const [errs, setErrs] = useState<{ alerts: boolean; sites: boolean; hosts: boolean }>({
    alerts: false,
    sites: false,
    hosts: false,
  });

  const load = useCallback(() => {
    // Falhas desta rodada, coletadas localmente e publicadas de uma vez ao final.
    const failed = { alerts: false, sites: false, hosts: false };
    // Cada fetch é tolerante a falha; se for fonte crítica, marca a falha.
    async function safe<T>(p: Promise<T>, set: (v: T) => void, key?: keyof typeof failed) {
      try {
        set(await p);
      } catch {
        if (key) failed[key] = true;
      }
    }
    Promise.allSettled([
      safe(listHosts(), (r) => setHosts(r.hosts), "hosts"),
      safe(healthWall(), (r) => setServers(r.cards)),
      safe(listAlerts(true), (r) => setAlerts(r.alerts), "alerts"),
      safe(listSiteChecks(), (r) => setSites(r.checks), "sites"),
      safe(listDashboards(), (r) => setHasDashboard(r.dashboards.length > 0)),
      // Canais são admin-only; usuário comum nem consulta (o checklist é só do admin).
      safe(isAdmin() ? listChannels() : Promise.resolve({ channels: [] }), (r) => setHasChannel(r.channels.length > 0)),
      safe(listAlertRules(), (r) => setHasRule(r.rules.length > 0)),
    ]).finally(() => {
      setErrs(failed);
      setLoading(false);
    });
  }, []);
  // UX-24: atualização periódica (~30s), respeitando visibilidade da aba.
  usePolling(load, 30000, [load]);

  const frota = resumoFrota(servers, hostLabel);

  const hostsUp = hosts ? hosts.filter((h) => h.up).length : 0;
  const sitesUp = sites ? sites.filter((s) => isUp(s.state)).length : 0;
  const activeAlerts = alerts ?? [];
  const downSites = sites ? sites.filter((s) => isDown(s.state)) : [];

  // Alguma fonte crítica falhou no último fetch? Então NÃO podemos afirmar "tranquilo".
  const failedSources = [
    errs.alerts ? "alertas" : "",
    errs.sites ? "sites" : "",
    errs.hosts ? "hosts" : "",
  ].filter(Boolean);
  const criticalFailed = failedSources.length > 0;

  // Recursos no limite pelo MESMO semáforo do Mural: a Início não pode dizer
  // "tudo em ordem" com um servidor pintado de vermelho lá.
  const noLimite = recursosNoLimite(servers);
  const hostsCrit = new Set(noLimite.filter((r) => r.state === "crit").map((r) => r.host));
  const hostsWarn = new Set(noLimite.filter((r) => !hostsCrit.has(r.host)).map((r) => r.host));

  // "Atenção agora": alertas ativos + sites fora do ar, ordenados por severidade
  // (pior primeiro) e cada item com Badge de severidade.
  const attention: {
    key: string;
    href: string;
    logsHref?: string;
    text: string;
    sub: string;
    severity: string;
  }[] = [
    ...activeAlerts.map((a) => {
      const host = a.labels.host || a.labels.instance || "";
      const container = a.labels.container || "";
      // Onde disparou: servidor (nome amigável) › container.
      const where = [host ? hostLabel(host) : "", container].filter(Boolean).join(" › ");
      const since = fmtRelAbs(a.started_at).rel;
      return {
        key: `a${a.id}`,
        href: "#/alerts",
        // Link para os logs do servidor/container que disparou (só quando há host).
        logsHref: host
          ? `#/logs?host=${encodeURIComponent(host)}${container ? `&container=${encodeURIComponent(container)}` : ""}`
          : undefined,
        text: a.rule_name || "Alerta",
        sub: [where, `desde ${since}`, a.flapping ? "oscilando" : ""].filter(Boolean).join(" · "),
        severity: a.severity,
      };
    }),
    ...noLimite.map((r) => ({
      key: `r${r.host}:${r.recurso}`,
      href: `#/hosts/${encodeURIComponent(r.host)}`,
      logsHref: `#/logs?host=${encodeURIComponent(r.host)}`,
      text: `${RECURSO_NOME[r.recurso]} em ${formatValue(r.pct, "percent")}`,
      sub: `${hostLabel(r.host)} · ${r.state === "crit" ? "acima do limite crítico" : "acima do limite de atenção"}`,
      severity: r.state === "crit" ? "critical" : "warning",
    })),
    ...downSites.map((s) => ({
      key: `s${s.id}`,
      href: "#/websites",
      // Site fora do ar é sempre crítico.
      text: s.name,
      sub: `Site fora do ar${s.last_diagnosis ? ` · ${s.last_diagnosis}` : ""}`,
      severity: "critical",
    })),
  ].sort((a, b) => sevRank(b.severity) - sevRank(a.severity));

  const atencaoVisivel = attention.slice(0, ATENCAO_VISIVEL);
  const atencaoResto = attention.slice(ATENCAO_VISIVEL);

  // Checklist de primeiros passos.
  const steps: { done: boolean; label: string; hint: ReactNode }[] = [
    {
      done: !!hosts && hosts.length > 0,
      label: "Instalar o primeiro agente",
      hint: (
        <>
          Nenhum host reportando ainda. <a href="#/hosts">Veja como instalar o agente</a>.
        </>
      ),
    },
    {
      done: hasDashboard === true,
      label: "Criar o primeiro dashboard",
      hint: (
        <>
          Monte painéis das suas métricas. <a href="#/dashboards">Ir para Dashboards</a>.
        </>
      ),
    },
    {
      done: hasChannel === true,
      label: "Cadastrar um canal de notificação",
      hint: (
        <>
          Defina para onde os alertas são enviados. <a href="#/notify">Ir para Notificações</a>.
        </>
      ),
    },
    {
      done: hasRule === true,
      label: "Criar a primeira regra de alerta",
      hint: (
        <>
          Vigie uma métrica e seja avisado. <a href="#/alerts">Ir para Alertas</a>.
        </>
      ),
    },
    {
      done: !!sites && sites.length > 0,
      label: "Monitorar um site",
      hint: (
        <>
          Acompanhe se seus sites estão no ar. <a href="#/websites">Ir para Websites</a>.
        </>
      ),
    },
  ];
  const feitos = steps.filter((s) => s.done).length;
  const allKnown = !loading;
  const allDone = allKnown && steps.every((s) => s.done);

  // A grade "em ordem" abre sozinha quando não há problema nenhum — sem nada
  // para investigar, o que a pessoa quer ver é a frota. Havendo problema, ela
  // nasce fechada para não competir com o que precisa de olho.
  const semProblemas = frota.comProblema.length === 0;
  const problemasVisiveis = frota.comProblema.slice(0, SERVIDORES_VISIVEIS);
  const problemasResto = frota.comProblema.slice(SERVIDORES_VISIVEIS);

  return (
    <div className="page">
      <PageHeader
        title="Início"
        actions={
          !loading ? (
            <FreshnessIndicator
              status={criticalFailed || conexao === "reconectando" ? "reconnecting" : "live"}
            />
          ) : undefined
        }
        onHelp={() => setHelpOpen(true)}
      />

      {/* O veredito: a resposta a "está tudo bem?" antes de qualquer número. Só
          aparece com os dados carregados — um "Tudo em ordem" durante o
          carregamento seria uma afirmação sem medição. */}
      {!loading && (
        <Veredito
          veredito={vereditoDaFrota({
            hostsTotal: hosts?.length ?? 0,
            hostsUp,
            alertasCrit: activeAlerts.filter((a) => sevRank(a.severity) >= 3).length,
            alertasOutros: activeAlerts.filter((a) => sevRank(a.severity) < 3).length,
            sitesTotal: sites?.length ?? 0,
            sitesUp,
            sitesFora: downSites.length,
            fontesFalhas: failedSources,
            hostsCrit: hostsCrit.size,
            hostsWarn: hostsWarn.size,
          })}
        />
      )}

      {/* Faixa de indicadores: a primeira coisa da página, em toda largura de tela.
          Três contagens (o que está de pé) e três recursos (o que está apertado). */}
      <KpiGrid>
        <Kpi
          rotulo="Servidores no ar"
          valor={hosts ? `${hostsUp}/${hosts.length}` : "—"}
          sub={hosts ? "reportando agora" : undefined}
          tone={hosts && hosts.length > 0 && hostsUp < hosts.length ? "warn" : undefined}
          href="#/hosts"
          loading={loading}
        />
        <Kpi
          rotulo="Alertas ativos"
          valor={alerts ? activeAlerts.length : "—"}
          sub="em disparo agora"
          tone={alerts && activeAlerts.length > 0 ? "crit" : undefined}
          href="#/alerts"
          loading={loading}
        />
        <Kpi
          rotulo="Sites no ar"
          valor={sites ? `${sitesUp}/${sites.length}` : "—"}
          sub={sites ? "respondendo" : undefined}
          tone={sites && sites.length - sitesUp > 0 ? "crit" : undefined}
          href="#/websites"
          loading={loading}
        />
        <KpiRecurso recurso="cpu" cards={servers} loading={loading} hostLabel={hostLabel} />
        <KpiRecurso recurso="mem" cards={servers} loading={loading} hostLabel={hostLabel} />
        <KpiRecurso recurso="disk" cards={servers} loading={loading} hostLabel={hostLabel} />
      </KpiGrid>

      <div className="stack">
        {/* Ordem dos blocos: os SERVIDORES vêm antes do "precisa de atenção".
            A frota é o que se olha todo dia, inclusive (e principalmente) quando
            não há problema nenhum — e, quando há, ele já está anunciado em
            vermelho na faixa de indicadores logo acima. */}
        <Card
          title={
            servers
              ? `Servidores, ${frota.noAr}/${frota.total} no ar`
              : "Servidores"
          }
        >
          {loading && servers === null ? (
            <Skeleton height={72} />
          ) : frota.total === 0 ? (
            <p className="attention-empty">Nenhum servidor reportando ainda.</p>
          ) : (
            <>
              {problemasVisiveis.length > 0 && (
                <div className="home-servers">
                  {problemasVisiveis.map((c) => (
                    <SrvTile key={c.host} card={c} label={hostLabel(c.host)} />
                  ))}
                </div>
              )}
              {problemasResto.length > 0 && (
                <Collapsible
                  titulo={`Mais ${problemasResto.length} precisando de olho`}
                  variante="discreto"
                  resumo={resumirEstados(problemasResto)}
                >
                  <div className="home-servers">
                    {problemasResto.map((c) => (
                      <SrvTile key={c.host} card={c} label={hostLabel(c.host)} />
                    ))}
                  </div>
                </Collapsible>
              )}
              {frota.emOrdem.length > 0 && (
                <Collapsible
                  titulo="Servidores em ordem"
                  contador={frota.emOrdem.length}
                  variante={frota.comProblema.length > 0 ? "discreto" : "secao"}
                  defaultOpen={semProblemas}
                  resumo="nenhum recurso acima do limiar"
                >
                  <div className="home-servers">
                    {frota.emOrdem.map((c) => (
                      <SrvTile key={c.host} card={c} label={hostLabel(c.host)} />
                    ))}
                  </div>
                </Collapsible>
              )}
            </>
          )}
        </Card>

        <Card title="Precisa de atenção agora">
          {loading ? (
            <Skeleton height={48} />
          ) : criticalFailed ? (
            // Dado desconhecido NUNCA pode parecer saúde: em vez de "tranquilo",
            // avisamos que não deu para verificar (em cor de atenção).
            <p className="attention-empty" style={{ color: "var(--warn)" }} role="status">
              Não foi possível verificar o estado (falha ao carregar: {failedSources.join("/")})
            </p>
          ) : attention.length === 0 ? (
            <p className="attention-empty">Tudo tranquilo ✓</p>
          ) : (
            <>
              <div className="attention-list">
                {atencaoVisivel.map((it) => (
                  <AttentionItem key={it.key} it={it} />
                ))}
              </div>
              {atencaoResto.length > 0 && (
                // Mais de cinco problemas simultâneos é quase sempre uma causa só
                // (o gateway caiu, a rede caiu). A lista inteira empurrava a grade
                // de servidores para fora da tela; ela continua aqui, a um clique.
                <Collapsible
                  titulo={`Ver os outros ${atencaoResto.length}`}
                  variante="discreto"
                  resumo={resumirSeveridades(atencaoResto)}
                >
                  <div className="attention-list">
                    {atencaoResto.map((it) => (
                      <AttentionItem key={it.key} it={it} />
                    ))}
                  </div>
                </Collapsible>
              )}
            </>
          )}
        </Card>

        {/* Checklist de configuração: todas as ações são de admin (instalar agente,
            canais, regras…) — para usuário comum seria uma lista eternamente pendente.
            Recolhido: é tarefa de montagem, não de operação do dia. */}
        {isAdmin() &&
          !allDone &&
          (loading ? (
            <Skeleton height={44} />
          ) : (
            <Collapsible
              titulo="Primeiros passos"
              contador={steps.length - feitos}
              resumo={`${feitos} de ${steps.length} concluídos`}
              persistKey="home-primeiros-passos"
            >
              <ul className="checklist">
                {steps.map((s, i) => (
                  <li key={i} className="checklist__item">
                    <span
                      className={`checklist__mark checklist__mark--${s.done ? "done" : "todo"}`}
                      aria-hidden="true"
                    >
                      {s.done ? <CheckCircle2 size={18} /> : <Circle size={18} />}
                    </span>
                    <span className="checklist__body">
                      <span className={`checklist__label${s.done ? " checklist__label--done" : ""}`}>
                        {s.label}
                      </span>
                      {!s.done && <div className="checklist__hint">{s.hint}</div>}
                    </span>
                  </li>
                ))}
              </ul>
            </Collapsible>
          ))}
      </div>

      <HelpPanel
        open={helpOpen}
        onClose={() => setHelpOpen(false)}
        title={help.pages.home.title}
        sections={homeHelpSections()}
      />
    </div>
  );
}

interface ItemAtencao {
  key: string;
  href: string;
  logsHref?: string;
  text: string;
  sub: string;
  severity: string;
}

function AttentionItem({ it }: { it: ItemAtencao }) {
  return (
    <div className="attention-item">
      <Badge state={sevToState(it.severity)}>
        {SEV_LABEL[it.severity.toLowerCase()] ?? it.severity.toUpperCase()}
      </Badge>
      <a className="attention-item__text" href={it.href}>
        {it.text}
        <span className="attention-item__sub">, {it.sub}</span>
      </a>
      {it.logsHref && (
        <a className="attention-item__logs" href={it.logsHref}>
          logs →
        </a>
      )}
    </div>
  );
}

// Linha de contexto do bloco recolhido: recolher não pode virar esconder, então o
// resumo diz quantos de cada gravidade ficaram lá dentro.
function resumirSeveridades(itens: ItemAtencao[]): string {
  let crit = 0;
  let warn = 0;
  for (const i of itens) {
    const s = sevToState(i.severity);
    if (s === "crit") crit++;
    else if (s === "warn") warn++;
  }
  const partes = [
    crit > 0 ? `${crit} crítico${crit === 1 ? "" : "s"}` : "",
    warn > 0 ? `${warn} de atenção` : "",
  ].filter(Boolean);
  return partes.length > 0 ? partes.join(" · ") : `${itens.length} itens`;
}

// Mesma ideia para a cauda da grade de servidores: o bloco fechado precisa dizer
// o que ficou lá dentro, senão recolher vira esconder.
function resumirEstados(cards: HealthCard[]): string {
  const conta: Record<string, number> = {};
  for (const c of cards) conta[c.state] = (conta[c.state] ?? 0) + 1;
  const rotulo: Record<string, (n: number) => string> = {
    crit: (n) => `${n} crítico${n === 1 ? "" : "s"}`,
    nosignal: (n) => `${n} sem sinal`,
    warn: (n) => `${n} em atenção`,
  };
  return (["crit", "nosignal", "warn"] as const)
    .filter((k) => conta[k] > 0)
    .map((k) => rotulo[k](conta[k]))
    .join(" · ");
}

// Infraestrutura (UX.12): hosts em cards com semáforo (up/down por last_seen) e
// métricas do inventário, linha do tempo por host em modal, aba "Serviços
// descobertos" (lista crua de /api/discovery) e estado vazio que ensina a instalar
// o agente. Preserva o banner de auto-discovery (starter packs) das duas abas.
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { LayoutGrid, Rows3, Search, Server } from "lucide-react";
import {
  Badge,
  Button,
  DataTable,
  EmptyState,
  HelpPanel,
  LastSeen,
  Modal,
  PageHeader,
  Skeleton,
  Tabs,
  useToast,
  IconButton,
  ActionIcons,
  type Column,
  type State,
} from "../components";
import {
  getRole,
  hostMetrics,
  hostTimeline,
  listDiscovery,
  listHosts,
  updateHost,
  listProvisionTargets,
  logsStorage,
  setLogsStorageLimit,
  purgeLogsPreview,
  purgeLogs,
  purgeLogsStatus,
  purgeLogsStuck,
  purgeLogsCancel,
  deleteHost,
  type DeleteHostResult,
  type StuckMutation,
  type HealthMetric,
  type HostDetail,
  type HostMetrics,
  type HostService,
  type LogStorage,
  type NotPurgedItem,
  type ProvisionTarget,
  type PurgeFilter,
} from "../api";
import { invalidateHostNames } from "../hooks/useHostNames";
import { usePolling } from "../hooks/usePolling";
import { help } from "../help";
import { fmtRelAbs, formatBytes, formatValue, mensagemDeErro } from "../format";
import { healthFreshness, type HealthTile } from "../metrics/lastPoint";
import { InstalarServidorModal } from "./instalar/InstalarServidorModal";
import { UpdateAgentModal } from "./AddServerModal";
import { ContainersBlock } from "./containers";
import { DiscoveryTab, ServicosChips } from "./ServicosDescobertos";
import { agruparServicosPorHost, type ServicoDescrito } from "./servicos";

type ToastApi = ReturnType<typeof useToast>;
type TabId = "hosts" | "discovery";

// Comando de instalação do agente (README §instalação). O host aparece na lista
// assim que o agente envia as primeiras métricas.
const INSTALL_CMD = `curl -fsSL ${window.location.origin}/install.sh | sh -s -- --key <CHAVE> --gateway ${window.location.origin} --agent-url ${window.location.origin}/revoada-agent`;

// Seções do HelpPanel a partir de help.pages.hosts (padrão das telas).
function hostsHelpSections(): { heading: string; body: ReactNode }[] {
  const p = help.pages.hosts;
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

const MUTED_12 = { fontSize: "var(--fs-12)", color: "var(--text-3)" } as const;

// Cor (token de design) do semáforo de um recurso — semântica só para estado.
const METRIC_COLOR: Record<HealthMetric["state"], string> = {
  ok: "--ok",
  warn: "--warn",
  crit: "--crit",
  nodata: "--text-3", // cinza de "não sei", nunca a cor de um estado saudável
};

// Taxa de rede legível: reaproveita formatBytes e acrescenta "/s". Ex.: "1,2 MB/s".
// Taxa AUSENTE (undefined) = não foi possível calcular a partir do contador: um ponto
// só na janela, ou contador reiniciado. Travessão, nunca "0 B/s" — zero afirmaria que
// não trafegou nada num host que está trafegando.
function formatRate(bps: number | undefined): string {
  return bps == null ? "—" : `${formatBytes(bps)}/s`;
}

// ---- Versão do agente na frota --------------------------------------------------
//
// Não existe auto-atualização do agente: a frota fica misturada até alguém atualizar
// servidor por servidor. Isso importa para a LEITURA do painel, não só para o
// inventário — versões diferentes podem medir CPU de formas diferentes, e a tela
// pinta todas com a mesma cor e a mesma autoridade. Até aqui a versão só aparecia um
// host por vez, no rodapé do cartão; auditar a frota exigia abrir cartão por cartão.

// versionParts quebra "1.4.10-rc2" em [1, 4, 10] — só os números, que é o que ordena.
function versionParts(v: string): number[] {
  return (v.match(/\d+/g) ?? []).map(Number);
}

// compareVersions ordena versões numericamente por segmento (para "1.10" > "1.9",
// que a comparação de texto erraria). Versão vazia é sempre a MENOR: um host que não
// informa a versão é o caso mais suspeito, e ele fica junto dos desatualizados.
function compareVersions(a: string, b: string): number {
  if (!a && !b) return 0;
  if (!a) return -1;
  if (!b) return 1;
  const pa = versionParts(a);
  const pb = versionParts(b);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const d = (pa[i] ?? 0) - (pb[i] ?? 0);
    if (d !== 0) return d;
  }
  return a.localeCompare(b); // mesmos números: desempata pelo sufixo (rc, beta…)
}

// newestAgentVersion é a versão mais nova VISTA NA FROTA — não a última publicada.
// O painel não sabe qual é a última release; sabe o que os agentes reportam. Chamar
// isso de "a mais nova que existe" seria inventar, então o rótulo na tela também diz
// "na frota".
function newestAgentVersion(hosts: HostDetail[]): string {
  let best = "";
  for (const h of hosts) {
    const v = (h.agent_version ?? "").trim();
    if (v && compareVersions(v, best) > 0) best = v;
  }
  return best;
}

// AgentVersionCell mostra a versão e destaca quem está atrás da mais nova da frota.
// Usa os tokens de cor existentes (--warn para "precisa de atenção", --text-3 para
// "não sei"), o mesmo vocabulário visual de MetricCell.
function AgentVersionCell({ version, newest }: { version: string; newest: string }) {
  const v = (version ?? "").trim();
  if (!v) {
    return (
      <span style={{ color: "var(--text-3)" }} title="Este servidor não informou a versão do agente">
—
      </span>
    );
  }
  const atrasado = newest !== "" && compareVersions(v, newest) < 0;
  return (
    <span
      className="tabular"
      style={{ color: atrasado ? "var(--warn)" : "var(--text-2)", fontWeight: atrasado ? 600 : 400 }}
      title={
        atrasado
          ? `Agente desatualizado: ${v}. A versão mais nova vista na frota é ${newest}.`
          : `Agente ${v}, a mais nova vista na frota.`
      }
    >
      {v}
    </span>
  );
}

// RAM/Disco em bytes usados/totais quando vieram; senão cai no %. O backend pode
// emitir 0 (não null) sem snapshot, por isso exigimos total_bytes > 0.
function bytesOrPct(m: { pct: number; used_bytes?: number; total_bytes?: number }): string {
  if (m.used_bytes != null && m.total_bytes != null && m.total_bytes > 0) {
    return `${formatBytes(m.used_bytes)} / ${formatBytes(m.total_bytes)}`;
  }
  // formatValue (1 casa) para bater com a coluna % da tabela densa logo acima —
  // antes o mesmo host aparecia como "100%" no cartão e "99,5%" na tabela.
  return formatValue(m.pct, "percent");
}

// MeterRow: rótulo + valor legível (cor do semáforo) + barrinha proporcional ao pct.
// Estilo inline equivalente ao do Mural de Saúde, mas local (não importa de HealthWall).
function MeterRow({
  label,
  value,
  pct,
  state,
}: {
  label: string;
  value: string;
  pct: number;
  state: HealthMetric["state"];
}) {
  const color = `var(${METRIC_COLOR[state]})`;
  const w = Math.max(0, Math.min(100, pct));
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
      <div style={{ display: "flex", justifyContent: "space-between", gap: "var(--sp-2)" }}>
        <span style={{ ...MUTED_12, wordBreak: "break-all" }}>{label}</span>
        <span
          className="tabular"
          style={{ fontSize: "var(--fs-12)", color, fontWeight: 600, whiteSpace: "nowrap" }}
        >
          {value}
        </span>
      </div>
      <span
        aria-hidden
        style={{
          display: "block",
          height: 6,
          borderRadius: 999,
          background: "var(--bg-3)",
          overflow: "hidden",
        }}
      >
        <span style={{ display: "block", height: "100%", width: `${w}%`, background: color }} />
      </span>
    </div>
  );
}

// UX-32: cor do Badge da linha do tempo por tipo de evento (coerente com a semântica).
// Quedas/alertas → crítico; recuperações/volta ao ar → ok; deploy → neutro; resto → info.
const HOST_KIND_STATE: Record<string, State> = {
  alert: "crit",
  queda: "crit",
  down: "crit",
  offline: "crit",
  recovery: "ok",
  recuperacao: "ok",
  recuperação: "ok",
  up: "ok",
  online: "ok",
  provision: "ok", // servidor provisionado — agente instalado (evento positivo)
  deploy: "neutral",
  update: "info", // agente atualizado
  boot: "info",
  restart: "info",
  reboot: "info",
};
const hostKindState = (kind: string): State => HOST_KIND_STATE[kind?.toLowerCase()] ?? "info";

// Rótulo humano (pt-BR) do tipo de evento no Badge da linha do tempo. Kinds
// desconhecidos caem no próprio texto do kind (fallback), sem quebrar a UI.
const HOST_KIND_LABEL: Record<string, string> = {
  provision: "provisionado",
  update: "atualizado",
  deploy: "deploy",
  alert: "alerta",
  recovery: "recuperação",
  boot: "boot",
  restart: "reinício",
  reboot: "reinício",
};
const hostKindLabel = (kind: string): string => HOST_KIND_LABEL[kind?.toLowerCase()] ?? kind;

// Casa um host do inventário com um alvo provisionado por SSH (Fase G), para
// oferecer "Atualizar agente" e recuperar a credencial guardada no "Apagar servidor".
function matchTarget(host: HostDetail, targets: ProvisionTarget[]): ProvisionTarget | null {
  const hn = host.hostname;
  const dn = host.display_name?.trim();
  // 1º: casamento CORRETO (Fase G) — o alvo agora carrega o hostname REAL da
  // máquina, que bate 1:1 com host.hostname do inventário.
  const byHostname = targets.find((t) => t.hostname && t.hostname === hn);
  if (byHostname) return byHostname;
  // 2º: fallback legado para alvos antigos sem hostname (ainda não reprovisionados),
  // que só têm nome-amigável/IP — casamento aproximado por nome/apelido/host.
  return (
    targets.find((t) => t.name === hn || (dn && t.name === dn) || t.host === hn) ?? null
  );
}

export function Hosts() {
  const toast = useToast();
  const [tab, setTab] = useState<TabId>("hosts");
  const [helpOpen, setHelpOpen] = useState(false);
  const [addOpen, setAddOpen] = useState(false);
  const [reloadKey, setReloadKey] = useState(0);
  const isAdmin = getRole() === "admin";

  return (
    <div className="page">
      <PageHeader
        title="Infraestrutura"
        subtitle="Seus hosts e os serviços descobertos neles"
        onHelp={() => setHelpOpen(true)}
        actions={
          isAdmin ? (
            <Button variant="primary" onClick={() => setAddOpen(true)}>
              Adicionar servidor
            </Button>
          ) : undefined
        }
      />
      {isAdmin && (
        <InstalarServidorModal
          open={addOpen}
          onClose={() => setAddOpen(false)}
          onProvisioned={() => setReloadKey((k) => k + 1)}
        />
      )}

      <Tabs
        tabs={[
          { id: "hosts", label: "Hosts" },
          { id: "discovery", label: "Serviços descobertos" },
        ]}
        active={tab}
        onChange={(id) => setTab(id as TabId)}
      />

      <div style={{ marginTop: "var(--sp-4)" }}>
        {tab === "hosts" ? (
          <HostsTab toast={toast} reloadKey={reloadKey} onAdicionar={isAdmin ? () => setAddOpen(true) : undefined} />
        ) : (
          <DiscoveryTab />
        )}
      </div>

      <HelpPanel
        open={helpOpen}
        onClose={() => setHelpOpen(false)}
        title={help.pages.hosts.title}
        sections={hostsHelpSections()}
      />
    </div>
  );
}

// ---- Aba Hosts: busca + cards com semáforo + timeline em modal ----
function HostsTab({
  toast,
  reloadKey,
  onAdicionar,
}: {
  toast: ToastApi;
  reloadKey: number;
  onAdicionar?: () => void;
}) {
  const [hosts, setHosts] = useState<HostDetail[] | null>(null);
  const [search, setSearch] = useState("");
  // Host aberto no modal (objeto inteiro: precisamos do apelido para o título e do
  // hostname técnico para as consultas de timeline/armazenamento).
  const [selected, setSelected] = useState<HostDetail | null>(null);
  const [editing, setEditing] = useState<HostDetail | null>(null);
  const [deleting, setDeleting] = useState<HostDetail | null>(null);
  // Alvos provisionados por SSH (Fase G) — só admin; habilita "Atualizar agente".
  const [targets, setTargets] = useState<ProvisionTarget[]>([]);
  const [updating, setUpdating] = useState<ProvisionTarget | null>(null);
  // Métricas ao vivo por hostname (merge com o inventário pela chave técnica).
  const [metricsByHost, setMetricsByHost] = useState<Map<string, HostMetrics>>(new Map());
  // Serviços descobertos (Apache, PHP-FPM, bancos, Docker) por hostname, para a
  // linha expandida da tabela e o card. Muda a cada 5 min no agente: carrega junto
  // com o inventário e quando a frota muda (reloadKey), sem polling próprio.
  const [servicos, setServicos] = useState<HostService[]>([]);
  // Visão da listagem: tabela densa (novo padrão) ou cards. Persistida por sessão.
  const [view, setView] = useState<"table" | "cards">(() =>
    (localStorage.getItem("hosts.view") as "table" | "cards") || "table",
  );
  const setViewPersist = (v: "table" | "cards") => {
    setView(v);
    localStorage.setItem("hosts.view", v);
  };
  const canEdit = getRole() === "admin";
  const isAdmin = getRole() === "admin";

  const reload = useCallback(
    (term: string) => {
      listHosts(term)
        .then((r) => setHosts(r.hosts))
        .catch(() => {
          setHosts([]);
          toast.error("Não foi possível carregar os hosts.");
        });
    },
    [toast],
  );

  useEffect(() => {
    const t = setTimeout(() => reload(search), 200);
    return () => clearTimeout(t);
  }, [search, reload]);

  // Reforça o inventário (traz o `last_seen` de "Último contato") a cada 15s. Sem
  // isto, CPU/RAM/Estado ficavam ao vivo (loadMetrics já pollava) mas "Último
  // contato" congelava no valor da carga inicial da página — a idade relativa só
  // crescia, dando a falsa impressão de agente parado mesmo com dado fresco chegando.
  const searchForPoll = useRef(search);
  searchForPoll.current = search;
  const reloadForPoll = useRef(reload);
  reloadForPoll.current = reload;
  usePolling(() => reloadForPoll.current(searchForPoll.current), 15000, []);

  // Carrega os alvos provisionados (admin) no início e quando algo muda a frota.
  const loadTargets = useCallback(() => {
    if (!isAdmin) return;
    listProvisionTargets()
      .then((r) => setTargets(r.targets))
      .catch(() => setTargets([]));
  }, [isAdmin]);
  // Recarrega hosts + alvos quando um provisionamento termina (reloadKey muda).
  // `search`/`reload` de propósito fora das deps: a busca já tem seu próprio efeito
  // com debounce; aqui só reagimos ao reloadKey/loadTargets.
  const reloadRef = useRef(reload);
  reloadRef.current = reload;
  const searchRef = useRef(search);
  searchRef.current = search;
  useEffect(() => {
    loadTargets();
    reloadRef.current(searchRef.current);
    let vivo = true;
    listDiscovery()
      .then((r) => vivo && setServicos(r.services ?? []))
      .catch(() => {
        /* sem descoberta a linha só não expande; o inventário segue normal */
      });
    return () => {
      vivo = false;
    };
  }, [reloadKey, loadTargets]);
  // Agrupado UMA vez por carga: a tabela chama servicosDe para cada linha a cada
  // render (poll de 15 s), e refiltrar a lista inteira por linha seria trabalho à toa.
  const servicosPorHost = useMemo(
    () => new Map(agruparServicosPorHost(servicos).map((g) => [g.hostname, g.servicos])),
    [servicos],
  );
  const servicosDe = useCallback((hostname: string) => servicosPorHost.get(hostname) ?? [], [servicosPorHost]);

  // Polling das métricas do servidor inteiro (~5s, pausado com a aba oculta).
  // Em falha mantém o último snapshot — o frescor real vem do campo `up` de cada host.
  const loadMetrics = useCallback(() => {
    hostMetrics()
      .then((r) => setMetricsByHost(new Map(r.hosts.map((m) => [m.host, m]))))
      .catch(() => {
        /* mantém o último snapshot; não inventa zeros */
      });
  }, []);
  // 15 s = a cadência REAL da coleta do agente. A 5 s, duas de cada três chamadas
  // reliam exatamente os mesmos pontos — e não sai barato: /api/hosts/metrics dispara
  // três consultas ao ClickHouse (gauges, rede e containers), ~93 ms somados, sendo a
  // de containers a mais cara (38,5 ms med, 7,14 MiB lidos, porque `labels['container']`
  // no WHERE obriga a ler o Map da janela inteira). A 5 s isso é ~1,9% de um núcleo POR
  // ABA ABERTA, multiplicado por operador; a 15 s cai a um terço sem perder um ponto.
  // É a mesma cadência que o inventário logo acima já usa.
  usePolling(loadMetrics, 15000, [loadMetrics]);

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-4)" }}>
      <div style={{ display: "flex", gap: "var(--sp-2)", flexWrap: "wrap", alignItems: "center" }}>
        <input
          className="field"
          placeholder="Buscar por host, SO ou kernel…"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          aria-label="Buscar host"
          style={{ flex: "1 1 240px", maxWidth: 400 }}
        />
        <div className="row" role="group" aria-label="Modo de exibição" style={{ gap: "var(--sp-1)" }}>
          <IconButton
            icon={Rows3}
            label="Ver em tabela"
            variant={view === "table" ? "primary" : "ghost"}
            aria-pressed={view === "table"}
            onClick={() => setViewPersist("table")}
          />
          <IconButton
            icon={LayoutGrid}
            label="Ver em cards"
            variant={view === "cards" ? "primary" : "ghost"}
            aria-pressed={view === "cards"}
            onClick={() => setViewPersist("cards")}
          />
        </div>
      </div>

      {hosts === null ? (
        <div className="grid-3">
          {Array.from({ length: 6 }).map((_, i) => (
            <div className="card" key={i}>
              <Skeleton height={18} width="60%" />
              <div style={{ marginTop: "var(--sp-3)" }}>
                <Skeleton height={44} />
              </div>
            </div>
          ))}
        </div>
      ) : hosts.length === 0 ? (
        search.trim() ? (
          <EmptyState
            icon={<Search size={32} strokeWidth={1.5} />}
            title="Nenhum host corresponde à busca"
            body={`Nada encontrado para "${search.trim()}". Ajuste o termo ou limpe a busca.`}
          />
        ) : (
          <EmptyState
            icon={<Server size={32} strokeWidth={1.5} />}
            title="Nenhum host reportando ainda"
            body={<InstallBlock toast={toast} onAdicionar={onAdicionar} />}
            steps={[
              "Instale o agente no host com o comando acima.",
              "Aguarde o agente enviar as primeiras métricas.",
              "O host aparece automaticamente aqui.",
            ]}
          />
        )
      ) : view === "table" ? (
        <HostsTable
          hosts={hosts}
          metricsByHost={metricsByHost}
          servicosDe={servicosDe}
          canEdit={canEdit}
          isAdmin={isAdmin}
          targets={targets}
          onEdit={(h) => setEditing(h)}
          onUpdateAgent={(t) => setUpdating(t)}
          onDelete={(h) => setDeleting(h)}
        />
      ) : (
        <div className="wall-grid">
          {/* UX-11: hosts que precisam de atenção (sem sinal) primeiro; desempate por nome. */}
          {[...hosts]
            .sort(
              (a, b) =>
                Number(a.up) - Number(b.up) ||
                (a.display_name?.trim() || a.hostname).localeCompare(b.display_name?.trim() || b.hostname),
            )
            .map((h) => (
            <HostCard
              key={h.hostname}
              host={h}
              metrics={metricsByHost.get(h.hostname) ?? null}
              servicos={servicosDe(h.hostname)}
              canEdit={canEdit}
              target={isAdmin ? matchTarget(h, targets) : null}
              onOpen={() => setSelected(h)}
              onEdit={() => setEditing(h)}
              onUpdateAgent={(t) => setUpdating(t)}
              onDelete={isAdmin ? () => setDeleting(h) : undefined}
            />
          ))}
        </div>
      )}

      {updating && (
        <UpdateAgentModal
          target={updating}
          onClose={() => setUpdating(null)}
          onUpdated={() => {
            loadTargets();
            reload(search);
          }}
        />
      )}

      <Modal
        open={selected !== null}
        onClose={() => setSelected(null)}
        title={selected ? (selected.display_name?.trim() || selected.hostname) : undefined}
      >
        {selected && <HostModalBody host={selected} isAdmin={isAdmin} toast={toast} />}
      </Modal>

      {deleting && (
        <RemoveServerModal
          host={deleting}
          toast={toast}
          onClose={() => setDeleting(null)}
          onDeleted={() => {
            setDeleting(null);
            invalidateHostNames();
            loadTargets();
            reload(search);
          }}
        />
      )}

      {editing && (
        <RenameHostModal
          host={editing}
          toast={toast}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            invalidateHostNames();
            reload(search);
          }}
        />
      )}
    </div>
  );
}

// Pior disco do host (maior uso) — o número que importa para saber se vai encher.
function worstDisk(m: HostMetrics): HealthMetric | null {
  if (!m.disks.length) return null;
  return m.disks.reduce((worst, d) => (d.pct > worst.pct ? d : worst), m.disks[0]);
}

// Célula de medidor (CPU/RAM/Disco) na tabela densa: valor em % com a cor do semáforo.
//
// O frescor é POR LADRILHO, não por host: um coletor pode calar enquanto os outros
// seguem reportando (o de CPU deixa de emitir quando não consegue medir), e nesse caso
// o host continua "no ar" — mas repetir aqui o último valor, com a cor do limiar e sem
// dizer de quando é, faz um pico crítico já passado seguir vermelho na tela. Mesmo
// critério dos painéis (~3× o passo real da série, piso de 2 min).
function MetricCell({ metric }: { metric: HealthMetric | null }) {
  const f = healthFreshness(metric);
  if (!metric || f.state === "nodata") {
    return (
      <span style={{ color: "var(--text-3)" }} title="Não medido nesta janela">
—
      </span>
    );
  }
  if (f.state === "stale") {
    const idade = metric.ts != null ? fmtRelAbs(metric.ts * 1000) : null;
    return (
      <span
        className="tabular"
        style={{ color: "var(--text-3)" }}
        title={
          idade
            ? `Último valor medido ${idade.rel} (${formatValue(metric.pct, "percent")}), ${idade.abs}. Este coletor parou de reportar.`
            : "Este coletor parou de reportar."
        }
      >
, <span style={{ fontSize: "var(--fs-12)" }}>{idade ? idade.rel : "sem dado recente"}</span>
      </span>
    );
  }
  return (
    <span className="tabular" style={{ color: `var(${METRIC_COLOR[metric.state]})`, fontWeight: 600 }}>
      {formatValue(metric.pct, "percent")}
    </span>
  );
}

// ---- Listagem densa (novo padrão): DataTable v2 com métricas ao vivo por host ----
function HostsTable({
  hosts,
  metricsByHost,
  servicosDe,
  canEdit,
  isAdmin,
  targets,
  onEdit,
  onUpdateAgent,
  onDelete,
}: {
  hosts: HostDetail[];
  metricsByHost: Map<string, HostMetrics>;
  servicosDe: (hostname: string) => ServicoDescrito[];
  canEdit: boolean;
  isAdmin: boolean;
  targets: ProvisionTarget[];
  onEdit: (h: HostDetail) => void;
  onUpdateAgent: (t: ProvisionTarget) => void;
  onDelete: (h: HostDetail) => void;
}) {
  // FONTE ÚNICA de "este número ainda vale": `h.up` — o mesmo sinal que pinta o selo
  // "no ar / sem sinal" da linha.
  //
  // Havia dois relógios de frescor brigando: `h.up` (inventário: visto nos últimos
  // 2 min) e `m.up` (/api/host-metrics: dado nos últimos 5 min). Entre 2 e 5 minutos
  // depois do agente cair, a mesma linha dizia "sem sinal" no selo e "73,4%" na
  // coluna de CPU, lado a lado. Agora o selo manda: host sem sinal não mostra número.
  const liveOf = (h: HostDetail): HostMetrics | null => {
    if (!h.up) return null;
    const m = metricsByHost.get(h.hostname);
    return m && m.up ? m : null;
  };
  const lastMs = (h: HostDetail): number => {
    const t = new Date(h.last_seen).getTime();
    return Number.isFinite(t) ? t : 0;
  };
  // Referência da coluna "Agente": a maior versão vista entre os hosts LISTADOS.
  const newestAgent = newestAgentVersion(hosts);

  const columns: Column<HostDetail>[] = [
    {
      key: "state",
      label: "Estado",
      sortable: true,
      sortValue: (h) => (h.up ? 1 : 0),
      render: (h) => <Badge state={h.up ? "ok" : "crit"}>{h.up ? "no ar" : "sem sinal"}</Badge>,
    },
    {
      key: "name",
      label: "Nome",
      sortable: true,
      sortValue: (h) => (h.display_name?.trim() || h.hostname).toLowerCase(),
      render: (h) => {
        const label = h.display_name?.trim() || h.hostname;
        const hasAlias = label !== h.hostname;
        return (
          <div style={{ minWidth: 0 }}>
            <a
              href={`#/hosts/${encodeURIComponent(h.hostname)}`}
              title={`Abrir a visão de ${label}`}
              style={{ color: "var(--accent, var(--text-1))", textDecoration: "none", fontWeight: 600 }}
            >
              {label}
            </a>
            {hasAlias && <div style={{ ...MUTED_12, wordBreak: "break-all" }}>{h.hostname}</div>}
          </div>
        );
      },
    },
    { key: "os", label: "SO", sortable: true, hideOnMobile: true, render: (h) => h.os || "—" },
    {
      key: "cpu",
      label: "CPU",
      align: "right",
      sortable: true,
      sortValue: (h) => liveOf(h)?.cpu.pct ?? -1,
      render: (h) => <MetricCell metric={liveOf(h)?.cpu ?? null} />,
    },
    {
      key: "mem",
      label: "RAM",
      align: "right",
      sortable: true,
      sortValue: (h) => liveOf(h)?.mem.pct ?? -1,
      render: (h) => <MetricCell metric={liveOf(h)?.mem ?? null} />,
    },
    {
      key: "disk",
      label: "Disco",
      align: "right",
      sortable: true,
      sortValue: (h) => {
        const m = liveOf(h);
        return m ? worstDisk(m)?.pct ?? -1 : -1;
      },
      render: (h) => {
        const m = liveOf(h);
        return <MetricCell metric={m ? worstDisk(m) : null} />;
      },
    },
    {
      key: "net",
      label: "Rede ↓/↑",
      align: "right",
      hideOnMobile: true,
      render: (h) => {
        const m = liveOf(h);
        if (!m) return <span style={{ color: "var(--text-3)" }}>—</span>;
        // Taxa é DERIVADA de dois pontos do contador: com um ponto só na janela, ou
        // com o contador reiniciado por reboot, ela não existe — e o backend agora
        // omite o campo em vez de mandar 0. Travessão, nunca "0 B/s": zero afirmaria
        // "não trafegou nada" sobre um host que está trafegando.
        const semTaxa = m.net.rx_bps == null || m.net.tx_bps == null;
        return (
          <span
            className="tabular"
            style={{ whiteSpace: "nowrap", color: semTaxa ? "var(--text-3)" : undefined }}
            title={
              semTaxa
                ? "Download / upload, travessão: taxa não calculável (um único ponto do contador na janela ou contador reiniciado)"
                : "Download / upload"
            }
          >
            ↓ {formatRate(m.net.rx_bps)} · ↑ {formatRate(m.net.tx_bps)}
          </span>
        );
      },
    },
    {
      key: "uptime",
      label: "Uptime",
      align: "right",
      hideOnMobile: true,
      sortable: true,
      sortValue: (h) => liveOf(h)?.uptime_secs ?? h.uptime_secs ?? 0,
      render: (h) => formatUptime(liveOf(h)?.uptime_secs ?? h.uptime_secs),
    },
    {
      key: "agent",
      label: "Agente",
      align: "right",
      sortable: true,
      hideOnMobile: true,
      // Ordena por versão de verdade (numérica por segmento), não por texto. Ordenar
      // crescente traz os desatualizados — e os que nem informam versão — para o topo,
      // que é exatamente a pergunta que esta coluna existe para responder.
      sortValue: (h) => {
        const p = versionParts(h.agent_version ?? "");
        if (p.length === 0) return -1; // sem versão: antes de qualquer versão conhecida
        return (p[0] ?? 0) * 1e6 + (p[1] ?? 0) * 1e3 + (p[2] ?? 0);
      },
      render: (h) => <AgentVersionCell version={h.agent_version} newest={newestAgent} />,
    },
    {
      key: "last_seen",
      label: "Último contato",
      sortable: true,
      sortValue: (h) => lastMs(h),
      render: (h) => <LastSeen ts={h.last_seen} />,
    },
  ];

  // Sem `pageSize`: o DataTable usa 25 no desktop e 10 no celular, onde cada
  // linha vira um card empilhado e 25 delas dariam nove telas de rolagem.
  return (
    <DataTable
      columns={columns}
      rows={hosts}
      keyFn={(h) => h.hostname}
      initialSort={{ key: "state", dir: "asc" }}
      expandable={(h) => {
        const m = liveOf(h);
        const svcs = servicosDe(h.hostname);
        const containers = m?.containers ?? [];
        // Um servidor cPanel sem Docker tem o que mostrar: Apache, PHP-FPM, banco.
        // Antes a linha só expandia com containers, e parecia que não rodava nada.
        if (svcs.length === 0 && containers.length === 0) return null;
        return (
          <div className="stack">
            {svcs.length > 0 && (
              <ServicosChips servicos={svcs} rotulo={`Serviços em ${h.display_name?.trim() || h.hostname}`} />
            )}
            {m && containers.length > 0 && (
              <ContainersBlock
                containers={containers}
                criticalMetric={m.cpu.state === "crit" ? "cpu" : m.mem.state === "crit" ? "mem" : undefined}
                collapsible={false}
              />
            )}
          </div>
        );
      }}
      empty={
        <EmptyState
          icon={<Server size={32} strokeWidth={1.5} />}
          title="Nenhum host reportando ainda"
          body="Instale o agente num servidor para vê-lo aqui."
        />
      }
      rowActions={
        canEdit
          ? (h) => {
              const target = isAdmin ? matchTarget(h, targets) : null;
              return (
                <div className="row" style={{ gap: "var(--sp-1)", justifyContent: "flex-end" }}>
                  {target && (
                    <IconButton
                      icon={ActionIcons.reload}
                      label="Atualizar agente"
                      onClick={() => onUpdateAgent(target)}
                    />
                  )}
                  <IconButton icon={ActionIcons.edit} label="Renomear host" onClick={() => onEdit(h)} />
                  {isAdmin && (
                    <IconButton
                      icon={ActionIcons.delete}
                      label="Apagar servidor"
                      className="btn--icon-danger"
                      onClick={() => onDelete(h)}
                    />
                  )}
                </div>
              );
            }
          : undefined
      }
    />
  );
}

// Modal simples de rename: define o nome amigável (vazio limpa). O hostname técnico
// é a chave e não é editável — aparece como legenda.
function RenameHostModal({
  host,
  toast,
  onClose,
  onSaved,
}: {
  host: HostDetail;
  toast: ToastApi;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [name, setName] = useState(host.display_name ?? "");
  const [busy, setBusy] = useState(false);

  const save = async () => {
    setBusy(true);
    try {
      await updateHost(host.hostname, name.trim());
      toast.success(name.trim() ? "Nome atualizado." : "Nome amigável removido.");
      onSaved();
    } catch {
      setBusy(false);
      toast.error("Não foi possível salvar o nome.");
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title="Renomear host"
      footer={
        <>
          <Button onClick={onClose}>Cancelar</Button>
          <Button variant="primary" onClick={save} disabled={busy}>
            {busy ? "Salvando…" : "Salvar"}
          </Button>
        </>
      }
    >
      <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
        <label htmlFor="host-display-name" style={MUTED_12}>
          Nome amigável
        </label>
        <input
          id="host-display-name"
          className="field"
          value={name}
          maxLength={120}
          placeholder="ex.: Servidor de produção"
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !busy) {
              e.preventDefault();
              void save();
            }
          }}
          aria-label="Nome amigável do host"
        />
        <p style={{ ...MUTED_12, margin: 0 }}>
          Hostname técnico (chave imutável): <code>{host.hostname}</code>. Deixe em branco para
          remover o nome amigável.
        </p>
      </div>
    </Modal>
  );
}

// Itens que a exclusão apaga — mostrados no modal para o admin saber exatamente o
// alcance antes de confirmar (nada de surpresa numa ação irreversível).
//
// A lista depende do MODO: a linha do agente e o destino da chave mudam entre "ele se
// desinstala" e "apagar só os dados". Uma lista fixa dizia que o agente se remove
// mesmo quando o operador escolhia não pedir isso a ninguém, e prometia a chave
// apagada num modo em que ela sobrevive de propósito — sem ela, o agente não teria
// como voltar para receber a ordem.
function itensDaExclusao(auto: boolean): string[] {
  return [
    auto
      ? "O agente instalado na máquina (ele mesmo se remove quando receber a ordem)"
      : "O agente NÃO é removido da máquina, só os dados saem do painel",
    "A linha do servidor no inventário",
    "Métricas e históricos (7 dias / 90 dias / 2 anos)",
    "Logs, traces e eventos deste servidor",
    auto
      ? "A chave de ingestão (serverkey) é revogada agora e apagada quando o agente confirmar, ou em 7 dias, se ele não voltar"
      : "A chave de ingestão (serverkey)",
    "Serviços descobertos e limiares personalizados",
    "As permissões de quem podia ver este servidor e a participação dele em grupos",
    "Os alertas abertos deste servidor são encerrados, e ele sai do escopo das regras",
  ];
}

// RemoveServerModal — exclusão total do servidor, com confirmação máxima (digitar o
// hostname + checkbox). O agente sai da máquina por conta própria; a segunda opção
// existe para máquina já desativada, onde não há a quem pedir.
function RemoveServerModal({
  host,
  toast,
  onClose,
  onDeleted,
}: {
  host: HostDetail;
  toast: ToastApi;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const label = host.display_name?.trim() || host.hostname;
  // Como tirar o agente da máquina. Duas vias — o SSH saiu daqui.
  //
  // Pedir credencial para apagar deixou de fazer sentido quando o próprio agente
  // passou a se desinstalar: ele recebe a ordem no canal que já usa, executa e
  // confirma (medido: 23 s entre o clique e o início da remoção). Digitar usuário,
  // senha e IP para chegar ao mesmo lugar é trabalho manual com resultado pior — e
  // uma credencial a mais trafegando por uma tela que não precisava dela.
  const [modo, setModo] = useState<"auto" | "so-dados">("auto");
  const skipSsh = modo === "so-dados";
  const autoUninstall = modo === "auto";
  const [typed, setTyped] = useState("");
  const [confirmChecked, setConfirmChecked] = useState(false);
  const [busy, setBusy] = useState(false);
  // `resultado` segura o modal aberto depois da exclusão. O que a exclusão não
  // alcança (backup sem cifra, trilha, histórico de alertas) e o que ainda está
  // saindo do ClickHouse precisam ser LIDOS — num toast que some em segundos, não
  // são. Mesmo padrão do expurgo de logs, e pelo mesmo motivo.
  const [resultado, setResultado] = useState<DeleteHostResult | null>(null);

  const canDelete = typed.trim() === host.hostname && confirmChecked && !busy;

  const manualCmd = `curl -fsSL ${window.location.origin}/install.sh | sudo sh -s -- --uninstall`;

  const submit = async () => {
    setBusy(true);
    try {
      const body: Parameters<typeof deleteHost>[1] = { display_name: host.display_name };
      if (autoUninstall) {
        body.auto_uninstall = true;
      } else {
        body.skip_ssh = true;
      }
      const res = await deleteHost(host.hostname, body);

      // O QUE O BACKEND DIZ SOBRE OS DADOS VEM PRIMEIRO.
      //
      // Ele calcula honestamente se a exclusão terminou — mutation do ClickHouse
      // pendente ou falhada, passo do Postgres que não passou — e esta tela jogava
      // tudo fora e mostrava o mesmo toast verde. Num servidor com volume real, a
      // espera de 6 s do backend acaba MUITO antes da mutation (medido: 47 s para
      // 40 M linhas), então "apagado por completo" saía com milhões de linhas de log
      // daquele host ainda legíveis na tela de Logs. Quem fecha um pedido de LGPD lê
      // este toast.
      const pendentes = [...(res.ch_pending ?? []), ...(res.ch_failed ?? []), ...(res.ch_errors ?? [])];
      const dadosIncompletos = res.data_deleted === false || pendentes.length > 0 ||
        (res.postgres_failed?.length ?? 0) > 0;

      if (dadosIncompletos) {
        const onde = pendentes.length > 0 ? ` Ainda saindo de: ${[...new Set(pendentes)].join(", ")}.` : "";
        const pg = (res.postgres_failed?.length ?? 0) > 0
          ? ` Falhou no Postgres: ${res.postgres_failed!.join(", ")}.` : "";
        toast.error(
          `O servidor saiu do painel, mas a limpeza NÃO terminou.${onde}${pg} Confira na tela de Logs antes de declarar o dado apagado.`,
        );
      } else if (res.ssh.startsWith("falhou")) {
        toast.error(`Dados apagados, mas a desinstalação do agente falhou (${res.ssh}). Remova o agente à mão.`);
      } else if (autoUninstall) {
        // Sem chave para avisar, a ordem não vai a lugar nenhum — e prometer o
        // contrário deixaria um agente rodando com o operador achando que não.
        if ((res.auto_uninstall ?? 0) === 0) {
          toast.error("Dados apagados, mas não achei chave deste servidor para pedir a auto-desinstalação. Remova o agente à mão.");
        } else {
          toast.success("Servidor apagado. O agente se desinstala sozinho na próxima consulta ao painel (até 1 h).");
        }
      } else if (res.ssh.startsWith("pulado")) {
        toast.success("Servidor apagado do painel. Lembre de remover o agente da máquina, se existir.");
      } else {
        toast.success("Servidor apagado por completo (agente + dados).");
      }

      setResultado(res);
      setBusy(false);
    } catch (e) {
      toast.error(e instanceof Error ? `Não foi possível apagar: ${e.message}` : "Não foi possível apagar o servidor.");
      setBusy(false);
    }
  };

  // Depois de apagar, o modal vira RELATÓRIO: o que ficou pendente e o que continua
  // guardado fora do painel. Só ao fechar é que a lista de servidores recarrega.
  if (resultado) {
    const pendentes = [...new Set([...(resultado.ch_pending ?? []), ...(resultado.ch_failed ?? [])])];
    return (
      <Modal
        open
        onClose={onDeleted}
        title={`Servidor ${label} apagado`}
        footer={
          <div className="row row--end">
            <Button onClick={onDeleted}>Fechar</Button>
          </div>
        }
      >
        <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-3)" }}>
          {pendentes.length > 0 ? (
            <p style={{ margin: 0, color: "var(--crit)", fontWeight: 600 }}>
              A limpeza ainda NÃO terminou em: {pendentes.join(", ")}. O apagamento no ClickHouse roda em segundo
              plano e pode levar minutos em servidor com bastante histórico. Confira na tela de Logs antes de
              declarar o dado apagado.
            </p>
          ) : (
            <p style={{ margin: 0 }}>Os dados deste servidor saíram do painel.</p>
          )}
          {(resultado.postgres_failed?.length ?? 0) > 0 && (
            <p style={{ margin: 0, color: "var(--crit)" }}>
              Falhou ao apagar no cadastro: {resultado.postgres_failed!.join(", ")}.
            </p>
          )}
          {(resultado.alerts_closed ?? 0) > 0 && (
            <p style={{ ...MUTED_12, margin: 0 }}>
              {resultado.alerts_closed} alerta(s) aberto(s) deste servidor foram encerrados.
            </p>
          )}
          <NotPurgedList items={resultado.not_purged ?? []} />
        </div>
      </Modal>
    );
  }

  return (
    <Modal
      open
      onClose={onClose}
      title={`Apagar servidor ${label}?`}
      footer={
        <div className="row row--end">
          <Button onClick={onClose} disabled={busy}>
            Cancelar
          </Button>
          <button type="button" className="btn btn--danger" onClick={submit} disabled={!canDelete}>
            {busy ? "Apagando…" : "Apagar servidor definitivamente"}
          </button>
        </div>
      }
    >
      <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-3)" }}>
        <p style={{ margin: 0, color: "var(--crit)", fontWeight: 600 }}>
          Esta ação é permanente e não tem volta. Serão apagados:
        </p>
        <ul style={{ margin: 0, paddingLeft: "1.1rem", fontSize: "var(--fs-14)", display: "flex", flexDirection: "column", gap: 2 }}>
          {itensDaExclusao(autoUninstall).map((it) => (
            <li key={it}>{it}</li>
          ))}
        </ul>

        {/* Como tirar o agente da máquina. Sem SSH: o agente se remove sozinho. */}
        <div
          style={{
            display: "flex",
            flexDirection: "column",
            gap: "var(--sp-2)",
            border: "1px solid var(--border)",
            borderRadius: "var(--radius)",
            padding: "var(--sp-2)",
          }}
        >
          <label style={{ display: "flex", alignItems: "flex-start", gap: "var(--sp-2)", fontSize: "var(--fs-14)" }}>
            <input
              type="radio"
              name="modo-remocao"
              checked={modo === "auto"}
              onChange={() => setModo("auto")}
              style={{ marginTop: 3 }}
            />
            <span>
              O agente se desinstala sozinho
              <span style={{ ...MUTED_12, display: "block" }}>
                A ordem chega pelo canal que ele já usa; ele se remove e avisa o painel. Máquina desligada recebe
                quando voltar, o painel desiste depois de 7 dias.
              </span>
            </span>
          </label>
          <label style={{ display: "flex", alignItems: "flex-start", gap: "var(--sp-2)", fontSize: "var(--fs-14)" }}>
            <input
              type="radio"
              name="modo-remocao"
              checked={modo === "so-dados"}
              onChange={() => setModo("so-dados")}
              style={{ marginTop: 3 }}
            />
            <span>
              Apagar só os dados do painel
              <span style={{ ...MUTED_12, display: "block" }}>
                Para máquina já desativada, onde não há agente a quem pedir.
              </span>
            </span>
          </label>
          {skipSsh && (
            <p style={{ ...MUTED_12, margin: 0 }}>
              Se o agente ainda estiver rodando nessa máquina, o servidor reaparece aqui em instantes, o painel
              faz uma segunda limpeza um minuto e meio depois, mas o caminho limpo é a máquina estar calada. Para
              removê-lo à mão: <code style={{ wordBreak: "break-all" }}>{manualCmd}</code>
            </p>
          )}
        </div>

        {/* Confirmação máxima */}
        <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
          <label htmlFor="confirm-hostname" style={MUTED_12}>
            Digite <code>{host.hostname}</code> para confirmar
          </label>
          <input
            id="confirm-hostname"
            className="field"
            style={{ width: "100%" }}
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
            placeholder={host.hostname}
            aria-label="Digite o hostname para confirmar a exclusão"
            autoComplete="off"
          />
          <label style={{ display: "flex", alignItems: "center", gap: "var(--sp-2)", fontSize: "var(--fs-12)" }}>
            <input type="checkbox" checked={confirmChecked} onChange={(e) => setConfirmChecked(e.target.checked)} />
            Entendo que isto apaga TUDO deste servidor e é permanente.
          </label>
        </div>
      </div>
    </Modal>
  );
}

function HostCard({
  host,
  metrics,
  servicos,
  canEdit,
  target,
  onOpen,
  onEdit,
  onUpdateAgent,
  onDelete,
}: {
  host: HostDetail;
  metrics: HostMetrics | null;
  servicos: ServicoDescrito[];
  canEdit: boolean;
  target: ProvisionTarget | null;
  onOpen: () => void;
  onEdit: () => void;
  onUpdateAgent: (t: ProvisionTarget) => void;
  onDelete?: () => void;
}) {
  const open = () => onOpen();
  const label = host.display_name?.trim() || host.hostname;
  const hasAlias = label !== host.hostname;
  // Métricas confiáveis só quando o host está reportando. `host.up` (o mesmo sinal do
  // selo "no ar / sem sinal" logo acima) é a fonte única — sem isso, o cartão dizia
  // "sem sinal" no topo e continuava exibindo CPU/RAM logo abaixo por até 3 minutos.
  const live = host.up && metrics && metrics.up ? metrics : null;
  const uptimeSecs = live ? live.uptime_secs : host.uptime_secs;
  return (
    <div
      className="card"
      style={{ display: "flex", flexDirection: "column", gap: "var(--sp-3)" }}
    >
      <div
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "flex-start",
          gap: "var(--sp-2)",
        }}
      >
        <div style={{ minWidth: 0 }}>
          {/* Só o NOME abre a linha do tempo — clicar no resto do card (métricas,
              containers) não dispara mais o modal. */}
          <strong
            role="button"
            tabIndex={0}
            aria-label={`Abrir linha do tempo de ${label}`}
            title={host.hostname}
            onClick={open}
            onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                open();
              }
            }}
            style={{ fontSize: "var(--fs-14)", wordBreak: "break-all", display: "block", cursor: "pointer", color: "var(--accent, var(--text-1))" }}
          >
            {label}
          </strong>
          {hasAlias && <div style={{ ...MUTED_12, wordBreak: "break-all" }}>{host.hostname}</div>}
        </div>
        <div style={{ display: "flex", alignItems: "center", gap: "var(--sp-2)", flexShrink: 0 }}>
          <Badge state={host.up ? "ok" : "crit"}>{host.up ? "no ar" : "sem sinal"}</Badge>
          {canEdit && (
            <IconButton
              icon={ActionIcons.edit}
              label="Renomear host"
              onClick={(e) => {
                e.stopPropagation();
                onEdit();
              }}
            />
          )}
          {/* Apagar servidor por completo (admin). Destrutivo → ícone crítico ao lado do editar. */}
          {onDelete && (
            <IconButton
              icon={ActionIcons.delete}
              label="Apagar servidor"
              className="btn--icon-danger"
              onClick={(e) => {
                e.stopPropagation();
                onDelete();
              }}
            />
          )}
        </div>
      </div>

      {/* Métricas ao vivo do servidor inteiro (merge por hostname). */}
      {live ? (
        <HostLiveMetrics m={live} />
      ) : (
        <div style={{ ...MUTED_12, fontStyle: "italic" }}>Sem métricas recentes</div>
      )}

      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: "var(--sp-2)" }}>
        <Metric label="CPUs" value={host.cpu_cores ? String(host.cpu_cores) : "—"} numeric />
        <Metric label="Uptime" value={formatUptime(uptimeSecs)} numeric />
        <Metric label="SO" value={host.os || "—"} />
        <Metric label="Kernel" value={host.kernel || "—"} />
      </div>

      <div style={MUTED_12}>
        {[host.arch, host.ips, host.agent_version ? `agente ${host.agent_version}` : null]
          .filter(Boolean)
          .join(" · ") || "—"}
      </div>

      {/* O que roda aqui (Apache, PHP-FPM, banco, Docker): um servidor cPanel sem
          containers não é um servidor vazio. */}
      {servicos.length > 0 && <ServicosChips servicos={servicos} rotulo={`Serviços em ${label}`} />}

      <div
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
          gap: "var(--sp-2)",
          flexWrap: "wrap",
        }}
      >
        {/* Link para os logs deste servidor (a tela de Logs lê ?host= da URL). */}
        <a
          href={`#/logs?host=${encodeURIComponent(host.hostname)}`}
          onClick={(e) => e.stopPropagation()}
          style={{
            fontSize: "var(--fs-12)",
            color: "var(--accent, var(--text-2))",
            textDecoration: "none",
            fontWeight: 600,
          }}
        >
          Ver logs →
        </a>
        <div style={{ display: "flex", alignItems: "center", gap: "var(--sp-2)", flexWrap: "wrap" }}>
          {/* Fase G: host provisionado por SSH → reinstalar/atualizar via cofre (admin). */}
          {target && (
            <Button
              variant="ghost"
              onClick={(e) => {
                e.stopPropagation();
                onUpdateAgent(target);
              }}
            >
              Atualizar agente
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}

// medidorDe resolve o que a barra de um recurso deve mostrar, considerando as DUAS
// formas de o número não valer:
//  - "nodata": o backend diz que a métrica não chegou (senão apareceria "0,0% em uso");
//  - "stale":  chegou, mas é velha demais para a cadência real da série. Este é o caso
//    que faltava: um coletor pode calar (o de CPU não emite quando não consegue medir)
//    enquanto os outros seguem, e o host continua "no ar" — a célula repetia o último
//    valor com a cor do limiar por minutos, então um pico crítico já passado seguia
//    vermelho. Aqui ele vira "sem dado recente" com a idade, e a barra zera.
function medidorDe(
  m: HealthTile,
  quandoMedido: string,
): { value: string; pct: number; state: HealthMetric["state"] } {
  const f = healthFreshness(m);
  if (f.state === "nodata") return { value: "não medido nesta janela", pct: 0, state: "nodata" };
  if (f.state === "stale") {
    const idade = m.ts != null ? fmtRelAbs(m.ts * 1000).rel : "";
    return {
      value: idade ? `sem dado recente (último ${idade})` : "sem dado recente",
      pct: 0,
      state: "nodata",
    };
  }
  return { value: quandoMedido, pct: m.pct, state: m.state };
}

// Bloco de métricas ao vivo do servidor inteiro: CPU/RAM/swap com semáforo próprio,
// disco por montagem (uma linha por mount), rede (↓/↑) e nº de processos.
function HostLiveMetrics({ m }: { m: HostMetrics }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
      <MeterRow label="CPU" {...medidorDe(m.cpu, `${formatValue(m.cpu.pct, "percent")} em uso`)} />
      <MeterRow label="RAM" {...medidorDe(m.mem, bytesOrPct(m.mem))} />
      {m.swap && <MeterRow label="Swap" {...medidorDe(m.swap, bytesOrPct(m.swap))} />}

      {m.disks.length > 0 && (
        <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
          <div style={MUTED_12}>Disco</div>
          {m.disks.map((d) => (
            <MeterRow key={d.mount} label={d.mount} {...medidorDe(d, bytesOrPct(d))} />
          ))}
        </div>
      )}

      <div
        style={{
          display: "flex",
          justifyContent: "space-between",
          gap: "var(--sp-2)",
          flexWrap: "wrap",
        }}
      >
        <span style={MUTED_12}>
          Processos{" "}
          <span
            className="tabular"
            style={{ color: m.procs === null ? "var(--text-3)" : "var(--text-1)", fontWeight: 600 }}
            title={m.procs === null ? "A contagem de processos não chegou na última janela de medição" : undefined}
          >
            {/* "—" e não "0": sem medida, o card não afirma que o servidor está sem
                processos. Mesmo tratamento que cpu/mem já recebem via MetricCell. */}
            {m.procs === null ? "—" : m.procs}
          </span>
        </span>
        <span
          className="tabular"
          style={MUTED_12}
          title={
            m.net.rx_bps == null || m.net.tx_bps == null
              ? "Rede: download / upload, travessão: taxa não calculável (um único ponto do contador na janela ou contador reiniciado)"
              : "Rede: download / upload"
          }
        >
          ↓ {formatRate(m.net.rx_bps)}  ↑ {formatRate(m.net.tx_bps)}
        </span>
      </div>

      {m.containers && m.containers.length > 0 && <ContainersBlock containers={m.containers} />}
    </div>
  );
}

function Metric({ label, value, numeric }: { label: string; value: string; numeric?: boolean }) {
  return (
    <div>
      <div style={MUTED_12}>{label}</div>
      <div
        className={numeric ? "tabular" : undefined}
        style={{ fontSize: "var(--fs-14)", color: "var(--text-1)", wordBreak: "break-word" }}
      >
        {value}
      </div>
    </div>
  );
}

function TimelineBody({ host }: { host: string }) {
  const [events, setEvents] = useState<{ kind: string; ts: string; title: string }[] | null>(null);
  useEffect(() => {
    setEvents(null);
    hostTimeline(host)
      .then((r) => setEvents(r.events))
      .catch(() => setEvents([]));
  }, [host]);

  if (events === null) {
    return (
      <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
        <Skeleton height={20} />
        <Skeleton height={20} width="80%" />
        <Skeleton height={20} width="90%" />
      </div>
    );
  }
  if (events.length === 0) {
    return <p style={{ color: "var(--text-3)", margin: 0 }}>Sem eventos registrados para este host.</p>;
  }
  return (
    <ul style={{ listStyle: "none", padding: 0, margin: 0 }}>
      {events.map((e, i) => (
        <li key={i} style={{ padding: "var(--sp-2) 0", borderBottom: "1px solid var(--border)" }}>
          <Badge state={hostKindState(e.kind)}>{hostKindLabel(e.kind)}</Badge>{" "}
          <span style={MUTED_12} title={fmtRelAbs(e.ts).abs}>{fmtRelAbs(e.ts).rel}</span>
          <div style={{ fontSize: "var(--fs-14)" }}>{e.title}</div>
        </li>
      ))}
    </ul>
  );
}

// Corpo do modal do host: legenda do hostname técnico (quando há apelido) + abas
// "Linha do tempo" e "Armazenamento de logs".
function HostModalBody({
  host,
  isAdmin,
  toast,
}: {
  host: HostDetail;
  isAdmin: boolean;
  toast: ToastApi;
}) {
  const [tab, setTab] = useState<"timeline" | "storage">("timeline");
  const label = host.display_name?.trim() || host.hostname;
  const hasAlias = label !== host.hostname;
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-3)" }}>
      {hasAlias && (
        <div style={MUTED_12}>
          Hostname técnico: <code>{host.hostname}</code>
        </div>
      )}
      <Tabs
        tabs={[
          { id: "timeline", label: "Linha do tempo" },
          { id: "storage", label: "Armazenamento de logs" },
        ]}
        active={tab}
        onChange={(id) => setTab(id as "timeline" | "storage")}
      />
      {tab === "timeline" ? (
        <TimelineBody host={host.hostname} />
      ) : (
        <HostLogStorage host={host.hostname} isAdmin={isAdmin} toast={toast} />
      )}
    </div>
  );
}

// GiB para conversão do limite (mesma base 1024 do formatBytes, para round-trip).
const GIB = 1024 ** 3;

// Cor (token) do medidor conforme a fração do limite usada.
function gaugeColor(pct: number): string {
  if (pct >= 0.9) return "--crit";
  if (pct >= 0.75) return "--warn";
  return "--ok";
}

// Meia-noite local de hoje (base dos presets de "manter N dias").
function startOfToday(): Date {
  const d = new Date();
  d.setHours(0, 0, 0, 0);
  return d;
}

type PurgePreset = "keep7" | "keepToday" | "keepN" | "date" | "all";

// cutoffFor devolve o instante de corte (apaga tudo ANTES dele) do preset escolhido,
// no fuso local do usuário. null = parâmetro inválido (ex.: data não escolhida).
function cutoffFor(preset: PurgePreset, keepN: number, customDate: string): Date | null {
  switch (preset) {
    case "keepToday":
      return startOfToday();
    case "keep7": {
      const d = startOfToday();
      d.setDate(d.getDate() - 6); // hoje + 6 dias anteriores = 7 dias
      return d;
    }
    case "keepN": {
      if (!Number.isFinite(keepN) || keepN < 1) return null;
      const d = startOfToday();
      d.setDate(d.getDate() - (keepN - 1));
      return d;
    }
    case "date": {
      const [y, m, dd] = customDate.split("-").map(Number);
      if (!y || !m || !dd) return null;
      return new Date(y, m - 1, dd); // meia-noite local da data escolhida
    }
    case "all":
      return new Date(); // agora → apaga tudo do host
  }
}

// Aba "Armazenamento de logs": medidor de uso vs. limite configurável + zona de
// perigo para apagar logs deste host por data (só admin). Nada é apagado num clique
// só: exige pré-visualizar e confirmar.
function HostLogStorage({
  host,
  isAdmin,
  toast,
}: {
  host: string;
  isAdmin: boolean;
  toast: ToastApi;
}) {
  const [data, setData] = useState<LogStorage | null>(null);
  const [err, setErr] = useState(false);

  const load = useCallback(() => {
    logsStorage(host)
      .then((d) => {
        setData(d);
        setErr(false);
      })
      .catch(() => setErr(true));
  }, [host]);

  useEffect(() => {
    setData(null);
    setErr(false);
    load();
  }, [host, load]);

  if (err) {
    return <p style={{ color: "var(--crit)", margin: 0 }}>Não foi possível carregar o armazenamento.</p>;
  }
  if (data === null) {
    return (
      <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
        <Skeleton height={20} />
        <Skeleton height={40} />
      </div>
    );
  }

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-4)" }}>
      {/* Medidor global (tabela inteira, todos os servidores) é assunto de admin — o
          backend devolve zeros para usuário comum, então nem renderizamos o gauge. */}
      {isAdmin && <StorageGauge data={data} isAdmin={isAdmin} toast={toast} onSaved={load} />}
      <HostLogDetail data={data} />
      {isAdmin && <PurgeZone host={host} toast={toast} onPurged={load} />}
    </div>
  );
}

// Medidor: uso atual da tabela de logs vs. limite. Admin pode definir/alterar o teto.
function StorageGauge({
  data,
  isAdmin,
  toast,
  onSaved,
}: {
  data: LogStorage;
  isAdmin: boolean;
  toast: ToastApi;
  onSaved: () => void;
}) {
  const hasLimit = data.limit_bytes > 0;
  const pct = hasLimit ? Math.min(1, data.pct) : 0;
  const color = `var(${gaugeColor(data.pct)})`;
  const [editing, setEditing] = useState(false);
  const [gb, setGb] = useState(hasLimit ? (data.limit_bytes / GIB).toString() : "");
  const [busy, setBusy] = useState(false);

  const save = async () => {
    const n = Number(gb.replace(",", "."));
    if (!Number.isFinite(n) || n < 0) {
      toast.error("Informe um valor válido em GB (0 remove o limite).");
      return;
    }
    setBusy(true);
    try {
      await setLogsStorageLimit(Math.round(n * GIB));
      toast.success(n > 0 ? "Limite atualizado." : "Limite removido.");
      setEditing(false);
      onSaved();
    } catch {
      toast.error("Não foi possível salvar o limite.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "baseline", gap: "var(--sp-2)" }}>
        <strong style={{ fontSize: "var(--fs-14)" }}>Armazenamento de logs (ClickHouse)</strong>
        {isAdmin && !editing && (
          <Button variant="ghost" onClick={() => setEditing(true)}>
            {hasLimit ? "Alterar limite" : "Definir limite"}
          </Button>
        )}
      </div>

      {hasLimit ? (
        <>
          <span
            aria-hidden
            style={{ display: "block", height: 10, borderRadius: 999, background: "var(--bg-3)", overflow: "hidden" }}
          >
            <span style={{ display: "block", height: "100%", width: `${pct * 100}%`, background: color }} />
          </span>
          <div style={{ display: "flex", justifyContent: "space-between", gap: "var(--sp-2)" }}>
            <span className="tabular" style={{ fontSize: "var(--fs-12)", color, fontWeight: 600 }}>
              {formatBytes(data.total_bytes)} de {formatBytes(data.limit_bytes)}
            </span>
            <span className="tabular" style={{ fontSize: "var(--fs-12)", color, fontWeight: 600 }}>
              {/* `pct` aqui é fração 0..1; formatValue mantém 1 casa e evita que
                  99,5% do teto de armazenamento apareça como "100%". */}
              {formatValue(data.pct * 100, "percent")}
            </span>
          </div>
        </>
      ) : (
        <p style={{ ...MUTED_12, margin: 0 }}>
          Uso atual: <strong className="tabular" style={{ color: "var(--text-1)" }}>{formatBytes(data.total_bytes)}</strong>{" "}
          em {data.total_rows.toLocaleString("pt-BR")} linhas. Sem limite definido, defina um teto para acompanhar
          se está crescendo demais.
        </p>
      )}

      {data.oldest_ts > 0 && (
        <div style={MUTED_12}>
          Log mais antigo guardado:{" "}
          <span title={fmtRelAbs(new Date(data.oldest_ts * 1000)).abs}>
            {fmtRelAbs(new Date(data.oldest_ts * 1000)).rel}
          </span>{" "}
          · retenção automática de 30 dias (TTL).
        </div>
      )}

      {editing && (
        <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
          <label htmlFor="logs-limit-gb" style={MUTED_12}>
            Limite em GB (0 remove o limite)
          </label>
          <div style={{ display: "flex", gap: "var(--sp-2)", flexWrap: "wrap" }}>
            <input
              id="logs-limit-gb"
              className="field tabular"
              type="number"
              min={0}
              step="0.5"
              value={gb}
              onChange={(e) => setGb(e.target.value)}
              aria-label="Limite de armazenamento de logs em GB"
              style={{ maxWidth: 160 }}
            />
            <Button variant="primary" onClick={save} disabled={busy}>
              {busy ? "Salvando…" : "Salvar"}
            </Button>
            <Button onClick={() => setEditing(false)} disabled={busy}>
              Cancelar
            </Button>
          </div>
          <p style={{ ...MUTED_12, margin: 0 }}>
            É só um alerta visual, o limite não apaga nada sozinho. A retenção de 30 dias já descarta logs antigos
            automaticamente; para reduzir agora, use “Apagar logs”.
          </p>
        </div>
      )}
    </div>
  );
}

// Detalhe do host aberto: linhas e período coberto.
function HostLogDetail({ data }: { data: LogStorage }) {
  const rows = data.host_rows ?? 0;
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
      <div style={MUTED_12}>Logs deste servidor</div>
      {rows > 0 ? (
        <div style={{ fontSize: "var(--fs-14)" }}>
          <span className="tabular" style={{ fontWeight: 600 }}>{rows.toLocaleString("pt-BR")}</span> linhas
          {data.host_oldest_ts && data.host_oldest_ts > 0 ? (
            <span style={MUTED_12}>
              {" "}· de {fmtRelAbs(new Date(data.host_oldest_ts * 1000)).abs}
              {" "}até {fmtRelAbs(new Date((data.host_newest_ts || data.host_oldest_ts) * 1000)).abs}
            </span>
          ) : null}
        </div>
      ) : (
        <div style={{ ...MUTED_12, fontStyle: "italic" }}>Sem logs deste servidor no momento.</div>
      )}
    </div>
  );
}

// NotPurgedList: checklist do que o expurgo NÃO alcança.
//
// O backend já devolvia esta lista em `not_purged` e o front a descartava. Sem ela, a
// tela terminava com "N linha(s) apagada(s). Espaço liberado." e o operador fechava o
// chamado de LGPD enquanto o mesmo dado seguia vivo em metrics_1h (730 dias), no
// notification_log e no backup do MinIO. É o que ainda falta fazer à mão.
function NotPurgedList({ items }: { items: NotPurgedItem[] }) {
  if (items.length === 0) return null;
  return (
    <div
      style={{
        border: "1px solid var(--warn)",
        borderRadius: "var(--radius)",
        padding: "var(--sp-3)",
        display: "flex",
        flexDirection: "column",
        gap: "var(--sp-2)",
      }}
    >
      <strong style={{ fontSize: "var(--fs-14)", color: "var(--warn)" }}>
        O que esta limpeza NÃO alcança ({items.length})
      </strong>
      <p style={{ ...MUTED_12, margin: 0 }}>
        Apagar aqui não apaga estas cópias. Enquanto elas existirem, o dado ainda existe, trate cada item à mão
        antes de dar o caso por encerrado.
      </p>
      <ul style={{ margin: 0, paddingLeft: "var(--sp-4)", display: "flex", flexDirection: "column", gap: 4 }}>
        {items.map((it) => (
          <li key={it.store} style={{ fontSize: "var(--fs-12)" }}>
            <span style={{ color: "var(--text-1)", fontWeight: 600 }}>{it.store}</span>
            <span style={MUTED_12}> · retenção: {it.retention}</span>
            <div style={MUTED_12}>{it.note}</div>
          </li>
        ))}
      </ul>
    </div>
  );
}

// LimpezasTravadas: as mutations de expurgo que falharam de forma PERMANENTE.
//
// Elas não somem sozinhas e continuam contando como "limpeza em andamento": uma só
// basta para que todo expurgo futuro seja recusado com "Aguarde." indefinidamente.
// Até aqui a única saída era abrir um cliente do ClickHouse na mão, em produção,
// com alguém esperando o cumprimento de um pedido de LGPD. Aparece só quando existe
// alguma — no dia a dia esta caixa não está na tela.
function LimpezasTravadas({
  itens,
  cancelando,
  onCancelar,
}: {
  itens: StuckMutation[];
  cancelando: string | null;
  onCancelar: (m: StuckMutation) => void;
}) {
  if (itens.length === 0) return null;
  return (
    <div
      style={{
        display: "flex",
        flexDirection: "column",
        gap: "var(--sp-2)",
        border: "1px solid var(--warn)",
        borderRadius: "var(--radius-sm)",
        padding: "var(--sp-3)",
      }}
    >
      <strong style={{ fontSize: "var(--fs-13)", color: "var(--warn)" }}>
        Limpezas travadas ({itens.length})
      </strong>
      <p style={{ ...MUTED_12, margin: 0 }}>
        Estas limpezas falharam e continuam registradas. Enquanto existirem, <strong>nenhum expurgo novo
        começa</strong>: o painel responde “Já existe uma limpeza em andamento. Aguarde.” para sempre.
        Cancelar remove só o registro da limpeza que falhou, <strong>nenhum log é apagado e nenhum log
        apagado volta</strong>. Depois, refaça o expurgo pelo formulário abaixo.
      </p>
      <div style={{ overflowX: "auto" }}>
        <table className="dtable" style={{ width: "100%" }}>
          <thead>
            <tr>
              <th>Quando começou</th>
              <th>Partes restantes</th>
              <th>Por que falhou</th>
              <th style={{ textAlign: "right" }}>Ação</th>
            </tr>
          </thead>
          <tbody>
            {itens.map((m) => (
              <tr key={m.mutation_id}>
                <td title={`Identificação interna: ${m.mutation_id}`} style={{ whiteSpace: "nowrap" }}>
                  {m.created_at || "—"}
                </td>
                <td className="tabular">{m.parts_remaining}</td>
                {/* O motivo é o texto cru do ClickHouse: é o que um operador leva
                    para o suporte. Traduzi-lo esconderia justamente o detalhe útil. */}
                <td style={{ ...MUTED_12, wordBreak: "break-word" }}>{m.fail_reason || "não informado"}</td>
                <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                  <Button
                    variant="ghost"
                    disabled={cancelando === m.mutation_id}
                    onClick={() => onCancelar(m)}
                    title="Remove o registro desta limpeza que falhou, liberando novos expurgos. Não apaga nem restaura log nenhum."
                  >
                    {cancelando === m.mutation_id ? "Cancelando…" : "Cancelar"}
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// Zona de perigo: expurgo CIRÚRGICO de logs. Fluxo em duas etapas (pré-visualizar →
// confirmar). Apaga de verdade (o backend reescreve as parts e libera disco).
//
// A tela mandava só host + data de corte, embora o backend aceite três eixos
// (servidor OU "linhas sem servidor" × janela de dois lados × trecho do conteúdo).
// Na hora de um vazamento, isso deixava um único caminho: apagar tudo do servidor
// anterior a uma data — destruição em massa para remover uma linha. E as linhas sem
// rótulo de host, que é por onde entra o OTLP de terceiro (o caminho mais provável
// de um segredo), eram simplesmente inalcançáveis pela interface.
function PurgeZone({ host, toast, onPurged }: { host: string; toast: ToastApi; onPurged: () => void }) {
  const [preset, setPreset] = useState<PurgePreset>("keep7");
  const [keepN, setKeepN] = useState(14);
  const [customDate, setCustomDate] = useState("");
  const [fromDate, setFromDate] = useState(""); // início da janela (opcional)
  const [bodyLike, setBodyLike] = useState(""); // trecho do conteúdo (opcional)
  const [noHost, setNoHost] = useState(false); // alvo = linhas SEM servidor
  // Recorte capturado na pré-visualização (o MESMO enviado no expurgo — é o que
  // garante que o número mostrado antes é o número apagado depois).
  const [preview, setPreview] = useState<{ rows: number; scope: string; filter: PurgeFilter; notPurged: NotPurgedItem[] } | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const [confirmChecked, setConfirmChecked] = useState(false);
  const [typedHost, setTypedHost] = useState("");
  const [purging, setPurging] = useState(false);
  // Resultado do último expurgo: fica na tela com a checklist do que sobrou.
  const [feito, setFeito] = useState<{ rows: number; scope: string; notPurged: NotPurgedItem[] } | null>(null);
  // Limpezas TRAVADAS: mutations que falharam de forma permanente e continuam
  // registradas no ClickHouse. Enquanto uma delas existir, a guarda de concorrência
  // recusa todo expurgo novo com "Já existe uma limpeza em andamento. Aguarde." —
  // uma espera que nunca termina. Por isso a lista é carregada ao abrir a zona, e
  // não só depois de alguém esbarrar no 409.
  const [travadas, setTravadas] = useState<StuckMutation[]>([]);
  const [cancelando, setCancelando] = useState<string | null>(null);

  const recarregarTravadas = useCallback(() => {
    purgeLogsStuck()
      .then(setTravadas)
      // Silêncio proposital: não achar mutation travada é o caso normal, e um erro
      // aqui não pode roubar a tela de quem veio apagar log.
      .catch(() => setTravadas([]));
  }, []);

  useEffect(() => {
    recarregarTravadas();
  }, [recarregarTravadas]);

  const cancelarTravada = async (m: StuckMutation) => {
    setCancelando(m.mutation_id);
    try {
      await purgeLogsCancel(m.mutation_id);
      toast.success("Limpeza travada cancelada. Já é possível iniciar um novo expurgo.");
      recarregarTravadas();
    } catch (e) {
      toast.error(mensagemDeErro(e, "Não foi possível cancelar a limpeza travada."));
    } finally {
      setCancelando(null);
    }
  };

  // Qualquer mudança nos parâmetros invalida a pré-visualização.
  const resetPreview = () => {
    setPreview(null);
    setConfirmChecked(false);
    setTypedHost("");
  };
  const onParamChange = <T,>(set: (v: T) => void) => (v: T) => {
    set(v);
    resetPreview();
  };

  // Alvo da confirmação reforçada: o texto que o operador precisa digitar.
  const alvo = noHost ? "SEM SERVIDOR" : host;
  // Confirmação reforçada quando o recorte é largo: janela inteira (preset "all") ou
  // as linhas sem servidor, que atingem o que veio de fora do agente.
  const needsTyping = preset === "all" || noHost;

  const montarFiltro = (): PurgeFilter | null => {
    const cutoff = cutoffFor(preset, keepN, customDate);
    if (!cutoff) return null;
    const f: PurgeFilter = { to: cutoff.toISOString() };
    if (noHost) f.no_host = true;
    else f.host = host;
    if (fromDate) {
      const [y, m, d] = fromDate.split("-").map(Number);
      if (!y || !m || !d) return null;
      const inicio = new Date(y, m - 1, d);
      if (inicio >= cutoff) return null; // início depois do fim: recorte impossível
      f.from = inicio.toISOString();
    }
    const trecho = bodyLike.trim();
    if (trecho) f.body_like = trecho;
    return f;
  };

  const doPreview = async () => {
    const f = montarFiltro();
    if (!f) {
      toast.error("Recorte inválido: confira as datas (o início precisa ser anterior ao fim).");
      return;
    }
    setPreviewing(true);
    try {
      const r = await purgeLogsPreview(f);
      setPreview({ rows: r.rows, scope: r.scope, filter: f, notPurged: r.not_purged ?? [] });
      setConfirmChecked(false);
      setTypedHost("");
      setFeito(null);
    } catch (e) {
      toast.error(mensagemDeErro(e, "Não foi possível pré-visualizar."));
    } finally {
      setPreviewing(false);
    }
  };

  const doPurge = async () => {
    if (!preview) return;
    setPurging(true);
    try {
      let res = await purgeLogs(preview.filter);
      const notPurged = res.not_purged ?? preview.notPurged;
      const scope = res.scope ?? preview.scope;
      // Volume grande: acompanha a mutation em segundo plano até concluir/falhar.
      while (!res.done && res.mutation_id && !res.fail_reason) {
        await new Promise((r) => setTimeout(r, 1000));
        const s = await purgeLogsStatus(res.mutation_id);
        res = { done: s.done, mutation_id: res.mutation_id, fail_reason: s.fail_reason, deleted_rows: preview.rows };
      }
      if (res.fail_reason) {
        toast.error(`A limpeza falhou: ${res.fail_reason}`);
        return;
      }
      const n = res.deleted_rows ?? preview.rows;
      // O toast NÃO diz mais "espaço liberado" e ponto: a conclusão fica no bloco
      // abaixo, junto do que ainda não foi alcançado.
      toast.success(`${n.toLocaleString("pt-BR")} linha(s) apagada(s) de logs.`);
      setFeito({ rows: n, scope, notPurged });
      resetPreview();
      onPurged();
    } catch (e) {
      // No 409, recarrega a lista de travadas: se a "limpeza em andamento" for na
      // verdade uma que falhou de vez, o bloco abaixo aparece com o botão de
      // cancelar — em vez de mandar o operador esperar por algo que não vai acabar.
      const concorrencia = e instanceof Error && e.message.includes("409");
      if (concorrencia) recarregarTravadas();
      toast.error(
        concorrencia
          ? "Já existe uma limpeza em andamento. Se ela estiver travada, o bloco “Limpezas travadas” abaixo permite cancelá-la."
          : "Não foi possível apagar os logs.",
      );
    } finally {
      setPurging(false);
    }
  };

  const cutoffNow = cutoffFor(preset, keepN, customDate);
  const cutoffLabel = cutoffNow ? fmtRelAbs(cutoffNow).abs : "—";
  const canDelete =
    preview !== null &&
    preview.rows > 0 &&
    confirmChecked &&
    (!needsTyping || typedHost.trim() === alvo);

  const presets: { id: PurgePreset; label: string }[] = [
    { id: "keep7", label: "Manter últimos 7 dias" },
    { id: "keepToday", label: "Manter só hoje" },
    { id: "keepN", label: "Manter últimos N dias" },
    { id: "date", label: "Escolher data" },
    { id: "all", label: "Apagar TUDO até agora" },
  ];

  return (
    <div
      style={{
        display: "flex",
        flexDirection: "column",
        gap: "var(--sp-2)",
        border: "1px solid var(--crit)",
        borderRadius: "var(--radius)",
        padding: "var(--sp-3)",
      }}
    >
      <strong style={{ fontSize: "var(--fs-14)", color: "var(--crit)" }}>Apagar logs</strong>
      <p style={{ ...MUTED_12, margin: 0 }}>
        Remove permanentemente as linhas de log que casarem com o recorte abaixo. Quanto mais estreito o recorte,
        menos histórico legítimo se perde junto. Ação irreversível.
      </p>

      <LimpezasTravadas itens={travadas} cancelando={cancelando} onCancelar={cancelarTravada} />

      <label style={{ display: "flex", alignItems: "flex-start", gap: "var(--sp-2)", fontSize: "var(--fs-14)" }}>
        <input type="checkbox" checked={noHost} onChange={(e) => onParamChange(setNoHost)(e.target.checked)} />
        <span>
          Apagar as <strong>linhas sem servidor</strong> em vez das de <code>{host}</code>
          <div style={MUTED_12}>
            Linhas que chegaram sem rótulo de host, normalmente OTLP de coletores de terceiros. Como não pertencem
            a nenhum servidor, são inalcançáveis por qualquer outro filtro desta tela.
          </div>
        </span>
      </label>

      <fieldset style={{ border: 0, margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 4 }}>
        <legend style={{ ...MUTED_12, padding: 0 }}>Até quando apagar (fim da janela)</legend>
        {presets.map((p) => (
          <label key={p.id} style={{ display: "flex", alignItems: "center", gap: "var(--sp-2)", fontSize: "var(--fs-14)" }}>
            <input
              type="radio"
              name="purge-preset"
              checked={preset === p.id}
              onChange={() => onParamChange(setPreset)(p.id)}
            />
            {p.label}
          </label>
        ))}
      </fieldset>

      {preset === "keepN" && (
        <input
          className="field tabular"
          type="number"
          min={1}
          value={keepN}
          onChange={(e) => onParamChange(setKeepN)(Math.max(1, Number(e.target.value) || 1))}
          aria-label="Número de dias a manter"
          style={{ maxWidth: 120 }}
        />
      )}
      {preset === "date" && (
        <input
          className="field"
          type="date"
          value={customDate}
          onChange={(e) => onParamChange(setCustomDate)(e.target.value)}
          aria-label="Apagar logs anteriores a esta data"
          style={{ maxWidth: 200 }}
        />
      )}

      {/* Início da janela: sem ele, corrigir um vazamento de terça obriga a apagar
          também segunda, domingo e todo o resto do histórico anterior. */}
      <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
        <label htmlFor="purge-from" style={MUTED_12}>
          A partir de (opcional), deixe vazio para apagar desde o log mais antigo
        </label>
        <input
          id="purge-from"
          className="field"
          type="date"
          value={fromDate}
          onChange={(e) => onParamChange(setFromDate)(e.target.value)}
          style={{ maxWidth: 200 }}
        />
      </div>

      {/* Trecho do conteúdo: é o eixo que transforma "apagar o mês inteiro" em
          "apagar as 3 linhas que citam o segredo". */}
      <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
        <label htmlFor="purge-body" style={MUTED_12}>
          Trecho do conteúdo (opcional), só apaga linhas que contenham este texto
        </label>
        <input
          id="purge-body"
          className="field"
          type="text"
          maxLength={512}
          value={bodyLike}
          placeholder="ex.: o token vazado"
          onChange={(e) => onParamChange(setBodyLike)(e.target.value)}
          style={{ maxWidth: 420 }}
        />
      </div>

      <div style={MUTED_12}>
        Alvo: <strong>{noHost ? "linhas sem servidor" : host}</strong>
        {fromDate ? ` · de ${fromDate}` : " · desde o início"}
        {` · até ${cutoffLabel}`}
        {bodyLike.trim() ? " · só linhas com o trecho informado" : ""}
      </div>

      {preview === null ? (
        <div>
          <Button variant="ghost" onClick={doPreview} disabled={previewing || (preset === "date" && !customDate)}>
            {previewing ? "Calculando…" : "Pré-visualizar"}
          </Button>
        </div>
      ) : preview.rows === 0 ? (
        <p style={{ ...MUTED_12, margin: 0 }}>Nada a apagar com esse critério.</p>
      ) : (
        <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
          <p style={{ fontSize: "var(--fs-14)", margin: 0 }}>
            <strong className="tabular" style={{ color: "var(--crit)" }}>
              {preview.rows.toLocaleString("pt-BR")}
            </strong>{" "}
            linha(s) serão apagadas.
            {/* O escopo vem do BACKEND, escrito por quem monta o WHERE: é a leitura
                literal do que vai sair, e não uma segunda versão montada aqui. */}
            <span style={MUTED_12}> Recorte: {preview.scope}</span>
          </p>
          <NotPurgedList items={preview.notPurged} />
          <label style={{ display: "flex", alignItems: "center", gap: "var(--sp-2)", fontSize: "var(--fs-12)" }}>
            <input type="checkbox" checked={confirmChecked} onChange={(e) => setConfirmChecked(e.target.checked)} />
            Entendo que esta ação é permanente.
          </label>
          {needsTyping && (
            <input
              className="field"
              placeholder={`Digite ${alvo} para confirmar`}
              value={typedHost}
              onChange={(e) => setTypedHost(e.target.value)}
              aria-label="Digite o alvo para confirmar a exclusão"
              style={{ maxWidth: 280 }}
            />
          )}
          <div style={{ display: "flex", gap: "var(--sp-2)", flexWrap: "wrap" }}>
            <button
              type="button"
              className="btn btn--danger"
              onClick={doPurge}
              disabled={!canDelete || purging}
            >
              {purging ? "Apagando…" : `Apagar ${preview.rows.toLocaleString("pt-BR")} linha(s)`}
            </button>
            <Button onClick={resetPreview} disabled={purging}>
              Cancelar
            </Button>
          </div>
        </div>
      )}

      {feito && (
        <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
          <p style={{ fontSize: "var(--fs-14)", margin: 0 }}>
            <strong className="tabular">{feito.rows.toLocaleString("pt-BR")}</strong> linha(s) apagadas da tabela de
            logs. <span style={MUTED_12}>Recorte: {feito.scope}</span>
          </p>
          <NotPurgedList items={feito.notPurged} />
        </div>
      )}
    </div>
  );
}

// ---- Banner de auto-discovery (starter packs) — apply → toast ----

// Estado vazio dos hosts: oferece a AÇÃO (abrir o fluxo de adicionar servidor) em
// vez de só descrevê-la, e deixa o comando de uma linha como saída secundária para
// quem prefere o terminal.
function InstallBlock({ toast, onAdicionar }: { toast: ToastApi; onAdicionar?: () => void }) {
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(INSTALL_CMD);
      toast.success("Comando copiado.");
    } catch {
      toast.error("Não foi possível copiar. Selecione o comando e copie manualmente.");
    }
  };

  return (
    <div style={{ textAlign: "left", maxWidth: 620, margin: "0 auto" }}>
      {onAdicionar && (
        <div style={{ marginBottom: "var(--sp-3)" }}>
          <Button variant="primary" onClick={onAdicionar}>
            Adicionar servidor
          </Button>
          <p style={{ ...MUTED_12, margin: "var(--sp-2) 0 0" }}>
            O painel pergunta como você quer instalar, baixando um arquivo (Linux, Windows ou macOS)
            ou conectando por SSH, e explica o que cada caminho exige.
          </p>
        </div>
      )}
      <div
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
          gap: "var(--sp-2)",
          marginBottom: "var(--sp-2)",
        }}
      >
        <span style={MUTED_12}>Ou cole este comando no servidor (Linux)</span>
        <Button variant="ghost" onClick={copy}>
          Copiar
        </Button>
      </div>
      <pre
        style={{
          margin: 0,
          padding: "var(--sp-3)",
          background: "var(--bg-1)",
          border: "1px solid var(--border)",
          borderRadius: "var(--radius-sm)",
          overflowX: "auto",
          whiteSpace: "pre",
        }}
      >
        <code style={{ fontFamily: "var(--font-mono)", fontSize: "var(--fs-12)", color: "var(--text-1)" }}>
          {INSTALL_CMD}
        </code>
      </pre>
      <p style={{ ...MUTED_12, marginTop: "var(--sp-2)", marginBottom: 0 }}>
        Troque <code>&lt;CHAVE&gt;</code> pela chave de ingestão do host (na tabela de agentes). O
        endereço do gateway já vem preenchido.
      </p>
    </div>
  );
}

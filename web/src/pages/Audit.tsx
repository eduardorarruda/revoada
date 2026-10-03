// Auditoria (admin): quem alterou o quê, quando e com qual conteúdo.
// Somente leitura — a trilha é gravada pelo backend em toda rota de escrita.
import { Fragment, useCallback, useEffect, useMemo, useState } from "react";
import { Badge, Button, Card, DataTable, HelpPanel, PageHeader, type Column } from "../components";
import { isAdmin, listAudit, type AuditEntry } from "../api";
import { formatDateTime } from "../format";
import { help } from "../help";

const PAGE_SIZE = 100;

// Métodos HTTP traduzidos para o que o usuário entende. A trilha guarda o método
// porque é o que a API usa; a tela mostra a INTENÇÃO.
const ACTION_LABEL: Record<string, string> = {
  POST: "Criou",
  PUT: "Alterou",
  PATCH: "Alterou",
  DELETE: "Removeu",
};

// Nome amigável de cada recurso. Recurso desconhecido (tela nova) cai no próprio
// identificador — a trilha nunca fica em branco por falta de tradução.
const RESOURCE_LABEL: Record<string, string> = {
  "alert-rules": "Regras de alerta",
  alerts: "Alertas",
  agents: "Chaves de agente",
  audit: "Auditoria",
  auth: "Conta / sessão",
  dashboards: "Dashboards",
  discovery: "Descoberta",
  "host-thresholds": "Limiares de saúde",
  hosts: "Servidores",
  journeys: "Jornadas",
  logs: "Logs",
  notify: "Canais de alerta",
  provision: "Provisionamento",
  "server-groups": "Grupos de servidores",
  "site-checks": "Websites",
  traces: "Traces",
  tv: "TVs & Playlists",
  users: "Usuários",
  agent: "Agente",
  query: "Consulta",
};

function resourceLabel(r: string): string {
  return RESOURCE_LABEL[r] ?? r;
}

// Data/hora completa no HORÁRIO DE BRASÍLIA — auditoria exige precisão, não "há 2h",
// e um registro de auditoria lido numa máquina em UTC não pode divergir 3 h do que
// outro operador vê na mesma tela.
function fmtWhen(iso: string): string {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return formatDateTime(iso);
}

// localDayKey/dayRange: o filtro de dia trabalha no fuso do navegador (o que o
// usuário vê na tela) e envia os limites em ISO/UTC para o backend.
function localDayKey(d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}
function dayRange(dayKey: string): { from: string; to: string } {
  const [y, m, d] = dayKey.split("-").map(Number);
  const from = new Date(y, m - 1, d, 0, 0, 0, 0);
  const to = new Date(y, m - 1, d + 1, 0, 0, 0, 0);
  return { from: from.toISOString(), to: to.toISOString() };
}

export function Audit() {
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [resources, setResources] = useState<string[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(0);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string>();
  const [helpOpen, setHelpOpen] = useState(false);

  // Filtros
  const [actor, setActor] = useState("");
  const [resource, setResource] = useState("");
  const [method, setMethod] = useState("");
  const [day, setDay] = useState("");
  const today = localDayKey(new Date());

  // A trilha é admin-only. Sem isto a tela de um usuário comum ainda dispararia a
  // consulta, que voltaria 403 — barulho inútil no servidor e no console.
  const admin = isAdmin();

  const load = useCallback(() => {
    if (!admin) return;
    setLoading(true);
    const range = day ? dayRange(day) : {};
    listAudit({ actor, resource, method, ...range, limit: PAGE_SIZE, offset: page * PAGE_SIZE })
      .then((r) => {
        setEntries(r.entries ?? []);
        setTotal(r.total ?? 0);
        setResources(r.resources ?? []);
        setErr(undefined);
      })
      .catch((e) => {
        console.error("listAudit falhou", e);
        setErr("Não foi possível carregar a trilha de auditoria.");
      })
      .finally(() => setLoading(false));
  }, [admin, actor, resource, method, day, page]);

  useEffect(() => {
    load();
  }, [load]);

  // Trocar um filtro sempre volta para a 1ª página — senão o usuário veria "vazio"
  // por estar num offset que não existe no novo recorte.
  const setFilter = (fn: () => void) => {
    fn();
    setPage(0);
  };

  const columns = useMemo<Column<AuditEntry>[]>(
    () => [
      {
        key: "created_at",
        label: "Quando",
        width: "150px",
        sortable: true,
        sortValue: (e) => e.created_at,
        render: (e) => <span style={{ fontVariantNumeric: "tabular-nums" }}>{fmtWhen(e.created_at)}</span>,
      },
      {
        key: "actor",
        label: "Quem",
        width: "160px",
        sortable: true,
        sortValue: (e) => e.actor_name,
        render: (e) => (
          <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
            <span>{e.actor_name || "—"}</span>
            {e.actor_role && (
              <span style={{ color: "var(--text-3)", fontSize: "var(--fs-12)" }}>{e.actor_role}</span>
            )}
          </div>
        ),
      },
      {
        key: "action",
        label: "Ação",
        width: "110px",
        render: (e) => (
          <Badge state={e.method === "DELETE" ? "crit" : e.method === "POST" ? "ok" : "info"}>
            {ACTION_LABEL[e.method] ?? e.method}
          </Badge>
        ),
      },
      {
        key: "resource",
        label: "Onde",
        width: "150px",
        sortable: true,
        sortValue: (e) => e.resource,
        render: (e) => (
          <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
            <span>{resourceLabel(e.resource)}</span>
            {e.target && (
              <span style={{ color: "var(--text-3)", fontSize: "var(--fs-12)", wordBreak: "break-all" }}>
                {e.target}
              </span>
            )}
          </div>
        ),
      },
      {
        key: "status",
        label: "Resultado",
        width: "124px",
        render: (e) =>
          e.status >= 400 ? (
            <Badge state="crit">falhou ({e.status})</Badge>
          ) : (
            <Badge state="ok">ok</Badge>
          ),
      },
      {
        key: "ip",
        label: "Origem",
        width: "116px",
        render: (e) => <span style={{ color: "var(--text-3)" }}>{e.ip || "—"}</span>,
      },
    ],
    [],
  );

  const pages = Math.ceil(total / PAGE_SIZE);
  const filtering = Boolean(actor || resource || method || day);

  // O menu já esconde o item; esta guarda cobre quem chega pela URL direta, com
  // uma explicação em vez de um erro seco de permissão.
  if (!admin) {
    return (
      <div className="page stack">
        <PageHeader title="Auditoria" subtitle="Registro de alterações do painel." />
        <Card>
          <p style={{ color: "var(--text-2)", margin: 0 }}>
            Esta tela é exclusiva de administradores, ela mostra o que todos os usuários alteraram no
            painel. Se você precisa consultar a trilha, peça a um administrador.
          </p>
        </Card>
      </div>
    );
  }

  return (
    // .page dá a margem lateral e o limite de largura que todas as telas usam
    // (o shell não aplica padding); .stack só cuida do espaço entre os blocos.
    <div className="page stack">
      <PageHeader
        title="Auditoria"
        subtitle="Registro de todas as alterações feitas no painel: quem fez, quando e o quê."
        onHelp={() => setHelpOpen(true)}
      />

      <Card>
        <p style={{ color: "var(--text-2)", marginTop: 0 }}>
          Cada linha é uma alteração gravada automaticamente pelo sistema. Clique numa linha para ver
          exatamente o que foi enviado. Senhas, chaves SSH e tokens aparecem sempre ocultos, nem a
          auditoria guarda segredo. Consultas e telas apenas visualizadas não entram aqui: a trilha
          registra alterações, não navegação.
        </p>

        <div className="row" style={{ gap: "var(--sp-3)", alignItems: "flex-end", flexWrap: "wrap" }}>
          <label className="stack" style={{ gap: 4 }}>
            <span style={{ fontSize: "var(--fs-12)", color: "var(--text-3)" }}>Quem</span>
            <input
              className="field"
              placeholder="nome do usuário"
              value={actor}
              onChange={(e) => setFilter(() => setActor(e.target.value))}
            />
          </label>
          <label className="stack" style={{ gap: 4 }}>
            <span style={{ fontSize: "var(--fs-12)", color: "var(--text-3)" }}>Onde</span>
            <select
              className="field"
              value={resource}
              onChange={(e) => setFilter(() => setResource(e.target.value))}
            >
              <option value="">todos os recursos</option>
              {resources.map((r) => (
                <option key={r} value={r}>
                  {resourceLabel(r)}
                </option>
              ))}
            </select>
          </label>
          <label className="stack" style={{ gap: 4 }}>
            <span style={{ fontSize: "var(--fs-12)", color: "var(--text-3)" }}>Ação</span>
            <select className="field" value={method} onChange={(e) => setFilter(() => setMethod(e.target.value))}>
              <option value="">todas</option>
              <option value="POST">Criou</option>
              <option value="PUT">Alterou</option>
              <option value="DELETE">Removeu</option>
            </select>
          </label>
          <label className="stack" style={{ gap: 4 }}>
            <span style={{ fontSize: "var(--fs-12)", color: "var(--text-3)" }}>Dia</span>
            <input
              type="date"
              className="field"
              value={day}
              max={today}
              onChange={(e) => setFilter(() => setDay(e.target.value))}
            />
          </label>
          {filtering && (
            <Button
              variant="ghost"
              onClick={() =>
                setFilter(() => {
                  setActor("");
                  setResource("");
                  setMethod("");
                  setDay("");
                })
              }
            >
              limpar filtros
            </Button>
          )}
        </div>
      </Card>

      <Card>
        <DataTable
          columns={columns}
          rows={entries}
          keyFn={(e) => e.id}
          loading={loading}
          error={err}
          fit
          initialSort={{ key: "created_at", dir: "desc" }}
          expandable={(e) => <PayloadDetail entry={e} />}
          empty={
            filtering
              ? "Nenhuma alteração encontrada com estes filtros. Tente ampliar o período ou limpar os filtros."
              : "Nenhuma alteração registrada ainda. Assim que alguém criar, editar ou remover algo no painel, aparecerá aqui."
          }
        />
        {pages > 1 && (
          <div
            className="row"
            style={{ justifyContent: "space-between", alignItems: "center", marginTop: "var(--sp-3)" }}
          >
            <span style={{ color: "var(--text-3)", fontSize: "var(--fs-12)" }}>
              {total} alterações · página {page + 1} de {pages}
            </span>
            <div className="row" style={{ gap: "var(--sp-2)" }}>
              <Button variant="ghost" disabled={page === 0} onClick={() => setPage((p) => Math.max(0, p - 1))}>
                ← anteriores
              </Button>
              <Button
                variant="ghost"
                disabled={page + 1 >= pages}
                onClick={() => setPage((p) => p + 1)}
              >
                próximas →
              </Button>
            </div>
          </div>
        )}
      </Card>

      <HelpPanel
        open={helpOpen}
        onClose={() => setHelpOpen(false)}
        title={help.pages.audit.title}
        sections={[
          { heading: "O que é esta tela", body: help.pages.audit.what },
          {
            heading: "Como usar",
            body: (
              <ol className="help-section__how">
                {help.pages.audit.how.map((h, i) => (
                  <li key={i}>{h}</li>
                ))}
              </ol>
            ),
          },
        ]}
      />
    </div>
  );
}

// PayloadDetail mostra o conteúdo enviado na alteração. É o "o quê" da auditoria:
// os campos que a pessoa preencheu, já sem segredos.
function PayloadDetail({ entry }: { entry: AuditEntry }) {
  const fields = Object.entries(entry.payload ?? {});
  return (
    <div className="stack" style={{ gap: "var(--sp-2)" }}>
      <div style={{ color: "var(--text-3)", fontSize: "var(--fs-12)" }}>
        {entry.method} {entry.path}
      </div>
      {fields.length === 0 ? (
        <p style={{ color: "var(--text-3)", margin: 0 }}>
          Esta ação não enviou conteúdo, o que ela fez está no recurso e no alvo acima (por exemplo,
          uma remoção).
        </p>
      ) : (
        // Grade em vez de <table>: a linha expandida já vive DENTRO da tabela da
        // listagem, e uma tabela aninhada herdaria .dtable (zebra, hover e bordas
        // do pai vazando para cá). A grade fica imune a essa cascata.
        <dl
          style={{
            display: "grid",
            gridTemplateColumns: "minmax(120px, 200px) 1fr",
            gap: "var(--sp-1) var(--sp-3)",
            margin: 0,
          }}
        >
          {fields.map(([k, v]) => (
            <Fragment key={k}>
              <dt style={{ color: "var(--text-3)", overflowWrap: "anywhere" }}>{k}</dt>
              <dd
                style={{
                  margin: 0,
                  fontFamily: "var(--font-mono)",
                  fontSize: "var(--fs-12)",
                  // pre-wrap preserva a identação do JSON de objetos/listas — sem
                  // isto tudo colapsa numa linha só e vira ilegível.
                  whiteSpace: "pre-wrap",
                  overflowWrap: "anywhere",
                }}
              >
                {fmtValue(v)}
              </dd>
            </Fragment>
          ))}
        </dl>
      )}
    </div>
  );
}

// fmtValue deixa legível o valor de um campo do payload (objetos viram JSON
// identado; booleanos viram sim/não).
function fmtValue(v: unknown): string {
  if (v === null || v === undefined) return "—";
  if (typeof v === "boolean") return v ? "sim" : "não";
  if (typeof v === "object") return JSON.stringify(v, null, 2);
  return String(v);
}

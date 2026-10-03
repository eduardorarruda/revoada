// Gestão de dashboards (UX.5): lista, busca, criar, apagar e "Visão do Host" (starter).
import { useCallback, useEffect, useState, type ReactNode } from "react";
import { LayoutDashboard } from "lucide-react";
import {
  Badge,
  Button,
  Card,
  ConfirmDialog,
  EmptyState,
  FormField,
  HelpPanel,
  Modal,
  PageHeader,
  Skeleton,
  useToast,
  IconButton,
  ActionIcons,
} from "../components";
import {
  createDashboard,
  deleteDashboard,
  listDashboards,
  listHosts,
  starterDashboard,
  type DashboardModel,
  type HostDetail,
} from "../api";
import { help } from "../help";

type DashboardMeta = { uid: string; title: string; folder: string; version: number };

// Reconhece os dashboards de host gerados automaticamente ("Visão do Host"): o UID
// sempre começa com "host-" (por-servidor "host-<host>" e o genérico "host-visao-geral").
// Esses ficam destacados na lista. Espelha IsHostDashboard do backend.
function isHostDashboard(uid: string): boolean {
  return uid.startsWith("host-");
}

// Seções do HelpPanel a partir de help.pages.dashboards (padrão das telas da Fase 3).
function dashboardsHelpSections(): { heading: string; body: ReactNode }[] {
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

// Transforma o título num uid legível (minúsculas, hífens, sem acentos).
function slugify(title: string): string {
  const base = title
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  return base || "dashboard";
}

function go(hash: string) {
  location.hash = hash;
}

export function Dashboards() {
  const toast = useToast();
  const [items, setItems] = useState<DashboardMeta[] | null>(null);
  const [failed, setFailed] = useState(false);
  const [search, setSearch] = useState("");
  const [helpOpen, setHelpOpen] = useState(false);

  // Modais.
  const [createOpen, setCreateOpen] = useState(false);
  const [starterOpen, setStarterOpen] = useState(false);
  const [toDelete, setToDelete] = useState<DashboardMeta | null>(null);

  const load = useCallback(() => {
    setFailed(false);
    setItems(null);
    listDashboards()
      .then((r) => setItems(r.dashboards))
      .catch(() => {
        setFailed(true);
        setItems([]);
        toast.error("Não foi possível carregar os dashboards.");
      });
  }, [toast]);

  useEffect(() => {
    load();
  }, [load]);

  const filtered = (items ?? []).filter((d) => {
    const q = search.trim().toLowerCase();
    if (!q) return true;
    return d.title.toLowerCase().includes(q) || d.folder.toLowerCase().includes(q);
  });

  async function handleDelete() {
    const d = toDelete;
    if (!d) return;
    setToDelete(null);
    try {
      await deleteDashboard(d.uid);
      toast.success(`Dashboard "${d.title}" apagado.`);
      load();
    } catch {
      toast.error("Não foi possível apagar o dashboard.");
    }
  }

  return (
    <div className="page">
      <PageHeader
        title="Dashboards"
        subtitle="Painéis do seu ambiente"
        onHelp={() => setHelpOpen(true)}
        actions={
          <>
            <Button variant="primary" onClick={() => setStarterOpen(true)}>
              Criar Visão do Host
            </Button>
            <Button onClick={() => setCreateOpen(true)}>Dashboard em branco</Button>
          </>
        }
      />

      {items !== null && (items.length > 0 || search) && (
        <input
          className="field"
          type="search"
          placeholder="Buscar por título ou pasta…"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          aria-label="Buscar dashboards"
          style={{ maxWidth: 360, marginBottom: "var(--sp-4)" }}
        />
      )}

      {/* Carregando */}
      {items === null && (
        <div className="stack">
          {[0, 1, 2].map((i) => (
            <Card key={i}>
              <Skeleton height={20} width="40%" />
              <div style={{ marginTop: "var(--sp-3)" }}>
                <Skeleton height={14} width="25%" />
              </div>
            </Card>
          ))}
        </div>
      )}

      {/* Erro de carregamento */}
      {failed && items !== null && (
        <EmptyState
          title="Falha ao carregar"
          body="Não foi possível buscar os dashboards agora."
          action={{ label: "Tentar novamente", onClick: load }}
        />
      )}

      {/* Vazio (sem nenhum dashboard) */}
      {!failed && items !== null && items.length === 0 && (
        <EmptyState
          icon={<LayoutDashboard size={40} aria-hidden="true" />}
          title="Nenhum dashboard ainda"
          body="Um dashboard reúne painéis (gráficos e números) numa tela só. O jeito mais rápido de começar é a Visão do Host: escolha um servidor e o Revoada monta um dashboard pronto com o servidor inteiro, CPU, memória, disco, rede e tempo no ar."
          steps={[
            "Clique em \"Criar Visão do Host\" (recomendado)",
            "Escolha o servidor que já está reportando",
            "Prefere montar do zero? Use \"Dashboard em branco\"",
          ]}
          action={{ label: "Criar Visão do Host", onClick: () => setStarterOpen(true) }}
        />
      )}

      {/* Busca sem resultado */}
      {!failed && items !== null && items.length > 0 && filtered.length === 0 && (
        <EmptyState
          title="Nenhum resultado"
          body={`Nada encontrado para "${search}". Tente outro termo.`}
        />
      )}

      {/* Lista de dashboards: um por linha. Os de host ("Visão do Host") ficam destacados. */}
      {filtered.length > 0 && (
        <div className="stack">
          {filtered.map((d) => {
            const isHost = isHostDashboard(d.uid);
            return (
              <div
                key={d.uid}
                className="card"
                style={isHost ? { borderLeft: "3px solid var(--accent)" } : undefined}
              >
                <div
                  style={{
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "space-between",
                    gap: "var(--sp-3)",
                    flexWrap: "wrap",
                  }}
                >
                  <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)", minWidth: 0 }}>
                    <div style={{ display: "flex", alignItems: "center", gap: "var(--sp-2)", flexWrap: "wrap" }}>
                      {isHost && <LayoutDashboard size={16} aria-hidden="true" style={{ color: "var(--accent)" }} />}
                      <strong style={{ fontSize: "var(--fs-16)" }}>{d.title}</strong>
                    </div>
                    <div style={{ display: "flex", alignItems: "center", gap: "var(--sp-2)", flexWrap: "wrap" }}>
                      {isHost && <Badge state="info">Visão do Host</Badge>}
                      <Badge state="neutral">{d.folder || "Geral"}</Badge>
                      <span style={{ color: "var(--text-3)", fontSize: "var(--fs-12)" }}>v{d.version}</span>
                    </div>
                  </div>
                  <div style={{ display: "flex", gap: "var(--sp-2)", flexWrap: "wrap" }}>
                    <IconButton icon={ActionIcons.open} label="Abrir" onClick={() => go(`#/d/${d.uid}`)} />
                    <IconButton icon={ActionIcons.edit} label="Editar" onClick={() => go(`#/d/${d.uid}/edit`)} />
                    <IconButton
                      icon={ActionIcons.history}
                      label="Histórico"
                      onClick={() => go(`#/d/${d.uid}/versions`)}
                    />
                    <IconButton icon={ActionIcons.delete} label="Apagar" onClick={() => setToDelete(d)} />
                  </div>
                </div>
              </div>
            );
          })}
        </div>
      )}

      <HelpPanel
        open={helpOpen}
        onClose={() => setHelpOpen(false)}
        title={help.pages.dashboards.title}
        sections={dashboardsHelpSections()}
      />

      {createOpen && (
        <CreateModal
          onClose={() => setCreateOpen(false)}
          onCreated={(uid) => {
            setCreateOpen(false);
            go(`#/d/${uid}/edit`);
          }}
          toast={toast}
        />
      )}

      {starterOpen && (
        <StarterModal
          onClose={() => setStarterOpen(false)}
          onCreated={(uid) => {
            setStarterOpen(false);
            go(`#/d/${uid}`);
          }}
          toast={toast}
        />
      )}

      <ConfirmDialog
        open={toDelete !== null}
        onCancel={() => setToDelete(null)}
        onConfirm={handleDelete}
        verb="Apagar"
        target={toDelete?.title ?? ""}
        consequences="O histórico de versões também é removido."
        danger
      />
    </div>
  );
}

type ToastApi = ReturnType<typeof useToast>;

// Modal "Novo dashboard": título + pasta → cria modelo vazio e navega para a edição.
function CreateModal({
  onClose,
  onCreated,
  toast,
}: {
  onClose: () => void;
  onCreated: (uid: string) => void;
  toast: ToastApi;
}) {
  const [title, setTitle] = useState("");
  const [folder, setFolder] = useState("Geral");
  const [error, setError] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);

  async function submit() {
    const t = title.trim();
    if (!t) {
      setError("Informe um título.");
      return;
    }
    setError(undefined);
    setBusy(true);
    const base = slugify(t);
    // Modelo vazio; o editor (UX.5b) preenche os painéis.
    const model: DashboardModel = {
      uid: base,
      title: t,
      variables: [],
      timeRange: { from: "now-6h", to: "now" },
      panels: [],
    };
    // Tenta o uid; em conflito (409), acrescenta sufixo -2, -3…
    for (let n = 1; n <= 20; n++) {
      const uid = n === 1 ? base : `${base}-${n}`;
      try {
        const r = await createDashboard({ uid, title: t, folder: folder.trim() || "Geral", model: { ...model, uid } });
        toast.success(`Dashboard "${t}" criado.`);
        onCreated(r.uid);
        return;
      } catch (e) {
        const msg = e instanceof Error ? e.message : "";
        if (msg.startsWith("409")) continue; // uid já existe → tenta o próximo
        setBusy(false);
        setError("Não foi possível criar o dashboard.");
        toast.error("Não foi possível criar o dashboard.");
        return;
      }
    }
    setBusy(false);
    setError("Não foi possível gerar um endereço único. Tente outro título.");
  }

  return (
    <Modal
      open
      onClose={onClose}
      title="Dashboard em branco (avançado)"
      footer={
        <>
          <Button onClick={onClose}>Cancelar</Button>
          <Button variant="primary" onClick={submit} disabled={busy}>
            {busy ? "Criando…" : "Criar"}
          </Button>
        </>
      }
    >
      <p style={{ marginTop: 0, color: "var(--text-2)" }}>
        Começa vazio, para você montar cada painel manualmente. Para um dashboard pronto do
        servidor inteiro, prefira a <strong>Visão do Host</strong>.
      </p>
      <FormField label="Título" help={help.fields["dashboard.title"]} error={error} required>
        <input
          className="field"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          placeholder="Ex.: Visão da Produção"
          autoFocus
        />
      </FormField>
      <FormField label="Pasta" help={help.fields["dashboard.folder"]}>
        <input
          className="field"
          value={folder}
          onChange={(e) => setFolder(e.target.value)}
          placeholder="Geral"
        />
      </FormField>
    </Modal>
  );
}

// Modal "Criar Visão do Host": escolhe um host e gera o dashboard starter.
function StarterModal({
  onClose,
  onCreated,
  toast,
}: {
  onClose: () => void;
  onCreated: (uid: string) => void;
  toast: ToastApi;
}) {
  const [hosts, setHosts] = useState<HostDetail[] | null>(null);
  const [host, setHost] = useState("");
  const [error, setError] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    listHosts()
      .then((r) => {
        setHosts(r.hosts);
        if (r.hosts.length > 0) setHost(r.hosts[0].hostname);
      })
      .catch(() => {
        setHosts([]);
        toast.error("Não foi possível carregar os hosts.");
      });
  }, [toast]);

  async function submit() {
    if (!host) {
      setError("Escolha um host.");
      return;
    }
    setError(undefined);
    setBusy(true);
    try {
      const r = await starterDashboard(host);
      const chosen = (hosts ?? []).find((h) => h.hostname === host);
      const label = chosen?.display_name?.trim() || host;
      toast.success(`Visão do Host "${label}" criada.`);
      onCreated(r.uid);
    } catch {
      setBusy(false);
      setError("Não foi possível gerar a Visão do Host.");
      toast.error("Não foi possível gerar a Visão do Host.");
    }
  }

  const noHosts = hosts !== null && hosts.length === 0;

  return (
    <Modal
      open
      onClose={onClose}
      title="Criar Visão do Host"
      footer={
        <>
          <Button onClick={onClose}>Cancelar</Button>
          <Button variant="primary" onClick={submit} disabled={busy || noHosts}>
            {busy ? "Gerando…" : "Gerar"}
          </Button>
        </>
      }
    >
      <p style={{ marginTop: 0, color: "var(--text-2)" }}>
        Caminho recomendado. Escolha um servidor e o Revoada gera um dashboard pronto com o
        servidor inteiro: CPU, memória, disco, rede e tempo no ar. Você pode editar depois.
      </p>
      <FormField
        label="Servidor"
        help={help.fields["dashboard.starterHost"]}
        error={error}
        hint={noHosts ? "Nenhum host reportando ainda. Instale o agente num host primeiro." : undefined}
        required
      >
        <select
          className="field"
          value={host}
          onChange={(e) => setHost(e.target.value)}
          disabled={hosts === null || noHosts}
        >
          {hosts === null && <option>Carregando…</option>}
          {noHosts && <option>Nenhum host disponível</option>}
          {(hosts ?? []).map((h) => (
            <option key={h.hostname} value={h.hostname}>
              {h.display_name?.trim() || h.hostname}
            </option>
          ))}
        </select>
      </FormField>
    </Modal>
  );
}

// TVs & Playlists (UX.7): gestão de telões de parede.
// Aba "TVs": tokens somente-leitura (um por TV) em tabela, com playlist inline,
// abrir/recarregar/revogar. Aba "Playlists": criar e listar sequências de telas.
// O backend não expõe DELETE/UPDATE de playlist nem devolve a URL de tokens já
// existentes — o valor em claro só aparece na criação; guardamos essas URLs em
// memória (createdUrls) para a sessão atual.
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Film, Tv } from "lucide-react";
import { usePolling } from "../hooks/usePolling";
import {
  Badge,
  Button,
  Card,
  ConfirmDialog,
  DataTable,
  EmptyState,
  FormField,
  HelpPanel,
  InfoTip,
  Modal,
  PageHeader,
  Skeleton,
  Tabs,
  useToast,
  IconButton,
  ActionIcons,
  type Column,
} from "../components";
import {
  listDashboards,
  tvCreatePlaylist,
  tvCreateToken,
  tvDeletePlaylist,
  tvDeleteToken,
  tvForceReload,
  tvRegenerate,
  tvListPlaylists,
  tvRevoke,
  tvSetPlaylist,
  tvTokens,
  WALL_UID,
  type Playlist,
  type PlaylistItem,
  type TVTokenInfo,
} from "../api";
import { help } from "../help";

type DashOpt = { uid: string; title: string };

// Monta as seções do HelpPanel a partir de help.pages.tvs (mesmo padrão das demais telas).
function tvHelpSections(): { heading: string; body: ReactNode }[] {
  const p = help.pages.tvs;
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

// URL de quiosque a abrir na TV: usa hash-routing (#/tv/{token}).
function kioskUrl(path: string): string {
  return `${window.location.origin}/#${path}`;
}

// Copia texto para a área de transferência. Em produção o painel roda em HTTP
// (contexto NÃO seguro), onde `navigator.clipboard` não existe — por isso o
// fallback via <textarea> + execCommand, que ainda funciona em HTTP.
async function copyToClipboard(text: string): Promise<boolean> {
  try {
    if (window.isSecureContext && navigator.clipboard) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // cai no fallback abaixo
  }
  try {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.top = "-9999px";
    ta.setAttribute("readonly", "");
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand("copy");
    document.body.removeChild(ta);
    return ok;
  } catch {
    return false;
  }
}

// TV online = vista nos últimos 30s.
function isOnline(t: TVTokenInfo): boolean {
  return Date.now() - new Date(t.last_seen).getTime() < 30000;
}

export function TVConsole() {
  const toast = useToast();
  const [tab, setTab] = useState<"tvs" | "playlists">("tvs");
  const [helpOpen, setHelpOpen] = useState(false);

  const [tokens, setTokens] = useState<TVTokenInfo[]>([]);
  const [playlists, setPlaylists] = useState<Playlist[]>([]);
  const [dashboards, setDashboards] = useState<DashOpt[]>([]);
  const [loading, setLoading] = useState(true);

  // Link de quiosque de uma TV, montado a partir do token guardado. "" em tokens
  // legados (criados antes de o valor em claro ser persistido) — nesse caso o
  // botão de copiar regenera o link sob demanda.
  const tvLink = (t: TVTokenInfo) => (t.token ? kioskUrl(`/tv/${t.token}`) : "");

  // Modais / confirmação.
  const [tokenModal, setTokenModal] = useState(false);
  const [playlistModal, setPlaylistModal] = useState(false);
  const [revokeTarget, setRevokeTarget] = useState<TVTokenInfo | null>(null);
  const [deleteTokenTarget, setDeleteTokenTarget] = useState<TVTokenInfo | null>(null);
  const [deletePlaylistTarget, setDeletePlaylistTarget] = useState<Playlist | null>(null);

  const dashTitle = useMemo(() => {
    const m = new Map(dashboards.map((d) => [d.uid, d.title]));
    return (uid: string) =>
      uid === WALL_UID ? "Mural de Saúde" : (m.get(uid) ?? uid);
  }, [dashboards]);

  // --- carregamento ---
  const loadTokens = () => tvTokens().then((r) => setTokens(r.tokens));
  const loadPlaylists = () => tvListPlaylists().then((r) => setPlaylists(r.playlists));

  useEffect(() => {
    let alive = true;
    Promise.all([tvTokens(), tvListPlaylists(), listDashboards()])
      .then(([tk, pl, db]) => {
        if (!alive) return;
        setTokens(tk.tokens);
        setPlaylists(pl.playlists);
        setDashboards(db.dashboards.map((d) => ({ uid: d.uid, title: d.title })));
      })
      .catch(() => toast.error("Falha ao carregar TVs e playlists."))
      .finally(() => alive && setLoading(false));
    return () => {
      alive = false;
    };
  }, [toast]);

  // Atualiza o estado das TVs (online/visto por último) a cada 5 s. Era um
  // `setInterval` cru, que ignorava a visibilidade da aba: um console de TVs deixado
  // aberto num monitor secundário mantinha 17.280 requisições/dia contra uma tabela
  // que ninguém estava lendo. `usePolling` já pausa em aba escondida.
  // A primeira invocação é pulada porque o Promise.all acima já está buscando estes
  // mesmos tokens — sem a guarda seriam duas chamadas idênticas no carregamento.
  const primeiroTick = useRef(true);
  const refreshTokens = useCallback(() => {
    if (primeiroTick.current) {
      primeiroTick.current = false;
      return;
    }
    tvTokens()
      .then((r) => setTokens(r.tokens))
      .catch(() => {});
  }, []);
  usePolling(refreshTokens, 5000, [refreshTokens]);

  // --- ações de token ---
  const changePlaylist = async (t: TVTokenInfo, playlistId: number) => {
    try {
      await tvSetPlaylist(t.id, playlistId);
      await loadTokens();
      toast.success(
        playlistId === 0
          ? `Playlist removida da TV ${t.name}.`
          : `Playlist atribuída à TV ${t.name}.`,
      );
    } catch {
      toast.error("Não foi possível alterar a playlist.");
    }
  };

  const copyLink = async (t: TVTokenInfo) => {
    // TVs novas já têm o token guardado → copia direto. TVs legadas (sem token em
    // claro) regeneram o link sob demanda: o antigo deixa de valer e o novo é copiado.
    let url = tvLink(t);
    let regenerated = false;
    if (!url) {
      try {
        const r = await tvRegenerate(t.id);
        url = kioskUrl(r.url);
        regenerated = true;
        await loadTokens();
      } catch {
        toast.error("Não foi possível gerar o link da TV.");
        return;
      }
    }
    const ok = await copyToClipboard(url);
    if (!ok) {
      toast.error("Não foi possível copiar. Abra a coluna URL e copie o link manualmente.");
      return;
    }
    if (regenerated) toast.success(`Novo link da TV ${t.name} gerado e copiado (o link antigo deixou de valer).`);
    else toast.success(`Link da TV ${t.name} copiado.`);
  };

  const reload = async (t: TVTokenInfo) => {
    try {
      await tvForceReload(t.id);
      toast.success(`Sinal de recarregar enviado para ${t.name}.`);
    } catch {
      toast.error("Falha ao recarregar a TV.");
    }
  };

  const doRevoke = async (t: TVTokenInfo) => {
    setRevokeTarget(null);
    try {
      await tvRevoke(t.id);
      await loadTokens();
      toast.success(`TV ${t.name} revogada.`);
    } catch {
      toast.error("Falha ao revogar a TV.");
    }
  };

  const doDeleteToken = async (t: TVTokenInfo) => {
    setDeleteTokenTarget(null);
    try {
      await tvDeleteToken(t.id);
      await loadTokens();
      toast.success(`TV ${t.name} apagada.`);
    } catch {
      toast.error("Falha ao apagar a TV.");
    }
  };

  const doDeletePlaylist = async (p: Playlist) => {
    setDeletePlaylistTarget(null);
    try {
      await tvDeletePlaylist(p.id);
      // As TVs que usavam a playlist voltam ao dashboard fixo (playlist_id muda).
      await Promise.all([loadPlaylists(), loadTokens()]);
      toast.success(`Playlist ${p.name} apagada.`);
    } catch {
      toast.error("Falha ao apagar a playlist.");
    }
  };

  // --- colunas da aba TVs ---
  const tokenColumns: Column<TVTokenInfo>[] = [
    {
      key: "name",
      label: "Nome",
      render: (t) => (
        <span className="row" style={{ gap: "var(--sp-2)" }}>
          <span>{t.name}</span>
          <Badge state={t.revoked ? "crit" : isOnline(t) ? "ok" : "neutral"}>
            {t.revoked ? "revogada" : isOnline(t) ? "online" : "offline"}
          </Badge>
        </span>
      ),
    },
    { key: "location", label: "Local", render: (t) => t.location || "—", hideOnMobile: true },
    { key: "dashboard_uid", label: "Dashboard", render: (t) => dashTitle(t.dashboard_uid) },
    {
      key: "playlist",
      label: "Playlist",
      render: (t) => (
        <select
          className="field"
          aria-label={`Playlist da TV ${t.name}`}
          value={String(t.playlist_id ?? 0)}
          disabled={t.revoked}
          onChange={(e) => changePlaylist(t, Number(e.target.value))}
        >
          <option value="0">Nenhuma (dashboard fixo)</option>
          {playlists.map((p) => (
            <option key={p.id} value={String(p.id)}>
              {p.name}
            </option>
          ))}
        </select>
      ),
    },
    {
      key: "url",
      label: "URL",
      hideOnMobile: true,
      render: (t) => {
        const url = tvLink(t);
        if (!url) return <span style={{ color: "var(--text-2)" }}>clique em Copiar link para gerar</span>;
        return (
          <a href={url} target="_blank" rel="noreferrer" style={{ color: "var(--accent)" }}>
            abrir na TV
          </a>
        );
      },
    },
  ];

  const tokenActions = (t: TVTokenInfo) => {
    const url = tvLink(t);
    return (
      <div className="row" style={{ gap: "var(--sp-2)" }}>
        <IconButton
          icon={ActionIcons.copyLink}
          label={url ? "Copiar link da TV" : "Gerar e copiar link da TV"}
          onClick={() => copyLink(t)}
        />
        {url && (
          <IconButton
            icon={ActionIcons.open}
            label="Abrir"
            onClick={() => window.open(url, "_blank", "noopener")}
          />
        )}
        <IconButton icon={ActionIcons.reload} label="Recarregar" onClick={() => reload(t)} />
        {!t.revoked && (
          <IconButton icon={ActionIcons.revoke} label="Revogar" onClick={() => setRevokeTarget(t)} />
        )}
        <IconButton icon={ActionIcons.delete} label="Apagar" onClick={() => setDeleteTokenTarget(t)} />
      </div>
    );
  };

  // --- ações da aba Playlists ---
  const playlistActions = (p: Playlist) => (
    <div className="row" style={{ gap: "var(--sp-2)" }}>
      <IconButton icon={ActionIcons.delete} label="Apagar" onClick={() => setDeletePlaylistTarget(p)} />
    </div>
  );

  // --- colunas da aba Playlists ---
  const playlistColumns: Column<Playlist>[] = [
    { key: "name", label: "Nome" },
    { key: "count", label: "Nº itens", render: (p) => p.items.length },
    {
      key: "items",
      label: "Itens",
      hideOnMobile: true,
      render: (p) => (
        <span style={{ color: "var(--text-2)" }}>
          {p.items.map((i) => `${dashTitle(i.dashboard_uid)} (${i.duration_seconds}s)`).join(" → ")}
        </span>
      ),
    },
  ];

  return (
    <div className="page">
      <PageHeader
        title="TVs & Playlists"
        subtitle="Telões de parede: cada token abre uma TV em modo quiosque"
        onHelp={() => setHelpOpen(true)}
        actions={
          tab === "tvs" ? (
            <Button variant="primary" onClick={() => setTokenModal(true)}>
              Nova TV
            </Button>
          ) : (
            <Button variant="primary" onClick={() => setPlaylistModal(true)}>
              Nova playlist
            </Button>
          )
        }
      />

      <Tabs
        tabs={[
          { id: "tvs", label: "TVs" },
          { id: "playlists", label: "Playlists" },
        ]}
        active={tab}
        onChange={(id) => setTab(id as "tvs" | "playlists")}
      />

      {loading ? (
        <Card>
          <Skeleton height={28} />
          <div style={{ marginTop: "var(--sp-3)" }}>
            <Skeleton height={120} />
          </div>
        </Card>
      ) : tab === "tvs" ? (
        <div style={{ marginTop: "var(--sp-4)" }}>
          <DataTable
            columns={tokenColumns}
            rows={tokens}
            keyFn={(t) => t.id}
            rowActions={tokenActions}
            empty={
              <EmptyState
                icon={<Tv size={32} strokeWidth={1.5} />}
                title="Nenhuma TV cadastrada"
                body="Gere um token somente-leitura para exibir um dashboard numa TV, sem login."
                steps={[
                  "Crie um token informando nome, local e dashboard",
                  "Abra a URL do token na própria TV (modo quiosque)",
                  "Opcional: atribua uma playlist para a TV alternar entre telas",
                ]}
                action={{ label: "Nova TV", onClick: () => setTokenModal(true) }}
              />
            }
          />
        </div>
      ) : (
        <div style={{ marginTop: "var(--sp-4)" }}>
          <DataTable
            columns={playlistColumns}
            rows={playlists}
            keyFn={(p) => p.id}
            rowActions={playlistActions}
            empty={
              <EmptyState
                icon={<Film size={32} strokeWidth={1.5} />}
                title="Nenhuma playlist criada"
                body="Playlists fazem a TV alternar sozinha entre vários dashboards."
                steps={[
                  "Crie uma playlist com 2+ dashboards",
                  "Crie/edite um token de TV",
                  "Atribua a playlist ao token na aba TVs",
                  "Abra a URL do token na TV",
                ]}
                action={{ label: "Nova playlist", onClick: () => setPlaylistModal(true) }}
              />
            }
          />
        </div>
      )}

      {tokenModal && (
        <TokenModal
          dashboards={dashboards}
          onClose={() => setTokenModal(false)}
          onCreated={async () => {
            // O token volta na listagem (coluna `token`), então basta recarregar:
            // o link já aparece na coluna URL e no botão de copiar.
            await loadTokens();
            toast.success("TV criada. Use o botão Copiar link para copiar o link e abrir na tela.");
            setTokenModal(false);
          }}
          onError={() => toast.error("Falha ao criar a TV.")}
        />
      )}

      {playlistModal && (
        <PlaylistModal
          dashboards={dashboards}
          onClose={() => setPlaylistModal(false)}
          onCreated={async () => {
            await loadPlaylists();
            toast.success("Playlist criada.");
            setPlaylistModal(false);
          }}
          onError={() => toast.error("Falha ao criar a playlist.")}
        />
      )}

      {revokeTarget && (
        <ConfirmDialog
          open
          danger
          verb="Revogar"
          target={`a TV ${revokeTarget.name}`}
          consequences="O link para de funcionar imediatamente e a tela fica sem acesso. Não dá para desfazer; você precisará criar um novo token."
          onCancel={() => setRevokeTarget(null)}
          onConfirm={() => doRevoke(revokeTarget)}
        />
      )}

      {deleteTokenTarget && (
        <ConfirmDialog
          open
          danger
          verb="Apagar"
          target={`a TV ${deleteTokenTarget.name}`}
          consequences="A TV é removida da lista definitivamente e o link para de funcionar. Não dá para desfazer; você precisará criar uma nova TV do zero."
          onCancel={() => setDeleteTokenTarget(null)}
          onConfirm={() => doDeleteToken(deleteTokenTarget)}
        />
      )}

      {deletePlaylistTarget && (
        <ConfirmDialog
          open
          danger
          verb="Apagar"
          target={`a playlist ${deletePlaylistTarget.name}`}
          consequences="As TVs que estavam usando esta playlist voltam ao dashboard fixo. Não dá para desfazer."
          onCancel={() => setDeletePlaylistTarget(null)}
          onConfirm={() => doDeletePlaylist(deletePlaylistTarget)}
        />
      )}

      <HelpPanel
        open={helpOpen}
        onClose={() => setHelpOpen(false)}
        title={help.pages.tvs.title}
        sections={tvHelpSections()}
      />
    </div>
  );
}

// --- Modal: nova TV (token) ---
function TokenModal({
  dashboards,
  onClose,
  onCreated,
  onError,
}: {
  dashboards: DashOpt[];
  onClose: () => void;
  onCreated: (r: { url: string }) => void | Promise<void>;
  onError: () => void;
}) {
  const [name, setName] = useState("");
  const [location, setLocation] = useState("");
  // Sem dashboards, a TV ainda pode ser criada como Health Wall (não depende de dashboard).
  const [dashboardUid, setDashboardUid] = useState(dashboards[0]?.uid ?? WALL_UID);
  const [saving, setSaving] = useState(false);
  const [touched, setTouched] = useState(false);

  const nameError = touched && !name.trim() ? "Informe um nome para a TV." : undefined;
  const valid = name.trim() !== "" && dashboardUid !== "";

  const submit = async () => {
    setTouched(true);
    if (!valid) return;
    setSaving(true);
    try {
      const r = await tvCreateToken(name.trim(), location.trim(), dashboardUid);
      await onCreated(r);
    } catch {
      onError();
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title="Nova TV"
      footer={
        <div className="row row--end">
          <Button onClick={onClose}>Cancelar</Button>
          <Button variant="primary" onClick={submit} disabled={saving}>
            {saving ? "Gerando…" : "Gerar token"}
          </Button>
        </div>
      }
    >
      <div className="stack">
        <FormField label="Nome" required help={help.fields["tv.token"]} error={nameError}>
          <input
            className="field"
            value={name}
            placeholder="Ex.: Telão da Recepção"
            onChange={(e) => setName(e.target.value)}
            autoFocus
          />
        </FormField>
        <FormField label="Local" hint="Onde a TV fica fisicamente. Ex.: Recepção, NOC.">
          <input
            className="field"
            value={location}
            placeholder="Ex.: Recepção"
            onChange={(e) => setLocation(e.target.value)}
          />
        </FormField>
        <FormField
          label="Tela"
          required
          hint="Um dashboard ou o Mural de Saúde (semáforo de todos os servidores)."
        >
          <select
            className="field"
            value={dashboardUid}
            onChange={(e) => setDashboardUid(e.target.value)}
          >
            <option value={WALL_UID}>Mural de Saúde, todos os servidores</option>
            {dashboards.map((d) => (
              <option key={d.uid} value={d.uid}>
                {d.title}
              </option>
            ))}
          </select>
        </FormField>
      </div>
    </Modal>
  );
}

// --- Modal: nova playlist ---
function PlaylistModal({
  dashboards,
  onClose,
  onCreated,
  onError,
}: {
  dashboards: DashOpt[];
  onClose: () => void;
  onCreated: () => void | Promise<void>;
  onError: () => void;
}) {
  const [name, setName] = useState("");
  const [items, setItems] = useState<PlaylistItem[]>([]);
  const [saving, setSaving] = useState(false);
  const [touched, setTouched] = useState(false);

  const noDashboards = dashboards.length === 0;
  const nameError = touched && !name.trim() ? "Dê um nome à playlist." : undefined;
  const itemsError = touched && items.length < 1 ? "Adicione ao menos um item." : undefined;
  const valid = name.trim() !== "" && items.length >= 1;

  const addItem = () =>
    setItems((xs) => [...xs, { dashboard_uid: dashboards[0]?.uid ?? "", duration_seconds: 30 }]);
  const removeItem = (i: number) => setItems((xs) => xs.filter((_, k) => k !== i));
  const patchItem = (i: number, patch: Partial<PlaylistItem>) =>
    setItems((xs) => xs.map((it, k) => (k === i ? { ...it, ...patch } : it)));
  const move = (i: number, dir: -1 | 1) =>
    setItems((xs) => {
      const j = i + dir;
      if (j < 0 || j >= xs.length) return xs;
      const next = [...xs];
      [next[i], next[j]] = [next[j], next[i]];
      return next;
    });

  const submit = async () => {
    setTouched(true);
    if (!valid) return;
    setSaving(true);
    try {
      await tvCreatePlaylist({ name: name.trim(), items });
      await onCreated();
    } catch {
      onError();
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      open
      wide
      onClose={onClose}
      title="Nova playlist"
      footer={
        <div className="row row--end">
          <button type="button" className="btn" onClick={onClose}>
            Cancelar
          </button>
          <button
            type="button"
            className="btn btn--primary"
            onClick={submit}
            disabled={saving || noDashboards}
          >
            Criar playlist
          </button>
        </div>
      }
    >
      <div className="stack">
        {noDashboards && (
          <p className="form-field__error" role="alert">
            Crie um dashboard antes de montar uma playlist.
          </p>
        )}
        <FormField label="Nome" required help={help.fields["playlist.name"]} error={nameError}>
          <input
            className="field"
            value={name}
            placeholder='Ex.: "Telão NOC"'
            onChange={(e) => setName(e.target.value)}
          />
        </FormField>

        <FormField
          label="Itens"
          help={help.fields["playlist.items"]}
          hint="Recomendado 2 ou mais telas para a TV alternar. A ordem define a sequência."
          error={itemsError}
        >
          <div className="stack">
            {items.length > 0 && (
              <div
                className="row"
                style={{ gap: "var(--sp-1)", color: "var(--text-2)", fontSize: "var(--fs-12)" }}
              >
                <span>Cada item: tela + quantos segundos fica no ar</span>
                <InfoTip text={help.fields["playlist.itemDuration"]} title="Duração do item" />
              </div>
            )}
            {items.map((it, i) => (
              <div key={i} className="row" style={{ gap: "var(--sp-2)", alignItems: "flex-end" }}>
                <div style={{ flex: "2 1 220px" }}>
                  <select
                    className="field"
                    aria-label={`Dashboard do item ${i + 1}`}
                    value={it.dashboard_uid}
                    onChange={(e) => patchItem(i, { dashboard_uid: e.target.value })}
                  >
                    {dashboards.map((d) => (
                      <option key={d.uid} value={d.uid}>
                        {d.title}
                      </option>
                    ))}
                  </select>
                </div>
                <div style={{ flex: "0 1 120px" }}>
                  <div className="row" style={{ gap: "var(--sp-2)" }}>
                    <input
                      className="field"
                      type="number"
                      min={1}
                      aria-label={`Segundos do item ${i + 1}`}
                      style={{ width: "5rem" }}
                      value={it.duration_seconds}
                      onChange={(e) =>
                        patchItem(i, { duration_seconds: Math.max(1, Number(e.target.value) || 0) })
                      }
                    />
                    <span style={{ color: "var(--text-2)" }}>s</span>
                  </div>
                </div>
                <Button
                  variant="ghost"
                  aria-label="Mover para cima"
                  title="Mover para cima"
                  onClick={() => move(i, -1)}
                  disabled={i === 0}
                >
                  ↑
                </Button>
                <Button
                  variant="ghost"
                  aria-label="Mover para baixo"
                  title="Mover para baixo"
                  onClick={() => move(i, 1)}
                  disabled={i === items.length - 1}
                >
                  ↓
                </Button>
                <Button variant="ghost" onClick={() => removeItem(i)}>
                  Remover
                </Button>
              </div>
            ))}
            <div>
              <Button variant="default" onClick={addItem} disabled={noDashboards}>
                + Adicionar item
              </Button>
            </div>
          </div>
        </FormField>
      </div>
    </Modal>
  );
}

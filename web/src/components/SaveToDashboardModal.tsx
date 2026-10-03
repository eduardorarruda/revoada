// SaveToDashboardModal — modal reutilizável para salvar um painel num dashboard
// existente (append) ou criar um novo. Importado direto onde é usado (Explore na
// Fase 3), não vai no index.tsx. Usa Modal/FormField/useToast do kit.
import { useEffect, useState } from "react";
import { Modal, FormField, Button, useToast } from "./index";
import {
  listDashboards,
  getDashboard,
  updateDashboard,
  createDashboard,
  type DashboardModel,
} from "../api";

// O painel a salvar tem exatamente o formato de um item de DashboardModel.panels.
export type SavePanel = DashboardModel["panels"][number];

// Gera um slug simples e único-o-bastante para o uid de um dashboard novo.
function slugify(title: string): string {
  const base = title
    .toLowerCase()
    .normalize("NFD")
    .replace(/[\u0300-\u036f]/g, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  const suffix = Date.now().toString(36).slice(-4);
  return `${base || "dashboard"}-${suffix}`;
}

// Reposiciona o painel: novo id incremental e gridPos padrão (empilhado).
function placePanel(panel: SavePanel, existing: SavePanel[]): SavePanel {
  const nextId = existing.reduce((m, p) => Math.max(m, p.id), 0) + 1;
  return { ...panel, id: nextId, gridPos: { x: 0, y: existing.length * 8, w: 12, h: 8 } };
}

export function SaveToDashboardModal({
  open,
  onClose,
  panel,
}: {
  open: boolean;
  onClose: () => void;
  panel: SavePanel;
}) {
  const toast = useToast();
  const [mode, setMode] = useState<"existing" | "new">("existing");
  const [dashboards, setDashboards] = useState<{ uid: string; title: string }[]>([]);
  const [uid, setUid] = useState("");
  const [title, setTitle] = useState("");
  const [error, setError] = useState<string | undefined>();
  const [saving, setSaving] = useState(false);

  // Carrega os dashboards existentes ao abrir; se não houver, força modo "novo".
  useEffect(() => {
    if (!open) return;
    setError(undefined);
    listDashboards()
      .then((r) => {
        setDashboards(r.dashboards);
        if (r.dashboards.length === 0) setMode("new");
        else {
          setMode("existing");
          setUid(r.dashboards[0].uid);
        }
      })
      .catch(() => {
        setDashboards([]);
        setMode("new");
      });
  }, [open]);

  async function save() {
    setError(undefined);
    try {
      setSaving(true);
      if (mode === "existing") {
        if (!uid) {
          setError("Escolha um dashboard.");
          return;
        }
        const dash = await getDashboard(uid);
        const placed = placePanel(panel, dash.model.panels);
        await updateDashboard(uid, {
          title: dash.title,
          folder: dash.folder,
          model: { ...dash.model, panels: [...dash.model.panels, placed] },
        });
        toast.success(`Painel adicionado a "${dash.title}".`);
      } else {
        const t = title.trim();
        if (!t) {
          setError("Informe um título para o novo dashboard.");
          return;
        }
        const newUid = slugify(t);
        const placed = placePanel(panel, []);
        await createDashboard({
          uid: newUid,
          title: t,
          model: {
            uid: newUid,
            title: t,
            variables: [],
            timeRange: { from: "now-6h", to: "now" },
            panels: [placed],
          },
        });
        toast.success(`Dashboard "${t}" criado com o painel.`);
      }
      onClose();
    } catch {
      toast.error("Não foi possível salvar o painel.");
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Salvar em dashboard"
      footer={
        <div className="row row--end">
          <Button variant="ghost" onClick={onClose} disabled={saving}>
            Cancelar
          </Button>
          <Button variant="primary" onClick={save} disabled={saving}>
            {saving ? "Salvando…" : "Salvar painel"}
          </Button>
        </div>
      }
    >
      <FormField
        label="Onde salvar"
        help="Escolha adicionar o painel a um dashboard que já existe ou criar um novo só com ele."
      >
        <select
          className="field"
          value={mode}
          onChange={(e) => setMode(e.target.value as "existing" | "new")}
        >
          <option value="existing" disabled={dashboards.length === 0}>
            Adicionar a um dashboard existente
          </option>
          <option value="new">Criar um novo dashboard</option>
        </select>
      </FormField>

      {mode === "existing" ? (
        <FormField
          label="Dashboard"
          help="O painel é anexado ao final deste dashboard como uma nova versão."
          error={error}
        >
          <select className="field" value={uid} onChange={(e) => setUid(e.target.value)}>
            {dashboards.map((d) => (
              <option key={d.uid} value={d.uid}>
                {d.title}
              </option>
            ))}
          </select>
        </FormField>
      ) : (
        <FormField
          label="Título do novo dashboard"
          help="Nome exibido na lista e no topo. Ex.: Visão da Produção."
          error={error}
          required
        >
          <input
            className="field"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder="Ex.: Visão da Produção"
          />
        </FormField>
      )}
    </Modal>
  );
}

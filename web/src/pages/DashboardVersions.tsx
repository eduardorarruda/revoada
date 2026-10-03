// Histórico de versões + rollback (UX.6) — expõe o versionamento que o backend
// já faz. Toda alteração salva vira uma versão; restaurar cria uma NOVA versão a
// partir de uma antiga, sem apagar nada do histórico.
import { useCallback, useEffect, useState, type ReactNode } from "react";
import { History } from "lucide-react";
import {
  Button,
  Badge,
  Skeleton,
  DataTable,
  EmptyState,
  PageHeader,
  HelpPanel,
  ConfirmDialog,
  useToast,
  type Column,
} from "../components";
import {
  dashboardVersions,
  rollbackDashboard,
  getDashboard,
  type DashboardVersion,
} from "../api";
import { help } from "../help";
import { fmtRelAbs } from "../format";

// Monta as seções do HelpPanel: "o que é a tela" (help.pages.dashboards) + uma
// seção dedicada ao modelo de versões (help.fields["dashboard.version"]).
function versionsHelpSections(): { heading: string; body: ReactNode }[] {
  const p = help.pages.dashboards;
  const sections: { heading: string; body: ReactNode }[] = [
    { heading: "O que é esta tela", body: p.what },
    {
      heading: "Como funciona o versionamento",
      body: help.fields["dashboard.version"],
    },
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

export function DashboardVersions({ uid }: { uid: string }) {
  const toast = useToast();
  const [helpOpen, setHelpOpen] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  const [versions, setVersions] = useState<DashboardVersion[]>([]);
  const [panelCount, setPanelCount] = useState<number | null>(null);
  // Versão selecionada para o ConfirmDialog de restauração.
  const [toRestore, setToRestore] = useState<DashboardVersion | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(false);
    try {
      const r = await dashboardVersions(uid);
      // Mais recentes primeiro.
      const sorted = [...r.versions].sort((a, b) => b.version - a.version);
      setVersions(sorted);
      // Contagem de painéis só é derivável da versão atual (payload de versões
      // não traz o modelo); mostramos como pista na linha "atual".
      try {
        const d = await getDashboard(uid);
        setPanelCount(d.model.panels.length);
      } catch {
        setPanelCount(null);
      }
    } catch {
      setError(true);
      toast.error("Não foi possível carregar o histórico de versões.");
    } finally {
      setLoading(false);
    }
  }, [uid, toast]);

  useEffect(() => {
    void load();
  }, [load]);

  // Executa o rollback e recarrega a lista.
  async function confirmRestore() {
    const v = toRestore;
    if (!v) return;
    setToRestore(null);
    try {
      const r = await rollbackDashboard(uid, v.version);
      toast.success(`Restaurado da v${r.restored_from} → nova v${r.new_version}`);
      await load();
    } catch {
      toast.error("Não foi possível restaurar esta versão.");
    }
  }

  const latest = versions.length > 0 ? versions[0].version : -1;

  const columns: Column<DashboardVersion>[] = [
    {
      key: "version",
      label: "Versão",
      render: (r) => (
        <span className="row">
          <strong className="tabular">v{r.version}</strong>
          {r.version === latest && (
            <>
              <Badge state="info">atual</Badge>
              {panelCount != null && (
                <span>
                  {panelCount} {panelCount === 1 ? "painel" : "painéis"}
                </span>
              )}
            </>
          )}
        </span>
      ),
    },
    {
      key: "created_at",
      label: "Quando",
      render: (r) => {
        const t = fmtRelAbs(r.created_at);
        return (
          <span className="tabular" title={t.abs}>
            {t.rel}
          </span>
        );
      },
    },
    {
      key: "created_by",
      label: "Autor",
      render: (r) => r.created_by || "—",
    },
  ];

  return (
    <div className="page">
      <PageHeader
        title={`Histórico: ${uid}`}
        subtitle="Toda alteração salva vira uma versão. Restaurar cria uma nova versão, nada é apagado."
        actions={
          <Button onClick={() => (location.hash = "/d/" + uid + "/edit")}>
            Voltar a editar
          </Button>
        }
        onHelp={() => setHelpOpen(true)}
      />

      {loading ? (
        <div className="stack">
          <Skeleton height={44} />
          <Skeleton height={44} />
          <Skeleton height={44} />
        </div>
      ) : error ? (
        <EmptyState
          icon={<History size={40} />}
          title="Não foi possível carregar o histórico"
          body="Houve um erro ao buscar as versões deste dashboard. Tente novamente."
          action={{ label: "Tentar de novo", onClick: () => void load() }}
        />
      ) : (
        <DataTable
          columns={columns}
          rows={versions}
          keyFn={(r) => r.version}
          rowActions={(r) =>
            r.version === latest ? null : (
              <Button variant="ghost" onClick={() => setToRestore(r)}>
                Restaurar esta versão
              </Button>
            )
          }
          empty={
            <EmptyState
              icon={<History size={40} />}
              title="Sem histórico ainda"
              body="As versões aparecem aqui assim que você editar e salvar o dashboard. Cada alteração salva vira uma nova versão que você pode restaurar depois."
              steps={[
                "Abra o editor deste dashboard.",
                "Faça uma alteração e salve.",
                "Volte aqui para ver a versão registrada.",
              ]}
              action={{
                label: "Voltar a editar",
                onClick: () => (location.hash = "/d/" + uid + "/edit"),
              }}
            />
          }
        />
      )}

      <ConfirmDialog
        open={toRestore != null}
        onCancel={() => setToRestore(null)}
        onConfirm={() => void confirmRestore()}
        verb="Restaurar"
        target={toRestore ? `versão v${toRestore.version}` : ""}
        consequences="Cria uma nova versão a partir desta. O histórico continua íntegro."
      />

      <HelpPanel
        open={helpOpen}
        onClose={() => setHelpOpen(false)}
        title={help.pages.dashboards.title}
        sections={versionsHelpSections()}
      />
    </div>
  );
}

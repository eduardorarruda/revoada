// Página /dev/kit — vitrine COMPLETA do design system nos dois temas (P2.1 / UX-27).
// Referência confiável de todos os componentes com seus estados. Rota pública.
import { useState } from "react";
import {
  AdvancedSection,
  Badge,
  Button,
  Card,
  ConfirmDialog,
  DataTable,
  EmptyState,
  FormField,
  FreshnessIndicator,
  IconButton,
  InfoTip,
  Modal,
  Tabs,
  ToastProvider,
  useToast,
  ActionIcons,
  Skeleton,
  type Column,
  type State,
} from "../components";
import { getTheme, setTheme, type Theme } from "../theme";

const STATES: State[] = ["ok", "warn", "crit", "info", "neutral"];

// ── Toast: precisa do hook dentro do provider (DevKit é rota pública, fora do
// ToastProvider do App — por isso envolvemos a vitrine num provider local). ──
function ToastDemo() {
  const toast = useToast();
  return (
    <div style={{ display: "flex", gap: "var(--sp-3)", flexWrap: "wrap" }}>
      <Button variant="primary" onClick={() => toast.success("Alterações salvas com sucesso.")}>
        Disparar sucesso
      </Button>
      <Button onClick={() => toast.error("Falha ao salvar: verifique a conexão.")}>
        Disparar erro
      </Button>
    </div>
  );
}

// ── Linha de exemplo da DataTable (coluna de texto + colunas numéricas). ──
type ProcRow = { pid: number; cmd: string; cpu: number; mem: number | null };

const PROC_COLUMNS: Column<ProcRow>[] = [
  { key: "cmd", label: "Comando" },
  { key: "pid", label: "PID", align: "right" },
  { key: "cpu", label: "%CPU", align: "right", render: (r) => r.cpu.toFixed(1) },
  { key: "mem", label: "%MEM", align: "right", render: (r) => (r.mem == null ? null : r.mem.toFixed(1)) },
];

const PROC_ROWS: ProcRow[] = [
  { pid: 3009, cmd: "app", cpu: 62.4, mem: 5.7 },
  { pid: 1200, cmd: "postgres", cpu: 18.0, mem: 12.3 },
  { pid: 880, cmd: "nginx", cpu: 4.2, mem: null },
];

const TABS = [
  { id: "visao", label: "Visão geral" },
  { id: "metricas", label: "Métricas" },
  { id: "logs", label: "Logs" },
];

export function DevKit() {
  const [theme, setThemeState] = useState<Theme>(getTheme());
  const [modalOpen, setModalOpen] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [activeTab, setActiveTab] = useState("visao");

  const toggle = () => {
    const next: Theme = theme === "dark" ? "light" : "dark";
    setTheme(next);
    setThemeState(next);
  };

  return (
    <ToastProvider>
      <div style={{ padding: "var(--sp-6)", maxWidth: 960, margin: "0 auto" }}>
        <header
          style={{
            display: "flex",
            justifyContent: "space-between",
            alignItems: "center",
            marginBottom: "var(--sp-6)",
          }}
        >
          <div>
            <h1 style={{ margin: 0, fontSize: "var(--fs-28)" }}>Revoada, Design Kit</h1>
            <p style={{ color: "var(--text-2)", margin: "var(--sp-1) 0 0" }}>
              Tema atual: <strong>{theme}</strong>
            </p>
          </div>
          <Button variant="primary" onClick={toggle}>
            Alternar tema
          </Button>
        </header>

        <section style={{ display: "grid", gap: "var(--sp-4)" }}>
          {/* ─────────────── Base ─────────────── */}
          <h2 style={{ fontSize: "var(--fs-20)", margin: "var(--sp-2) 0 0" }}>Base</h2>

          <Card title="Botões">
            <div style={{ display: "flex", gap: "var(--sp-3)", flexWrap: "wrap" }}>
              <Button>Padrão</Button>
              <Button variant="primary">Primário</Button>
              <Button variant="ghost">Ghost</Button>
              <Button disabled>Desabilitado</Button>
            </div>
          </Card>

          <Card title="Ações (botões só-ícone, hover mostra o tooltip, aria-label p/ leitor de tela)">
            <div style={{ display: "flex", gap: "var(--sp-3)", flexWrap: "wrap", alignItems: "center" }}>
              <IconButton icon={ActionIcons.reload} label="Recarregar" />
              <IconButton icon={ActionIcons.revoke} label="Revogar" />
              <IconButton icon={ActionIcons.delete} label="Apagar" />
              <IconButton icon={ActionIcons.edit} label="Editar" />
              <IconButton icon={ActionIcons.history} label="Histórico" />
              <IconButton icon={ActionIcons.open} label="Abrir" />
              <IconButton icon={ActionIcons.edit} label="Editar (destaque)" variant="primary" />
            </div>
          </Card>

          <Card title="Badges de estado (cor + ícone + rótulo)">
            <div style={{ display: "flex", gap: "var(--sp-3)", flexWrap: "wrap" }}>
              {STATES.map((s) => (
                <Badge key={s} state={s}>
                  {s.toUpperCase()}
                </Badge>
              ))}
            </div>
          </Card>

          {/* ─────────────── Formulários ─────────────── */}
          <h2 style={{ fontSize: "var(--fs-20)", margin: "var(--sp-4) 0 0" }}>Formulários</h2>

          <Card title="FormField (label, hint, erro role=alert, obrigatório, disabled)">
            <div style={{ display: "grid", gap: "var(--sp-4)", maxWidth: 420 }}>
              <FormField
                label="Nome do host"
                hint="Como o host aparece nos painéis."
                help="Use um nome curto e único. Ex.: web-prod-01."
              >
                <input className="field" type="text" defaultValue="web-prod-01" />
              </FormField>

              <FormField
                label="Intervalo de coleta (s)"
                error="Informe um número entre 5 e 3600."
              >
                {/* aria-invalid=true + erro com role=alert (testável) */}
                <input className="field" type="text" defaultValue="0" />
              </FormField>

              <FormField label="URL do endpoint" required hint="Obrigatório. Inclua http:// ou https://.">
                <input className="field" type="text" placeholder="https://…" autoFocus />
              </FormField>

              <FormField label="Token (somente leitura)" hint="Gerado pelo servidor.">
                <input className="field" type="text" defaultValue="••••••••" disabled />
              </FormField>
            </div>
          </Card>

          {/* ─────────────── Sobreposições ─────────────── */}
          <h2 style={{ fontSize: "var(--fs-20)", margin: "var(--sp-4) 0 0" }}>
            Diálogos, confirmações e notificações
          </h2>

          <Card title="Modal e ConfirmDialog">
            <div style={{ display: "flex", gap: "var(--sp-3)", flexWrap: "wrap" }}>
              <Button onClick={() => setModalOpen(true)}>Abrir Modal</Button>
              <Button variant="ghost" onClick={() => setConfirmOpen(true)}>
                Abrir ConfirmDialog (danger)
              </Button>
            </div>
          </Card>

          <Card title="Toast (sucesso some sozinho; erro é role=alert/assertivo e fica até dispensar)">
            <ToastDemo />
          </Card>

          {/* ─────────────── Navegação e listas ─────────────── */}
          <h2 style={{ fontSize: "var(--fs-20)", margin: "var(--sp-4) 0 0" }}>Navegação e listas</h2>

          <Card title="Tabs (setas ← → navegam; aba ativa sublinhada)">
            <Tabs tabs={TABS} active={activeTab} onChange={setActiveTab} />
            <p style={{ color: "var(--text-2)", marginTop: "var(--sp-3)" }}>
              Aba ativa: <strong>{TABS.find((t) => t.id === activeTab)?.label}</strong>
            </p>
          </Card>

          <Card title="DataTable (coluna de texto + numéricas à direita, tabular, zebra, célula, )">
            <DataTable
              columns={PROC_COLUMNS}
              rows={PROC_ROWS}
              keyFn={(r) => r.pid}
              empty={<EmptyState title="Sem processos" body="Nada para exibir." />}
            />
          </Card>

          <Card title="EmptyState (lista vazia vira mini-tutorial: ícone + passos + ação)">
            <EmptyState
              icon={<ActionIcons.history size={40} aria-hidden={true} />}
              title="Nenhum dashboard ainda"
              body="Crie o primeiro dashboard para começar a monitorar seus hosts."
              steps={[
                "Clique em “Criar dashboard”.",
                "Escolha um painel (ex.: CPU, memória).",
                "Salve e compartilhe o link.",
              ]}
              action={{ label: "Criar dashboard", onClick: () => setModalOpen(true) }}
            />
          </Card>

          {/* ─────────────── Ajuda e progressão de complexidade ─────────────── */}
          <h2 style={{ fontSize: "var(--fs-20)", margin: "var(--sp-4) 0 0" }}>Ajuda contextual</h2>

          <Card title="InfoTip e AdvancedSection">
            <p style={{ display: "flex", alignItems: "center", gap: "var(--sp-2)", margin: "0 0 var(--sp-4)" }}>
              Taxa de amostragem
              <InfoTip
                title="Taxa de amostragem"
                text="Frequência de coleta das métricas. Valores menores dão mais detalhe, porém geram mais dados."
              />
            </p>
            <AdvancedSection label="Opções avançadas">
              <div style={{ display: "grid", gap: "var(--sp-3)", maxWidth: 420 }}>
                <FormField label="Retenção (dias)" hint="Por quanto tempo guardar as amostras.">
                  <input className="field" type="text" defaultValue="30" />
                </FormField>
                <FormField label="Compressão" hint="Reduz espaço em disco.">
                  <input className="field" type="text" defaultValue="zstd" />
                </FormField>
              </div>
            </AdvancedSection>
          </Card>

          {/* ─────────────── Dados e carregamento ─────────────── */}
          <h2 style={{ fontSize: "var(--fs-20)", margin: "var(--sp-4) 0 0" }}>Dados e carregamento</h2>

          <Card title="Números com tabular figures">
            <div className="tabular" style={{ fontSize: "var(--fs-40)" }}>
              42.07%
            </div>
            <div className="tabular" style={{ fontSize: "var(--fs-20)", color: "var(--text-2)" }}>
              1.111,00 · 8.888,00 · 3.333,00
            </div>
          </Card>

          <Card title="Indicador de frescor">
            <div style={{ display: "flex", gap: "var(--sp-5)", flexWrap: "wrap" }}>
              <FreshnessIndicator status="live" ageSeconds={2} />
              <FreshnessIndicator status="stale" />
              <FreshnessIndicator status="reconnecting" />
            </div>
          </Card>

          <Card title="Skeleton (loading)">
            <div style={{ display: "grid", gap: "var(--sp-2)" }}>
              <Skeleton height={24} width="40%" />
              <Skeleton height={16} />
              <Skeleton height={16} width="80%" />
            </div>
          </Card>
        </section>

        {/* Overlays controlados pelos botões acima. */}
        <Modal
          open={modalOpen}
          onClose={() => setModalOpen(false)}
          title="Exemplo de Modal"
          footer={
            <div className="row row--end">
              <Button onClick={() => setModalOpen(false)}>Fechar</Button>
              <Button variant="primary" onClick={() => setModalOpen(false)}>
                Confirmar
              </Button>
            </div>
          }
        >
          <p style={{ margin: 0 }}>
            Diálogo centrado com foco preso: <kbd>Tab</kbd> circula dentro dele, <kbd>Esc</kbd> ou
            clique no fundo fecham. No mobile ocupa a tela cheia.
          </p>
        </Modal>

        <ConfirmDialog
          open={confirmOpen}
          onCancel={() => setConfirmOpen(false)}
          onConfirm={() => setConfirmOpen(false)}
          verb="Apagar"
          target="host web-prod-01"
          consequences="O host e todo o histórico de métricas serão removidos. Esta ação não pode ser desfeita."
          danger
        />
      </div>
    </ToastProvider>
  );
}

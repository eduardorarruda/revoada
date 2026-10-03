// Componentes base do design system Revoada.
import type { ButtonHTMLAttributes, ComponentType, ReactNode } from "react";
import { RefreshCw, Ban, Trash2, Pencil, History, ExternalLink, Link2, UserCheck, CircleCheckBig, BellOff, BellRing } from "lucide-react";
import "./ui.css";

// Kit de componentes (UX.2) — re-exportados aqui para import único.
export { InfoTip } from "./InfoTip";
export { FormField } from "./FormField";
export { Checkbox, Radio } from "./Check";
export { LastSeen } from "./LastSeen";
export { ServerContainerFilter } from "./ServerContainerFilter";
export { Modal } from "./Modal";
export { ConfirmDialog } from "./ConfirmDialog";
export { ToastProvider, useToast } from "./Toast";
export { EmptyState } from "./EmptyState";
export { PageHeader } from "./PageHeader";
export { HelpPanel } from "./HelpPanel";
export { Tabs } from "./Tabs";
export { DataTable, type Column } from "./DataTable";
export { AdvancedSection } from "./AdvancedSection";
export { Collapsible } from "./Collapsible";

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "default" | "primary" | "ghost";
};

export function Button({ variant = "default", className = "", ...props }: ButtonProps) {
  const v = variant === "default" ? "" : ` btn--${variant}`;
  return <button className={`btn${v} ${className}`} {...props} />;
}

type IconType = ComponentType<{ size?: number | string; "aria-hidden"?: boolean }>;

// Ícones de ação padronizados em TODO o sistema (uma ação = um ícone).
export const ActionIcons = {
  reload: RefreshCw,
  revoke: Ban,
  delete: Trash2,
  edit: Pencil,
  history: History,
  open: ExternalLink,
  copyLink: Link2,
  // Alertas: duas ações diferentes, dois ícones diferentes. "Reconhecer" é
  // alguém assumindo o alerta (a pessoa entra na figura → UserCheck);
  // "resolver" é o fim da história (o círculo fechado → CircleCheckBig). Um
  // "check" simples nos dois faria a ação irreversível parecer a reversível.
  ack: UserCheck,
  resolve: CircleCheckBig,
  // Container parado de propósito: parar de alertar (sino cortado) e voltar a vigiar.
  ignore: BellOff,
  unignore: BellRing,
} satisfies Record<string, IconType>;

type IconButtonProps = Omit<ButtonHTMLAttributes<HTMLButtonElement>, "children"> & {
  icon: IconType;
  /** Rótulo da ação — vira aria-label e tooltip (mantém a UI explicativa). */
  label: string;
  variant?: "default" | "primary" | "ghost";
};

// Botão só-ícone acessível: sempre com aria-label + title (tooltip no hover).
export function IconButton({ icon: Icon, label, variant = "ghost", className = "", ...props }: IconButtonProps) {
  const v = variant === "default" ? "" : ` btn--${variant}`;
  return (
    <button className={`btn btn--icon${v} ${className}`} aria-label={label} title={label} {...props}>
      <Icon size={16} aria-hidden={true} />
    </button>
  );
}

export function Card({ title, children }: { title?: ReactNode; children: ReactNode }) {
  return (
    <div className="card">
      {title && <h3 className="card__title">{title}</h3>}
      {children}
    </div>
  );
}

export type State = "ok" | "warn" | "crit" | "info" | "neutral";

const STATE_ICON: Record<State, string> = {
  ok: "●",
  warn: "▲",
  crit: "■",
  info: "ℹ",
  neutral: "○",
};

// Badge de estado: cor + ícone + rótulo. Nunca comunica só por cor (acessível).
export function Badge({ state, children }: { state: State; children: ReactNode }) {
  return (
    <span className={`badge badge--${state}`}>
      <span className="badge__icon" aria-hidden="true">
        {STATE_ICON[state]}
      </span>
      {children}
    </span>
  );
}

export function Skeleton({ width = "100%", height = 16 }: { width?: number | string; height?: number | string }) {
  return <div className="skeleton" style={{ width, height }} aria-hidden="true" />;
}

export type Freshness = "live" | "stale" | "reconnecting";

// Rótulos em PT-BR e honestos: "stale · reconectando" misturava duas coisas
// diferentes (dado velho e conexão caindo) e ainda usava uma palavra em inglês.
// `stale` agora significa uma coisa só: o dado exibido NÃO é de agora.
const FRESH_LABEL: Record<Freshness, (age?: number) => string> = {
  live: (age) => (age != null ? `ao vivo · ${age}s` : "ao vivo"),
  stale: (age) => (age != null ? `sem dado novo há ${humanAge(age)}` : "sem dado novo"),
  reconnecting: () => "reconectando…",
};

// Idade curta e legível para o selo do painel ("45s", "6 min", "2 h", "3 d").
function humanAge(seconds: number): string {
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)} min`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} h`;
  return `${Math.floor(seconds / 86400)} d`;
}

// Indicador de frescor dos dados de um painel.
export function FreshnessIndicator({ status, ageSeconds }: { status: Freshness; ageSeconds?: number }) {
  return (
    <span className={`freshness freshness--${status}`} role="status">
      <span className="freshness__dot" aria-hidden="true" />
      {FRESH_LABEL[status](ageSeconds)}
    </span>
  );
}

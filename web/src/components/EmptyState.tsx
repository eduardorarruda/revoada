// EmptyState — lista vazia nunca é tabela vazia: vira mini-tutorial (ícone grande,
// título, texto didático, passos numerados e ação primária opcionais).
import type { ReactNode } from "react";
import { Button } from "./index";

export function EmptyState({
  icon,
  title,
  body,
  steps,
  action,
}: {
  icon?: ReactNode;
  title: string;
  body?: ReactNode;
  steps?: string[];
  action?: { label: string; onClick: () => void };
}) {
  return (
    <div className="empty-state">
      {icon && (
        <div className="empty-state__icon" aria-hidden="true">
          {icon}
        </div>
      )}
      <h3 className="empty-state__title">{title}</h3>
      {body && <div className="empty-state__body">{body}</div>}
      {steps && steps.length > 0 && (
        <ol className="empty-state__steps">
          {steps.map((s, i) => (
            <li key={i}>{s}</li>
          ))}
        </ol>
      )}
      {action && (
        <Button variant="primary" onClick={action.onClick}>
          {action.label}
        </Button>
      )}
    </div>
  );
}

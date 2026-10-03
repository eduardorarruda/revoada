// HelpPanel — gaveta lateral direita genérica (mesmo padrão do CorrelationDrawer).
// Recebe o conteúdo por props (title + seções); Esc e clique no backdrop fecham.
import { useEffect, type ReactNode } from "react";
import { Button } from "./index";

export function HelpPanel({
  open,
  onClose,
  title,
  sections,
}: {
  open: boolean;
  onClose: () => void;
  title?: string;
  sections: { heading: string; body: ReactNode }[];
}) {
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  if (!open) return null;

  return (
    <>
      <div className="drawer__backdrop" onClick={onClose} />
      <aside className="drawer" role="dialog" aria-modal="true" aria-label={title ?? "Ajuda"}>
        <div className="drawer__head">
          <strong className="drawer__title">{title ?? "Ajuda"}</strong>
          <Button variant="ghost" onClick={onClose}>
            fechar ✕
          </Button>
        </div>
        <div className="drawer__body">
          {sections.map((s, i) => (
            <section key={i} className="drawer__section">
              <div className="drawer__section-title">{s.heading}</div>
              <div className="drawer__section-body">{s.body}</div>
            </section>
          ))}
        </div>
      </aside>
    </>
  );
}

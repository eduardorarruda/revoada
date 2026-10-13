// HelpPanel — gaveta lateral direita genérica (mesmo padrão do CorrelationDrawer).
// Recebe o conteúdo por props (title + seções); Esc e clique no backdrop fecham.
// É um diálogo modal: o foco entra ao abrir, fica preso nele e volta a quem abriu.
import { useRef, type ReactNode } from "react";
import { Button } from "./index";
import { useFocoPreso } from "./useFocoPreso";

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
  const painelRef = useRef<HTMLElement>(null);
  useFocoPreso(painelRef, open, onClose);

  if (!open) return null;

  return (
    <>
      <div className="drawer__backdrop" onClick={onClose} />
      <aside
        ref={painelRef}
        className="drawer"
        role="dialog"
        aria-modal="true"
        aria-label={title ?? "Ajuda"}
        tabIndex={-1}
      >
        <div className="drawer__head">
          <strong className="drawer__title">{title ?? "Ajuda"}</strong>
          <Button variant="ghost" onClick={onClose}>
            fechar ✕
          </Button>
        </div>
        <div className="drawer__body">
          {sections.map((s, i) => (
            <section key={i} className="drawer__section">
              {/* h3: leitor de tela navega pelas seções da ajuda por título. */}
              <h3 className="drawer__section-title">{s.heading}</h3>
              <div className="drawer__section-body">{s.body}</div>
            </section>
          ))}
        </div>
      </aside>
    </>
  );
}

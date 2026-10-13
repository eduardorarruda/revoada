// Modal — diálogo centrado; prende o foco; Esc e clique no backdrop fecham.
// No mobile (<768px) ocupa a tela cheia (ver ui.css). role=dialog aria-modal.
import { useId, useRef, type ReactNode, type RefObject } from "react";
import { createPortal } from "react-dom";
import { useFocoPreso } from "./useFocoPreso";

export function Modal({
  open,
  onClose,
  title,
  footer,
  wide,
  children,
  initialFocusRef,
}: {
  open: boolean;
  onClose: () => void;
  title?: string;
  footer?: ReactNode;
  wide?: boolean;
  children: ReactNode;
  initialFocusRef?: RefObject<HTMLElement | null>;
}) {
  const dialogRef = useRef<HTMLDivElement>(null);
  const titleId = useId();
  // Foco inicial, Tab preso, Esc e devolução do foco ao fechar: ver useFocoPreso.
  useFocoPreso(dialogRef, open, onClose, initialFocusRef);

  if (!open) return null;

  return createPortal(
    <div
      className="modal__backdrop"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        ref={dialogRef}
        className={`modal${wide ? " modal--wide" : ""}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby={title ? titleId : undefined}
        aria-label={title ? undefined : "Diálogo"}
        tabIndex={-1}
      >
        {title && (
          <div className="modal__head">
            <h2 id={titleId} className="modal__title">
              {title}
            </h2>
            <button type="button" className="btn btn--ghost" aria-label="Fechar" onClick={onClose}>
              ✕
            </button>
          </div>
        )}
        <div className="modal__body">{children}</div>
        {footer && <div className="modal__foot">{footer}</div>}
      </div>
    </div>,
    document.body,
  );
}

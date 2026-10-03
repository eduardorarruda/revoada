// Modal — diálogo centrado; prende o foco; Esc e clique no backdrop fecham.
// No mobile (<768px) ocupa a tela cheia (ver ui.css). role=dialog aria-modal.
import { useEffect, useId, useRef, type ReactNode, type RefObject } from "react";
import { createPortal } from "react-dom";

const FOCUSABLE =
  'a[href],button:not([disabled]),textarea,input,select,[tabindex]:not([tabindex="-1"])';

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
  const prevFocus = useRef<HTMLElement | null>(null);
  const titleId = useId();

  // O efeito abaixo roda UMA vez por abertura. `onClose` e a ref de foco inicial
  // entram por ref, não pelas dependências: as telas passam `onClose` como arrow
  // inline, e o painel re-renderiza a tela de trás a cada 5–15 s. Com `onClose` nas
  // dependências, cada atualização desmontava e remontava o efeito — devolvia o foco
  // para fora e depois focava o ✕ —, e quem estava digitando perdia o cursor.
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  const initialFocus = useRef(initialFocusRef);
  initialFocus.current = initialFocusRef;

  useEffect(() => {
    if (!open) return;
    prevFocus.current = document.activeElement as HTMLElement | null;
    const dialog = dialogRef.current;

    // Foco inicial: ref explícita (ex.: Cancelar do ConfirmDialog) ou 1º focável.
    const alvoInicial = initialFocus.current?.current;
    if (alvoInicial) alvoInicial.focus();
    else (dialog?.querySelector<HTMLElement>(FOCUSABLE) ?? dialog)?.focus();

    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        onCloseRef.current();
        return;
      }
      if (e.key !== "Tab" || !dialog) return;
      const items = Array.from(dialog.querySelectorAll<HTMLElement>(FOCUSABLE));
      if (items.length === 0) {
        e.preventDefault();
        return;
      }
      const first = items[0];
      const last = items[items.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", onKey, true);
    return () => {
      document.removeEventListener("keydown", onKey, true);
      prevFocus.current?.focus?.();
    };
  }, [open]);

  if (!open) return null;

  return createPortal(
    <div
      className="modal__backdrop"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onCloseRef.current();
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

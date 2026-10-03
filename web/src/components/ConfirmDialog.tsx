// ConfirmDialog — confirmação nomeada e obrigatória para toda ação destrutiva.
// Título "{verb} {target}?"; consequências no corpo; botão de confirmar em --crit
// quando danger. NÃO confirma com Enter (exige clique); Cancelar tem o foco inicial.
import { useRef, type RefObject } from "react";
import { Modal } from "./Modal";

export function ConfirmDialog({
  open,
  onCancel,
  onConfirm,
  verb,
  target,
  consequences,
  danger,
}: {
  open: boolean;
  onCancel: () => void;
  onConfirm: () => void;
  verb: string;
  target: string;
  consequences?: string;
  danger?: boolean;
}) {
  const cancelRef = useRef<HTMLButtonElement>(null);
  return (
    <Modal
      open={open}
      onClose={onCancel}
      title={`${verb} ${target}?`}
      initialFocusRef={cancelRef as RefObject<HTMLElement | null>}
      footer={
        <div className="row row--end">
          <button type="button" ref={cancelRef} className="btn" onClick={onCancel}>
            Cancelar
          </button>
          <button
            type="button"
            className={`btn ${danger ? "btn--danger" : "btn--primary"}`}
            onClick={onConfirm}
          >
            {`${verb} ${target}`}
          </button>
        </div>
      }
    >
      <p className="confirm__body">
        {consequences ?? "Esta ação precisa de confirmação explícita."}
      </p>
    </Modal>
  );
}

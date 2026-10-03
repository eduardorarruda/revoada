// Toast — fila global de feedback. Sucesso some sozinho em 4s; erro fica até
// dispensar (tem botão fechar). Pilha fixa no canto superior direito (embaixo no
// mobile), aria-live=polite. Substitui todo alert() do app.
import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";

type ToastKind = "success" | "error";
type ToastItem = { id: number; kind: ToastKind; msg: string };
type ToastApi = { success: (msg: string) => void; error: (msg: string) => void };

const ToastCtx = createContext<ToastApi | null>(null);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const seq = useRef(0);

  const remove = useCallback(
    (id: number) => setItems((xs) => xs.filter((x) => x.id !== id)),
    [],
  );

  const push = useCallback(
    (kind: ToastKind, msg: string) => {
      const id = ++seq.current;
      setItems((xs) => [...xs, { id, kind, msg }]);
      if (kind === "success") setTimeout(() => remove(id), 4000);
    },
    [remove],
  );

  const api = useMemo<ToastApi>(
    () => ({ success: (m) => push("success", m), error: (m) => push("error", m) }),
    [push],
  );

  return (
    <ToastCtx.Provider value={api}>
      {children}
      <div className="toast-stack" aria-live="polite" aria-atomic="false">
        {items.map((t) => (
          <div
            key={t.id}
            className={`toast toast--${t.kind}`}
            role={t.kind === "error" ? "alert" : "status"}
            aria-live={t.kind === "error" ? "assertive" : "polite"}
          >
            <span className="toast__msg">{t.msg}</span>
            <button
              type="button"
              className="toast__close"
              aria-label="Dispensar"
              onClick={() => remove(t.id)}
            >
              ✕
            </button>
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  );
}

export function useToast(): ToastApi {
  const ctx = useContext(ToastCtx);
  if (!ctx) throw new Error("useToast precisa estar dentro de <ToastProvider>.");
  return ctx;
}

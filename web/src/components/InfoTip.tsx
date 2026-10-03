// InfoTip — o coração do "à prova de burros": ícone ℹ discreto que abre um
// popover explicativo. Abre por clique E hover; fecha por Esc/blur. No mobile
// (<768px) vira bottom-sheet, pois popover não funciona bem com o dedo.
//
// No desktop o popover é renderizado num PORTAL (document.body) com position:
// fixed e coordenadas calculadas a partir do botão, com clamp no viewport. Assim
// ele NUNCA é cortado por um ancestral com overflow/transform (ex.: o corpo
// rolável de um modal) — era o que fazia o texto aparecer cortado.
import { useEffect, useId, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";

// Detecta viewport mobile de forma segura (jsdom não implementa matchMedia).
function useIsMobile(): boolean {
  const [mobile, setMobile] = useState(false);
  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return;
    const mq = window.matchMedia("(max-width: 767px)");
    const update = () => setMobile(mq.matches);
    update();
    mq.addEventListener("change", update);
    return () => mq.removeEventListener("change", update);
  }, []);
  return mobile;
}

const POP_MAX_W = 280; // casa com o max-width de .infotip__pop
const VIEWPORT_MARGIN = 8;

export function InfoTip({ text, title }: { text: ReactNode; title?: string }) {
  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null);
  const id = useId();
  const mobile = useIsMobile();
  // "Mais informações sobre MySQL", e não só "MySQL": lido pelo leitor de tela, o
  // nome do serviço sozinho não diz que este botão abre uma explicação.
  const label = title ? `Mais informações sobre ${title}` : "Mais informações";
  const btnRef = useRef<HTMLButtonElement>(null);
  const closeTimer = useRef<number | null>(null);

  const cancelClose = () => {
    if (closeTimer.current !== null) {
      clearTimeout(closeTimer.current);
      closeTimer.current = null;
    }
  };
  // Fecha com um pequeno atraso para o mouse conseguir "atravessar" o vão entre
  // o ícone e o popover sem que ele suma.
  const scheduleClose = () => {
    cancelClose();
    closeTimer.current = window.setTimeout(() => setOpen(false), 120);
  };

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [open]);

  // Ancoragem do popover (desktop): abaixo do botão, centralizado, mas preso ao
  // viewport para nunca sangrar pelas bordas. Recalcula em scroll/resize.
  useLayoutEffect(() => {
    if (!open || mobile || !btnRef.current) {
      setPos(null);
      return;
    }
    const compute = () => {
      const el = btnRef.current;
      if (!el) return;
      const r = el.getBoundingClientRect();
      const maxLeft = window.innerWidth - POP_MAX_W - VIEWPORT_MARGIN;
      let left = r.left + r.width / 2 - POP_MAX_W / 2;
      left = Math.max(VIEWPORT_MARGIN, Math.min(left, Math.max(VIEWPORT_MARGIN, maxLeft)));
      setPos({ left, top: r.bottom + 6 });
    };
    compute();
    window.addEventListener("scroll", compute, true);
    window.addEventListener("resize", compute);
    return () => {
      window.removeEventListener("scroll", compute, true);
      window.removeEventListener("resize", compute);
    };
  }, [open, mobile]);

  useEffect(() => () => cancelClose(), []);

  return (
    <span
      className="infotip"
      onMouseEnter={() => {
        if (!mobile) {
          cancelClose();
          setOpen(true);
        }
      }}
      onMouseLeave={() => {
        if (!mobile) scheduleClose();
      }}
    >
      <button
        ref={btnRef}
        type="button"
        className="infotip__btn"
        aria-label={label}
        aria-expanded={open}
        aria-controls={open ? id : undefined}
        onClick={() => setOpen((o) => !o)}
        onBlur={() => !mobile && setOpen(false)}
      >
        ℹ
      </button>
      {open &&
        (mobile ? (
          <div className="infotip__sheet-backdrop" onClick={() => setOpen(false)}>
            <div
              id={id}
              role="dialog"
              aria-label={label}
              className="infotip__sheet"
              onClick={(e) => e.stopPropagation()}
            >
              {title && <div className="infotip__title">{title}</div>}
              <div className="infotip__body">{text}</div>
              <button type="button" className="btn" onClick={() => setOpen(false)}>
                Fechar
              </button>
            </div>
          </div>
        ) : (
          pos &&
          createPortal(
            <span
              id={id}
              role="tooltip"
              className="infotip__pop"
              style={{ position: "fixed", left: pos.left, top: pos.top, transform: "none" }}
              onMouseEnter={cancelClose}
              onMouseLeave={scheduleClose}
            >
              {title && <div className="infotip__title">{title}</div>}
              <div className="infotip__body">{text}</div>
            </span>,
            document.body,
          )
        ))}
    </span>
  );
}

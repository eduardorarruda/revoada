// MotionRoot liga a camada de animação do painel.
//
//  - LazyMotion com recursos carregados sob demanda (ver recursos.ts): o pacote
//    inicial leva só os componentes `m.*`, e o motor chega depois da 1ª pintura.
//  - reducedMotion="user": quem pede menos movimento no sistema operacional recebe
//    as telas paradas — transformações somem, só a opacidade troca.
//  - O brilho que segue o cursor nos cartões é UM ouvinte só, no documento, e
//    escreve duas variáveis CSS por quadro. Nenhum cartão precisa saber dele.
import { useEffect, type ReactNode } from "react";
import { LazyMotion, MotionConfig } from "motion/react";

const carregarRecursos = () => import("./recursos").then((m) => m.default);

/** Cartões que ganham o brilho do cursor. */
export const SELETOR_BRILHO = ".card, .stat-card, .kpi, .srv-tile, .bento";

export function MotionRoot({ children }: { children: ReactNode }) {
  useBrilhoDoCursor();
  return (
    <LazyMotion features={carregarRecursos} strict>
      <MotionConfig reducedMotion="user">{children}</MotionConfig>
    </LazyMotion>
  );
}

function useBrilhoDoCursor() {
  useEffect(() => {
    // Toque não tem cursor: o brilho seria um borrão parado onde o dedo encostou.
    if (!window.matchMedia?.("(hover: hover) and (pointer: fine)").matches) return;
    let quadro = 0;
    let alvo: HTMLElement | null = null;
    let x = 0;
    let y = 0;
    const pintar = () => {
      quadro = 0;
      if (!alvo) return;
      const r = alvo.getBoundingClientRect();
      alvo.style.setProperty("--mx", `${x - r.left}px`);
      alvo.style.setProperty("--my", `${y - r.top}px`);
    };
    const mover = (e: PointerEvent) => {
      const el = (e.target as Element | null)?.closest?.(SELETOR_BRILHO) as HTMLElement | null;
      alvo = el;
      x = e.clientX;
      y = e.clientY;
      if (el && !quadro) quadro = requestAnimationFrame(pintar);
    };
    document.addEventListener("pointermove", mover, { passive: true });
    return () => {
      document.removeEventListener("pointermove", mover);
      if (quadro) cancelAnimationFrame(quadro);
    };
  }, []);
}

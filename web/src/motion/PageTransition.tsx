// PageTransition: a tela nova entra subindo 6px e acendendo. Só ENTRADA — esperar a
// saída da tela anterior deixaria a navegação lenta, e navegar é o que mais se faz.
//
// É CSS de propósito, e não um `m.div`: o motor do Motion chega sob demanda (ver
// recursos.ts), e um `m.div` pinta o estado inicial (opacidade 0) até ele chegar —
// se o pedaço falhasse ao carregar, a tela ficaria invisível para sempre.
import type { ReactNode } from "react";

export function PageTransition({ rota, children }: { rota: string; children: ReactNode }) {
  return (
    <div key={rota} className="page-transition">
      {children}
    </div>
  );
}

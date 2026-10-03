// AdvancedSection — <details> estilizado "Opções avançadas" (progressão de
// complexidade: o caminho feliz fica visível, o resto escondido atrás do toggle).
import type { ReactNode } from "react";

export function AdvancedSection({ label, children }: { label?: string; children: ReactNode }) {
  return (
    <details className="advanced">
      <summary className="advanced__summary">{label ?? "Opções avançadas"}</summary>
      <div className="advanced__body">{children}</div>
    </details>
  );
}

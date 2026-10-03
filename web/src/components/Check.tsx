import type { ReactNode } from "react";

// Checkbox e Radio base do painel. Encapsulam o input nativo (estilizado em ui.css
// com accent-color e alinhamento) num rótulo clicável, com foco visível por teclado
// e área de toque adequada. Usar no lugar de <input type="checkbox"> cru para manter
// alinhamento e comportamento consistentes em todas as telas.

interface CheckProps {
  checked: boolean;
  label?: ReactNode;
  disabled?: boolean;
  /** Alinha o controle ao topo quando o rótulo tem várias linhas. */
  alignTop?: boolean;
  name?: string;
  title?: string;
}

export function Checkbox({
  checked,
  onChange,
  label,
  disabled,
  alignTop,
  name,
  title,
}: CheckProps & { onChange: (checked: boolean) => void }) {
  return (
    <label className={cls(alignTop, disabled)} title={title}>
      <input
        type="checkbox"
        name={name}
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
      />
      {label != null && <span>{label}</span>}
    </label>
  );
}

export function Radio({
  checked,
  onChange,
  label,
  disabled,
  alignTop,
  name,
  title,
}: CheckProps & { onChange: () => void }) {
  return (
    <label className={cls(alignTop, disabled)} title={title}>
      <input type="radio" name={name} checked={checked} disabled={disabled} onChange={() => onChange()} />
      {label != null && <span>{label}</span>}
    </label>
  );
}

function cls(alignTop?: boolean, disabled?: boolean): string {
  return ["check", alignTop && "check--top", disabled && "check--disabled"].filter(Boolean).join(" ");
}

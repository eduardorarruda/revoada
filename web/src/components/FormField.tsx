// FormField — rótulo + InfoTip (help) + controle + hint (mudo) + erro (--crit).
// O <label htmlFor> é ligado ao controle filho: injetamos id/aria-describedby/
// aria-invalid via cloneElement quando o filho é um único elemento (input/select/
// textarea). Se o filho já traz id, respeitamos o dele.
import { cloneElement, isValidElement, useId, type ReactElement, type ReactNode } from "react";
import { InfoTip } from "./InfoTip";

type ControlProps = {
  id?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
};

export function FormField({
  label,
  help,
  error,
  hint,
  required,
  children,
}: {
  label: string;
  help?: ReactNode;
  error?: string;
  hint?: string;
  required?: boolean;
  children: ReactNode;
}) {
  const uid = useId();
  const hintId = `${uid}-hint`;
  const errorId = `${uid}-error`;

  // aria-describedby aponta para hint e/ou erro presentes.
  const describedBy =
    [hint ? hintId : "", error ? errorId : ""].filter(Boolean).join(" ") || undefined;

  // Injeta id + ARIA no controle filho (quando é um único elemento com props).
  let control: ReactNode = children;
  let controlId = uid;
  if (isValidElement(children)) {
    const child = children as ReactElement<ControlProps>;
    controlId = child.props.id ?? uid;
    control = cloneElement(child, {
      id: controlId,
      "aria-describedby": describedBy,
      "aria-invalid": error ? true : undefined,
    });
  }

  return (
    <div className="form-field">
      <div className="form-field__label">
        <label htmlFor={controlId}>
          {label}
          {required && (
            <span className="form-field__req" aria-hidden="true">
              {" *"}
            </span>
          )}
        </label>
        {help != null && <InfoTip text={help} title={label} />}
      </div>
      {control}
      {hint && (
        <div id={hintId} className="form-field__hint">
          {hint}
        </div>
      )}
      {error && (
        <div id={errorId} className="form-field__error" role="alert">
          {error}
        </div>
      )}
    </div>
  );
}

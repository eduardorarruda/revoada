import type { ReactNode } from "react";
import { FreshnessIndicator, Skeleton } from "../components";
import type { PanelBaseProps } from "./types";
import "./panels.css";

// Moldura comum a todos os painéis: título, descrição explicativa, frescor,
// e os estados loading / vazio / erro.
export function PanelFrame({
  title,
  description,
  state = "ok",
  error,
  freshness,
  note,
  children,
}: PanelBaseProps & { children: ReactNode }) {
  return (
    <section className="panel">
      <header className="panel__head">
        <div className="panel__titlewrap">
          <h3 className="panel__title">{title}</h3>
          {description && (
            <span className="panel__info" title={description} aria-label={description}>
              ?
            </span>
          )}
        </div>
        {freshness && <FreshnessIndicator status={freshness.status} ageSeconds={freshness.ageSeconds} />}
      </header>
      <div className="panel__body">
        {state === "loading" && (
          <div className="panel__stack">
            <Skeleton height={20} width="50%" />
            <Skeleton height={80} />
          </div>
        )}
        {state === "empty" && <p className="panel__msg">Sem dados no período.</p>}
        {state === "error" && <p className="panel__msg panel__msg--err">Erro: {error ?? "desconhecido"}</p>}
        {state === "ok" && children}
      </div>
      {/* Rodapé explicativo: o que este número/gráfico realmente é (agregação) e com
          que resolução foi lido. Só aparece quando há dado — em "carregando"/"vazio"
          seria ruído. */}
      {state === "ok" && note && <p className="panel__note" title={note}>{note}</p>}
    </section>
  );
}

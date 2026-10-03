import { dataFreshness, fmtRelAbs } from "../format";

// LastSeen — frescor do dado POR OBJETO ("atualizado há X"), com tooltip da data
// absoluta e marcação visual quando o dado está desatualizado. Diferencia "sem dado
// recente" (stale) de "nunca recebeu" (nodata) — nunca exibe dado velho como se
// fosse atual (regra 5.1). O ponto colorido comunica o estado (verde/âmbar/cinza)
// além do texto, então não depende só de cor.
export function LastSeen({
  ts,
  staleAfterSeconds = 120,
  prefix = "atualizado",
}: {
  ts: string | number | Date | null | undefined;
  /** Intervalo esperado de coleta; acima dele o dado vira "stale". */
  staleAfterSeconds?: number;
  prefix?: string;
}) {
  const { state } = dataFreshness(ts, staleAfterSeconds);
  if (state === "nodata") {
    return (
      <span className="lastseen lastseen--nodata" title="Nenhum dado recebido ainda">
        <span className="lastseen__dot" aria-hidden="true" />
        sem dado
      </span>
    );
  }
  const { rel, abs } = fmtRelAbs(ts!);
  return (
    <span className={`lastseen lastseen--${state}`} title={abs}>
      <span className="lastseen__dot" aria-hidden="true" />
      {state === "stale" ? `sem dado ${rel}` : `${prefix} ${rel}`}
    </span>
  );
}

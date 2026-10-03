// Sobreposições de alerta da TV: o aviso de alerta NOVO (30 s por cima da tela) e a
// tomada de tela por crítico ativo. Usadas pelo TVView (dashboards e mural).
import { useEffect, useState, type ReactNode } from "react";
import { OctagonAlert } from "lucide-react";
import type { TVAlert, TVTakeover } from "../../api";
import { haQuanto } from "./estado";
import { LetreiroTV, TopoTV } from "./pecas";
import { valorDoAlerta } from "./valorAlerta";

// sevStyle mapeia a severidade para os tokens de cor existentes (--crit/--warn/
// --info/--ok) — sem cor hardcoded, mesmo esquema do HealthWall.
function sevStyle(sev: string): { base: string; label: string } {
  switch (sev) {
    case "critical":
      return { base: "crit", label: "CRÍTICO" };
    case "warning":
      return { base: "warn", label: "ATENÇÃO" };
    case "info":
      return { base: "info", label: "INFORMATIVO" };
    default:
      return { base: "ok", label: sev.toUpperCase() };
  }
}

function Fato({ rotulo, valor }: { rotulo: string; valor: ReactNode }) {
  return (
    <div className="tv-fato">
      <span className="tv-fato__rotulo">{rotulo}</span>
      <span className="tv-fato__valor">{valor}</span>
    </div>
  );
}

function desde(since: string): string {
  return haQuanto(Math.max(0, (Date.now() - new Date(since).getTime()) / 1000));
}

// AlertBurst: overlay de destaque forte para um alerta NOVO. Regra, servidor (nome
// amigável), severidade e há quanto tempo, com a contagem regressiva dos 30 s na
// base do cartão (o timer em si é do componente pai).
export function AlertBurst({
  alert,
  hostLabel,
  pendentes,
}: {
  alert: TVAlert;
  hostLabel: (h: string) => string;
  /** Alertas novos que não couberam neste burst (fila + descartados pelo teto). */
  pendentes: number;
}) {
  const { base, label } = sevStyle(alert.severity);
  const host = alert.host ? hostLabel(alert.host) : "";
  return (
    <div className={`tv-novo tv-novo--${base}`} role="alert">
      <div className="tv-novo__cartao">
        <div className="tv-novo__chip">Alerta novo · {label}</div>
        <h2 className="tv-novo__regra">{alert.rule}</h2>
        {/* Qual servidor disparou: SEMPRE visível e rotulado; cai para "não
            identificado" quando a regra não traz host/site/url. */}
        <div className="tv-alarme__onde">
          <small>Servidor</small>
          {host || "não identificado"}
        </div>
        <div className="tv-fatos">
          <Fato rotulo="Começou" valor={desde(alert.since)} />
          {typeof alert.value === "number" && <Fato rotulo="Ao disparar" valor={valorDoAlerta(alert.metric, alert.value)} />}
        </div>
        {/* Rajada maior que a fila: a parede diz quantos ficaram, nada some calado. */}
        {pendentes > 0 && (
          <div className="tv-novo__resto">
            +{pendentes} {pendentes === 1 ? "outro alerta novo" : "outros alertas novos"}, veja a lista completa em Alertas
          </div>
        )}
        <span className="tv-novo__tempo" aria-hidden="true" />
      </div>
    </div>
  );
}

// Takeover: um crítico ativo toma a tela. Carrossel de 8 s entre os críticos, com o
// que quebrou, onde, desde quando, o valor com unidade, diagnóstico e plantão.
export function Takeover({
  nome,
  estado,
  status,
  atrasado,
}: {
  nome: string;
  estado: { tom: "ok" | "warn" | "crit" | "neutro"; texto: string };
  status: { criticals: TVTakeover[]; warnings: TVTakeover[]; deploys: { ts: string; title: string }[] };
  atrasado: string | null;
}) {
  const { criticals, warnings, deploys } = status;
  const [i, setI] = useState(0);
  useEffect(() => {
    if (criticals.length <= 1) return;
    const t = setInterval(() => setI((x) => (x + 1) % criticals.length), 8000);
    return () => clearInterval(t);
  }, [criticals.length]);
  const atual = Math.min(i, criticals.length - 1);
  const c = criticals[atual];
  const onde = c.labels.host || c.labels.site || c.labels.url || "";
  const comLetreiro = warnings.length > 0 || deploys.length > 0;
  return (
    <>
      <div className={`tv tv--alarme${comLetreiro ? " tv--com-letreiro" : ""}`}>
        <TopoTV nome={nome} estado={estado} atrasado={atrasado} />
        <main className="tv-alarme" role="alert">
          <div className="tv-alarme__orbe" aria-hidden="true">
            <span className="tv-alarme__anel" />
            <span className="tv-alarme__anel tv-alarme__anel--2" />
            <OctagonAlert />
          </div>
          <div className="tv-alarme__chip">
            Alerta crítico{criticals.length > 1 ? ` · ${atual + 1} de ${criticals.length}` : ""}
          </div>
          <h2 className="tv-alarme__regra">{c.rule}</h2>
          {onde && (
            <div className="tv-alarme__onde">
              <small>{c.labels.host ? "Servidor" : "Onde"}</small>
              {onde}
            </div>
          )}
          <div className="tv-fatos">
            <Fato rotulo="Começou" valor={desde(c.since)} />
            {/* O valor guardado no alerta é o do DISPARO, não o de agora: um alerta de 42
                dias mostrava "6 min sem sinal" como se fosse o estado atual. */}
            <Fato rotulo="Ao disparar" valor={valorDoAlerta(c.metric, c.value)} />
            {c.diagnosis && <Fato rotulo="Diagnóstico" valor={c.diagnosis} />}
            {c.oncall && <Fato rotulo="Plantão" valor={c.oncall} />}
          </div>
          {criticals.length > 1 && (
            <div className="tv-alarme__passos" aria-hidden="true">
              {criticals.map((_, k) => (
                // A chave do passo atual muda a cada troca para REMONTAR o span e
                // reiniciar a barra de 8 s; os outros passos são decorativos.
                <span key={k === atual ? `atual-${atual}` : k} className={k === atual ? "tv-alarme__passo--atual" : undefined} />
              ))}
            </div>
          )}
        </main>
      </div>
      <LetreiroTV warnings={warnings} deploys={deploys} />
    </>
  );
}

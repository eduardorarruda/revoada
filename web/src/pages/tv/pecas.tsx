// Peças visuais do modo TV (pages/TVView.tsx e o modo TV do HealthWall): cabeçalho
// com estado e relógio, número grande com unidade, mini-série e letreiro.
import { useEffect, useId, useState, type ReactNode } from "react";
import { ArrowDownRight, ArrowRight, ArrowUpRight, Rocket, TriangleAlert } from "lucide-react";
import { APP_TIME_ZONE, APP_TIME_ZONE_LABEL, formatTime } from "../../format";
import { CountUp } from "../../motion";
import { separar } from "../../motion/CountUp";
import type { TVTakeover } from "../../api";
import { tendencia, type TomTV } from "./estado";
import "./tv.css";

const dataLonga = new Intl.DateTimeFormat("pt-BR", {
  timeZone: APP_TIME_ZONE,
  weekday: "long",
  day: "numeric",
  month: "long",
});

/** Relógio grande, sempre em Brasília, com a data e o selo de "ao vivo". */
export function Relogio({ atrasado }: { atrasado?: string | null }) {
  const [agora, setAgora] = useState(() => new Date());
  useEffect(() => {
    const t = setInterval(() => setAgora(new Date()), 1000);
    return () => clearInterval(t);
  }, []);
  return (
    <div className="tv-relogio" title={APP_TIME_ZONE_LABEL}>
      <span className="tv-relogio__hora">
        {formatTime(agora)}
        <small>BRT</small>
      </span>
      <span className={`tv-relogio__data${atrasado ? " tv-relogio__data--atrasado" : ""}`}>
        <span className="tv-relogio__vivo" aria-hidden="true" />
        {atrasado ?? `${dataLonga.format(agora)} · ao vivo`}
      </span>
    </div>
  );
}

export function EstadoTV({ tom, texto }: { tom: TomTV; texto: string }) {
  return (
    <span className={`tv-estado tv-estado--${tom}`} role="status">
      <span className="tv-estado__ponto" aria-hidden="true" />
      {texto}
    </span>
  );
}

/**
 * Cabeçalho de toda tela da TV. `progresso` (em segundos) desenha a barra que enche
 * até a próxima troca de tela; `chaveProgresso` reinicia a barra a cada troca.
 */
export function TopoTV({
  nome,
  sub,
  estado,
  atrasado,
  progresso,
  chaveProgresso,
}: {
  nome: string;
  sub?: ReactNode;
  estado: { tom: TomTV; texto: string };
  atrasado?: string | null;
  progresso?: number;
  chaveProgresso?: string | number;
}) {
  return (
    <header className="tv-topo">
      <div className="tv-topo__quem">
        <h1 className="tv-topo__nome">{nome || "Revoada"}</h1>
        {sub && <div className="tv-topo__sub">{sub}</div>}
      </div>
      <EstadoTV {...estado} />
      <Relogio atrasado={atrasado} />
      {progresso != null && progresso > 0 && (
        <div className="tv-progresso" aria-hidden="true">
          <span key={chaveProgresso} style={{ animationDuration: `${progresso}s` }} />
        </div>
      )}
    </header>
  );
}

/** Pontos da playlist: qual tela é esta, de quantas. */
export function PontosTV({ total, atual }: { total: number; atual: number }) {
  if (total <= 1) return null;
  return (
    <span className="tv-pontos" aria-label={`Tela ${atual + 1} de ${total}`}>
      {Array.from({ length: total }, (_, i) => (
        <span key={i} className={i === atual ? "tv-pontos__atual" : undefined} />
      ))}
    </span>
  );
}

/**
 * Número grande com a unidade menor ao lado: "42,3" em 13vh e "%" em 5vh. Junto, a
 * unidade competia com o número; separada, o olho lê o valor primeiro.
 */
export function NumeroTV({ texto }: { texto: string }) {
  // Sempre a mesma estrutura, com ou sem número: se o "—" trocasse o elemento, o
  // CountUp seria remontado e perderia o último valor medido.
  const p = separar(texto);
  const numero = p ? texto.slice(0, texto.length - p.depois.length) : texto;
  const unidade = p?.depois.trim();
  return (
    <>
      <span className="tv-bloco__numero">
        <CountUp texto={numero} />
      </span>
      {unidade && <span className="tv-bloco__unidade">{unidade}</span>}
    </>
  );
}

/** Seta de tendência da janela curta (≈10 min). */
export function TendenciaTV({ valores }: { valores: (number | null)[] }) {
  const t = tendencia(valores);
  if (!t) return null;
  const [Icone, texto] =
    t === "sobe" ? [ArrowUpRight, "subindo"] : t === "desce" ? [ArrowDownRight, "descendo"] : [ArrowRight, "estável"];
  return (
    <span className="tv-bloco__tendencia">
      <Icone size="1em" aria-hidden={true} style={{ verticalAlign: "-0.12em" }} /> {texto}
    </span>
  );
}

/** Mini-série dos últimos pontos, com área em degradê. */
export function FaiscaTV({ valores }: { valores: (number | null)[] }) {
  const id = useId().replace(/:/g, "");
  const pontos = valores.map((v, i) => ({ v, i })).filter((p): p is { v: number; i: number } => p.v != null && Number.isFinite(p.v));
  if (pontos.length < 2) return <div className="tv-faisca" aria-hidden="true" />;
  const min = Math.min(...pontos.map((p) => p.v));
  const max = Math.max(...pontos.map((p) => p.v));
  const faixa = max - min || 1;
  const n = Math.max(valores.length - 1, 1);
  const xy = pontos.map((p) => [(p.i / n) * 100, 36 - ((p.v - min) / faixa) * 30] as const);
  const linha = xy.map(([x, y], i) => `${i ? "L" : "M"}${x.toFixed(2)},${y.toFixed(2)}`).join(" ");
  const area = `${linha} L${xy[xy.length - 1][0].toFixed(2)},40 L${xy[0][0].toFixed(2)},40 Z`;
  return (
    <svg className="tv-faisca" viewBox="0 0 100 40" preserveAspectRatio="none" aria-hidden="true">
      <defs>
        <linearGradient id={`f${id}`} x1="0" x2="0" y1="0" y2="1">
          <stop offset="0" stopColor="currentColor" stopOpacity="0.35" />
          <stop offset="1" stopColor="currentColor" stopOpacity="0" />
        </linearGradient>
      </defs>
      <path d={area} fill={`url(#f${id})`} />
      <path d={linha} className="tv-faisca__linha" />
    </svg>
  );
}

/** Item extra do letreiro (eventos além de avisos e deploys). */
export interface ItemLetreiro {
  key: string;
  conteudo: ReactNode;
}

/**
 * Letreiro do rodapé: avisos (em âmbar), eventos extras (quando houver) e deploys
 * recentes, rolando. Sem avisos, com extras, o rótulo diz `rotuloExtras`, em dourado.
 */
export function LetreiroTV({
  warnings,
  deploys,
  extras = [],
  rotuloExtras = "Equipe",
}: {
  warnings: TVTakeover[];
  deploys: { ts: string; title: string }[];
  extras?: ItemLetreiro[];
  rotuloExtras?: string;
}) {
  const itens = [
    ...warnings.map((w, i) => ({
      key: `w${i}`,
      aviso: true,
      conteudo: (
        <>
          <TriangleAlert size="1em" aria-hidden={true} /> {w.rule}
          {w.labels.host ? `, ${w.labels.host}` : ""}
        </>
      ),
    })),
    ...extras.map((x) => ({ key: `x${x.key}`, aviso: false, conteudo: x.conteudo })),
    ...deploys.map((d, i) => ({
      key: `d${i}`,
      aviso: false,
      conteudo: (
        <>
          <Rocket size="1em" aria-hidden={true} /> {d.title}
        </>
      ),
    })),
  ];
  if (itens.length === 0) return null;
  const volta = (copia: string) =>
    itens.map((it) => (
      <span key={`${copia}-${it.key}`} className={`tv-letreiro__item${it.aviso ? " tv-letreiro__item--aviso" : ""}`}>
        {it.conteudo}
      </span>
    ));
  return (
    // aria-hidden: o letreiro é reforço visual (e tem duas cópias para o laço);
    // o que importa de verdade já é anunciado pelos avisos de alerta.
    <div className="tv-letreiro" aria-hidden="true">
      {warnings.length > 0 ? (
        <span className="tv-letreiro__rotulo">Avisos</span>
      ) : extras.length > 0 ? (
        <span className="tv-letreiro__rotulo tv-letreiro__rotulo--equipe">{rotuloExtras}</span>
      ) : (
        <span className="tv-letreiro__rotulo">Deploys</span>
      )}
      <div className="tv-letreiro__trilho">
        {/* Duas cópias em fila: a animação anda metade e recomeça sem emenda. */}
        <span className="tv-letreiro__fita">
          {volta("a")}
          <span className="tv-letreiro__copia">{volta("b")}</span>
        </span>
      </div>
    </div>
  );
}

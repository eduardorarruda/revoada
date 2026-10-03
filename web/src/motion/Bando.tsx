// Bando: o símbolo do Revoada em movimento — cinco pássaros em formação de V. É o
// indicador de "trabalhando" (carregando tela, tarefa rodando no agente). Com
// prefers-reduced-motion o bando fica parado (o CSS desliga as animações).
import "./bando.css";

const POSICOES = [
  { x: 40, y: 10 },
  { x: 26, y: 20 },
  { x: 54, y: 20 },
  { x: 12, y: 30 },
  { x: 68, y: 30 },
];

export function Bando({ rotulo = "Trabalhando…", tamanho = 72 }: { rotulo?: string; tamanho?: number }) {
  return (
    <span className="bando" role="status" aria-label={rotulo}>
      <svg viewBox="0 0 80 40" width={tamanho} height={tamanho / 2} aria-hidden="true">
        {POSICOES.map((p, i) => (
          <g key={i} className="bando__ave" style={{ animationDelay: `${i * 0.12}s` }} transform={`translate(${p.x} ${p.y})`}>
            <path className="bando__asa" style={{ animationDelay: `${i * 0.09}s` }} d="M-6 0 Q-3 -3 0 0 Q3 -3 6 0" />
          </g>
        ))}
      </svg>
    </span>
  );
}

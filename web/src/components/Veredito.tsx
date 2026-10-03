// Veredito — a frase grande do topo da Início: o estado da frota em uma linha.
//
// Vem ANTES dos números porque é o que a pessoa foi buscar: "está tudo bem?".
// O orbe à esquerda carrega o tom (verde, âmbar, vermelho, cinza) e pulsa só
// quando há problema — calmaria não pisca. Quando o veredito muda, a frase nova
// entra por baixo da antiga: a troca é percebida mesmo de canto de olho.
import { AnimatePresence, m } from "motion/react";
import { CheckCircle2, CircleAlert, CircleHelp, OctagonAlert, Sparkles } from "lucide-react";
import type { Veredito as V } from "../pages/homeResumo";
import { DUR, EASE } from "../motion";

const ICONE = {
  ok: CheckCircle2,
  warn: CircleAlert,
  crit: OctagonAlert,
  desconhecido: CircleHelp,
  vazio: Sparkles,
} as const;

const ROTULO = {
  ok: "Estado geral: em ordem",
  warn: "Estado geral: atenção",
  crit: "Estado geral: crítico",
  desconhecido: "Estado geral: desconhecido",
  vazio: "Estado geral: nada monitorado",
} as const;

export function Veredito({ veredito, rodape }: { veredito: V; rodape?: React.ReactNode }) {
  const Icone = ICONE[veredito.tom];
  return (
    <section className={`veredito veredito--${veredito.tom}`} aria-label={ROTULO[veredito.tom]}>
      {/* O leitor de tela ouve só esta linha; o título animado (que por 320 ms
          existe em dobro durante a troca) fica fora da região viva. */}
      <p className="so-leitor" aria-live="polite" aria-atomic="true">
        {veredito.titulo}. {veredito.detalhe}
      </p>
      <div className="veredito__orbe" aria-hidden="true" key={veredito.tom}>
        <span className="veredito__anel" />
        <span className="veredito__anel veredito__anel--2" />
        <Icone size={26} />
      </div>
      <div className="veredito__texto" aria-hidden="true">
        <AnimatePresence mode="popLayout" initial={false}>
          <m.h2
            key={veredito.titulo}
            className="veredito__titulo"
            initial={{ opacity: 0, y: 14 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -14 }}
            transition={{ duration: DUR.calmo, ease: EASE.saida }}
          >
            {veredito.titulo}
          </m.h2>
        </AnimatePresence>
        <p className="veredito__detalhe">{veredito.detalhe}</p>
      </div>
      {rodape && <div className="veredito__rodape">{rodape}</div>}
    </section>
  );
}

// PageHeader — esqueleto de topo de toda página: título (fs-28) + subtítulo
// didático (text-2) + slot de ações à direita + botão "?" que abre o HelpPanel.
//
// No celular, um subtítulo longo custa caro: três linhas de explicação empurram
// para baixo justamente o número que a pessoa abriu a tela para ver. Por isso o
// subtítulo longo vira um bloco recolhível "Sobre esta tela" em tela estreita, e
// continua um parágrafo normal no desktop, onde há espaço de sobra. O texto é o
// mesmo e continua no DOM — nada de duplicar conteúdo e esconder metade com
// `display: none`, que engana leitor de tela e Ctrl+F.
import type { ReactNode } from "react";
import { Collapsible } from "./Collapsible";
import { useMobile } from "../hooks/useMobile";

/**
 * Acima deste tamanho o subtítulo é considerado longo. ~80 caracteres é mais ou
 * menos duas linhas num aparelho de 375px: até aí o texto ajuda; daí em diante
 * ele vira parede.
 */
const LIMITE_SUBTITULO = 80;

export function PageHeader({
  title,
  subtitle,
  actions,
  onHelp,
}: {
  title: string;
  subtitle?: string;
  actions?: ReactNode;
  onHelp?: () => void;
}) {
  const estreita = useMobile();
  const recolher = !!subtitle && estreita && subtitle.length > LIMITE_SUBTITULO;

  return (
    <header className="page-header">
      <div className="page-header__main">
        <h1 className="page-header__title">{title}</h1>
        {subtitle &&
          (recolher ? (
            <Collapsible titulo="Sobre esta tela" variante="discreto">
              <p className="page-header__subtitle">{subtitle}</p>
            </Collapsible>
          ) : (
            <p className="page-header__subtitle">{subtitle}</p>
          ))}
      </div>
      <div className="page-header__actions">
        {actions}
        {onHelp && (
          <button
            type="button"
            className="btn btn--ghost page-header__help"
            aria-label="Ajuda desta página"
            onClick={onHelp}
          >
            ?
          </button>
        )}
      </div>
    </header>
  );
}

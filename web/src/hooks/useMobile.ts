// useMobile — "a tela é estreita?", com o MESMO ponto de corte do CSS (768px).
//
// A maior parte da responsividade do painel é CSS, e assim deve continuar. Este
// hook existe só para os casos em que o layout muda de ESTRUTURA e não de estilo
// — quando um texto deixa de ser um parágrafo e passa a ser um bloco recolhível,
// por exemplo. Duplicar o conteúdo no DOM e esconder um com `display: none`
// resolveria no visual e estragaria no leitor de tela e na busca da página.
import { useEffect, useState } from "react";

const CONSULTA = "(max-width: 767px)";

/**
 * `matchMedia` nem sempre existe: o jsdom dos testes não o implementa, e um
 * componente de layout não pode derrubar a árvore inteira por causa disso. Sem
 * a API, a resposta é "não é estreita" — o desktop é o caminho que mostra TUDO,
 * então errar para esse lado nunca esconde informação de ninguém.
 */
function consultar(): MediaQueryList | null {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return null;
  return window.matchMedia(CONSULTA);
}

export function useMobile(): boolean {
  const [estreita, setEstreita] = useState(() => consultar()?.matches ?? false);
  useEffect(() => {
    const mq = consultar();
    if (!mq) return;
    const aoMudar = () => setEstreita(mq.matches);
    aoMudar();
    mq.addEventListener("change", aoMudar);
    return () => mq.removeEventListener("change", aoMudar);
  }, []);
  return estreita;
}

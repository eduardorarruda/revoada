// useFocoPreso — o contrato de teclado de um diálogo modal (WCAG 2.1.2 / 2.4.3):
// ao abrir, leva o foco para dentro; Tab e Shift+Tab giram só entre os focáveis do
// diálogo; Esc fecha; ao fechar, o foco volta para quem abriu. Compartilhado por
// Modal, HelpPanel e CorrelationDrawer para os três se comportarem igual.
import { useEffect, useRef, type RefObject } from "react";

export const FOCAVEIS =
  'a[href],button:not([disabled]),textarea,input,select,[tabindex]:not([tabindex="-1"])';

// Pilha dos diálogos abertos: só o do topo reage a Esc e Tab. Sem ela, um diálogo
// aberto por cima de outro (o "Contexto" dos Logs sai do modal de intervalo)
// fechava os dois num Esc só — stopPropagation não segura outro ouvinte no mesmo nó.
let pilha: readonly object[] = [];

export function useFocoPreso(
  ref: RefObject<HTMLElement | null>,
  aberto: boolean,
  onFechar: () => void,
  focoInicialRef?: RefObject<HTMLElement | null>,
) {
  // O efeito roda UMA vez por abertura. `onFechar` e a ref de foco inicial entram
  // por ref, não pelas dependências: as telas passam `onFechar` como arrow inline,
  // e o painel re-renderiza a tela de trás a cada 5–15 s. Com `onFechar` nas
  // dependências, cada atualização desmontava e remontava o efeito — devolvia o
  // foco para fora e depois focava o ✕ —, e quem estava digitando perdia o cursor.
  const onFecharRef = useRef(onFechar);
  onFecharRef.current = onFechar;
  const focoInicial = useRef(focoInicialRef);
  focoInicial.current = focoInicialRef;

  useEffect(() => {
    if (!aberto) return;
    const anterior = document.activeElement as HTMLElement | null;
    const container = ref.current;
    const eu = {};
    pilha = [...pilha, eu];

    // Foco inicial: ref explícita (ex.: Cancelar do ConfirmDialog) ou 1º focável.
    const alvoInicial = focoInicial.current?.current;
    if (alvoInicial) alvoInicial.focus();
    else (container?.querySelector<HTMLElement>(FOCAVEIS) ?? container)?.focus();

    const onKey = (e: KeyboardEvent) => {
      if (pilha[pilha.length - 1] !== eu) return;
      if (e.key === "Escape") {
        e.stopPropagation();
        onFecharRef.current();
        return;
      }
      if (e.key !== "Tab" || !container) return;
      const itens = Array.from(container.querySelectorAll<HTMLElement>(FOCAVEIS));
      if (itens.length === 0) {
        e.preventDefault();
        return;
      }
      const primeiro = itens[0];
      const ultimo = itens[itens.length - 1];
      const atual = document.activeElement;
      // Foco fora do diálogo (o elemento focado sumiu ao trocar o conteúdo, como
      // no drawer de correlação): o próximo Tab volta para dentro, não para a página.
      if (!container.contains(atual)) {
        e.preventDefault();
        (e.shiftKey ? ultimo : primeiro).focus();
      } else if (e.shiftKey && atual === primeiro) {
        e.preventDefault();
        ultimo.focus();
      } else if (!e.shiftKey && atual === ultimo) {
        e.preventDefault();
        primeiro.focus();
      }
    };
    document.addEventListener("keydown", onKey, true);
    return () => {
      document.removeEventListener("keydown", onKey, true);
      pilha = pilha.filter((d) => d !== eu);
      anterior?.focus?.();
    };
  }, [aberto, ref]);
}

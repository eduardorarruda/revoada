// Collapsible — o bloco recolhível padrão do Revoada.
//
// Por que existe: a tela precisa mostrar PRIMEIRO o número e o problema; a
// explicação, a lista inteira e o passo a passo entram depois, a pedido. Sem um
// componente único, cada tela inventava o seu <details> e a interface ficava com
// cinco jeitos diferentes de esconder texto.
//
// É um <details>/<summary> de verdade, não uma div com onClick: o teclado abre
// com Enter/Espaço, o leitor de tela anuncia "expandido/recolhido" e o Ctrl+F do
// navegador encontra o conteúdo fechado — três coisas que a versão com div perde.
import { useCallback, useState, type ReactNode } from "react";

// Estado aberto/fechado sobrevive ao recarregar quando a chamada passa
// `persistKey`. É preferência de quem usa, não dado — localStorage basta, e a
// falha (modo privado, cota cheia) nunca pode derrubar a tela.
function lerPersistido(key: string | undefined, fallback: boolean): boolean {
  if (!key) return fallback;
  try {
    const v = localStorage.getItem(`painel-collapsible:${key}`);
    if (v === "1") return true;
    if (v === "0") return false;
  } catch {
    /* sem localStorage — segue com o default */
  }
  return fallback;
}

function gravarPersistido(key: string | undefined, aberto: boolean) {
  if (!key) return;
  try {
    localStorage.setItem(`painel-collapsible:${key}`, aberto ? "1" : "0");
  } catch {
    /* sem localStorage — a preferência simplesmente não persiste */
  }
}

export interface CollapsibleProps {
  /** Rótulo do cabeçalho. É o que a pessoa lê com o bloco fechado. */
  titulo: ReactNode;
  /**
   * Contagem exibida ao lado do título (ex.: 62 servidores dentro). Só aparece
   * quando é um número — `0` some de propósito: "Ver todos (0)" é um convite a
   * abrir uma gaveta vazia.
   */
  contador?: number;
  /**
   * Uma linha de contexto à direita do título, visível com o bloco FECHADO.
   * É o que evita que recolher vire esconder: "3 críticos · pior: web01".
   */
  resumo?: ReactNode;
  /** Aberto na primeira visita (default: fechado). */
  defaultOpen?: boolean;
  /** Chave para lembrar aberto/fechado entre visitas. Sem ela, não persiste. */
  persistKey?: string;
  /**
   * `secao` (default) é o bloco com moldura, usado dentro de um cartão.
   * `discreto` é a versão sem moldura para dicas curtas e textos de apoio.
   */
  variante?: "secao" | "discreto";
  /**
   * Modo controlado: quando `open` é passado, quem manda no aberto/fechado é a
   * tela, não o componente. Usado no Guia, onde uma busca precisa abrir as
   * seções que casaram e o sumário precisa abrir a seção para onde vai rolar.
   */
  open?: boolean;
  onOpenChange?: (aberto: boolean) => void;
  /** id do <details>, para o sumário do Guia conseguir rolar até ele. */
  id?: string;
  children: ReactNode;
}

export function Collapsible({
  titulo,
  contador,
  resumo,
  defaultOpen = false,
  persistKey,
  variante = "secao",
  open,
  onOpenChange,
  id,
  children,
}: CollapsibleProps) {
  const [abertoInterno, setAbertoInterno] = useState(() => lerPersistido(persistKey, defaultOpen));
  const controlado = open !== undefined;
  const aberto = controlado ? open : abertoInterno;

  const onToggle = useCallback(
    (e: React.SyntheticEvent<HTMLDetailsElement>) => {
      const v = e.currentTarget.open;
      if (!controlado) {
        setAbertoInterno(v);
        gravarPersistido(persistKey, v);
      }
      onOpenChange?.(v);
    },
    [controlado, persistKey, onOpenChange],
  );

  return (
    <details id={id} className={`colap colap--${variante}`} open={aberto} onToggle={onToggle}>
      <summary className="colap__resumo">
        <span className="colap__seta" aria-hidden="true" />
        <span className="colap__titulo">{titulo}</span>
        {typeof contador === "number" && contador > 0 && (
          <span className="colap__contador tabular">{contador}</span>
        )}
        {resumo && <span className="colap__contexto">{resumo}</span>}
      </summary>
      <div className="colap__corpo">{children}</div>
    </details>
  );
}

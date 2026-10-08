// Linha do tempo do replay (um botão por passo; ↑ ↓ ← → Home End para andar) e o
// painel com o detalhe do passo escolhido. No desktop o detalhe fica numa coluna
// fixa ao lado; abaixo de 1100px ele abre DENTRO do passo escolhido, para não
// ficar a uma rolagem inteira de distância de quem tocou no passo.
import { useEffect, useRef, useState, type CSSProperties, type KeyboardEvent, type ReactNode } from "react";
import { AnimatePresence, m, useReducedMotion } from "motion/react";
import { Bot, Copy, MessageSquareText, Sparkles, Wrench } from "lucide-react";
import type { IaMensagem, IaPasso } from "../../api.ia";
import { Badge, Button, Card, Collapsible, useToast } from "../../components";
import { DUR, EASE } from "../../motion";
import { Aviso } from "./comum";
import { fmtCustoPasso, fmtMs } from "./formato";
import {
  barraDoPasso,
  mensagensDoPasso,
  requisicaoOpenAI,
  ROTULO_TIPO,
  rotuloDoPasso,
  rotuloMotivosFim,
  rotuloOperacao,
  textoTokens,
  tipoDoPasso,
  type MarcaRepeticao,
  type TipoPasso,
} from "./replay";

const ICONE_TIPO: Record<TipoPasso, typeof Bot> = {
  modelo: Sparkles,
  ferramenta: Wrench,
  agente: Bot,
  outro: MessageSquareText,
};

/** Mesma mola da pílula do menu: a seleção desliza de um passo para o outro sem quicar. */
const MOLA_SELECAO = { type: "spring", stiffness: 520, damping: 42 } as const;

// Tem de ser o MESMO corte do @media de .ia-replay em replay.css.
const CONSULTA_LARGA = "(min-width: 1100px)";

/**
 * "Cabe o detalhe numa coluna ao lado?" Sem matchMedia (jsdom dos testes) a
 * resposta é sim: o desktop é o caminho que mostra tudo de uma vez.
 */
export function useTelaLarga(): boolean {
  const consultar = () => (typeof window.matchMedia === "function" ? window.matchMedia(CONSULTA_LARGA) : null);
  const [larga, setLarga] = useState(() => consultar()?.matches ?? true);
  useEffect(() => {
    const mq = consultar();
    if (!mq) return;
    const aoMudar = () => setLarga(mq.matches);
    aoMudar();
    mq.addEventListener("change", aoMudar);
    return () => mq.removeEventListener("change", aoMudar);
  }, []);
  return larga;
}

/** CSS custom properties tipadas (o React não conhece `--nome`). */
export function vars(v: Record<string, string | number>): CSSProperties {
  return v as CSSProperties;
}

function Numeros({ p }: { p: IaPasso }) {
  const tipo = tipoDoPasso(p);
  const custo = fmtCustoPasso(p);
  return (
    <span className="ia-passo__nums tabular">
      {p.modelo && tipo !== "modelo" && <span>{p.modelo}</span>}
      {tipo === "modelo" && (
        <>
          <span>{textoTokens(p)}</span>
          <span>
            {custo.valor}
            {custo.origem && ` · ${custo.origem}`}
          </span>
        </>
      )}
      <span>{fmtMs(p.duracao_ms)}</span>
    </span>
  );
}

type Barra = { inicioPct: number; larguraPct: number };

function ConteudoBotao({ p, i, total, marca, barra }: { p: IaPasso; i: number; total: number; marca?: MarcaRepeticao; barra: Barra }) {
  const tipo = tipoDoPasso(p);
  const Icone = ICONE_TIPO[tipo];
  return (
    <>
      <span className="so-leitor">
        Passo {i + 1} de {total}.{" "}
      </span>
      <span className="ia-passo__linha">
        <span className="ia-passo__tipo">
          <Icone size={14} aria-hidden={true} /> {ROTULO_TIPO[tipo]}
        </span>
        <span className="ia-passo__titulo">{rotuloDoPasso(p)}</span>
        {p.erro && <Badge state="crit">erro</Badge>}
        {marca && (
          <Badge state="warn">
            repetição {marca.n} de {marca.repeticao.vezes}
          </Badge>
        )}
      </span>
      <Numeros p={p} />
      {p.erro && <span className="ia-passo__erro">{p.erro}</span>}
      <span className="ia-barra" aria-hidden="true">
        <span className="ia-barra__trecho" style={vars({ "--inicio": `${barra.inicioPct}%`, "--largura": `${barra.larguraPct}%` })} />
      </span>
    </>
  );
}

/** Pedido de destaque de uma repetição: `vez` muda a cada clique e remonta o brilho. */
export interface PedidoDestaque {
  primeiroSpanId: string;
  vez: number;
}

interface ItemPassoProps {
  p: IaPasso;
  barra: Barra;
  i: number;
  total: number;
  sel: boolean;
  marca?: MarcaRepeticao;
  destaque: PedidoDestaque | null;
  detalhe?: ReactNode;
  onSel: () => void;
  onKey: (e: KeyboardEvent) => void;
  refBotao: (el: HTMLButtonElement | null) => void;
}

function ItemPasso({ p, barra, i, total, sel, marca, destaque, detalhe, onSel, onKey, refBotao }: ItemPassoProps) {
  const reduzir = useReducedMotion();
  const destacar = marca != null && destaque != null && marca.repeticao.primeiro_span_id === destaque.primeiroSpanId;
  const classes = [
    "ia-passo",
    p.profundidade > 0 ? "ia-passo--filho" : "",
    sel ? "ia-passo--sel" : "",
    p.erro ? "ia-passo--erro" : "",
    marca ? "ia-passo--repeticao" : "",
  ];
  return (
    <li style={vars({ "--i": i })}>
      <button
        type="button"
        ref={refBotao}
        className={classes.filter(Boolean).join(" ")}
        style={vars({ "--nivel": p.profundidade })}
        aria-current={sel ? "step" : undefined}
        tabIndex={sel ? 0 : -1}
        onClick={onSel}
        onKeyDown={onKey}
      >
        {sel && <m.span className="ia-passo__sel" layoutId="ia-passo-sel" transition={MOLA_SELECAO} aria-hidden="true" />}
        {destacar && (
          <span key={destaque.vez} className="ia-passo__destaque" style={vars({ "--n": marca.n - 1 })} aria-hidden="true" />
        )}
        <ConteudoBotao p={p} i={i} total={total} marca={marca} barra={barra} />
      </button>
      <AnimatePresence initial={false}>
        {sel && detalhe && (
          <m.div
            key="detalhe"
            className="ia-passo__detalhe"
            initial={{ height: 0, opacity: 0 }}
            animate={{ height: "auto", opacity: 1 }}
            exit={{ height: 0, opacity: 0 }}
            // Altura não é coberta pelo MotionConfig: a preferência é lida aqui.
            transition={reduzir ? { duration: 0 } : { duration: DUR.calmo, ease: EASE.saida }}
          >
            {detalhe}
          </m.div>
        )}
      </AnimatePresence>
    </li>
  );
}

/** Para onde cada tecla leva, a partir do passo atual. */
function destinoDaTecla(tecla: string, sel: number, total: number): number | null {
  switch (tecla) {
    case "ArrowRight":
    case "ArrowDown":
      return sel + 1;
    case "ArrowLeft":
    case "ArrowUp":
      return sel - 1;
    case "Home":
      return 0;
    case "End":
      return total - 1;
    default:
      return null;
  }
}

/** Foco (e rolagem até o passo) quando quem pede é algo fora da lista. */
function usePedidoDeFoco(pedidoFoco: number, alvo: () => HTMLButtonElement | null | undefined) {
  const reduzir = useReducedMotion();
  useEffect(() => {
    if (pedidoFoco <= 0) return;
    const el = alvo();
    if (!el) return;
    el.focus({ preventScroll: true });
    // scrollIntoView suave não é coberto pela regra global de menos movimento.
    if (typeof el.scrollIntoView === "function") el.scrollIntoView({ block: "center", behavior: reduzir ? "auto" : "smooth" });
    // Só o pedido dispara o foco; trocar `sel` pelo clique ou pelas setas já foca sozinho.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pedidoFoco]);
}

export interface LinhaDoTempoProps {
  passos: IaPasso[];
  inicioMs: number;
  duracaoMs: number;
  repetidos: Map<string, MarcaRepeticao>;
  sel: number;
  onSel: (i: number) => void;
  /** Muda quando quem está fora da lista escolhe um passo e o foco deve ir até ele. */
  pedidoFoco?: number;
  /** A cada "Ir para a primeira chamada": acende os passos daquela repetição. */
  destaque?: PedidoDestaque | null;
  /** Tela estreita: o detalhe do passo escolhido abre dentro dele. */
  detalheNoPasso?: (p: IaPasso, i: number) => ReactNode;
}

export function LinhaDoTempo(props: LinhaDoTempoProps) {
  const { passos, inicioMs, duracaoMs, repetidos, sel, onSel, pedidoFoco = 0, destaque = null, detalheNoPasso } = props;
  const botoes = useRef<(HTMLButtonElement | null)[]>([]);
  usePedidoDeFoco(pedidoFoco, () => botoes.current[sel]);
  const ir = (i: number) => {
    const alvo = Math.max(0, Math.min(passos.length - 1, i));
    onSel(alvo);
    botoes.current[alvo]?.focus();
  };
  const onKey = (e: KeyboardEvent) => {
    const destino = destinoDaTecla(e.key, sel, passos.length);
    if (destino == null) return;
    e.preventDefault();
    ir(destino);
  };
  return (
    <Card title="Passo a passo">
      <p className="ia-sub">
        Em ordem de início, recuado pelo nível na árvore. A barra mostra quando o passo rodou dentro da execução. Use ↑ ↓ ou ←
        → para andar entre os passos.
      </p>
      <ol className="ia-passos" aria-label="Passos da execução">
        {passos.map((p, i) => (
          <ItemPasso
            key={p.span_id}
            p={p}
            barra={barraDoPasso(p, inicioMs, duracaoMs)}
            i={i}
            total={passos.length}
            sel={i === sel}
            marca={repetidos.get(p.span_id)}
            destaque={destaque}
            detalhe={i === sel && detalheNoPasso ? detalheNoPasso(p, i) : undefined}
            onSel={() => onSel(i)}
            onKey={onKey}
            refBotao={(el) => {
              botoes.current[i] = el;
            }}
          />
        ))}
      </ol>
    </Card>
  );
}

function CopiarRequisicao({ passo, mensagens }: { passo: IaPasso; mensagens: IaMensagem[] | null }) {
  const toast = useToast();
  const copiar = async () => {
    if (!mensagens) return;
    const entrada = mensagensDoPasso(mensagens, passo.span_id, "entrada");
    const json = JSON.stringify(requisicaoOpenAI(passo, mensagens), null, 2);
    try {
      if (!navigator.clipboard) throw new Error("área de transferência indisponível neste navegador");
      await navigator.clipboard.writeText(json);
      const ressalva = entrada.some((m) => m.redigido || m.truncado) ? " Atenção: há trechos redigidos ou truncados." : "";
      toast.success(`Requisição copiada no formato OpenAI. O Revoada não chama o modelo: rode onde quiser.${ressalva}`);
    } catch (e) {
      toast.error(`Não deu para copiar: ${e instanceof Error ? e.message : "erro desconhecido"}.`);
    }
  };
  return (
    <div className="ia-copiar">
      <div>
        <Button onClick={copiar} disabled={!mensagens}>
          <Copy size={16} aria-hidden={true} /> Copiar como requisição
        </Button>
      </div>
      {!mensagens && <span className="ia-sub">Mostre a conversa (abaixo) para copiar as mensagens deste passo.</span>}
    </div>
  );
}

type Linha = [rotulo: string, valor: ReactNode];

function Operacao({ operacao }: { operacao: string }) {
  const { rotulo, cru } = rotuloOperacao(operacao);
  return (
    <>
      {rotulo}
      {cru && <span className="ia-mono ia-detalhe__cru"> ({cru})</span>}
    </>
  );
}

function linhasDoPasso(passo: IaPasso, inicioMs: number): Linha[] {
  const linhas: Linha[] = [
    ["Operação", <Operacao operacao={passo.operacao} />],
    ["Começou", `+${fmtMs(Math.max(0, passo.ts_ms - inicioMs))} do início`],
    ["Duração", fmtMs(passo.duracao_ms)],
  ];
  if (tipoDoPasso(passo) === "modelo") {
    const custo = fmtCustoPasso(passo);
    linhas.push(
      ["Modelo", `${passo.modelo || "não informado"}${passo.provedor ? ` (${passo.provedor})` : ""}`],
      ["Tokens", textoTokens(passo, true)],
      ["Custo", custo.origem ? `${custo.valor} · ${custo.origem}` : custo.valor],
      ["Motivo do fim", rotuloMotivosFim(passo.motivos_fim)],
    );
  }
  if (passo.ferramenta) linhas.push(["Ferramenta", passo.ferramenta]);
  return linhas;
}

function ListaDetalhe({ linhas, mono = false }: { linhas: Linha[]; mono?: boolean }) {
  return (
    <dl className="ia-detalhe">
      {linhas.map(([k, v]) => (
        <div key={k} className="ia-detalhe__linha">
          <dt>{k}</dt>
          <dd className={mono ? "ia-mono" : "tabular"}>{v}</dd>
        </div>
      ))}
    </dl>
  );
}

function Identificadores({ passo }: { passo: IaPasso }) {
  const ids: Linha[] = [];
  if (passo.chamada_id) ids.push(["Id da chamada", passo.chamada_id]);
  ids.push(["Id do passo (span)", passo.span_id]);
  return (
    <Collapsible variante="discreto" titulo="Identificadores">
      <p className="ia-sub">Para procurar este passo nos logs ou no código da aplicação.</p>
      <ListaDetalhe linhas={ids} mono />
    </Collapsible>
  );
}

interface DetalhePassoProps {
  passo: IaPasso;
  /** Posição do passo (0 = primeiro) e o total, para o "Passo 2 de 7". */
  indice: number;
  total: number;
  inicioMs: number;
  mensagens: IaMensagem[] | null;
  /** Dentro do passo, na tela estreita: sem moldura própria (já está num cartão). */
  embutido?: boolean;
}

export function DetalhePasso({ passo, indice, total, inicioMs, mensagens, embutido = false }: DetalhePassoProps) {
  const conteudo = (
    <div className="ia-detalhe-corpo">
      <h3 className="ia-detalhe__titulo">{rotuloDoPasso(passo)}</h3>
      {passo.erro && (
        <Aviso tom="crit" titulo="Este passo terminou com erro">
          <p className="ia-mono">{passo.erro}</p>
        </Aviso>
      )}
      <ListaDetalhe linhas={linhasDoPasso(passo, inicioMs)} />
      <Identificadores passo={passo} />
      {tipoDoPasso(passo) === "modelo" && passo.operacao !== "embeddings" && <CopiarRequisicao passo={passo} mensagens={mensagens} />}
    </div>
  );
  if (embutido) return <div className="ia-detalhe-embutido">{conteudo}</div>;
  return <Card title={`Passo ${indice + 1} de ${total}`}>{conteudo}</Card>;
}

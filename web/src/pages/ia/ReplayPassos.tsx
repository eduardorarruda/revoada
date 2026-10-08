// Linha do tempo do replay (um botão por passo, ← → para andar) e o painel com o
// detalhe do passo escolhido.
import { useEffect, useRef, type CSSProperties, type KeyboardEvent } from "react";
import { Bot, Copy, MessageSquareText, Sparkles, Wrench } from "lucide-react";
import type { IaMensagem, IaPasso } from "../../api.ia";
import { Badge, Button, Card, useToast } from "../../components";
import { fmtCustoPasso, fmtMs, fmtTokens } from "./formato";
import {
  barraDoPasso,
  mensagensDoPasso,
  requisicaoOpenAI,
  ROTULO_TIPO,
  rotuloDoPasso,
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

function Numeros({ p }: { p: IaPasso }) {
  const tipo = tipoDoPasso(p);
  const custo = fmtCustoPasso(p);
  return (
    <span className="ia-passo__nums tabular">
      {p.modelo && tipo !== "modelo" && <span>{p.modelo}</span>}
      {tipo === "modelo" && (
        <>
          <span>
            {fmtTokens(p.tokens_entrada)} → {fmtTokens(p.tokens_saida)} tokens
          </span>
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

function ItemPasso({
  p,
  sel,
  marca,
  barra,
  onSel,
  refBotao,
}: {
  p: IaPasso;
  sel: boolean;
  marca?: MarcaRepeticao;
  barra: { inicioPct: number; larguraPct: number };
  onSel: () => void;
  refBotao: (el: HTMLButtonElement | null) => void;
}) {
  const tipo = tipoDoPasso(p);
  const Icone = ICONE_TIPO[tipo];
  const classes = ["ia-passo", sel ? "ia-passo--sel" : "", p.erro ? "ia-passo--erro" : "", marca ? "ia-passo--repeticao" : ""];
  return (
    <li>
      <button
        type="button"
        ref={refBotao}
        className={classes.filter(Boolean).join(" ")}
        style={{ "--nivel": p.profundidade } as CSSProperties}
        aria-current={sel ? "step" : undefined}
        tabIndex={sel ? 0 : -1}
        onClick={onSel}
      >
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
          <span className="ia-barra__trecho" style={{ left: `${barra.inicioPct}%`, width: `${barra.larguraPct}%` }} />
        </span>
      </button>
    </li>
  );
}

export function LinhaDoTempo({
  passos,
  inicioMs,
  duracaoMs,
  repetidos,
  sel,
  onSel,
  pedidoFoco = 0,
}: {
  passos: IaPasso[];
  inicioMs: number;
  duracaoMs: number;
  repetidos: Map<string, MarcaRepeticao>;
  sel: number;
  onSel: (i: number) => void;
  /** Muda quando quem está fora da lista escolhe um passo e o foco deve ir até ele. */
  pedidoFoco?: number;
}) {
  const botoes = useRef<(HTMLButtonElement | null)[]>([]);
  useEffect(() => {
    if (pedidoFoco > 0) botoes.current[sel]?.focus();
    // Só o pedido dispara o foco; trocar `sel` pelo clique ou pelas setas já foca sozinho.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pedidoFoco]);
  const ir = (i: number) => {
    const alvo = Math.max(0, Math.min(passos.length - 1, i));
    onSel(alvo);
    botoes.current[alvo]?.focus();
  };
  const onKey = (e: KeyboardEvent) => {
    const destino: Record<string, number> = { ArrowRight: sel + 1, ArrowLeft: sel - 1, Home: 0, End: passos.length - 1 };
    if (!(e.key in destino)) return;
    e.preventDefault();
    ir(destino[e.key]);
  };
  return (
    <Card title="Passo a passo">
      <p className="ia-sub">
        Em ordem de início, recuado pelo nível na árvore. A barra mostra quando o passo rodou dentro da execução. Use ← → para
        andar entre os passos.
      </p>
      <ol className="ia-passos" aria-label="Passos da execução" onKeyDown={onKey}>
        {passos.map((p, i) => (
          <ItemPasso
            key={p.span_id}
            p={p}
            sel={i === sel}
            marca={repetidos.get(p.span_id)}
            barra={barraDoPasso(p, inicioMs, duracaoMs)}
            onSel={() => onSel(i)}
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
    <div className="stack" style={{ gap: "var(--sp-1)" }}>
      <div>
        <Button onClick={copiar} disabled={!mensagens}>
          <Copy size={14} aria-hidden={true} /> Copiar como requisição
        </Button>
      </div>
      {!mensagens && <span className="ia-sub">Mostre a conversa (abaixo) para copiar as mensagens deste passo.</span>}
    </div>
  );
}

export function DetalhePasso({ passo, inicioMs, mensagens }: { passo: IaPasso; inicioMs: number; mensagens: IaMensagem[] | null }) {
  const tipo = tipoDoPasso(passo);
  const custo = fmtCustoPasso(passo);
  const linhas: [string, string][] = [
    ["Operação", passo.operacao || "não informada"],
    ["Começou", `+${fmtMs(Math.max(0, passo.ts_ms - inicioMs))} do início`],
    ["Duração", fmtMs(passo.duracao_ms)],
  ];
  if (tipo === "modelo") {
    linhas.push(
      ["Modelo", `${passo.modelo || "não informado"}${passo.provedor ? ` (${passo.provedor})` : ""}`],
      ["Tokens", `${fmtTokens(passo.tokens_entrada)} de entrada · ${fmtTokens(passo.tokens_saida)} de saída · ${fmtTokens(passo.tokens_cache_leitura)} do cache`],
      ["Custo", custo.origem ? `${custo.valor} · ${custo.origem}` : custo.valor],
      ["Motivo do fim", passo.motivos_fim.length > 0 ? passo.motivos_fim.join(", ") : "não informado"],
    );
  }
  if (passo.ferramenta) linhas.push(["Ferramenta", passo.ferramenta]);
  if (passo.chamada_id) linhas.push(["Id da chamada", passo.chamada_id]);
  linhas.push(["Span", passo.span_id]);
  return (
    <Card title={rotuloDoPasso(passo)}>
      {passo.erro && <p className="ia-passo__erro">Erro: {passo.erro}</p>}
      <dl className="ia-detalhe">
        {linhas.map(([k, v]) => (
          <div key={k} style={{ display: "contents" }}>
            <dt>{k}</dt>
            <dd className={k === "Span" || k === "Id da chamada" ? "ia-mono" : "tabular"}>{v}</dd>
          </div>
        ))}
      </dl>
      {tipo === "modelo" && passo.operacao !== "embeddings" && <CopiarRequisicao passo={passo} mensagens={mensagens} />}
    </Card>
  );
}

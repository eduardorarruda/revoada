// Conversa de uma execução: os prompts e respostas gravados, por passo.
//
// Conteúdo de prompt é dado pessoal (ADR 008): só aparece se a captura estiver
// ligada, se o papel da pessoa puder ver, e só depois de um clique; cada leitura
// fica na trilha de auditoria. Cada estado tem a sua explicação, nunca um vazio.
import { useCallback, useId, useRef, useState } from "react";
import { m, useReducedMotion } from "motion/react";
import { getIaConteudo, type IaExecucaoDetalhe, type IaMensagem, type IaPasso } from "../../api.ia";
import { Badge, Button, Card, Collapsible, InfoTip, Skeleton } from "../../components";
import { mensagemDeErro } from "../../format";
import { Bando, DUR, EASE } from "../../motion";
import { Aviso, statusDoErro } from "./comum";
import { mensagensDoPasso, rotuloDoPasso, rotuloPapel } from "./replay";
import { vars } from "./ReplayPassos";

export type EstadoConteudo =
  | { tipo: "ocioso" }
  | { tipo: "carregando" }
  | { tipo: "ok"; mensagens: IaMensagem[] }
  | { tipo: "sem_permissao" }
  | { tipo: "sem_conteudo" }
  | { tipo: "erro"; mensagem: string };

/** Busca o conteúdo sob demanda (nunca sozinho: a leitura é auditada). */
export function useConteudo(traceId: string): { estado: EstadoConteudo; carregar: () => void } {
  const [estado, setEstado] = useState<EstadoConteudo>({ tipo: "ocioso" });
  const atual = useRef(traceId);
  atual.current = traceId;
  const carregar = useCallback(() => {
    const pedido = traceId;
    setEstado({ tipo: "carregando" });
    getIaConteudo(pedido).then(
      (r) => {
        if (atual.current === pedido) setEstado({ tipo: "ok", mensagens: r.mensagens });
      },
      (e: unknown) => {
        if (atual.current !== pedido) return;
        const status = statusDoErro(e);
        if (status === 403) setEstado({ tipo: "sem_permissao" });
        else if (status === 404) setEstado({ tipo: "sem_conteudo" });
        else setEstado({ tipo: "erro", mensagem: mensagemDeErro(e, "Não foi possível carregar a conversa.") });
      },
    );
  }, [traceId]);
  return { estado, carregar };
}

const SEM_PERMISSAO =
  "Há conversa gravada para esta execução, mas o seu papel não pode vê-la. Ver conteúdo de IA é permitido a administradores e operadores, e cada leitura fica registrada na auditoria.";

const EXPLICA_CONVERSA =
  "Os prompts e as respostas que a aplicação mandou, por passo. Antes de gravar, o gateway troca dados pessoais (CPF, e-mail, cartão) por marcadores e corta mensagens muito longas. O conteúdo fica 7 dias, e cada vez que alguém abre a conversa a leitura entra na trilha de auditoria.";

/** Acima disto a mensagem chega recolhida (~8 linhas), com "Mostrar tudo". */
const LIMITE_CARACTERES = 600;
const LIMITE_LINHAS = 8;
/** Altura de ~8 linhas do texto da mensagem (13px × 1,55). */
const ALTURA_RECOLHIDA_PX = 160;

/** Texto que parece JSON (argumentos e resultados de ferramenta) vai em fonte mono. */
function pareceJson(texto: string): boolean {
  const t = texto.trim();
  return (t.startsWith("{") && t.endsWith("}")) || (t.startsWith("[") && t.endsWith("]"));
}

export function mensagemLonga(texto: string): boolean {
  return texto.length > LIMITE_CARACTERES || texto.split("\n").length > LIMITE_LINHAS;
}

/** Mensagem longa: recolhida em ~8 linhas, abre com a altura animada (exceto com menos movimento). */
function TextoLongo({ texto, classe }: { texto: string; classe: string }) {
  const [aberto, setAberto] = useState(false);
  const reduzir = useReducedMotion();
  const id = useId();
  return (
    <>
      <m.div
        id={id}
        className={`ia-msg__recorte${aberto ? "" : " ia-msg__recorte--fechado"}`}
        initial={false}
        animate={{ height: aberto ? "auto" : ALTURA_RECOLHIDA_PX }}
        transition={reduzir ? { duration: 0 } : { duration: DUR.calmo, ease: EASE.saida }}
      >
        <p className={classe}>{texto}</p>
      </m.div>
      <div>
        <button type="button" className="btn btn--ghost" aria-expanded={aberto} aria-controls={id} onClick={() => setAberto((a) => !a)}>
          {aberto ? "Mostrar menos" : "Mostrar tudo"}
        </button>
      </div>
    </>
  );
}

function Mensagem({ m: msg }: { m: IaMensagem }) {
  const mono = msg.papel === "tool" || pareceJson(msg.texto);
  const classe = `ia-msg__texto${mono ? " ia-mono" : ""}`;
  return (
    <div className={`ia-msg${msg.lado === "saida" ? " ia-msg--saida" : ""}`}>
      <div className="ia-msg__cabeca">
        <span className="ia-chip" title={msg.papel || undefined}>
          {rotuloPapel(msg.papel)}
        </span>
        <span className="ia-sub">{msg.lado === "entrada" ? "entrada" : "saída"}</span>
        {msg.redigido && <Badge state="info">redigido</Badge>}
        {msg.truncado && <Badge state="warn">truncado</Badge>}
      </div>
      {mensagemLonga(msg.texto) ? <TextoLongo texto={msg.texto} classe={classe} /> : <p className={classe}>{msg.texto}</p>}
    </div>
  );
}

function Mensagens({
  passos,
  mensagens,
  onIrParaPasso,
}: {
  passos: IaPasso[];
  mensagens: IaMensagem[];
  onIrParaPasso: (spanId: string) => void;
}) {
  const grupos = passos
    .map((p) => ({ passo: p, msgs: mensagensDoPasso(mensagens, p.span_id) }))
    .filter((g) => g.msgs.length > 0);
  if (grupos.length === 0) return <p className="ia-texto">A execução tem conteúdo gravado, mas nenhuma mensagem casou com os passos.</p>;
  return (
    <div className="ia-conversa">
      <p className="ia-sub">
        Trechos marcados como “redigido” tiveram dados pessoais trocados antes de gravar; “truncado” passou do limite de
        tamanho e foi cortado. Toque no nome do passo para vê-lo no passo a passo.
      </p>
      {grupos.map((g, i) => (
        <section
          key={g.passo.span_id}
          className="ia-conversa__passo"
          style={vars({ "--i": i })}
          aria-label={rotuloDoPasso(g.passo)}
        >
          <button
            type="button"
            className="ia-conversa__titulo"
            title="Mostrar este passo no passo a passo"
            onClick={() => onIrParaPasso(g.passo.span_id)}
          >
            {rotuloDoPasso(g.passo)}
          </button>
          {g.msgs.map((msg) => (
            <Mensagem key={`${msg.lado}-${msg.ordem}`} m={msg} />
          ))}
        </section>
      ))}
    </div>
  );
}

function CapturaDesligada() {
  return (
    <Aviso tom="neutro" titulo="A captura de conteúdo está desligada">
      <p>
        Prompts e respostas não são gravados por padrão, porque costumam ter dado pessoal. O passo a passo, os tokens e o
        custo acima funcionam sem isso.
      </p>
      <Collapsible variante="discreto" titulo="Como ligar a captura">
        <p>
          Ligue <code>REVOADA_GENAI_CONTEUDO</code> no gateway. O conteúdo passa a ser gravado redigido (dados pessoais
          trocados por marcadores) e guardado por 7 dias. Passo a passo em <code>docs/instrumentacao-ia.md</code>.
        </p>
      </Collapsible>
    </Aviso>
  );
}

function CarregandoConversa() {
  return (
    <div className="ia-conversa-carregando">
      <Bando rotulo="Carregando a conversa" tamanho={56} />
      {[0, 1, 2].map((i) => (
        <Skeleton key={i} height={56} />
      ))}
    </div>
  );
}

function PedirConversa({ carregar }: { carregar: () => void }) {
  return (
    <div className="ia-conversa-pedir">
      <p className="ia-texto">A leitura da conversa fica registrada na trilha de auditoria.</p>
      <div>
        <Button variant="primary" onClick={carregar}>
          Mostrar conversa
        </Button>
      </div>
    </div>
  );
}

interface ConversaProps {
  conteudo: IaExecucaoDetalhe["conteudo"];
  estado: EstadoConteudo;
  carregar: () => void;
  passos: IaPasso[];
  /** O nome do passo, na conversa, leva até ele no passo a passo. */
  onIrParaPasso: (spanId: string) => void;
}

function CorpoConversa({ conteudo, estado, carregar, passos, onIrParaPasso }: ConversaProps) {
  if (!conteudo.disponivel) return <CapturaDesligada />;
  if (!conteudo.pode_ver || estado.tipo === "sem_permissao") {
    return (
      <Aviso tom="neutro" titulo="Sem permissão para ver a conversa">
        <p>{SEM_PERMISSAO}</p>
      </Aviso>
    );
  }
  switch (estado.tipo) {
    case "ok":
      return <Mensagens passos={passos} mensagens={estado.mensagens} onIrParaPasso={onIrParaPasso} />;
    case "sem_conteudo":
      return <p className="ia-texto">Nenhum conteúdo gravado para esta execução. Ele pode ter passado do prazo de 7 dias ou ter sido apagado.</p>;
    case "erro":
      return (
        <Aviso tom="crit" titulo="A conversa não carregou">
          <p>{estado.mensagem}</p>
          <p>
            <Button onClick={carregar}>Tentar de novo</Button>
          </p>
        </Aviso>
      );
    case "carregando":
      return <CarregandoConversa />;
    default:
      return <PedirConversa carregar={carregar} />;
  }
}

export function Conversa(props: ConversaProps) {
  return (
    <Card
      title={
        <span className="ia-titulo-dica">
          Conversa <InfoTip title="a conversa" text={EXPLICA_CONVERSA} />
        </span>
      }
    >
      <CorpoConversa {...props} />
    </Card>
  );
}

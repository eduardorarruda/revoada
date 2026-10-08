// Conversa de uma execução: os prompts e respostas gravados, por passo.
//
// Conteúdo de prompt é dado pessoal (ADR 008): só aparece se a captura estiver
// ligada, se o papel da pessoa puder ver, e só depois de um clique — cada leitura
// fica na trilha de auditoria. Cada estado tem a sua explicação, nunca um vazio.
import { useCallback, useRef, useState } from "react";
import { getIaConteudo, type IaExecucaoDetalhe, type IaMensagem, type IaPasso } from "../../api.ia";
import { Badge, Button, Card } from "../../components";
import { mensagemDeErro } from "../../format";
import { Aviso, statusDoErro } from "./comum";
import { mensagensDoPasso, rotuloDoPasso } from "./replay";

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

/** Texto que parece JSON (argumentos e resultados de ferramenta) vai em fonte mono. */
function pareceJson(texto: string): boolean {
  const t = texto.trim();
  return (t.startsWith("{") && t.endsWith("}")) || (t.startsWith("[") && t.endsWith("]"));
}

function Mensagem({ m }: { m: IaMensagem }) {
  const mono = m.papel === "tool" || pareceJson(m.texto);
  return (
    <div className="ia-msg">
      <div className="ia-msg__cabeca">
        <span className="ia-chip">{m.papel || "papel não informado"}</span>
        <span className="ia-sub">{m.lado === "entrada" ? "entrada" : "saída"}</span>
        {m.redigido && <Badge state="info">redigido</Badge>}
        {m.truncado && <Badge state="warn">truncado</Badge>}
      </div>
      <p className={`ia-msg__texto${mono ? " ia-mono" : ""}`}>{m.texto}</p>
    </div>
  );
}

function Mensagens({ passos, mensagens }: { passos: IaPasso[]; mensagens: IaMensagem[] }) {
  const grupos = passos
    .map((p) => ({ passo: p, msgs: mensagensDoPasso(mensagens, p.span_id) }))
    .filter((g) => g.msgs.length > 0);
  if (grupos.length === 0) return <p className="ia-texto">A execução tem conteúdo gravado, mas nenhuma mensagem casou com os passos.</p>;
  return (
    <div className="ia-conversa">
      <p className="ia-sub">
        Trechos marcados como “redigido” tiveram dados pessoais trocados antes de gravar; “truncado” passou do limite de
        tamanho e foi cortado.
      </p>
      {grupos.map((g) => (
        <section key={g.passo.span_id} className="ia-conversa__passo" aria-label={rotuloDoPasso(g.passo)}>
          <strong>{rotuloDoPasso(g.passo)}</strong>
          {g.msgs.map((m) => (
            <Mensagem key={`${m.lado}-${m.ordem}`} m={m} />
          ))}
        </section>
      ))}
    </div>
  );
}

function CorpoConversa({
  conteudo,
  estado,
  carregar,
  passos,
}: {
  conteudo: IaExecucaoDetalhe["conteudo"];
  estado: EstadoConteudo;
  carregar: () => void;
  passos: IaPasso[];
}) {
  if (!conteudo.disponivel) {
    return (
      <Aviso tom="neutro" titulo="A captura de conteúdo está desligada">
        <p>
          Prompts e respostas não são gravados por padrão, porque costumam ter dado pessoal. Para gravar (redigido e guardado
          por 7 dias), ligue <code>REVOADA_GENAI_CONTEUDO</code> no gateway. O passo a passo, os tokens e o custo acima
          funcionam sem isso. Guia: <code>docs/instrumentacao-ia.md</code>.
        </p>
      </Aviso>
    );
  }
  if (!conteudo.pode_ver || estado.tipo === "sem_permissao") {
    return (
      <Aviso tom="neutro" titulo="Sem permissão para ver a conversa">
        <p>{SEM_PERMISSAO}</p>
      </Aviso>
    );
  }
  switch (estado.tipo) {
    case "ok":
      return <Mensagens passos={passos} mensagens={estado.mensagens} />;
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
    default:
      return (
        <div className="stack" style={{ gap: "var(--sp-2)" }}>
          <p className="ia-texto">A leitura da conversa fica registrada na trilha de auditoria.</p>
          <div>
            <Button variant="primary" onClick={carregar} disabled={estado.tipo === "carregando"}>
              {estado.tipo === "carregando" ? "Carregando a conversa…" : "Mostrar conversa"}
            </Button>
          </div>
        </div>
      );
  }
}

export function Conversa(props: {
  conteudo: IaExecucaoDetalhe["conteudo"];
  estado: EstadoConteudo;
  carregar: () => void;
  passos: IaPasso[];
}) {
  return (
    <Card title="Conversa">
      <CorpoConversa {...props} />
    </Card>
  );
}

// Replay de uma execução (#/ia/execucoes/<trace_id>): o que o agente fez, passo a
// passo, com tokens, custo (e de onde ele veio), latência, erros e repetições.
// É visual: nada é reexecutado (ADR 008).
import { useCallback, useMemo, useState } from "react";
import { Server, Waypoints } from "lucide-react";
import { getIaExecucao, type IaExecucaoDetalhe } from "../../api.ia";
import { Button, Card, Skeleton } from "../../components";
import { Kpi, KpiGrid } from "../../components/Kpi";
import { formatDateTime } from "../../format";
import { Aviso, Custo, FalhaCarga, useCarga } from "./comum";
import { Conversa, useConteudo } from "./Conversa";
import { fmtInteiro, fmtMs, fmtTokens } from "./formato";
import { fraseRepeticao, passosRepetidos } from "./replay";
import { DetalhePasso, LinhaDoTempo } from "./ReplayPassos";
import { hrefHostNaHora, hrefIa, hrefWaterfall } from "./rotas";

function Cabecalho({ d, janelaId }: { d: IaExecucaoDetalhe; janelaId: string }) {
  const t = d.totais;
  return (
    <Card>
      <p className="ia-sub">
        <a href={hrefIa("/ia/execucoes", { janela: janelaId })}>← Execuções</a>
      </p>
      <h2 style={{ margin: "var(--sp-1) 0", fontSize: "var(--fs-20)" }}>Execução de {d.agente || d.service || "agente sem nome"}</h2>
      <p className="ia-texto">
        Serviço {d.service || "não informado"} · servidor {d.host || "não informado"}
        {d.conversa_id ? ` · conversa ${d.conversa_id}` : ""} · começou {formatDateTime(d.inicio_ms)} · durou {fmtMs(d.duracao_ms)}
      </p>
      <div className="row" style={{ marginBlock: "var(--sp-3)" }}>
        <a className="btn" href={hrefWaterfall(d.trace_id)}>
          <Waypoints size={14} aria-hidden={true} /> Ver no waterfall
        </a>
        {d.host ? (
          <a className="btn" href={hrefHostNaHora(d.host, d.inicio_ms)} title="Abre o servidor numa janela que inclui o início desta execução">
            <Server size={14} aria-hidden={true} /> Ver host nesta hora
          </a>
        ) : (
          <span className="ia-sub">Sem servidor associado: a execução chegou sem o rótulo de host.</span>
        )}
      </div>
      <KpiGrid>
        <Kpi rotulo="Chamadas de modelo" valor={fmtInteiro(t.chamadas_modelo)} />
        <Kpi rotulo="Chamadas de ferramenta" valor={fmtInteiro(t.chamadas_ferramenta)} />
        <Kpi rotulo="Erros" valor={fmtInteiro(t.erros)} tone={t.erros > 0 ? "crit" : undefined} />
        <Kpi rotulo="Tokens de entrada" valor={fmtTokens(t.tokens_entrada)} />
        <Kpi rotulo="Tokens de saída" valor={fmtTokens(t.tokens_saida)} />
        <Kpi rotulo="Custo" valor={<Custo valor={t.custo_usd} parcial={t.custo_parcial} />} />
      </KpiGrid>
    </Card>
  );
}

function Corpo({ d, janelaId }: { d: IaExecucaoDetalhe; janelaId: string }) {
  const [sel, setSel] = useState(0);
  const [pedidoFoco, setPedidoFoco] = useState(0);
  const { estado, carregar } = useConteudo(d.trace_id);
  const repetidos = useMemo(() => passosRepetidos(d.passos, d.repeticoes), [d.passos, d.repeticoes]);
  const mensagens = estado.tipo === "ok" ? estado.mensagens : null;
  const atual = d.passos[Math.min(sel, d.passos.length - 1)];
  const irPara = (spanId: string) => {
    const i = d.passos.findIndex((p) => p.span_id === spanId);
    if (i < 0) return;
    setSel(i);
    setPedidoFoco((n) => n + 1); // leva o foco do teclado até o passo, como as setas
  };

  return (
    <>
      <Cabecalho d={d} janelaId={janelaId} />
      {d.repeticoes.map((r) => (
        <Aviso key={`${r.ferramenta}-${r.primeiro_span_id}`} tom="warn" titulo={fraseRepeticao(r)}>
          <p>
            A mesma ferramenta chamada várias vezes seguidas costuma ser o agente sem conseguir sair do lugar (resposta que
            ele não entende, erro que ele tenta de novo). Cada chamada custa uma volta no modelo.
          </p>
          <p>
            <Button onClick={() => irPara(r.primeiro_span_id)}>Ir para a primeira chamada</Button>
          </p>
        </Aviso>
      ))}
      {atual ? (
        <div className="ia-replay">
          <LinhaDoTempo
            passos={d.passos}
            inicioMs={d.inicio_ms}
            duracaoMs={d.duracao_ms}
            repetidos={repetidos}
            sel={sel}
            onSel={setSel}
            pedidoFoco={pedidoFoco}
          />
          <div className="ia-replay__detalhe">
            <DetalhePasso passo={atual} inicioMs={d.inicio_ms} mensagens={mensagens} />
          </div>
        </div>
      ) : (
        <Card>
          <p className="ia-texto">Esta execução não tem passos de IA gravados.</p>
        </Card>
      )}
      <Conversa conteudo={d.conteudo} estado={estado} carregar={carregar} passos={d.passos} />
    </>
  );
}

export function Replay({ traceId, janelaId, versao }: { traceId: string; janelaId: string; versao: number }) {
  const buscar = useCallback(
    () => getIaExecucao(traceId),
    // `versao` entra de propósito: o botão Atualizar busca de novo (spans atrasados).
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [traceId, versao],
  );
  const { dados, erro, recarregar } = useCarga(buscar, "Não foi possível carregar a execução.");
  if (!dados) {
    if (erro) return <FalhaCarga erro={erro} onTentar={recarregar} />;
    return <Skeleton height={240} />;
  }
  // key: trocar de execução zera o passo escolhido e a conversa carregada.
  return <Corpo key={dados.trace_id} d={dados} janelaId={janelaId} />;
}

// Replay de uma execução (#/ia/execucoes/<trace_id>): o que o agente fez, passo a
// passo, com tokens, custo (e de onde ele veio), latência, erros e repetições.
// É visual: nada é reexecutado (ADR 008).
import { useCallback, useMemo, useState } from "react";
import { ArrowLeft, Footprints, Repeat, Server, Trash2, Waypoints } from "lucide-react";
import { isAdmin } from "../../api";
import { getIaExecucao, purgeIa, type IaExecucaoDetalhe, type IaRepeticao } from "../../api.ia";
import { Button, Card, ConfirmDialog, EmptyState, InfoTip, Skeleton, useToast } from "../../components";
import { Kpi, KpiGrid } from "../../components/Kpi";
import { formatDateTime, mensagemDeErro } from "../../format";
import { FalhaCarga, useCarga, useSinalizarCarga } from "./comum";
import { Conversa, useConteudo } from "./Conversa";
import { fmtInteiro, fmtMs, fmtTokens, fmtUsd, NAO_INFORMADO } from "./formato";
import { fraseRepeticao, passosRepetidos } from "./replay";
import { DetalhePasso, LinhaDoTempo, useTelaLarga, type PedidoDestaque } from "./ReplayPassos";
import { hrefHostNaHora, hrefIa, hrefWaterfall } from "./rotas";
import "./replay.css";

const EXPLICA_PARCIAL =
  "Algum passo desta execução ficou fora da soma: ou o modelo não tem preço cadastrado (cadastre em Modelos e preços) ou a biblioteca não informou os tokens. O custo real é maior que o mostrado.";

const CONSEQUENCIA_APAGAR =
  "Remove as chamadas de IA e o conteúdo gravado (prompts e respostas) desta execução. A remoção leva alguns segundos para valer em todas as telas e não pode ser desfeita. Os spans continuam na cascata do trace.";

function hrefExecucoes(janelaId: string): string {
  return hrefIa("/ia/execucoes", { janela: janelaId });
}

/** Botão de admin que apaga a execução e volta para a lista. */
function ApagarExecucao({ traceId, janelaId }: { traceId: string; janelaId: string }) {
  const toast = useToast();
  const [aberto, setAberto] = useState(false);
  const [apagando, setApagando] = useState(false);
  const confirmar = async () => {
    setAberto(false);
    setApagando(true);
    try {
      await purgeIa({ alvo: "trace", valor: traceId });
      toast.success("Execução apagada. Ela some das listas em alguns segundos.");
      window.location.hash = hrefExecucoes(janelaId).slice(1);
    } catch (e) {
      setApagando(false);
      toast.error(mensagemDeErro(e, "Não foi possível apagar a execução."));
    }
  };
  return (
    <>
      <Button onClick={() => setAberto(true)} disabled={apagando} aria-busy={apagando || undefined}>
        <Trash2 size={16} aria-hidden={true} /> {apagando ? "Apagando…" : "Apagar esta execução"}
      </Button>
      <ConfirmDialog
        open={aberto}
        onCancel={() => setAberto(false)}
        onConfirm={confirmar}
        verb="Apagar"
        target="esta execução"
        consequences={CONSEQUENCIA_APAGAR}
        danger
      />
    </>
  );
}

function Cabecalho({ d, janelaId }: { d: IaExecucaoDetalhe; janelaId: string }) {
  return (
    <Card>
      <h2 className="ia-cabecalho__titulo">Execução de {d.agente || d.service || "agente sem nome"}</h2>
      <p className="ia-texto">
        Serviço {d.service || "não informado"} · servidor {d.host || "não informado"}
        {d.conversa_id ? ` · conversa ${d.conversa_id}` : ""} · começou {formatDateTime(d.inicio_ms)} · durou {fmtMs(d.duracao_ms)}
      </p>
      <div className="row ia-cabecalho__acoes">
        <a className="btn" href={hrefWaterfall(d.trace_id)}>
          <Waypoints size={16} aria-hidden={true} /> Ver na cascata do trace
        </a>
        {d.host ? (
          <a className="btn" href={hrefHostNaHora(d.host, d.inicio_ms)} title="Abre o servidor numa janela que inclui o início desta execução">
            <Server size={16} aria-hidden={true} /> Ver host nesta hora
          </a>
        ) : (
          <span className="ia-sub">Sem servidor associado: a execução chegou sem o rótulo de host.</span>
        )}
        {isAdmin() && <ApagarExecucao traceId={d.trace_id} janelaId={janelaId} />}
      </div>
    </Card>
  );
}

function SubCusto({ valor, parcial }: { valor: number | null; parcial: boolean }) {
  if (!parcial) return <>{valor == null ? NAO_INFORMADO : "valor da biblioteca + estimativa pela tabela"}</>;
  return (
    <span className="ia-parcial">
      {valor == null ? `${NAO_INFORMADO} · parcial` : "parcial"} <InfoTip title="custo parcial" text={EXPLICA_PARCIAL} />
    </span>
  );
}

/** Token nulo é "não informado" na linha de baixo; o número grande fica vazio, nunca 0. */
function KpiTokens({ rotulo, valor }: { rotulo: string; valor: number | null }) {
  return <Kpi rotulo={rotulo} valor={valor == null ? null : fmtTokens(valor)} sub={valor == null ? NAO_INFORMADO : undefined} />;
}

function KpisExecucao({ t }: { t: IaExecucaoDetalhe["totais"] }) {
  const subErro = t.erros === 0 ? "nenhum passo falhou" : t.erros === 1 ? "1 passo falhou" : "passos falharam";
  return (
    <KpiGrid>
      <Kpi rotulo="Chamadas de modelo" valor={fmtInteiro(t.chamadas_modelo)} />
      <Kpi rotulo="Chamadas de ferramenta" valor={fmtInteiro(t.chamadas_ferramenta)} />
      <Kpi rotulo="Com erro" valor={fmtInteiro(t.erros)} sub={subErro} tone={t.erros > 0 ? "crit" : undefined} />
      <KpiTokens rotulo="Tokens de entrada" valor={t.tokens_entrada} />
      <KpiTokens rotulo="Tokens de saída" valor={t.tokens_saida} />
      <Kpi
        rotulo="Custo"
        valor={t.custo_usd == null ? null : fmtUsd(t.custo_usd)}
        sub={<SubCusto valor={t.custo_usd} parcial={t.custo_parcial} />}
      />
    </KpiGrid>
  );
}

/** Aviso de possível loop: o orbe solta três ondas ao aparecer (nunca para sempre). */
function AvisoLoop({ r, onIr }: { r: IaRepeticao; onIr: () => void }) {
  const frase = fraseRepeticao(r);
  return (
    <div className="ia-aviso ia-aviso--warn ia-loop" role="note" aria-label={frase}>
      <span className="ia-loop__orbe" aria-hidden="true">
        <span className="ia-loop__anel" />
        <span className="ia-loop__anel ia-loop__anel--2" />
        <Repeat size={16} />
      </span>
      <div className="ia-aviso__corpo">
        <strong>{frase}</strong>
        <p>
          A mesma ferramenta chamada várias vezes seguidas costuma ser o agente sem conseguir sair do lugar (resposta que ele
          não entende, erro que ele tenta de novo). Cada chamada custa uma volta no modelo.
        </p>
        <p>
          <Button onClick={onIr}>Ir para a primeira chamada</Button>
        </p>
      </div>
    </div>
  );
}

function SemPassos() {
  return (
    <Card>
      <EmptyState
        icon={<Footprints size={32} strokeWidth={1.5} />}
        title="Nenhum passo de IA gravado"
        body="Esta execução chegou sem spans de IA (chamada de modelo, de ferramenta ou de agente). Se ela acabou de rodar, os spans podem estar atrasados: use Atualizar daqui a pouco."
      />
    </Card>
  );
}

type Direcao = "frente" | "tras";

/** Passo escolhido, a direção da troca (anima o detalhe) e os pedidos vindos de fora da lista. */
function useEscolhaDePasso(d: IaExecucaoDetalhe) {
  const [escolha, setEscolha] = useState<{ sel: number; dir: Direcao }>({ sel: 0, dir: "frente" });
  const [pedidoFoco, setPedidoFoco] = useState(0);
  const [destaque, setDestaque] = useState<PedidoDestaque | null>(null);
  const selecionar = useCallback((i: number) => {
    setEscolha((e) => ({ sel: i, dir: i >= e.sel ? "frente" : "tras" }));
  }, []);
  const irPara = (spanId: string, primeiroDaRepeticao?: string) => {
    const i = d.passos.findIndex((p) => p.span_id === spanId);
    if (i < 0) return;
    selecionar(i);
    setPedidoFoco((n) => n + 1); // leva o foco do teclado até o passo, como as setas
    if (primeiroDaRepeticao) setDestaque((ant) => ({ primeiroSpanId: primeiroDaRepeticao, vez: (ant?.vez ?? 0) + 1 }));
  };
  return { ...escolha, pedidoFoco, destaque, selecionar, irPara };
}

function Corpo({ d, janelaId }: { d: IaExecucaoDetalhe; janelaId: string }) {
  const escolha = useEscolhaDePasso(d);
  const larga = useTelaLarga();
  const { estado, carregar } = useConteudo(d.trace_id);
  const repetidos = useMemo(() => passosRepetidos(d.passos, d.repeticoes), [d.passos, d.repeticoes]);
  const mensagens = estado.tipo === "ok" ? estado.mensagens : null;
  const sel = Math.min(escolha.sel, d.passos.length - 1);
  const atual = d.passos[sel];
  const detalhe = (embutido: boolean) =>
    atual && (
      <DetalhePasso passo={atual} indice={sel} total={d.passos.length} inicioMs={d.inicio_ms} mensagens={mensagens} embutido={embutido} />
    );

  return (
    <>
      <a className="ia-voltar" href={hrefExecucoes(janelaId)}>
        <ArrowLeft size={14} aria-hidden={true} /> Voltar às execuções
      </a>
      <Cabecalho d={d} janelaId={janelaId} />
      <KpisExecucao t={d.totais} />
      {d.repeticoes.map((r) => (
        <AvisoLoop key={`${r.ferramenta}-${r.primeiro_span_id}`} r={r} onIr={() => escolha.irPara(r.primeiro_span_id, r.primeiro_span_id)} />
      ))}
      {atual ? (
        <div className="ia-replay">
          <LinhaDoTempo
            passos={d.passos}
            inicioMs={d.inicio_ms}
            duracaoMs={d.duracao_ms}
            repetidos={repetidos}
            sel={sel}
            onSel={escolha.selecionar}
            pedidoFoco={escolha.pedidoFoco}
            destaque={escolha.destaque}
            detalheNoPasso={larga ? undefined : () => detalhe(true)}
          />
          {larga && (
            <div className="ia-replay__detalhe">
              <div key={atual.span_id} className="ia-detalhe-troca" data-dir={escolha.dir}>
                {detalhe(false)}
              </div>
            </div>
          )}
        </div>
      ) : (
        <SemPassos />
      )}
      <Conversa conteudo={d.conteudo} estado={estado} carregar={carregar} passos={d.passos} onIrParaPasso={(id) => escolha.irPara(id)} />
    </>
  );
}

const ROTULOS_KPI = ["Chamadas de modelo", "Chamadas de ferramenta", "Com erro", "Tokens de entrada", "Tokens de saída", "Custo"];

/** Esqueleto com a forma da tela: cabeçalho, os seis indicadores e a lista de passos. */
function EsqueletoReplay() {
  return (
    <div className="ia-esqueleto" role="status" aria-busy="true" aria-label="Carregando a execução">
      <Skeleton height={132} />
      <KpiGrid>
        {ROTULOS_KPI.map((r) => (
          <Kpi key={r} rotulo={r} valor={null} loading />
        ))}
      </KpiGrid>
      {Array.from({ length: 6 }, (_, i) => (
        <Skeleton key={i} height={44} />
      ))}
    </div>
  );
}

export function Replay({ traceId, janelaId, versao }: { traceId: string; janelaId: string; versao: number }) {
  const buscar = useCallback(
    () => getIaExecucao(traceId),
    // `versao` entra de propósito: o botão Atualizar busca de novo (spans atrasados).
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [traceId, versao],
  );
  const carga = useCarga(buscar, "Não foi possível carregar a execução.");
  useSinalizarCarga(carga);
  const { dados, erro, recarregar } = carga;
  if (!dados) {
    if (erro) return <FalhaCarga erro={erro} onTentar={recarregar} />;
    return <EsqueletoReplay />;
  }
  // key: trocar de execução zera o passo escolhido e a conversa carregada.
  return <Corpo key={dados.trace_id} d={dados} janelaId={janelaId} />;
}

// Execuções (#/ia/execucoes): cada trace com ao menos um span de IA, filtrável por
// agente, modelo, status e custo. Cada linha abre o replay passo a passo.
import { useCallback, useEffect, useState } from "react";
import { listIaExecucoes, type IaExecucao, type IaFiltroExecucoes } from "../../api.ia";
import { Badge, Card, DataTable, type Column } from "../../components";
import { intervaloDaJanela, type OpcaoJanela } from "../../components/JanelaTempo";
import { formatDateTime } from "../../format";
import { Custo, FalhaCarga, useCarga } from "./comum";
import { fmtInteiro, fmtMs, fmtTokens } from "./formato";
import { hrefReplay } from "./rotas";

/** Linhas pedidas ao servidor (o teto dele é 500). */
const LIMITE = 200;
/** Espera depois da última tecla antes de consultar. */
const ESPERA_DIGITACAO_MS = 400;

interface Filtros {
  agente: string;
  modelo: string;
  status: "" | "erro";
  custoMin: string;
}

function useAtrasado<T>(valor: T, ms: number): T {
  const [atrasado, setAtrasado] = useState(valor);
  useEffect(() => {
    const t = setTimeout(() => setAtrasado(valor), ms);
    return () => clearTimeout(t);
  }, [valor, ms]);
  return atrasado;
}

/** Filtros digitados → filtro da API. Custo mínimo inválido é ignorado (e avisado). */
export function filtroDaApi(f: Filtros, janela: OpcaoJanela, agoraMs?: number): IaFiltroExecucoes {
  const custo = Number(f.custoMin.replace(",", "."));
  return {
    ...intervaloDaJanela(janela, agoraMs),
    agente: f.agente.trim() || undefined,
    modelo: f.modelo.trim() || undefined,
    status: f.status || undefined,
    custo_min: f.custoMin.trim() !== "" && Number.isFinite(custo) && custo >= 0 ? custo : undefined,
    limit: LIMITE,
  };
}

function colunas(janelaId: string): Column<IaExecucao>[] {
  return [
    { key: "inicio_ms", label: "Início", sortable: true, render: (e) => formatDateTime(e.inicio_ms) },
    {
      key: "agente",
      label: "Agente",
      sortable: true,
      render: (e) => (
        <span>
          <a href={hrefReplay(e.trace_id, janelaId)}>{e.agente || e.service || "sem nome"}</a>
          <div className="ia-sub">
            {e.service || "serviço não informado"}
            {e.host ? ` · ${e.host}` : ""}
          </div>
        </span>
      ),
    },
    { key: "modelos", label: "Modelos", hideOnMobile: true, render: (e) => <span className="ia-mono">{e.modelos.join(", ")}</span> },
    {
      key: "passos",
      label: "Passos",
      align: "right",
      sortable: true,
      sortValue: (e) => e.chamadas_modelo + e.chamadas_ferramenta,
      render: (e) => `${fmtInteiro(e.chamadas_modelo)} modelo · ${fmtInteiro(e.chamadas_ferramenta)} ferramenta`,
    },
    {
      key: "tokens",
      label: "Tokens (entrada / saída)",
      align: "right",
      hideOnMobile: true,
      render: (e) => `${fmtTokens(e.tokens_entrada)} / ${fmtTokens(e.tokens_saida)}`,
    },
    {
      key: "custo_usd",
      label: "Custo",
      align: "right",
      sortable: true,
      sortValue: (e) => e.custo_usd ?? -1,
      render: (e) => <Custo valor={e.custo_usd} parcial={e.custo_parcial} />,
    },
    { key: "duracao_ms", label: "Duração", align: "right", sortable: true, render: (e) => fmtMs(e.duracao_ms) },
    {
      key: "status",
      label: "Status",
      sortable: true,
      render: (e) =>
        e.status === "erro" ? <Badge state="crit">erro ({fmtInteiro(e.erros)})</Badge> : <Badge state="ok">ok</Badge>,
    },
  ];
}

function CamposFiltro({
  filtros,
  mudar,
  custoInvalido,
}: {
  filtros: Filtros;
  mudar: (parcial: Partial<Filtros>) => void;
  custoInvalido: boolean;
}) {
  return (
    <div className="ia-filtros" style={{ marginBlock: "var(--sp-3)" }}>
      <label>
        Agente
        <input className="field" value={filtros.agente} placeholder="todos" onChange={(e) => mudar({ agente: e.target.value })} />
      </label>
      <label>
        Modelo
        <input className="field" value={filtros.modelo} placeholder="todos" onChange={(e) => mudar({ modelo: e.target.value })} />
      </label>
      <label>
        Status
        <select className="field" value={filtros.status} onChange={(e) => mudar({ status: e.target.value === "erro" ? "erro" : "" })}>
          <option value="">qualquer status</option>
          <option value="erro">só com erro</option>
        </select>
      </label>
      <label>
        Custo mínimo (US$)
        <input
          className="field tabular"
          inputMode="decimal"
          value={filtros.custoMin}
          placeholder="ex.: 0,05"
          aria-invalid={custoInvalido}
          onChange={(e) => mudar({ custoMin: e.target.value })}
        />
      </label>
    </div>
  );
}

export function Execucoes({ janela, versao, params }: { janela: OpcaoJanela; versao: number; params: URLSearchParams }) {
  const [filtros, setFiltros] = useState<Filtros>(() => ({
    agente: params.get("agente") ?? "",
    modelo: params.get("modelo") ?? "",
    status: params.get("status") === "erro" ? "erro" : "",
    custoMin: params.get("custo_min") ?? "",
  }));
  const aplicados = useAtrasado(filtros, ESPERA_DIGITACAO_MS);
  const buscar = useCallback(
    () => listIaExecucoes(filtroDaApi(aplicados, janela)).then((r) => r.execucoes),
    // `versao` entra de propósito: o botão Atualizar refaz a busca até agora.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [aplicados, janela, versao],
  );
  const { dados, erro, carregando, recarregar } = useCarga(buscar, "Não foi possível listar as execuções.");
  const mudar = (parcial: Partial<Filtros>) => setFiltros((f) => ({ ...f, ...parcial }));
  const custoInvalido = filtros.custoMin.trim() !== "" && filtroDaApi(filtros, janela).custo_min === undefined;

  return (
    <Card>
      <p className="ia-texto">
        Cada execução é um trace com ao menos uma chamada de IA. O agente é o do span invoke_agent mais alto; sem ele, o
        serviço. Clique numa execução para ver o passo a passo.
      </p>
      <CamposFiltro filtros={filtros} mudar={mudar} custoInvalido={custoInvalido} />
      {custoInvalido && <p className="ia-sub">Custo mínimo precisa ser um número; o filtro está sendo ignorado.</p>}
      {erro && dados && <FalhaCarga erro={erro} onTentar={recarregar} />}
      <DataTable
        columns={colunas(janela.id)}
        rows={dados ?? []}
        keyFn={(e) => e.trace_id}
        loading={carregando && !dados}
        error={!dados && erro ? erro : undefined}
        initialSort={{ key: "inicio_ms", dir: "desc" }}
        rowActions={(e) => (
          <a className="btn btn--ghost" href={hrefReplay(e.trace_id, janela.id)}>
            Ver replay
          </a>
        )}
        empty={<p className="ia-texto">Nenhuma execução com esses filtros nesta janela. Amplie a janela ou limpe os filtros.</p>}
      />
      {dados && dados.length >= LIMITE && (
        <p className="ia-sub">A lista parou em {LIMITE} execuções, o máximo pedido de uma vez. Refine os filtros ou encurte a janela para ver as demais.</p>
      )}
    </Card>
  );
}

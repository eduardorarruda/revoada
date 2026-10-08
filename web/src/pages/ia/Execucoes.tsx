// Execuções (#/ia/execucoes): cada trace com ao menos um span de IA, filtrável por
// agente, modelo, status e custo. Cada linha abre o replay passo a passo.
import { useCallback, useEffect, useState, type ReactNode } from "react";
import { ListTree } from "lucide-react";
import { listIaExecucoes, type IaExecucao, type IaFiltroExecucoes } from "../../api.ia";
import { Badge, Button, Card, DataTable, EmptyState, FormField, InfoTip, type Column } from "../../components";
import { intervaloDaJanela, janelaParaAmpliar, type OpcaoJanela } from "../../components/JanelaTempo";
import { fmtRelAbs } from "../../format";
import { Custo, FalhaCarga, useCarga, useSinalizarCarga } from "./comum";
import { fmtInteiro, fmtMs, fmtTokens, passosDaExecucao, SEM_PRECO } from "./formato";
import { hrefReplay } from "./rotas";
import { plural } from "./veredito";

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

const SEM_FILTROS: Filtros = { agente: "", modelo: "", status: "", custoMin: "" };

const EXPLICA_EXECUCOES =
  "Cada execução é um trace com ao menos uma chamada de IA. O agente é o que a aplicação declarou ao iniciar a execução; sem essa informação, usamos o nome do serviço. Clique numa execução para ver o passo a passo.";

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

/** Há algum filtro digitado (mesmo que inválido)? */
export function temFiltro(f: Filtros): boolean {
  return f.agente.trim() !== "" || f.modelo.trim() !== "" || f.status !== "" || f.custoMin.trim() !== "";
}

function Inicio({ ms }: { ms: number }) {
  const { rel, abs } = fmtRelAbs(ms);
  return <span title={abs}>{rel}</span>;
}

function colunas(janelaId: string): Column<IaExecucao>[] {
  return [
    { key: "inicio_ms", label: "Início", sortable: true, render: (e) => <Inicio ms={e.inicio_ms} /> },
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
      render: (e) => passosDaExecucao(e.chamadas_modelo, e.chamadas_ferramenta),
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
      render: (e) => <Custo valor={e.custo_usd} parcial={e.custo_parcial} vazio={e.sem_preco ? SEM_PRECO : undefined} />,
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
  limpar,
  custoInvalido,
}: {
  filtros: Filtros;
  mudar: (parcial: Partial<Filtros>) => void;
  limpar: () => void;
  custoInvalido: boolean;
}) {
  return (
    <div className="ia-filtros-bloco">
      <div className="filtros">
        <FormField label="Agente">
          <input className="field" value={filtros.agente} placeholder="todos" onChange={(e) => mudar({ agente: e.target.value })} />
        </FormField>
        <FormField label="Modelo">
          <input className="field" value={filtros.modelo} placeholder="todos" onChange={(e) => mudar({ modelo: e.target.value })} />
        </FormField>
        <FormField label="Status">
          <select className="field" value={filtros.status} onChange={(e) => mudar({ status: e.target.value === "erro" ? "erro" : "" })}>
            <option value="">qualquer status</option>
            <option value="erro">só com erro</option>
          </select>
        </FormField>
        <FormField
          label="Custo mínimo (US$)"
          error={custoInvalido ? "Use um número, ex.: 0,05. O filtro está sendo ignorado." : undefined}
        >
          <input
            className="field tabular"
            inputMode="decimal"
            value={filtros.custoMin}
            placeholder="ex.: 0,05"
            onChange={(e) => mudar({ custoMin: e.target.value })}
          />
        </FormField>
      </div>
      {temFiltro(filtros) && (
        <div className="row">
          <Button variant="ghost" onClick={limpar}>
            Limpar filtros
          </Button>
        </div>
      )}
    </div>
  );
}

/** Estado vazio: com filtro, oferece limpar; sem filtro, ampliar a janela. */
function SemExecucoes({
  filtrado,
  janela,
  limpar,
  mudarJanela,
}: {
  filtrado: boolean;
  janela: OpcaoJanela;
  limpar: () => void;
  mudarJanela?: (id: string) => void;
}) {
  const icone = <ListTree size={32} strokeWidth={1.5} />;
  if (filtrado) {
    return (
      <EmptyState
        icon={icone}
        title="Nenhuma execução com esses filtros"
        body={`${janela.frase} não teve execução que case com todos os filtros. Limpe os filtros ou amplie a janela.`}
        action={{ label: "Limpar filtros", onClick: limpar }}
      />
    );
  }
  const maior = ["24h", "7d", "30d"].map((id) => janelaParaAmpliar(janela, id)).find(Boolean) ?? null;
  return (
    <EmptyState
      icon={icone}
      title="Nenhuma execução nesta janela"
      body={`${janela.frase} sem nenhum trace com chamada de IA. Uma execução aparece aqui assim que a aplicação manda os spans dela.`}
      action={maior && mudarJanela ? { label: `Ampliar para ${maior.rotulo}`, onClick: () => mudarJanela(maior.id) } : undefined}
    />
  );
}

function TituloExecucoes({ n }: { n: number | null }) {
  return (
    <span className="ia-cartao-titulo">
      {n == null ? "Execuções" : n >= LIMITE ? `${fmtInteiro(LIMITE)}+ execuções` : plural(n, "execução", "execuções")}
      <InfoTip title="execuções" text={EXPLICA_EXECUCOES} />
    </span>
  );
}

export function Execucoes({
  janela,
  versao,
  params,
  mudarJanela,
}: {
  janela: OpcaoJanela;
  versao: number;
  params: URLSearchParams;
  mudarJanela?: (id: string) => void;
}) {
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
  const carga = useCarga(buscar, "Não foi possível listar as execuções.");
  useSinalizarCarga(carga);
  const { dados, erro, carregando, recarregar } = carga;
  const mudar = (parcial: Partial<Filtros>) => setFiltros((f) => ({ ...f, ...parcial }));
  const limpar = () => setFiltros(SEM_FILTROS);
  const custoInvalido = filtros.custoMin.trim() !== "" && filtroDaApi(filtros, janela).custo_min === undefined;
  const n = dados ? dados.length : null;

  return (
    <Card title={<TituloExecucoes n={n} />}>
      <CamposFiltro filtros={filtros} mudar={mudar} limpar={limpar} custoInvalido={custoInvalido} />
      <p className="so-leitor" aria-live="polite">
        {n == null ? "" : `${plural(n, "execução encontrada", "execuções encontradas")}.`}
      </p>
      {erro && dados && <FalhaCarga erro={erro} onTentar={recarregar} />}
      {!dados && erro ? (
        <FalhaCarga erro={erro} onTentar={recarregar} />
      ) : (
        <TabelaExecucoes
          execucoes={dados}
          carregando={carregando}
          janela={janela}
          vazio={<SemExecucoes filtrado={temFiltro(aplicados)} janela={janela} limpar={limpar} mudarJanela={mudarJanela} />}
        />
      )}
    </Card>
  );
}

function TabelaExecucoes({
  execucoes,
  carregando,
  janela,
  vazio,
}: {
  execucoes: IaExecucao[] | null;
  carregando: boolean;
  janela: OpcaoJanela;
  vazio: ReactNode;
}) {
  return (
    <div className="ia-recarregavel">
      <DataTable
        columns={colunas(janela.id)}
        rows={execucoes ?? []}
        keyFn={(e) => e.trace_id}
        loading={carregando && !execucoes}
        initialSort={{ key: "inicio_ms", dir: "desc" }}
        rowActions={(e) => (
          <a className="btn btn--ghost" href={hrefReplay(e.trace_id, janela.id)}>
            Ver replay
          </a>
        )}
        empty={vazio}
      />
      {execucoes && execucoes.length >= LIMITE && (
        <p className="ia-sub">
          A lista parou em {fmtInteiro(LIMITE)} execuções, o máximo pedido de uma vez. Refine os filtros ou encurte a janela
          para ver as demais.
        </p>
      )}
    </div>
  );
}

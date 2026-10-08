// Tabelas da Visão geral de IA: por modelo, agentes que mais gastam e ferramentas
// que mais falham.
import type { IaAgenteResumo, IaFerramentaResumo, IaModeloResumo } from "../../api.ia";
import { Badge, Card, DataTable, InfoTip, type Column } from "../../components";
import { Custo } from "./comum";
import { fmtDataPreco, fmtInteiro, fmtMs, fmtPct, fmtTokens, fmtUsd, SEM_PRECO } from "./formato";
import { hrefIa } from "./rotas";

const TOP_AGENTES = 8;
const TOP_FERRAMENTAS = 5;

/** Ordena custo do maior para o menor; custo desconhecido vai para o fim. */
const porCusto = (a: { custo_usd: number | null }, b: { custo_usd: number | null }) =>
  (b.custo_usd ?? -1) - (a.custo_usd ?? -1);

function PrecoVigente({ m }: { m: IaModeloResumo }) {
  if (!m.preco) return <span className="ia-sub">{SEM_PRECO}</span>;
  return (
    <span>
      <span className="tabular">
        {fmtUsd(m.preco.entrada_por_1m)} / {fmtUsd(m.preco.saida_por_1m)}
      </span>
      <div className="ia-sub">por 1 milhão de tokens · desde {fmtDataPreco(m.preco.vigente_desde)}</div>
    </span>
  );
}

const COLUNAS_MODELO: Column<IaModeloResumo>[] = [
  {
    key: "modelo",
    label: "Modelo",
    sortable: true,
    render: (m) => (
      <span>
        <span className="ia-mono">{m.modelo}</span> {m.sem_preco && <Badge state="warn">sem preço</Badge>}
        <div className="ia-sub">{m.provedor || "provedor não informado"}</div>
      </span>
    ),
  },
  { key: "chamadas", label: "Chamadas", align: "right", sortable: true, render: (m) => fmtInteiro(m.chamadas) },
  {
    key: "erros",
    label: "Erros",
    align: "right",
    sortable: true,
    render: (m) => `${fmtInteiro(m.erros)} (${fmtPct(m.chamadas > 0 ? m.erros / m.chamadas : null)})`,
  },
  {
    key: "tokens",
    label: "Tokens (entrada / saída)",
    align: "right",
    hideOnMobile: true,
    sortValue: (m) => m.tokens_entrada + m.tokens_saida,
    sortable: true,
    render: (m) => `${fmtTokens(m.tokens_entrada)} / ${fmtTokens(m.tokens_saida)}`,
  },
  {
    key: "custo_usd",
    label: "Custo",
    align: "right",
    sortable: true,
    sortValue: (m) => m.custo_usd ?? -1,
    render: (m) => <Custo valor={m.custo_usd} parcial={m.sem_preco && m.custo_usd != null} vazio={m.sem_preco ? SEM_PRECO : undefined} />,
  },
  {
    key: "p95",
    label: "Latência p95",
    align: "right",
    sortable: true,
    sortValue: (m) => m.latencia_p95_ms ?? -1,
    render: (m) => fmtMs(m.latencia_p95_ms),
  },
  { key: "preco", label: "Preço (entrada / saída)", hideOnMobile: true, render: (m) => <PrecoVigente m={m} /> },
];

export function TabelaModelos({ modelos }: { modelos: IaModeloResumo[] }) {
  return (
    <Card
      title={
        <>
          Por modelo{" "}
          <InfoTip
            title="custo por modelo"
            text="Custo estimado = tokens × preço vigente na data de cada chamada. Modelo marcado “sem preço” não entra na soma: cadastre o preço em Modelos e preços."
          />
        </>
      }
    >
      <DataTable
        columns={COLUNAS_MODELO}
        rows={modelos}
        keyFn={(m) => `${m.provedor}/${m.modelo}`}
        initialSort={{ key: "custo_usd", dir: "desc" }}
        empty={<p className="ia-texto">Nenhuma chamada de modelo nesta janela. Quando houver, cada modelo aparece aqui com o custo dele.</p>}
      />
    </Card>
  );
}

export function TabelaAgentes({ agentes, janelaId }: { agentes: IaAgenteResumo[]; janelaId: string }) {
  const top = [...agentes].sort(porCusto).slice(0, TOP_AGENTES);
  const colunas: Column<IaAgenteResumo>[] = [
    {
      key: "agente",
      label: "Agente",
      render: (a) => (
        <span>
          <a href={hrefIa("/ia/execucoes", { janela: janelaId, agente: a.agente })}>{a.agente || "sem nome"}</a>
          <div className="ia-sub">serviço {a.service || "não informado"}</div>
        </span>
      ),
    },
    { key: "chamadas", label: "Chamadas", align: "right", render: (a) => fmtInteiro(a.chamadas) },
    { key: "erros", label: "Erros", align: "right", render: (a) => fmtInteiro(a.erros) },
    { key: "custo_usd", label: "Custo", align: "right", render: (a) => fmtUsd(a.custo_usd) },
  ];
  return (
    <Card
      title={
        <>
          Agentes que mais gastam{" "}
          <InfoTip
            title="agentes que mais gastam"
            text={`Os ${TOP_AGENTES} agentes de maior custo na janela. O agente é o que a aplicação declarou ao iniciar a execução; sem essa informação, usamos o nome do serviço. Clique num agente para ver as execuções dele.`}
          />
        </>
      }
    >
      <DataTable
        columns={colunas}
        rows={top}
        keyFn={(a) => `${a.service}/${a.agente}`}
        pageSize={0}
        empty={
          <p className="ia-texto">
            Nenhum agente identificado nesta janela. O agente é o que a aplicação declarou ao iniciar a execução; sem essa
            informação, usamos o nome do serviço.
          </p>
        }
      />
    </Card>
  );
}

export function TabelaFerramentasComErro({ ferramentas, janelaId }: { ferramentas: IaFerramentaResumo[]; janelaId: string }) {
  const top = ferramentas
    .filter((f) => f.erros > 0)
    .sort((a, b) => b.erros - a.erros)
    .slice(0, TOP_FERRAMENTAS);
  const colunas: Column<IaFerramentaResumo>[] = [
    { key: "ferramenta", label: "Ferramenta", render: (f) => <span className="ia-mono">{f.ferramenta}</span> },
    {
      key: "erros",
      label: "Erros",
      align: "right",
      render: (f) => `${fmtInteiro(f.erros)} de ${fmtInteiro(f.chamadas)}`,
    },
    { key: "p95", label: "p95", align: "right", render: (f) => fmtMs(f.latencia_p95_ms) },
  ];
  return (
    <Card
      title={
        <>
          Ferramentas com mais erros{" "}
          <InfoTip
            title="ferramentas com mais erros"
            text={`As ${TOP_FERRAMENTAS} ferramentas que mais falharam na janela, com quantas chamadas deram erro e o p95 (95% das chamadas terminaram em até este tempo). Ferramenta sem erro não aparece aqui.`}
          />
        </>
      }
    >
      <DataTable
        columns={colunas}
        rows={top}
        keyFn={(f) => f.ferramenta}
        pageSize={0}
        empty={<p className="ia-texto">Nenhuma ferramenta falhou nesta janela. Bom sinal: as que foram chamadas responderam.</p>}
      />
      <p className="ia-sub">
        <a href={hrefIa("/ia/ferramentas", { janela: janelaId })}>Ver todas as ferramentas</a>
      </p>
    </Card>
  );
}

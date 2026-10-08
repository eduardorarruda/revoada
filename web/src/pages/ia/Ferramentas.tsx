// Ferramentas (#/ia/ferramentas): as funções que os agentes chamam (buscar pedido,
// consultar clima…), com volume, taxa de erro, latência e os erros mais comuns.
import { useCallback } from "react";
import { Wrench } from "lucide-react";
import { listIaFerramentas, type IaFerramenta } from "../../api.ia";
import { Badge, Card, DataTable, EmptyState, InfoTip, type Column } from "../../components";
import { intervaloDaJanela, janelaParaAmpliar, type OpcaoJanela } from "../../components/JanelaTempo";
import { FalhaCarga, useCarga, useSinalizarCarga } from "./comum";
import { fmtInteiro, fmtMs, fmtPct } from "./formato";
import { plural, TAXA_ERRO_ATENCAO, TAXA_ERRO_CRITICA, taxaErro } from "./veredito";

const EXPLICA_FERRAMENTAS =
  "Ferramentas são as funções que o agente pede para rodar no meio da conversa (buscar um pedido, consultar o clima…). Uma ferramenta lenta segura a resposta inteira; uma que falha faz o agente tentar de novo e gastar mais chamadas de modelo. p50 é o tempo típico; p95, o tempo em que 95% das chamadas terminaram.";

function TaxaErro({ f }: { f: IaFerramenta }) {
  const taxa = taxaErro(f);
  const texto = fmtPct(taxa);
  if (taxa != null && taxa >= TAXA_ERRO_CRITICA) return <Badge state="crit">{texto}</Badge>;
  if (taxa != null && taxa >= TAXA_ERRO_ATENCAO) return <Badge state="warn">{texto}</Badge>;
  return <span className="tabular">{texto}</span>;
}

function ErrosFrequentes({ f }: { f: IaFerramenta }) {
  if (f.erros_frequentes.length === 0) return <span className="ia-sub">{f.erros > 0 ? "sem mensagem de erro" : "nenhum erro"}</span>;
  return (
    <ul className="ia-lista-erros">
      {f.erros_frequentes.map((e) => (
        <li key={e.erro}>
          <span className="ia-mono">{e.erro}</span> <span className="ia-sub tabular">× {fmtInteiro(e.vezes)}</span>
        </li>
      ))}
    </ul>
  );
}

const COLUNAS: Column<IaFerramenta>[] = [
  { key: "ferramenta", label: "Ferramenta", sortable: true, render: (f) => <span className="ia-mono">{f.ferramenta}</span> },
  { key: "chamadas", label: "Chamadas", align: "right", sortable: true, render: (f) => fmtInteiro(f.chamadas) },
  { key: "erros", label: "Erros", align: "right", sortable: true, render: (f) => fmtInteiro(f.erros) },
  {
    key: "taxa_erro",
    label: "Taxa de erro",
    align: "right",
    sortable: true,
    sortValue: (f) => taxaErro(f) ?? -1,
    render: (f) => <TaxaErro f={f} />,
  },
  { key: "p50", label: "p50", align: "right", sortable: true, sortValue: (f) => f.latencia_p50_ms ?? -1, render: (f) => fmtMs(f.latencia_p50_ms) },
  { key: "p95", label: "p95", align: "right", sortable: true, sortValue: (f) => f.latencia_p95_ms ?? -1, render: (f) => fmtMs(f.latencia_p95_ms) },
  { key: "erros_frequentes", label: "Erros mais frequentes", render: (f) => <ErrosFrequentes f={f} /> },
];

function SemFerramentas({ janela, mudarJanela }: { janela: OpcaoJanela; mudarJanela?: (id: string) => void }) {
  const maior = ["24h", "7d", "30d"].map((id) => janelaParaAmpliar(janela, id)).find(Boolean) ?? null;
  return (
    <EmptyState
      icon={<Wrench size={32} strokeWidth={1.5} />}
      title="Nenhuma chamada de ferramenta nesta janela"
      body={`${janela.frase} sem nenhuma chamada de ferramenta registrada. Agentes que só conversam com o modelo, sem chamar funções, não aparecem aqui.`}
      action={maior && mudarJanela ? { label: `Ampliar para ${maior.rotulo}`, onClick: () => mudarJanela(maior.id) } : undefined}
    />
  );
}

function TituloFerramentas({ n }: { n: number | null }) {
  return (
    <span className="ia-cartao-titulo">
      {n == null ? "Ferramentas" : plural(n, "ferramenta", "ferramentas")}
      <InfoTip title="ferramentas" text={EXPLICA_FERRAMENTAS} />
    </span>
  );
}

export function Ferramentas({
  janela,
  versao,
  mudarJanela,
}: {
  janela: OpcaoJanela;
  versao: number;
  mudarJanela?: (id: string) => void;
}) {
  const buscar = useCallback(
    () => listIaFerramentas(intervaloDaJanela(janela)).then((r) => r.ferramentas),
    // `versao` entra de propósito: o botão Atualizar refaz a busca até agora.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [janela, versao],
  );
  const carga = useCarga(buscar, "Não foi possível listar as ferramentas.");
  useSinalizarCarga(carga);
  const { dados, erro, carregando, recarregar } = carga;
  return (
    <Card title={<TituloFerramentas n={dados ? dados.length : null} />}>
      {erro && dados && <FalhaCarga erro={erro} onTentar={recarregar} />}
      {!dados && erro ? (
        <FalhaCarga erro={erro} onTentar={recarregar} />
      ) : (
        <div className="ia-recarregavel">
          <DataTable
            columns={COLUNAS}
            rows={dados ?? []}
            keyFn={(f) => f.ferramenta}
            loading={carregando && !dados}
            initialSort={{ key: "erros", dir: "desc" }}
            searchable
            searchText={(f) => f.ferramenta}
            searchPlaceholder="Buscar ferramenta…"
            empty={<SemFerramentas janela={janela} mudarJanela={mudarJanela} />}
          />
        </div>
      )}
    </Card>
  );
}

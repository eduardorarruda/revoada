// Ferramentas (#/ia/ferramentas): as funções que os agentes chamam (buscar pedido,
// consultar clima…), com volume, taxa de erro, latência e os erros mais comuns.
import { useCallback } from "react";
import { listIaFerramentas, type IaFerramenta } from "../../api.ia";
import { Badge, Card, DataTable, type Column } from "../../components";
import { intervaloDaJanela, type OpcaoJanela } from "../../components/JanelaTempo";
import { FalhaCarga, useCarga } from "./comum";
import { fmtInteiro, fmtMs, fmtPct } from "./formato";
import { TAXA_ERRO_ATENCAO, TAXA_ERRO_CRITICA, taxaErro } from "./veredito";

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
    <ul style={{ margin: 0, paddingLeft: "var(--sp-4)" }}>
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

export function Ferramentas({ janela, versao }: { janela: OpcaoJanela; versao: number }) {
  const buscar = useCallback(
    () => listIaFerramentas(intervaloDaJanela(janela)).then((r) => r.ferramentas),
    // `versao` entra de propósito: o botão Atualizar refaz a busca até agora.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [janela, versao],
  );
  const { dados, erro, carregando, recarregar } = useCarga(buscar, "Não foi possível listar as ferramentas.");
  return (
    <Card>
      <p className="ia-texto">
        Ferramentas são as funções que o agente pede para rodar no meio da conversa. Uma ferramenta lenta segura a resposta
        inteira; uma que falha faz o agente tentar de novo e gastar mais chamadas de modelo. p50 é o tempo típico; p95, o
        tempo que só 5% das chamadas passam.
      </p>
      {erro && dados && <FalhaCarga erro={erro} onTentar={recarregar} />}
      <DataTable
        columns={COLUNAS}
        rows={dados ?? []}
        keyFn={(f) => f.ferramenta}
        loading={carregando && !dados}
        error={!dados && erro ? erro : undefined}
        initialSort={{ key: "erros", dir: "desc" }}
        searchable
        searchText={(f) => f.ferramenta}
        searchPlaceholder="Buscar ferramenta…"
        empty={<p className="ia-texto">Nenhuma chamada de ferramenta nesta janela.</p>}
      />
    </Card>
  );
}

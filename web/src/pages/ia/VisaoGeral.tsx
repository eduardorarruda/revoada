// Visão geral (#/ia): o veredito da janela, os indicadores, o custo no tempo e
// onde ele está (por modelo, por agente), além das ferramentas que mais falham.
import { useCallback } from "react";
import { BrainCircuit } from "lucide-react";
import { getIaResumo, type IaResumo, type IaTotais } from "../../api.ia";
import { Card, EmptyState, InfoTip, Skeleton } from "../../components";
import { Kpi, KpiGrid } from "../../components/Kpi";
import { Veredito } from "../../components/Veredito";
import { intervaloDaJanela, janelaParaAmpliar, type OpcaoJanela } from "../../components/JanelaTempo";
import { TimeSeriesPanel } from "../../panels";
import type { TimeSeriesData } from "../../panels/types";
import { Aviso, FalhaCarga, useCarga, useSinalizarCarga } from "./comum";
import { fmtInteiro, fmtMs, fmtPct, fmtReferencia, fmtTokens, fmtUsd, NAO_INFORMADO } from "./formato";
import { hrefIa } from "./rotas";
import { motivoParcial, plural, taxaErro, TAXA_ERRO_ATENCAO, TAXA_ERRO_CRITICA, vereditoIa } from "./veredito";
import { TabelaAgentes, TabelaFerramentasComErro, TabelaModelos } from "./visaoTabelas";

/**
 * O resumo junto da janela que o PRODUZIU. Enquanto uma janela nova carrega, a
 * tela continua mostrando o dado antigo; o veredito precisa dizer a janela desse
 * dado ("Última 1 h"), e não a que acabou de ser escolhida ("Últimos 7 dias").
 */
interface ResumoDaJanela {
  resumo: IaResumo;
  janela: OpcaoJanela;
}

const ROTULOS_KPI = ["Custo", "Chamadas de modelo", "Com erro", "Latência p95", "Tokens de entrada", "Tokens de saída"] as const;

export function VisaoGeral({
  janela,
  versao,
  mudarJanela,
}: {
  janela: OpcaoJanela;
  versao: number;
  mudarJanela?: (id: string) => void;
}) {
  const buscar = useCallback(
    (): Promise<ResumoDaJanela> => getIaResumo(intervaloDaJanela(janela)).then((resumo) => ({ resumo, janela })),
    // `versao` entra de propósito: o botão Atualizar refaz a busca até agora.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [janela, versao],
  );
  const carga = useCarga(buscar, "Não foi possível carregar o resumo de IA.");
  useSinalizarCarga(carga);
  const { dados, erro, recarregar } = carga;

  if (!dados) {
    if (erro) return <FalhaCarga erro={erro} onTentar={recarregar} />;
    return <EsqueletoVisao />;
  }
  const { resumo: r, janela: janelaDosDados } = dados;
  return (
    <>
      {erro && <FalhaCarga erro={erro} onTentar={recarregar} />}
      <Veredito veredito={vereditoIa(r.totais, janelaDosDados.frase)} />
      {r.totais.chamadas === 0 ? (
        <SemChamadas janela={janelaDosDados} mudarJanela={mudarJanela} />
      ) : (
        <>
          <Indicadores t={r.totais} />
          <AvisoPrecos r={r} janelaId={janelaDosDados.id} />
          <Graficos r={r} />
          <div className="ia-recarregavel">
            <TabelaModelos modelos={r.por_modelo} />
          </div>
          <div className="grid-2 ia-recarregavel">
            <TabelaAgentes agentes={r.por_agente} janelaId={janelaDosDados.id} />
            <TabelaFerramentasComErro ferramentas={r.ferramentas} janelaId={janelaDosDados.id} />
          </div>
        </>
      )}
    </>
  );
}

/** Esqueleto com a forma da tela pronta: veredito, seis indicadores e dois gráficos. */
function EsqueletoVisao() {
  return (
    <>
      <p className="so-leitor" role="status">
        Carregando o resumo de IA…
      </p>
      <Skeleton height={92} />
      <KpiGrid>
        {ROTULOS_KPI.map((r) => (
          <Kpi key={r} rotulo={r} valor={null} loading />
        ))}
      </KpiGrid>
      <div className="grid-2">
        <Card>
          <Skeleton height={220} />
        </Card>
        <Card>
          <Skeleton height={220} />
        </Card>
      </div>
    </>
  );
}

/** Janela sem nenhuma chamada: em vez de seis zeros e gráficos vazios, o caminho para ter dado. */
function SemChamadas({ janela, mudarJanela }: { janela: OpcaoJanela; mudarJanela?: (id: string) => void }) {
  const maior = janelaParaAmpliar(janela, "7d");
  const acao =
    maior && mudarJanela
      ? { label: `Ampliar para ${maior.rotulo}`, onClick: () => mudarJanela(maior.id) }
      : {
          label: "Abrir o Guia",
          onClick: () => {
            window.location.hash = "#/help";
          },
        };
  return (
    <Card>
      <EmptyState
        icon={<BrainCircuit size={32} strokeWidth={1.5} />}
        title="Nenhuma chamada de IA nesta janela"
        body={`${janela.frase} sem nenhum span de IA recebido. Se a aplicação já está instrumentada, amplie a janela; se não, siga os passos.`}
        steps={[
          "Instrumente a aplicação com OpenTelemetry (convenção GenAI)",
          "Aponte o exportador OTLP para o gateway do Revoada",
          "Gere uma conversa e volte aqui",
        ]}
        action={acao}
      />
    </Card>
  );
}

function tomDaTaxa(taxa: number | null): "warn" | "crit" | undefined {
  if (taxa == null) return undefined;
  if (taxa >= TAXA_ERRO_CRITICA) return "crit";
  return taxa >= TAXA_ERRO_ATENCAO ? "warn" : undefined;
}

function subDoCusto(t: IaTotais) {
  if (t.custo_usd == null) return NAO_INFORMADO;
  const parcial = motivoParcial(t);
  if (!parcial) return "valor da biblioteca + estimativa pela tabela";
  return (
    <span className="ia-parcial">
      parcial <InfoTip title="custo parcial" text={parcial} />
    </span>
  );
}

/**
 * Indicadores. Valor ausente vai como null (o Kpi mostra "—") e o motivo na linha
 * de baixo ("não informado"): a frase inteira no lugar do número estourava o
 * cartão em telas de 400px.
 */
function Indicadores({ t }: { t: IaTotais }) {
  const taxa = taxaErro(t);
  return (
    <KpiGrid className="ia-recarregavel">
      <Kpi rotulo="Custo" valor={t.custo_usd == null ? null : fmtUsd(t.custo_usd)} sub={subDoCusto(t)} />
      <Kpi rotulo="Chamadas de modelo" valor={fmtInteiro(t.chamadas)} sub={plural(t.execucoes, "execução", "execuções")} />
      <Kpi
        rotulo="Com erro"
        valor={taxa == null ? null : fmtPct(taxa)}
        tone={tomDaTaxa(taxa)}
        sub={plural(t.erros, "chamada falhou", "chamadas falharam")}
      />
      <Kpi
        rotulo="Latência p95"
        valor={t.latencia_p95_ms == null ? null : fmtMs(t.latencia_p95_ms)}
        title="95% das chamadas terminaram em até este tempo"
        sub={t.latencia_p95_ms == null ? NAO_INFORMADO : `p50 ${fmtMs(t.latencia_p50_ms)} · p99 ${fmtMs(t.latencia_p99_ms)}`}
      />
      <Kpi rotulo="Tokens de entrada" valor={fmtTokens(t.tokens_entrada)} sub={`${fmtTokens(t.tokens_cache_leitura)} lidos do cache`} />
      <Kpi
        rotulo="Tokens de saída"
        valor={fmtTokens(t.tokens_saida)}
        sub={
          t.sem_tokens > 0
            ? plural(t.sem_tokens, "chamada sem tokens informados", "chamadas sem tokens informados")
            : "todas as chamadas informaram tokens"
        }
      />
    </KpiGrid>
  );
}

function AvisoPrecos({ r, janelaId }: { r: IaResumo; janelaId: string }) {
  const semPreco = r.por_modelo.filter((m) => m.sem_preco);
  if (semPreco.length === 0 && !r.precos.desatualizada) return null;
  const idade =
    r.precos.dias_desde_atualizacao == null ? "idade desconhecida" : plural(r.precos.dias_desde_atualizacao, "dia", "dias");
  return (
    <Aviso tom="warn" titulo="Preços a conferir">
      {semPreco.length > 0 && (
        <p>
          {plural(semPreco.length, "modelo sem preço", "modelos sem preço")}:{" "}
          <span className="ia-mono">{semPreco.map((m) => m.modelo).join(", ")}</span>. O custo deles fica fora da soma até
          um preço ser cadastrado.
        </p>
      )}
      {r.precos.desatualizada && (
        <p>
          A tabela de preços de referência ({fmtReferencia(r.precos.referencia) || "versão não informada"}) tem {idade}.
          Provedores mudam preços; confira os modelos que você usa.
        </p>
      )}
      <p>
        <a href={hrefIa("/ia/precos", { janela: janelaId })}>Abrir Modelos e preços</a>
      </p>
    </Aviso>
  );
}

function serieDe(r: IaResumo, campo: "custo_usd" | "chamadas" | "erros"): (number | null)[] {
  return r.serie.map((p) => p[campo]);
}

function Graficos({ r }: { r: IaResumo }) {
  const ts = r.serie.map((p) => Math.floor(p.ts_ms / 1000));
  const estado = r.serie.length === 0 ? "empty" : "ok";
  const custo: TimeSeriesData = { ts, series: [{ label: "Custo (US$)", values: serieDe(r, "custo_usd") }] };
  const chamadas: TimeSeriesData = {
    ts,
    series: [
      { label: "Chamadas", values: serieDe(r, "chamadas") },
      { label: "Com erro", values: serieDe(r, "erros") },
    ],
  };
  const passo = r.janela.passo_s >= 60 ? `${fmtInteiro(r.janela.passo_s / 60)} min` : `${r.janela.passo_s} s`;
  const tabela = r.precos.referencia ? `tabela de referência de ${fmtReferencia(r.precos.referencia)}` : "tabela cadastrada";
  return (
    <div className="grid-2 ia-recarregavel">
      <TimeSeriesPanel
        title="Custo no tempo"
        description={`Dólares gastos a cada ${passo} (valor da biblioteca + estimativa pela tabela). Um ponto vazio é um intervalo sem custo calculável, não um custo zero. Preços: ${tabela}.`}
        state={estado}
        data={custo}
        unit="usd"
      />
      <TimeSeriesPanel
        title="Chamadas e erros"
        description={`Chamadas de modelo a cada ${passo} e quantas falharam. Erro sobe junto com chamada? É volume. Sobe sozinho? É o provedor ou a aplicação.`}
        state={estado}
        data={chamadas}
        unit="count"
      />
    </div>
  );
}

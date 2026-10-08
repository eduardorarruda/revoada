// Visão geral (#/ia): o veredito da janela, os indicadores, o custo no tempo e
// onde ele está (por modelo, por agente), além das ferramentas que mais falham.
import { useCallback } from "react";
import { getIaResumo, type IaResumo, type IaTotais } from "../../api.ia";
import { InfoTip, Skeleton } from "../../components";
import { Kpi, KpiGrid } from "../../components/Kpi";
import { Veredito } from "../../components/Veredito";
import { intervaloDaJanela, type OpcaoJanela } from "../../components/JanelaTempo";
import { TimeSeriesPanel } from "../../panels";
import type { TimeSeriesData } from "../../panels/types";
import { Aviso, FalhaCarga, useCarga } from "./comum";
import { fmtInteiro, fmtMs, fmtPct, fmtTokens, fmtUsd } from "./formato";
import { hrefIa } from "./rotas";
import { motivoParcial, taxaErro, TAXA_ERRO_ATENCAO, TAXA_ERRO_CRITICA, vereditoIa } from "./veredito";
import { TabelaAgentes, TabelaFerramentasComErro, TabelaModelos } from "./visaoTabelas";

export function VisaoGeral({ janela, versao }: { janela: OpcaoJanela; versao: number }) {
  const buscar = useCallback(
    () => getIaResumo(intervaloDaJanela(janela)),
    // `versao` entra de propósito: o botão Atualizar refaz a busca até agora.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [janela, versao],
  );
  const { dados, erro, recarregar } = useCarga(buscar, "Não foi possível carregar o resumo de IA.");

  if (!dados) {
    if (erro) return <FalhaCarga erro={erro} onTentar={recarregar} />;
    return <Skeleton height={160} />;
  }
  return (
    <>
      {erro && <FalhaCarga erro={erro} onTentar={recarregar} />}
      <Veredito veredito={vereditoIa(dados.totais, janela.frase)} />
      <Indicadores t={dados.totais} />
      <AvisoPrecos r={dados} janelaId={janela.id} />
      <Graficos r={dados} />
      <TabelaModelos modelos={dados.por_modelo} />
      <div className="grid-2">
        <TabelaAgentes agentes={dados.por_agente} janelaId={janela.id} />
        <TabelaFerramentasComErro ferramentas={dados.ferramentas} janelaId={janela.id} />
      </div>
    </>
  );
}

function tomDaTaxa(taxa: number | null): "warn" | "crit" | undefined {
  if (taxa == null) return undefined;
  if (taxa >= TAXA_ERRO_CRITICA) return "crit";
  return taxa >= TAXA_ERRO_ATENCAO ? "warn" : undefined;
}

function Indicadores({ t }: { t: IaTotais }) {
  const taxa = taxaErro(t);
  const parcial = motivoParcial(t);
  return (
    <KpiGrid>
      <Kpi
        rotulo="Custo"
        valor={fmtUsd(t.custo_usd)}
        sub={
          parcial ? (
            <span className="ia-parcial">
              parcial <InfoTip title="custo parcial" text={parcial} />
            </span>
          ) : (
            "informado + estimado"
          )
        }
      />
      <Kpi rotulo="Chamadas de modelo" valor={fmtInteiro(t.chamadas)} sub={`${fmtInteiro(t.execucoes)} execuções`} />
      <Kpi rotulo="Com erro" valor={fmtPct(taxa)} tone={tomDaTaxa(taxa)} sub={`${fmtInteiro(t.erros)} chamadas falharam`} />
      <Kpi rotulo="Latência p95" valor={fmtMs(t.latencia_p95_ms)} sub={`p50 ${fmtMs(t.latencia_p50_ms)} · p99 ${fmtMs(t.latencia_p99_ms)}`} />
      <Kpi rotulo="Tokens de entrada" valor={fmtTokens(t.tokens_entrada)} sub={`${fmtTokens(t.tokens_cache_leitura)} lidos do cache`} />
      <Kpi
        rotulo="Tokens de saída"
        valor={fmtTokens(t.tokens_saida)}
        sub={t.sem_tokens > 0 ? `${fmtInteiro(t.sem_tokens)} chamadas sem tokens informados` : "todas as chamadas informaram tokens"}
      />
    </KpiGrid>
  );
}

function AvisoPrecos({ r, janelaId }: { r: IaResumo; janelaId: string }) {
  const semPreco = r.por_modelo.filter((m) => m.sem_preco);
  if (semPreco.length === 0 && !r.precos.desatualizada) return null;
  return (
    <Aviso tom="warn" titulo="Preços a conferir">
      {semPreco.length > 0 && (
        <p>
          {semPreco.length === 1 ? "1 modelo sem preço" : `${semPreco.length} modelos sem preço`}:{" "}
          <span className="ia-mono">{semPreco.map((m) => m.modelo).join(", ")}</span>. O custo deles fica fora da soma até
          um preço ser cadastrado.
        </p>
      )}
      {r.precos.desatualizada && (
        <p>
          A tabela de preços de referência ({r.precos.referencia}) tem{" "}
          {r.precos.dias_desde_atualizacao == null ? "idade desconhecida" : `${fmtInteiro(r.precos.dias_desde_atualizacao)} dias`}.
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
  return (
    <div className="grid-2">
      <TimeSeriesPanel
        title="Custo no tempo"
        description={`Dólares gastos a cada ${passo} (informado + estimado). Um ponto vazio é um intervalo sem custo calculável, não um custo zero. Preços: ${r.precos.referencia ? `referência ${r.precos.referencia}` : "tabela cadastrada"}.`}
        state={estado}
        data={custo}
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


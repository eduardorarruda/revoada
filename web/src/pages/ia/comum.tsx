// Peças compartilhadas pelas telas de Agentes de IA.
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { CircleAlert, Info, TriangleAlert } from "lucide-react";
import { InfoTip } from "../../components";
import { mensagemDeErro } from "../../format";
import { fmtUsd, NAO_INFORMADO } from "./formato";

export interface Carga<T> {
  dados: T | null;
  erro: string | null;
  carregando: boolean;
  recarregar: () => void;
}

/**
 * Busca dados e guarda o último resultado. Uma resposta atrasada de uma busca antiga
 * (o filtro mudou no meio do caminho) é descartada. Erro mantém o último dado bom
 * na tela e diz o que falhou. `buscar` deve vir de useCallback: é a dependência.
 */
export function useCarga<T>(buscar: () => Promise<T>, alternativaErro = "Não foi possível carregar os dados."): Carga<T> {
  const [estado, setEstado] = useState<{ dados: T | null; erro: string | null; carregando: boolean }>({
    dados: null,
    erro: null,
    carregando: true,
  });
  const seq = useRef(0);

  const recarregar = useCallback(() => {
    const id = ++seq.current;
    setEstado((e) => ({ ...e, carregando: true }));
    buscar().then(
      (dados) => {
        if (id === seq.current) setEstado({ dados, erro: null, carregando: false });
      },
      (e: unknown) => {
        if (id === seq.current) setEstado((ant) => ({ dados: ant.dados, erro: mensagemDeErro(e, alternativaErro), carregando: false }));
      },
    );
  }, [buscar, alternativaErro]);

  useEffect(() => {
    recarregar();
    return () => {
      seq.current += 1; // desmontou ou trocou a busca: ignora o que ainda chegar
    };
  }, [recarregar]);

  return { ...estado, recarregar };
}

/** Código HTTP de um erro do cliente da API (403 de autorização ou "404: …"). */
export function statusDoErro(e: unknown): number | null {
  if (typeof e === "object" && e !== null && "codigo" in e) return 403;
  const m = e instanceof Error ? /^(\d{3}):/.exec(e.message) : null;
  return m ? Number(m[1]) : null;
}

type TomAviso = "warn" | "crit" | "info" | "neutro";
const ICONE_AVISO = { warn: TriangleAlert, crit: CircleAlert, info: Info, neutro: Info } as const;

/** Aviso em faixa: a cor diz o estado e o ícone repete a informação (não só cor). */
export function Aviso({ tom, titulo, children }: { tom: TomAviso; titulo: string; children?: ReactNode }) {
  const Icone = ICONE_AVISO[tom];
  return (
    <section className={`ia-aviso ia-aviso--${tom}`} aria-label={titulo}>
      <Icone size={18} aria-hidden={true} />
      <div className="ia-aviso__corpo">
        <strong>{titulo}</strong>
        {children}
      </div>
    </section>
  );
}

/** Mensagem de falha de carga, com o motivo e o que acontece a seguir. */
export function FalhaCarga({ erro, onTentar }: { erro: string; onTentar: () => void }) {
  return (
    <Aviso tom="crit" titulo="A consulta falhou">
      <p>{erro}</p>
      <p>
        <button type="button" className="btn btn--ghost" onClick={onTentar}>
          Tentar de novo
        </button>
      </p>
    </Aviso>
  );
}

const EXPLICA_PARCIAL =
  "Parte das chamadas não entrou na soma: ou o modelo não tem preço cadastrado (cadastre em Modelos e preços) ou a biblioteca não informou os tokens. O custo real é maior que o mostrado.";

/** Custo com o selo "parcial" visível e explicado quando a soma está incompleta. */
export function Custo({ valor, parcial, vazio = NAO_INFORMADO }: { valor: number | null; parcial: boolean; vazio?: string }) {
  return (
    <span className="ia-parcial tabular">
      {fmtUsd(valor, vazio)}
      {parcial && (
        <>
          <span className="ia-sub">parcial</span>
          <InfoTip title="custo parcial" text={EXPLICA_PARCIAL} />
        </>
      )}
    </span>
  );
}

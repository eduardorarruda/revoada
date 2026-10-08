// Peças compartilhadas pelas telas de Agentes de IA.
import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { CircleAlert, Info, TriangleAlert } from "lucide-react";
import { InfoTip } from "../../components";
import { mensagemDeErro } from "../../format";
import { fmtUsd, NAO_INFORMADO } from "./formato";

export interface Carga<T> {
  dados: T | null;
  erro: string | null;
  carregando: boolean;
  /** Quando chegou o último dado bom (epoch ms); null antes do primeiro. */
  atualizadoEm: number | null;
  recarregar: () => void;
}

type EstadoCarga<T> = Omit<Carga<T>, "recarregar">;

/**
 * Busca dados e guarda o último resultado. Uma resposta atrasada de uma busca antiga
 * (o filtro mudou no meio do caminho) é descartada. Erro mantém o último dado bom
 * na tela e diz o que falhou. `buscar` deve vir de useCallback: é a dependência.
 */
export function useCarga<T>(buscar: () => Promise<T>, alternativaErro = "Não foi possível carregar os dados."): Carga<T> {
  const [estado, setEstado] = useState<EstadoCarga<T>>({
    dados: null,
    erro: null,
    carregando: true,
    atualizadoEm: null,
  });
  const seq = useRef(0);

  const recarregar = useCallback(() => {
    const id = ++seq.current;
    setEstado((e) => ({ ...e, carregando: true }));
    buscar().then(
      (dados) => {
        if (id === seq.current) setEstado({ dados, erro: null, carregando: false, atualizadoEm: Date.now() });
      },
      (e: unknown) => {
        if (id === seq.current) setEstado((ant) => ({ ...ant, erro: mensagemDeErro(e, alternativaErro), carregando: false }));
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

/** O que a vista conta para a casca: se está buscando e de quando é o dado na tela. */
export interface SinalCarga {
  carregando: boolean;
  temDados: boolean;
  atualizadoEm: number | null;
}

export const SINAL_VAZIO: SinalCarga = { carregando: false, temDados: false, atualizadoEm: null };

const CargaDaVista = createContext<(s: SinalCarga) => void>(() => {});

/** A casca (Ia.tsx) recebe aqui o estado de carga da vista aberta. */
export const ProvedorCargaDaVista = CargaDaVista.Provider;

/**
 * A vista avisa a casca do estado da sua carga principal: é o que gira o botão
 * Atualizar, esmaece o dado velho durante a recarga e escreve "atualizado há X".
 * Fora da casca (testes, outra tela) não faz nada.
 */
export function useSinalizarCarga(c: Pick<Carga<unknown>, "carregando" | "dados" | "atualizadoEm">): void {
  const sinalizar = useContext(CargaDaVista);
  const temDados = c.dados != null;
  useEffect(() => {
    sinalizar({ carregando: c.carregando, temDados, atualizadoEm: c.atualizadoEm });
  }, [sinalizar, c.carregando, temDados, c.atualizadoEm]);
  // Saiu da tela: a próxima vista começa sem herdar o estado desta.
  useEffect(() => () => sinalizar(SINAL_VAZIO), [sinalizar]);
}

/** Código HTTP de um erro do cliente da API (403 de autorização ou "404: …"). */
export function statusDoErro(e: unknown): number | null {
  if (typeof e === "object" && e !== null && "codigo" in e) return 403;
  const m = e instanceof Error ? /^(\d{3}):/.exec(e.message) : null;
  return m ? Number(m[1]) : null;
}

type TomAviso = "warn" | "crit" | "info" | "neutro";
const ICONE_AVISO = { warn: TriangleAlert, crit: CircleAlert, info: Info, neutro: Info } as const;

/**
 * Aviso em faixa: a cor diz o estado e o ícone repete a informação (não só cor).
 * É uma nota (role="note") e não uma região: região é marco de navegação, e uma
 * tela com cinco avisos virava cinco marcos. Falha de carga usa `papel="alert"`.
 */
export function Aviso({
  tom,
  titulo,
  papel = "note",
  children,
}: {
  tom: TomAviso;
  titulo: string;
  papel?: "note" | "alert";
  children?: ReactNode;
}) {
  const Icone = ICONE_AVISO[tom];
  return (
    <div className={`ia-aviso ia-aviso--${tom}`} role={papel} aria-label={titulo}>
      <Icone size={18} aria-hidden={true} />
      <div className="ia-aviso__corpo">
        <strong>{titulo}</strong>
        {children}
      </div>
    </div>
  );
}

/** Mensagem de falha de carga, com o motivo e o que acontece a seguir. */
export function FalhaCarga({ erro, onTentar }: { erro: string; onTentar: () => void }) {
  return (
    <Aviso tom="crit" titulo="A consulta falhou" papel="alert">
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

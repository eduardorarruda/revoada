// JanelaTempo — seletor de janela de tempo ("últimos 15 min", "última 1 h"…).
//
// Hoje cada tela antiga tem a sua constante RANGES; este componente nasceu para as
// telas de Agentes de IA e é o candidato a substituí-las aos poucos, sem pressa.
// A janela é sempre RELATIVA a agora: o intervalo absoluto só é calculado na hora
// da consulta (intervaloDaJanela), para que "Atualizar" traga o minuto novo.
import { Button } from "./index";

export interface OpcaoJanela {
  id: string;
  /** Rótulo curto do botão. */
  rotulo: string;
  /** Como a janela entra numa frase ("Última 1 h: US$ 3,20…"). */
  frase: string;
  segundos: number;
}

export const JANELAS: readonly OpcaoJanela[] = [
  { id: "15m", rotulo: "15 min", frase: "Últimos 15 min", segundos: 15 * 60 },
  { id: "1h", rotulo: "1 h", frase: "Última 1 h", segundos: 60 * 60 },
  { id: "6h", rotulo: "6 h", frase: "Últimas 6 h", segundos: 6 * 60 * 60 },
  { id: "24h", rotulo: "24 h", frase: "Últimas 24 h", segundos: 24 * 60 * 60 },
  { id: "7d", rotulo: "7 dias", frase: "Últimos 7 dias", segundos: 7 * 24 * 60 * 60 },
  { id: "30d", rotulo: "30 dias", frase: "Últimos 30 dias", segundos: 30 * 24 * 60 * 60 },
];

/** Mesma janela padrão das rotas que não recebem `from`/`to`: a última 1 h. */
export const JANELA_PADRAO = "1h";

/** Acha a janela pelo id; id desconhecido (URL editada à mão) cai na padrão. */
export function acharJanela(id: string | null | undefined, opcoes: readonly OpcaoJanela[] = JANELAS): OpcaoJanela {
  return opcoes.find((j) => j.id === id) ?? opcoes.find((j) => j.id === JANELA_PADRAO) ?? opcoes[0];
}

/** Intervalo absoluto (RFC 3339) da janela, terminando em `agoraMs`. */
export function intervaloDaJanela(j: OpcaoJanela, agoraMs: number = Date.now()): { from: string; to: string } {
  return {
    from: new Date(agoraMs - j.segundos * 1000).toISOString(),
    to: new Date(agoraMs).toISOString(),
  };
}

export function JanelaTempo({
  valor,
  onChange,
  opcoes = JANELAS,
  rotulo = "Janela de tempo",
}: {
  valor: string;
  onChange: (id: string) => void;
  opcoes?: readonly OpcaoJanela[];
  rotulo?: string;
}) {
  return (
    <div className="row" role="group" aria-label={rotulo} style={{ gap: "var(--sp-1)" }}>
      {opcoes.map((j) => {
        const ativa = j.id === valor;
        return (
          <Button
            key={j.id}
            type="button"
            variant={ativa ? "primary" : "ghost"}
            aria-pressed={ativa}
            title={j.frase}
            onClick={() => onChange(j.id)}
          >
            {j.rotulo}
          </Button>
        );
      })}
    </div>
  );
}

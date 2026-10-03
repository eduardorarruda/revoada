// Tipos compartilhados dos painéis. A forma casa com a resposta da Query API.
import type { Unit } from "../metrics/dict";

export type PanelState = "ok" | "loading" | "empty" | "error";

export interface SeriesData {
  label: string;
  values: (number | null)[];
}

export interface TimeSeriesData {
  ts: number[]; // epoch segundos
  series: SeriesData[];
  // Agregação que o servidor realmente calculou, quando diferiu da pedida (ver
  // QueryResponse.agg_efetivo). Vai para o rodapé do painel.
  agg_efetivo?: string;
}

export interface PanelBaseProps {
  title: string;
  description?: string; // "o que é isto? qual o normal?" — explicativo por padrão (Revoada)
  state?: PanelState;
  error?: string;
  freshness?: { status: "live" | "stale" | "reconnecting"; ageSeconds?: number };
  // Unidade da métrica (do dicionário metrics/dict.ts). Alimenta eixo/legenda/valor
  // com o formatador único. Ausente = "none" (número puro).
  unit?: Unit;
  // Host ao qual este painel pertence. Quando presente, o clique de correlação
  // (P5.4) escopa logs/traces a esta máquina (labels['host']). Ausente = correlação
  // global no instante (painéis multi-host de dashboards custom).
  correlateHost?: string;
  // Nota discreta no rodapé: agregação aplicada e resolução efetiva da consulta
  // ("Máximo a cada 5 min · fonte: resumo de 1 min — picos… suavizados"). Sem ela o
  // operador lê um pico como valor típico e não percebe que a janela longa suavizou
  // o incidente. Ver metrics/resolution.ts.
  note?: string;
}

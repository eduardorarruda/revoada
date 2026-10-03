// Detecção de alerta NOVO na TV (o "burst"), extraída do TVView sem mudar o
// comportamento: quando um alerta inédito dispara (qualquer severidade), os de
// atenção/crítico entram numa fila e aparecem um por vez, 30 s cada. "Novo" = chave
// id+since inédita; a chave inclui `since` para que um alerta que resolve e dispara
// de novo (com `since` novo) conte como novo. Na primeira carga apenas SEMEIA os
// vistos (não despeja os alertas já ativos no boot).
//
// Opções para telas que coreografam o aviso: `onNovos` recebe os alertas novos de
// cada leitura e `atrasoMs` segura o aviso por um tempo. Sem opções, o comportamento
// é o padrão das TVs.
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { TVAlert, TVStatus } from "../../api";
import { sevRank } from "../../format";

// Teto do conjunto de alertas "já vistos". `alertKey` inclui `since`, então um alerta
// que oscila cria uma chave NOVA a cada disparo — e o conjunto nunca era podado. Numa
// parede que fica 12 h ou mais ligada, isso cresce para sempre. 500 chaves cobrem com
// folga qualquer rajada real; o Map preserva a ordem de inserção, então podar é
// simplesmente descartar as mais antigas.
export const MAX_VISTOS = 500;

// Teto da fila de bursts. `showNext` drena UM item a cada 30 s: acima de 2 alertas
// burstáveis por minuto a fila crescia sem limite e a TV passava a exibir, em tela
// cheia, alerta de horas atrás como se fosse agora. Com teto, o excedente de MENOR
// severidade é descartado e a tela informa "+N" — nada some em silêncio.
export const MAX_FILA_BURST = 5;

const BURST_MS = 30000;

// Ordem de exibição do burst: mais severo primeiro; empate, o mais recente.
function ordemBurst(a: TVAlert, b: TVAlert): number {
  return sevRank(b.severity) - sevRank(a.severity) || b.since.localeCompare(a.since);
}

// alertKey: chave de "visto" = id (fingerprint) + since. Inclui `since` para que
// um alerta que resolve e dispara de novo (since novo) conte como NOVO.
export function alertKey(a: TVAlert): string {
  return `${a.id}|${a.since}`;
}

// marcarVisto insere/renova a chave e poda as mais antigas acima do teto.
function marcarVisto(vistos: Map<string, true>, key: string): void {
  vistos.delete(key); // reinserir move para o fim: a ordem do Map vira recência
  vistos.set(key, true);
  while (vistos.size > MAX_VISTOS) {
    const maisAntiga = vistos.keys().next().value;
    if (maisAntiga === undefined) break;
    vistos.delete(maisAntiga);
  }
}

export interface OpcoesAlertasNovos {
  /** false desliga a detecção (outra parte da tela cuida dela). Padrão: true. */
  ativo?: boolean;
  /** Espera antes de mostrar o aviso de um lote novo. Padrão: 0 (imediato). */
  atrasoMs?: number;
  /** Recebe os alertas novos de atenção/crítico de cada leitura (após a semeadura). */
  onNovos?: (alertas: TVAlert[]) => void;
}

export function useAlertasNovos(
  status: TVStatus | null,
  { ativo = true, atrasoMs = 0, onNovos }: OpcoesAlertasNovos = {},
): { burst: TVAlert | null; pendentes: number } {
  const [burst, setBurst] = useState<TVAlert | null>(null);
  // Map (não Set) para poder podar pelo mais antigo — ver MAX_VISTOS/marcarVisto.
  const seenRef = useRef<Map<string, true>>(new Map());
  const seededRef = useRef(false);
  const queueRef = useRef<TVAlert[]>([]);
  const burstRef = useRef<TVAlert | null>(null);
  const burstTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const atrasoTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  // Quantos alertas novos ficaram de fora do burst atual (na fila + descartados pelo
  // teto). Vira o "+N" do overlay: a TV nunca engole um alerta sem dizer que existiu.
  const descartadosRef = useRef(0);
  const [pendentes, setPendentes] = useState(0);
  const onNovosRef = useRef(onNovos);
  onNovosRef.current = onNovos;

  // showNext: se nada está em exibição, puxa o próximo da fila e o mantém 30s;
  // ao expirar, some e chama a si mesmo para o próximo (múltiplos novos = fila,
  // um de cada vez, do mais severo/recente para o menos — ver ordenação abaixo).
  const showNext = useCallback(() => {
    if (burstRef.current) return;
    const next = queueRef.current.shift();
    if (!next) {
      // Fila drenada: não há mais "+N" a comunicar.
      descartadosRef.current = 0;
      setPendentes(0);
      return;
    }
    burstRef.current = next;
    setBurst(next);
    setPendentes(queueRef.current.length + descartadosRef.current);
    burstTimer.current = setTimeout(() => {
      burstRef.current = null;
      setBurst(null);
      showNext();
    }, BURST_MS);
  }, []);

  // Layout effect (e não effect): quem usa `onNovos` precisa decidir ANTES da pintura
  // se segura a tela de crítico — senão ela piscaria por um quadro antes da onda.
  useLayoutEffect(() => {
    if (!ativo) return;
    const alerts = status?.alerts ?? [];
    const vistos = seenRef.current;
    if (!seededRef.current) {
      if (!status) return; // aguarda a primeira resposta real antes de semear
      for (const a of alerts) marcarVisto(vistos, alertKey(a));
      seededRef.current = true;
      return;
    }
    const fresh = alerts.filter((a) => !vistos.has(alertKey(a)));
    if (fresh.length === 0) return;
    // Marca TODOS como vistos (mesmo os de baixa severidade, para não re-disparar),
    // mas só enfileira burst full-screen para warn/crit (sevRank >= 2). Info não
    // sequestra a tela (UX-02).
    for (const a of fresh) marcarVisto(vistos, alertKey(a));
    const burstable = fresh.filter((a) => sevRank(a.severity) >= 2);
    if (burstable.length === 0) return;
    onNovosRef.current?.(burstable);
    // Teto da fila: reordena tudo o que está pendente pelo mesmo critério (mais
    // severo, mais recente) e corta o excedente — o descarte sempre cai no menos
    // grave, e a contagem sobrevive no "+N" do overlay.
    const fila = [...queueRef.current, ...burstable].sort(ordemBurst);
    if (fila.length > MAX_FILA_BURST) {
      descartadosRef.current += fila.length - MAX_FILA_BURST;
      fila.length = MAX_FILA_BURST;
    }
    queueRef.current = fila;
    if (atrasoMs <= 0) {
      showNext();
      return;
    }
    if (atrasoTimer.current) clearTimeout(atrasoTimer.current);
    atrasoTimer.current = setTimeout(() => {
      atrasoTimer.current = null;
      showNext();
    }, atrasoMs);
  }, [status, showNext, ativo, atrasoMs]);

  useEffect(
    () => () => {
      if (burstTimer.current) clearTimeout(burstTimer.current);
      if (atrasoTimer.current) clearTimeout(atrasoTimer.current);
    },
    [],
  );

  return { burst, pendentes };
}

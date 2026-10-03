// useHostThresholds carrega os limiares de saúde configurados (/api/host-thresholds)
// UMA vez por sessão (cache em módulo, compartilhado entre telas) e os entrega aos
// painéis para que a cor do gauge e a do Mural venham do MESMO número.
//
// Tolerância deliberada: hoje o GET é restrito a admin no servidor. Para quem não é
// admin a chamada falha (403) e devolvemos lista vazia — os painéis então usam os
// DEFAULTS embutidos em metrics/thresholds.ts, que são cópia fiel de
// `health.Defaults` do servidor. Ou seja: sem permissão a cor continua correta para
// quem não mexeu nos limiares, e passa a ficar correta para todos assim que o GET
// for liberado a usuário autenticado. Nada quebra em nenhum dos dois cenários.
import { useEffect, useState } from "react";
import { listHostThresholds, type HostThreshold } from "../api";

let cache: HostThreshold[] | null = null;
let inflight: Promise<HostThreshold[]> | null = null;

async function load(force = false): Promise<HostThreshold[]> {
  if (cache && !force) return cache;
  if (!inflight || force) {
    inflight = listHostThresholds()
      .then((r) => {
        cache = r.thresholds ?? [];
        return cache;
      })
      .catch(() => {
        // Sem permissão ou servidor fora: memoriza vazio para não repetir a chamada
        // a cada painel montado. Os defaults embutidos assumem.
        cache = cache ?? [];
        return cache;
      })
      .finally(() => {
        inflight = null;
      });
  }
  return inflight;
}

/** Força o próximo uso a recarregar (chamado após salvar os limiares no modal). */
export function invalidateHostThresholds(): void {
  cache = null;
}

/**
 * Devolve os limiares configurados. Enquanto carrega devolve `null`, e é isso que
 * `resolveThreshold` interpreta como "use o default embutido" — nunca um 70/85
 * inventado.
 */
export function useHostThresholds(): HostThreshold[] | null {
  const [list, setList] = useState<HostThreshold[] | null>(cache);

  useEffect(() => {
    let alive = true;
    load().then((l) => {
      if (alive) setList(l);
    });
    return () => {
      alive = false;
    };
  }, []);

  return list;
}

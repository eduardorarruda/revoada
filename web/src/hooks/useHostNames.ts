// useHostNames resolve o hostname técnico (chave das métricas, ex.: "srv-03")
// para o nome amigável definido pelo usuário (display_name), com fallback para o
// próprio hostname. Carrega o inventário /api/hosts uma vez (cache em módulo,
// compartilhado entre telas) e reexpõe `hostLabel(hostname)`.
import { useCallback, useEffect, useState } from "react";
import { listHosts, type HostDetail } from "../api";

let cache: Record<string, string> | null = null;
let inflight: Promise<Record<string, string>> | null = null;

function buildMap(hosts: HostDetail[]): Record<string, string> {
  const m: Record<string, string> = {};
  for (const h of hosts) {
    if (h.display_name && h.display_name.trim() !== "") m[h.hostname] = h.display_name;
  }
  return m;
}

async function load(force = false): Promise<Record<string, string>> {
  if (cache && !force) return cache;
  if (!inflight || force) {
    inflight = listHosts()
      .then((r) => {
        cache = buildMap(r.hosts ?? []);
        return cache;
      })
      .catch(() => {
        cache = cache ?? {};
        return cache;
      })
      .finally(() => {
        inflight = null;
      });
  }
  return inflight;
}

// invalidateHostNames força o próximo uso a recarregar (ex.: após renomear um host).
export function invalidateHostNames(): void {
  cache = null;
}

export interface HostNames {
  hostLabel: (hostname: string) => string;
  names: Record<string, string>;
  loading: boolean;
  refresh: () => void;
}

export function useHostNames(): HostNames {
  const [names, setNames] = useState<Record<string, string>>(cache ?? {});
  const [loading, setLoading] = useState<boolean>(cache === null);

  useEffect(() => {
    let alive = true;
    setLoading(cache === null);
    load().then((m) => {
      if (alive) {
        setNames(m);
        setLoading(false);
      }
    });
    return () => {
      alive = false;
    };
  }, []);

  // hostLabel PRECISA ser referencialmente estável: vários consumidores o colocam em
  // arrays de dependência de useEffect/usePolling (ex.: PanelLoader do DashboardView).
  // Sem useCallback, uma nova função a cada render re-disparava o efeito, que fazia
  // setData e re-renderizava — loop infinito de /api/query. Só muda quando `names` muda.
  const hostLabel = useCallback(
    (hostname: string): string => names[hostname] || hostname,
    [names],
  );
  const refresh = useCallback(() => {
    load(true).then((m) => setNames({ ...m }));
  }, []);

  return { hostLabel, names, loading, refresh };
}

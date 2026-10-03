// useHostContainers carrega o inventário de containers por host (de /api/host-metrics,
// a mesma fonte do Mural/Infra) uma vez, com cache de módulo compartilhado entre as
// telas, e expõe a lista de hosts e os containers de cada host. Base do filtro
// encadeado servidor→container (regra 5.9), usado em /explore e /logs.
import { useCallback, useEffect, useState } from "react";
import { hostMetrics } from "../api";

type Map = Record<string, string[]>; // host → nomes de containers (ordenados)

let cache: Map | null = null;
let inflight: Promise<Map> | null = null;

function build(hosts: { host: string; containers?: { name: string }[] }[]): Map {
  const m: Map = {};
  for (const h of hosts) {
    m[h.host] = (h.containers ?? []).map((c) => c.name).sort((a, b) => a.localeCompare(b, "pt-BR"));
  }
  return m;
}

async function load(force = false): Promise<Map> {
  if (cache && !force) return cache;
  if (!inflight || force) {
    inflight = hostMetrics()
      .then((r) => {
        cache = build(r.hosts ?? []);
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

export function useHostContainers(): {
  hosts: string[];
  containersOf: (host: string) => string[];
  loading: boolean;
  refresh: () => void;
} {
  const [map, setMap] = useState<Map>(cache ?? {});
  const [loading, setLoading] = useState<boolean>(cache === null);

  useEffect(() => {
    let alive = true;
    setLoading(cache === null);
    load().then((m) => {
      if (alive) {
        setMap(m);
        setLoading(false);
      }
    });
    return () => {
      alive = false;
    };
  }, []);

  const containersOf = useCallback((host: string): string[] => (host ? map[host] ?? [] : []), [map]);
  const refresh = useCallback(() => {
    load(true).then((m) => setMap({ ...m }));
  }, []);

  return { hosts: Object.keys(map).sort((a, b) => a.localeCompare(b, "pt-BR")), containersOf, loading, refresh };
}

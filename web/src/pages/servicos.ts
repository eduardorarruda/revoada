// Serviços descobertos pelo agente — do dado cru ao texto que a pessoa lê.
//
// O agente manda só `kind` + `detail` ("php-fpm" + "8.1, 8.2, 8.3, 8.4";
// "apache" + "httpd"; "docker" + "api,worker"). Este módulo é a única tradução
// disso para nome, explicação e categoria; a tela (ServicosDescobertos.tsx) só
// desenha o que sai daqui. Puro, sem React, para ser testado à parte.
import type { HostService } from "../api";

export type CategoriaServico = "web" | "app" | "banco" | "cache" | "containers" | "outro";

export interface ServicoDescrito {
  kind: string;
  /** Nome como a pessoa conhece: "Apache", "PHP-FPM", "MariaDB". */
  nome: string;
  /** Complemento curto: "PHP 8.1 e 8.2", "processo httpd", "19 containers". Vazio quando não acrescenta nada. */
  detalhe: string;
  /** Uma frase explicando o que o serviço faz (princípio "explicativo por padrão"). */
  descricao: string;
  categoria: CategoriaServico;
  /** Só no Docker: os nomes dos containers, na ordem em que o agente os viu. */
  itens?: string[];
}

export interface GrupoServicos {
  hostname: string;
  /** Data/hora da descoberta mais recente entre os serviços do servidor (ISO). */
  vistoEm: string;
  servicos: ServicoDescrito[];
}

// Ordem de leitura dentro de um servidor: quem recebe a visita primeiro (web),
// depois quem executa a aplicação, depois o que guarda os dados.
const ORDEM_CATEGORIA: Record<CategoriaServico, number> = {
  web: 0,
  app: 1,
  banco: 2,
  cache: 3,
  containers: 4,
  outro: 5,
};

const CATALOGO: Record<string, { nome: string; categoria: CategoriaServico; descricao: string }> = {
  apache: {
    nome: "Apache",
    categoria: "web",
    descricao: "Servidor web: recebe as visitas do site e entrega as páginas. No cPanel o processo se chama httpd.",
  },
  nginx: {
    nome: "Nginx",
    categoria: "web",
    descricao: "Servidor web e proxy reverso: recebe as visitas e as repassa para a aplicação por trás dele.",
  },
  "php-fpm": {
    nome: "PHP-FPM",
    categoria: "app",
    descricao:
      "Executa o código PHP das aplicações (sistemas, site) a pedido do servidor web. Cada versão de PHP roda num processo próprio.",
  },
  mysql: {
    nome: "MySQL",
    categoria: "banco",
    descricao: "Banco de dados relacional onde as aplicações guardam seus registros.",
  },
  postgres: {
    nome: "PostgreSQL",
    categoria: "banco",
    descricao: "Banco de dados relacional onde as aplicações guardam seus registros.",
  },
  mongodb: {
    nome: "MongoDB",
    categoria: "banco",
    descricao: "Banco de dados de documentos, usado por aplicações que guardam registros sem esquema fixo.",
  },
  clickhouse: {
    nome: "ClickHouse",
    categoria: "banco",
    descricao:
      "Banco de dados analítico, feito para consultas rápidas sobre grandes volumes (é o que guarda as métricas deste painel).",
  },
  redis: {
    nome: "Redis",
    categoria: "cache",
    descricao: "Cache e fila em memória: acelera as aplicações guardando dados de acesso rápido.",
  },
  docker: {
    nome: "Docker",
    categoria: "containers",
    descricao: "Containers em execução neste servidor. Cada um tem métricas próprias na aba Containers do servidor.",
  },
};

const DESCRICAO_DESCONHECIDO =
  "Serviço detectado pelo agente pelo nome do processo. O painel ainda não tem uma descrição para ele.";

/** "8.4, 8.1,8.2" → ["8.1", "8.2", "8.4"]; texto sem versão ("php-fpm") → []. */
export function listarVersoesPHP(detail: string): string[] {
  const vistas = new Set<string>();
  for (const parte of detail.split(",")) {
    const v = parte.trim();
    if (/^\d+\.\d+$/.test(v)) vistas.add(v);
  }
  const numero = (v: string) => v.split(".").map((n) => Number(n));
  return [...vistas].sort((a, b) => {
    const [ma, na] = numero(a);
    const [mb, nb] = numero(b);
    return ma - mb || na - nb;
  });
}

/** ["8.1", "8.2", "8.3"] → "8.1, 8.2 e 8.3". */
export function juntarPtBR(itens: string[]): string {
  if (itens.length <= 1) return itens[0] ?? "";
  return `${itens.slice(0, -1).join(", ")} e ${itens[itens.length - 1]}`;
}

function detalheDeProcesso(detail: string, kind: string): string {
  const d = detail.trim();
  return d && d !== kind ? `processo ${d}` : "";
}

export function descreverServico(s: Pick<HostService, "kind" | "detail">): ServicoDescrito {
  const kind = s.kind.toLowerCase();
  const detail = s.detail ?? "";
  const base = CATALOGO[kind];
  if (!base) {
    return { kind, nome: s.kind, detalhe: detail.trim(), descricao: DESCRICAO_DESCONHECIDO, categoria: "outro" };
  }
  const comum = { kind, nome: base.nome, descricao: base.descricao, categoria: base.categoria };

  switch (kind) {
    case "php-fpm": {
      const versoes = listarVersoesPHP(detail);
      return { ...comum, detalhe: versoes.length ? `PHP ${juntarPtBR(versoes)}` : "versão não identificada" };
    }
    case "mysql": {
      // O agente manda o nome do processo; "mariadbd" é MariaDB, não MySQL.
      const nome = detail.trim().toLowerCase() === "mariadbd" ? "MariaDB" : "MySQL";
      return { ...comum, nome, detalhe: detalheDeProcesso(detail, kind) };
    }
    case "docker": {
      const itens = detail
        .split(",")
        .map((c) => c.trim())
        .filter(Boolean);
      const n = itens.length;
      const detalhe = n === 0 ? "nenhum container" : n === 1 ? "1 container" : `${n} containers`;
      return { ...comum, detalhe, itens };
    }
    default:
      return { ...comum, detalhe: detalheDeProcesso(detail, kind) };
  }
}

function maisRecente(a: string, b: string): string {
  const ta = Date.parse(a);
  const tb = Date.parse(b);
  if (!Number.isFinite(ta)) return b;
  if (!Number.isFinite(tb)) return a;
  return tb > ta ? b : a;
}

export function agruparServicosPorHost(services: HostService[]): GrupoServicos[] {
  const porHost = new Map<string, GrupoServicos>();
  for (const s of services) {
    const grupo = porHost.get(s.hostname) ?? { hostname: s.hostname, vistoEm: s.discovered_at, servicos: [] };
    grupo.vistoEm = maisRecente(grupo.vistoEm, s.discovered_at);
    grupo.servicos.push(descreverServico(s));
    porHost.set(s.hostname, grupo);
  }
  const grupos = [...porHost.values()].sort((a, b) => a.hostname.localeCompare(b.hostname, "pt-BR"));
  for (const g of grupos) {
    g.servicos.sort(
      (a, b) => ORDEM_CATEGORIA[a.categoria] - ORDEM_CATEGORIA[b.categoria] || a.nome.localeCompare(b.nome, "pt-BR"),
    );
  }
  return grupos;
}

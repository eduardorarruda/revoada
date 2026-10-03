// Serviços descobertos — a tela do que o agente encontrou rodando em cada servidor.
//
// Um servidor cPanel sem Docker aparecia como "1 serviço" (o banco) e parecia vazio,
// com as aplicações e o site rodando nele em Apache + PHP-FPM. Aqui cada serviço vira um
// chip com nome, complemento ("PHP 8.1, 8.2 e 8.4") e uma explicação de um toque,
// agrupado por servidor. A tradução do dado cru está em servicos.ts; este arquivo só
// desenha.
import { useEffect, useState, type CSSProperties } from "react";
import { Boxes, Braces, ChevronDown, ChevronRight, Database, Globe, Puzzle, Search, Zap } from "lucide-react";
import { Card, EmptyState, InfoTip, Skeleton } from "../components";
import { listDiscovery, type HostService } from "../api";
import { fmtRelAbs } from "../format";
import { useHostNames } from "../hooks/useHostNames";
import { agruparServicosPorHost, type CategoriaServico, type ServicoDescrito } from "./servicos";

const ICONE: Record<CategoriaServico, typeof Globe> = {
  web: Globe,
  app: Braces,
  banco: Database,
  cache: Zap,
  containers: Boxes,
  outro: Puzzle,
};

const FONTE_LABEL: Record<string, string> = {
  process: "encontrado pelo processo em execução",
  docker: "encontrado pelo Docker",
  port: "encontrado pela porta aberta",
};

/** Id de DOM estável a partir de um rótulo: "Serviços em WHM GCloud" → "servicos-em-whm-gcloud". */
function slug(texto: string): string {
  return texto
    .normalize("NFD")
    .replace(/\p{Diacritic}/gu, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

function resumoContagem(servidores: number, servicos: number): string {
  const s = servidores === 1 ? "1 servidor" : `${servidores} servidores`;
  const v = servicos === 1 ? "1 serviço" : `${servicos} serviços`;
  return `${s} · ${v}`;
}

/** Um chip por serviço. O Docker abre a lista de containers ao toque. */
function ServicoChip({ servico, indice, idBase }: { servico: ServicoDescrito; indice: number; idBase: string }) {
  const [aberto, setAberto] = useState(false);
  const Icone = ICONE[servico.categoria];
  const temItens = (servico.itens?.length ?? 0) > 0;
  // O id leva a lista de origem (o servidor): na aba há um chip Docker por
  // servidor, e dois "svc-itens-docker-4" na mesma página quebrariam o aria-controls.
  const idLista = `${idBase}-${servico.kind}`;

  return (
    <li className="svc" style={{ "--i": indice } as CSSProperties}>
      <span className="svc__icone" aria-hidden="true">
        <Icone size={16} strokeWidth={1.75} />
      </span>
      <span className="svc__texto">
        <span className="svc__nome">{servico.nome}</span>
        {servico.detalhe && <span className="svc__detalhe">{servico.detalhe}</span>}
      </span>
      <InfoTip title={servico.nome} text={servico.descricao} />
      {temItens && (
        <button
          type="button"
          className="svc__abrir"
          aria-expanded={aberto}
          aria-controls={idLista}
          aria-label={aberto ? "Ocultar containers" : "Ver containers"}
          onClick={() => setAberto((a) => !a)}
        >
          {aberto ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
        </button>
      )}
      {/* Sempre montada (escondida com `hidden`): o aria-controls do botão precisa
          apontar para um id que exista antes do primeiro clique. */}
      {temItens && (
        <ul id={idLista} className="svc__itens" aria-label="Containers" hidden={!aberto}>
          {servico.itens?.map((c) => (
            <li key={c} className="svc__item">
              {c}
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}

/** Lista de chips. Reutilizada na aba, na linha expandida da tabela e no detalhe do servidor. */
export function ServicosChips({
  servicos,
  rotulo = "Serviços descobertos",
}: {
  servicos: ServicoDescrito[];
  rotulo?: string;
}) {
  if (servicos.length === 0) return null;
  const idBase = `svc-${slug(rotulo)}`;
  return (
    <ul className="svc-lista" aria-label={rotulo}>
      {/* `kind` é único por servidor: o agente deduplica por tipo (discover.go,
          classificar) e o gateway substitui a lista inteira do host a cada envio
          (SaveDiscovery), sem acumular. Se um dia dois detectores reportarem o mesmo
          tipo, é lá que se resolve, não aqui. */}
      {servicos.map((s, i) => (
        <ServicoChip key={s.kind} servico={s} indice={i} idBase={idBase} />
      ))}
    </ul>
  );
}

/** Converte a lista crua de UM servidor nos chips já ordenados. */
export function servicosDoHost(services: HostService[], hostname: string): ServicoDescrito[] {
  const grupo = agruparServicosPorHost(services.filter((s) => s.hostname === hostname));
  return grupo[0]?.servicos ?? [];
}

/** Um bloco por servidor: nome amigável, hostname técnico, quando foi visto, chips. */
export function ServicosPorServidor({
  services,
  hostLabel,
}: {
  services: HostService[];
  hostLabel: (hostname: string) => string;
}) {
  const grupos = agruparServicosPorHost(services);
  if (grupos.length === 0) {
    return (
      <EmptyState
        icon={<Search size={32} strokeWidth={1.5} />}
        title="Nenhum serviço descoberto ainda"
        body="O agente procura, a cada 5 minutos, servidores web (Apache, Nginx), PHP-FPM, bancos (MySQL/MariaDB, PostgreSQL, MongoDB, ClickHouse), Redis e containers Docker. O que ele encontrar aparece aqui sem configuração."
      />
    );
  }
  const total = grupos.reduce((n, g) => n + g.servicos.length, 0);
  const fonteDe = (hostname: string) =>
    [...new Set(services.filter((s) => s.hostname === hostname).map((s) => FONTE_LABEL[s.source] ?? s.source))]
      .filter(Boolean)
      .join("; ");

  return (
    <div className="stack">
      <p className="svc-resumo">
        {resumoContagem(grupos.length, total)}
        <span className="svc-resumo__nota"> · atualizado pelo agente a cada 5 minutos</span>
      </p>
      <div className="svc-servidores">
        {grupos.map((g) => {
          const rotulo = hostLabel(g.hostname);
          const visto = fmtRelAbs(g.vistoEm);
          const fonte = fonteDe(g.hostname);
          return (
            <section key={g.hostname} className="svc-servidor" aria-labelledby={`svc-h-${g.hostname}`}>
              <header className="svc-servidor__topo">
                <div className="svc-servidor__nome">
                  {/* Título de verdade (h3): quem navega por cabeçalhos encontra cada servidor. */}
                  <h3 className="svc-servidor__titulo">
                    <a
                      id={`svc-h-${g.hostname}`}
                      href={`#/hosts/${encodeURIComponent(g.hostname)}`}
                      className="svc-servidor__link"
                    >
                      {rotulo}
                    </a>
                  </h3>
                  {rotulo !== g.hostname && <span className="svc-servidor__hostname">{g.hostname}</span>}
                </div>
                <span className="svc-servidor__visto" title={fonte ? `${visto.abs} · ${fonte}` : visto.abs}>
                  visto {visto.rel}
                </span>
              </header>
              <ServicosChips servicos={g.servicos} rotulo={`Serviços em ${rotulo}`} />
            </section>
          );
        })}
      </div>
    </div>
  );
}

/** Aba "Serviços descobertos" da Infraestrutura: busca uma vez e desenha por servidor. */
export function DiscoveryTab() {
  const [services, setServices] = useState<HostService[] | null>(null);
  const { hostLabel } = useHostNames();

  useEffect(() => {
    let vivo = true;
    listDiscovery()
      .then((r) => vivo && setServices(r.services ?? []))
      .catch(() => vivo && setServices([]));
    return () => {
      vivo = false;
    };
  }, []);

  if (services === null) {
    return (
      <Card>
        <Skeleton height={24} />
        <div style={{ marginTop: "var(--sp-3)" }}>
          <Skeleton height={40} />
        </div>
      </Card>
    );
  }
  return <ServicosPorServidor services={services} hostLabel={hostLabel} />;
}

/** Cartão "Serviços neste servidor" do detalhe do host. */
export function ServicosDoServidor({ hostname }: { hostname: string }) {
  const [servicos, setServicos] = useState<ServicoDescrito[] | null>(null);

  useEffect(() => {
    let vivo = true;
    setServicos(null);
    listDiscovery()
      .then((r) => vivo && setServicos(servicosDoHost(r.services ?? [], hostname)))
      .catch(() => vivo && setServicos([]));
    return () => {
      vivo = false;
    };
  }, [hostname]);

  return (
    <Card title="Serviços neste servidor">
      {servicos === null && <Skeleton height={28} />}
      {servicos !== null && servicos.length === 0 && (
        <p className="svc-vazio">
          Nenhum serviço conhecido detectado. O agente procura servidores web, PHP-FPM, bancos, Redis e Docker a cada 5
          minutos.
        </p>
      )}
      {servicos !== null && servicos.length > 0 && (
        <ServicosChips servicos={servicos} rotulo="Serviços neste servidor" />
      )}
    </Card>
  );
}

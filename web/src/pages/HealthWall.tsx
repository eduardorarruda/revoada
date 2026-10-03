// Health Wall (P3.2): semáforo do departamento — cartões grandes, problemas primeiro,
// legível a 5-10m. Estado = cor + ícone + posição (não só cor). Relógio + frescor sempre visíveis.
// Fase A: cada recurso (CPU/RAM/Disco) mostra valor legível + semáforo próprio, e o
// estado do card é o pior entre os recursos e os alertas ativos.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Server } from "lucide-react";
import { getRole, healthWall, tvWall, type HealthCard, type HealthMetric } from "../api";
import { usePolling } from "../hooks/usePolling";
import { useHostNames } from "../hooks/useHostNames";
import { EmptyState } from "../components/EmptyState";
import { Button } from "../components";
import { APP_TIME_ZONE_LABEL, fmtRelAbs, formatBytes, formatTime, formatValue } from "../format";
import { healthFreshness } from "../metrics/lastPoint";
import { HealthThresholdsModal } from "./HealthThresholdsModal";
import { ContainersBlock } from "./containers";
import { estadoDoMural } from "./tv/estado";
import { PontosTV, TopoTV } from "./tv/pecas";

export const STATE: Record<HealthCard["state"], { color: string; icon: string; label: string }> = {
  crit: { color: "--crit", icon: "■", label: "CRÍTICO" },
  warn: { color: "--warn", icon: "▲", label: "ATENÇÃO" },
  ok: { color: "--ok", icon: "●", label: "OK" },
  nosignal: { color: "--text-3", icon: "○", label: "SEM SINAL" },
};

// Ordem "pior primeiro" por estado do card: crítico > atenção > sem sinal > ok.
export const STATE_RANK: Record<HealthCard["state"], number> = { crit: 3, warn: 2, nosignal: 1, ok: 0 };

// Cor (token) do semáforo de um recurso individual.
const METRIC_COLOR: Record<HealthMetric["state"], string> = {
  crit: "--crit",
  warn: "--warn",
  ok: "--ok",
  // "nodata" = não foi medida nesta janela. Cinza de "não sei": pintar de verde
  // um recurso que ninguém mediu é a mentira que este estado existe para evitar.
  nodata: "--text-3",
};

// Cadência de busca do mural = cadência da coleta (~15 s). Ver o comentário em `load`.
const POLL_MS = 15000;
// "stale" no relógio do cabeçalho: dois ciclos sem resposta nova. Amarrado ao POLL_MS
// para os dois números não se descolarem — com o limite fixo em 10 s, subir a cadência
// deixaria o cabeçalho permanentemente em "stale" com o mural perfeitamente saudável.
const STALE_S = (POLL_MS / 1000) * 2;

type ResourceKind = "cpu" | "mem" | "disk";
const RESOURCE_LABEL: Record<ResourceKind, string> = { cpu: "CPU", mem: "RAM", disk: "Disco" };

// Texto legível por recurso: CPU sempre em %, RAM/Disco em GB usados/totais quando
// os bytes vieram (senão cai no %). Ex.: "CPU 72% em uso", "RAM 5,2 GB / 8 GB".
// O backend emite 0 (não null) quando não há bytes no snapshot, por isso exigimos
// total_bytes > 0 — senão cairíamos em "RAM 0 B / 0 B".
function resourceText(kind: ResourceKind, m: HealthMetric): string {
  const label = RESOURCE_LABEL[kind];
  if (kind !== "cpu" && m.used_bytes != null && m.total_bytes != null && m.total_bytes > 0) {
    return `${label} ${formatBytes(m.used_bytes)} / ${formatBytes(m.total_bytes)}`;
  }
  // formatValue(..., "percent") em vez de Math.round: 99,5% virava "100%" aqui e
  // continuava "99,5%" na listagem de servidores — a mesma métrica, dois números.
  return `${label} ${formatValue(m.pct, "percent")} em uso`;
}

// `token` presente = modo TV/kiosk: busca a wall autenticada só pelo token (sem login).
// Sem token (uso normal logado), usa o endpoint autenticado por JWT.
export function HealthWall({ tv = false, token, nomeTV }: { tv?: boolean; token?: string; nomeTV?: string }) {
  const [cards, setCards] = useState<HealthCard[]>([]);
  const [age, setAge] = useState(0);
  // Distingue "vazio real" (fetch OK devolveu lista vazia) de "sem conexão"
  // (fetch falhou): sem sinal do servidor NUNCA pode parecer saudável.
  const [erroConexao, setErroConexao] = useState(false);
  const [thresholdsOpen, setThresholdsOpen] = useState(false);
  const lastOk = useRef(Date.now());
  const { hostLabel } = useHostNames();
  // Botão de limiares: só admin e só fora do modo TV (kiosk não edita config).
  const isAdmin = !tv && getRole() === "admin";

  // Poll da malha de saúde na CADÊNCIA DO DADO — pausado quando a aba está em
  // segundo plano.
  //
  // Eram 3 s. O agente coleta a cada ~15 s, então 4 de cada 5 respostas voltavam byte
  // a byte idênticas à anterior (medido: 5 de 9), e cada uma custa uma varredura de
  // dezenas de milhares de linhas no ClickHouse. Pior: esta é a tela do modo TV, que
  // fica aberta 24/7 num kiosk — e kiosk NUNCA fica `hidden`, então a pausa por
  // visibilidade não a protege. Eram ~28.800 requisições/dia por aba, cadência mais
  // agressiva que os 4–8 s que já levaram o WHM a 90% de CPU. Buscar mais rápido do
  // que o dado muda não deixa a tela mais atual, só mais cara.
  const load = useCallback(() => {
    (token ? tvWall(token) : healthWall())
      .then((r) => {
        setCards(r.cards);
        lastOk.current = Date.now();
        setErroConexao(false);
      })
      .catch(() => setErroConexao(true));
  }, [token]);
  usePolling(load, POLL_MS, [load]);

  // Relógio local de frescor (não faz rede): recalcula a idade a cada 1s.
  useEffect(() => {
    const a = setInterval(() => setAge(Math.floor((Date.now() - lastOk.current) / 1000)), 1000);
    return () => clearInterval(a);
  }, []);

  // No modo TV/kiosk o wrap vira coluna flex para a área do grid ocupar o resto da
  // tela (flex:1) e paginar por dentro; no modo logado, layout centrado que rola.
  const wrap: React.CSSProperties | undefined = tv ? undefined : { padding: "var(--sp-5)", maxWidth: 1400, margin: "0 auto" };

  // UX-04: pior primeiro (crit > warn > nosignal > ok) antes de renderizar.
  const ordered = useMemo(
    () => [...cards].sort((a, b) => STATE_RANK[b.state] - STATE_RANK[a.state]),
    [cards],
  );

  // --- Paginação em rodízio (só no modo TV) -------------------------------------
  // Num kiosk não há rolagem: se houver mais hosts do que cabem na tela, os
  // excedentes seriam CORTADOS silenciosamente. Em vez disso, dividimos os cartões
  // em páginas que cabem inteiras (quebra sempre na borda de uma LINHA — nunca no
  // meio de um cartão) e alternamos com fade. Assim todo host aparece, um grupo por
  // vez, sem cortar. Cada página = { top, height }: o grid inteiro é renderizado uma
  // vez (para medir), deslocado por translateY(-top) e RECORTADO à altura da própria
  // página — assim a linha da página seguinte não "espia" no rodapé vazio.
  const areaRef = useRef<HTMLDivElement>(null);
  const gridRef = useRef<HTMLDivElement>(null);
  const [pages, setPages] = useState<{ top: number; height: number }[]>([]);
  const [page, setPage] = useState(0);
  const [fade, setFade] = useState(false);

  useLayoutEffect(() => {
    if (!tv) return;
    const area = areaRef.current;
    const grid = gridRef.current;
    if (!area || !grid) return;
    const compute = () => {
      const cardsEl = Array.from(grid.children) as HTMLElement[];
      if (cardsEl.length === 0) { setPages([]); return; }
      const availH = area.clientHeight;
      // Agrupa cartões em linhas pelo topo (mesma linha do grid = mesmo offsetTop).
      const rows: { top: number; bottom: number }[] = [];
      for (const c of cardsEl) {
        const top = c.offsetTop;
        const bottom = top + c.offsetHeight;
        const r = rows.find((x) => Math.abs(x.top - top) < 4);
        if (r) r.bottom = Math.max(r.bottom, bottom);
        else rows.push({ top, bottom });
      }
      rows.sort((a, b) => a.top - b.top);
      // Empacota linhas em páginas: acumula enquanto a linha inteira couber na altura.
      const result: { top: number; height: number }[] = [];
      let i = 0;
      while (i < rows.length) {
        const start = rows[i].top;
        let j = i;
        let bottom = rows[i].bottom;
        while (j < rows.length && rows[j].bottom - start <= availH) { bottom = rows[j].bottom; j++; }
        if (j === i) { j = i + 1; bottom = rows[i].bottom; } // linha mais alta que a tela: força avançar
        result.push({ top: start, height: bottom - start });
        i = j;
      }
      setPages(result);
    };
    compute();
    // Recalcula ao mudar o tamanho da área (banner de erro, resize da TV) ou o grid
    // (cartões que crescem com bloco de containers).
    const ro = new ResizeObserver(compute);
    ro.observe(area);
    ro.observe(grid);
    return () => ro.disconnect();
  }, [tv, ordered]);

  // Mantém a página dentro do intervalo quando o nº de páginas muda.
  useEffect(() => { if (pages.length > 0 && page >= pages.length) setPage(0); }, [pages, page]);

  // Rodízio: alterna a página a cada 20s com fade, quando há mais de uma.
  useEffect(() => {
    if (!tv || pages.length <= 1) return;
    const t = setInterval(() => {
      setFade(true);
      setTimeout(() => { setPage((p) => (p + 1) % pages.length); setFade(false); }, 400);
    }, 20000);
    return () => clearInterval(t);
  }, [tv, pages.length]);

  const gridCards = ordered.map((c) => <Card key={c.host} card={c} label={hostLabel(c.host)} big={tv} />);
  const cur = pages[Math.min(page, pages.length - 1)];

  return (
    <div className={tv ? "tv" : undefined} style={wrap}>
      {tv ? (
        <TopoTV
          nome={nomeTV || "Mural de Saúde"}
          sub={
            <>
              <span>
                Mural de saúde · {ordered.length} {ordered.length === 1 ? "servidor" : "servidores"}
              </span>
              <PontosTV total={pages.length} atual={Math.min(page, Math.max(pages.length - 1, 0))} />
            </>
          }
          estado={estadoDoMural(ordered)}
          atrasado={erroConexao ? "sem conexão, dados podem estar desatualizados" : age > STALE_S ? `sem atualização há ${age} s` : null}
          progresso={pages.length > 1 ? 20 : undefined}
          chaveProgresso={page}
        />
      ) : (
      <header style={{ display: "flex", justifyContent: "space-between", alignItems: "baseline", marginBottom: "var(--sp-5)", gap: "var(--sp-3)", flexWrap: "wrap" }}>
          <h1 style={{ fontSize: tv ? "var(--fs-40)" : "var(--fs-28)", margin: 0 }}>
            Mural de Saúde
            {tv && pages.length > 1 && (
              <span style={{ fontSize: "var(--fs-20)", color: "var(--text-3)", marginLeft: "var(--sp-3)" }}>
                página {Math.min(page, pages.length - 1) + 1}/{pages.length} · {ordered.length} {ordered.length === 1 ? "host" : "hosts"}
              </span>
            )}
          </h1>
          <div style={{ display: "flex", alignItems: "baseline", gap: "var(--sp-3)" }}>
            {isAdmin && (
              <Button onClick={() => setThresholdsOpen(true)}>Limiares</Button>
            )}
            <span className="tabular" style={{ color: erroConexao || age > STALE_S ? "var(--warn)" : "var(--text-2)", fontSize: "var(--fs-20)" }}>
              {formatTime(Date.now())} <span style={{ color: "var(--text-3)" }} title={APP_TIME_ZONE_LABEL}>BRT</span> · {erroConexao ? "sem conexão" : age > STALE_S ? `stale ${age}s` : "ao vivo"}
            </span>
          </div>
        </header>
      )}
      {/* UX-03: falha de rede é AVISO explícito, nunca confundido com "vazio saudável". */}
      {erroConexao && tv && (
        <div role="alert" className="tv-aviso-conexao">
          ▲ Sem conexão com o servidor, dados podem estar desatualizados
        </div>
      )}
      {erroConexao && !tv && (
        <div
          role="alert"
          style={{
            background: "var(--warn-bg)",
            border: "2px solid var(--warn)",
            borderRadius: "var(--radius)",
            padding: "var(--sp-4)",
            marginBottom: "var(--sp-4)",
            color: "var(--warn)",
            fontWeight: 600,
            fontSize: tv ? "var(--fs-28)" : "var(--fs-20)",
          }}
        >
          ▲ Sem conexão com o servidor, dados podem estar desatualizados
        </div>
      )}
      {tv ? (
        // Kiosk: `area` (flex:1) mede o espaço disponível; a `janela` recorta à altura
        // EXATA da página atual (não à da tela), e o grid desliza por translateY(-top).
        // Recortar pela altura da página evita que a linha da página seguinte espie no
        // rodapé quando a página atual é mais curta que a tela.
        <div ref={areaRef} className="tv-mural">
          <div className="tv-mural__janela" style={{ height: cur ? cur.height : "100%", opacity: fade ? 0 : 1 }}>
            <div ref={gridRef} className="wall-grid wall-grid--tv" style={{ transform: `translateY(${-(cur ? cur.top : 0)}px)` }}>
              {gridCards}
            </div>
          </div>
        </div>
      ) : (
        <div className="wall-grid">{gridCards}</div>
      )}
      {/* Vazio REAL (fetch OK, lista vazia) só aparece quando não há erro de conexão. */}
      {ordered.length === 0 && !erroConexao && (
        <EmptyState
          icon={<Server size={32} strokeWidth={1.5} />}
          title="Nenhum host."
          body="Nenhum host está reportando métricas para o mural no momento."
        />
      )}
      {isAdmin && <HealthThresholdsModal open={thresholdsOpen} onClose={() => setThresholdsOpen(false)} />}
    </div>
  );
}

// Linha de um recurso: rótulo + valor legível + barrinha colorida pelo semáforo do recurso.
function ResourceRow({ kind, metric, big }: { kind: ResourceKind; metric: HealthMetric; big: boolean }) {
  // Frescor POR RECURSO, não por card: um coletor pode calar enquanto os outros
  // seguem, o host continua "no ar" e este ladrilho repetiria o último valor com a
  // cor do limiar — um pico crítico já passado ficaria vermelho na parede por
  // minutos. Mesmo critério dos painéis (~3× o passo real, piso de 2 min).
  const f = healthFreshness(metric);
  const semDado = f.state !== "fresh";
  const color = semDado ? `var(${METRIC_COLOR.nodata})` : `var(${METRIC_COLOR[metric.state]})`;
  const idade = f.state === "stale" && metric.ts != null ? fmtRelAbs(metric.ts * 1000) : null;
  // Barra zerada quando não há medida: uma barra curta e verde seria lida como
  // "recurso folgado".
  const pct = semDado ? 0 : Math.max(0, Math.min(100, metric.pct));
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
      <span
        className="tabular"
        title={
          idade
            ? `Último valor medido ${idade.rel} (${formatValue(metric.pct, "percent")}), ${idade.abs}. Este coletor parou de reportar.`
            : semDado
              ? "Não medido nesta janela"
              : undefined
        }
        style={{
          fontSize: big ? "var(--fs-20)" : "var(--fs-14)",
          color,
          fontWeight: 600,
          lineHeight: 1.2,
        }}
      >
        {idade
          ? `${RESOURCE_LABEL[kind]}, sem dado recente (${idade.rel})`
          : semDado
            ? `${RESOURCE_LABEL[kind]}, não medido`
            : resourceText(kind, metric)}
      </span>
      <span
        aria-hidden
        style={{
          display: "block",
          height: big ? 8 : 6,
          borderRadius: 999,
          background: "var(--bg-3)",
          overflow: "hidden",
        }}
      >
        <span style={{ display: "block", height: "100%", width: `${pct}%`, background: color }} />
      </span>
    </div>
  );
}

function Card({ card, label, big }: { card: HealthCard; label: string; big: boolean }) {
  const s = STATE[card.state];
  const nosignal = card.state === "nosignal";
  return (
    <div className={`saude saude--${card.state}${big ? " saude--tv" : ""}`}>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", gap: "var(--sp-2)" }}>
        <span title={card.host} className="saude__nome">{label}</span>
        <span className="saude__selo">
          <span aria-hidden>{s.icon}</span> {s.label}
        </span>
      </div>

      {nosignal ? (
        // Sem sinal: nada de métricas velhas parecendo atuais — só o estado grande.
        <div className="tabular" style={{ fontSize: big ? "var(--fs-64)" : "var(--fs-40)", fontWeight: 700, color: `var(${s.color})`, lineHeight: 1 }}>
—
        </div>
      ) : (
        <div style={{ display: "flex", flexDirection: "column", gap: big ? "var(--sp-3)" : "var(--sp-2)" }}>
          <ResourceRow kind="cpu" metric={card.cpu} big={big} />
          <ResourceRow kind="mem" metric={card.mem} big={big} />
          <ResourceRow kind="disk" metric={card.disk} big={big} />
          {card.containers && card.containers.length > 0 && (
            <ContainersBlock
              containers={card.containers}
              criticalMetric={card.cpu.state === "crit" ? "cpu" : card.mem.state === "crit" ? "mem" : undefined}
            />
          )}
        </div>
      )}

    </div>
  );
}

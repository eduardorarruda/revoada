// Containers (Fase C) — bloco/linha reutilizável de status de containers Docker.
// Extraído de Hosts.tsx para ser compartilhado entre a Infraestrutura (card do host),
// o Mural de Saúde e a Visão do Host, sem duplicar helpers.
import { useMemo, useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";
import { InfoTip } from "../components";
import { help } from "../help";
import { formatBytes, formatDuration, formatValue } from "../format";
import { containerFreshness } from "../metrics/lastPoint";
import type { ContainerStatus } from "../api";

const MUTED_12 = { fontSize: "var(--fs-12)", color: "var(--text-3)" } as const;

// Parâmetros da lista virtualizada de containers (regra 5.4: servidores com 35+
// containers não podem esticar o card nem gerar scroll horizontal). Altura de linha
// fixa + janela de render + scroll interno próprio com altura máxima.
const ROW_H = 30; // px por linha (compacta, fixa)
const VIEW_H = 300; // altura máxima da área rolável
const OVERSCAN = 4; // linhas extra renderizadas acima/abaixo da janela

// Cor (token de design) do indicador de estado de um container — semântica só
// para estado; "neutral" (parado sem falha, criado…) fica cinza/terciário.
export const CONTAINER_COLOR: Record<ContainerStatus["status"], string> = {
  ok: "--ok",
  warn: "--warn",
  crit: "--crit",
  neutral: "--text-3",
};

// Rótulos amigáveis pt-BR do `state` do Docker (running/exited/…).
export const CONTAINER_STATE_LABEL: Record<string, string> = {
  running: "no ar",
  exited: "parado",
  restarting: "reiniciando",
  paused: "pausado",
  dead: "morto",
  created: "criado",
};

// Rótulos amigáveis pt-BR do `health` (healthcheck do Docker).
// "none" = container sem healthcheck → sem rótulo (string vazia).
export const CONTAINER_HEALTH_LABEL: Record<string, string> = {
  unhealthy: "sem saúde",
  healthy: "saudável",
  starting: "iniciando",
  none: "",
};

// Texto legível do estado do container: state traduzido (+ código de saída só
// quando parou com código ≠ 0) e, se houver healthcheck, o estado de saúde.
// Ex.: "no ar · saudável", "parado (código 1)", "reiniciando · sem saúde".
export function containerStateText(c: ContainerStatus): string {
  const key = c.state?.toLowerCase() ?? "";
  const stateLabel = CONTAINER_STATE_LABEL[key] ?? c.state ?? "—";
  const withCode =
    key === "exited" && c.exit_code != null && c.exit_code !== 0
      ? `${stateLabel} (código ${c.exit_code})`
      : stateLabel;
  const healthLabel = CONTAINER_HEALTH_LABEL[c.health?.toLowerCase() ?? ""] ?? "";
  return healthLabel ? `${withCode} · ${healthLabel}` : withCode;
}

// Resumo compacto de uso só para containers no ar: "cpu 12% · mem 256 MB"
// (mem em GB/MB via formatBytes quando há bytes; senão cai no %). null = nada a mostrar.
//
// Número VELHO não é mostrado como se fosse de agora: passado o frescor, o resumo
// vira "sem dado recente (há N)". Era daqui que saía o 52,4% de CPU de um container
// que rodava a 4% — o valor ficava na tela, verde, até a janela de 5 min do servidor
// expirar e o container sumir da lista inteira.
export function containerUsage(c: ContainerStatus, now?: number): string | null {
  if (!c.running) return null;
  const frescor = containerFreshness(c, now);
  if (frescor.state === "stale") {
    return frescor.ageSeconds != null
      ? `sem dado recente (há ${formatDuration(frescor.ageSeconds)})`
      : "sem dado recente";
  }
  const parts: string[] = [];
  // formatValue mantém 1 casa: "99,5%" não pode virar "100%" só neste resumo.
  if (c.cpu_pct != null) parts.push(`cpu ${formatValue(c.cpu_pct, "percent")}`);
  if (c.mem_used_bytes != null && c.mem_used_bytes > 0) {
    parts.push(`mem ${formatBytes(c.mem_used_bytes)}`);
  } else if (c.mem_pct != null) {
    parts.push(`mem ${formatValue(c.mem_pct, "percent")}`);
  }
  return parts.length > 0 ? parts.join(" · ") : null;
}

// Rank de ordenação: problemas primeiro (crit > warn), depois consumo. Empata pelo
// nome para estabilidade.
const STATUS_RANK: Record<ContainerStatus["status"], number> = { crit: 0, warn: 1, ok: 2, neutral: 3 };

function sortContainers(list: ContainerStatus[]): ContainerStatus[] {
  return [...list].sort((a, b) => {
    const r = STATUS_RANK[a.status] - STATUS_RANK[b.status];
    if (r !== 0) return r;
    const cpu = (b.cpu_pct ?? 0) - (a.cpu_pct ?? 0);
    if (cpu !== 0) return cpu;
    const mem = (b.mem_used_bytes ?? 0) - (a.mem_used_bytes ?? 0);
    if (mem !== 0) return mem;
    return a.name.localeCompare(b.name, "pt-BR");
  });
}

// Seção "Containers" do card: COLAPSADA por padrão, com resumo no cabeçalho (total +
// quantos com problema). Ao abrir, mostra uma lista VIRTUALIZADA (altura de linha
// fixa, janela de render, scroll interno próprio com altura máxima) — servidores com
// dezenas de containers não esticam o card nem geram scroll horizontal. Busca interna
// quando há muitos; ordenação padrão põe os problemáticos primeiro, depois por consumo.
// `criticalMetric` (opcional) destaca os maiores consumidores da métrica crítica (5.5).
export function ContainersBlock({
  containers,
  defaultOpen = false,
  criticalMetric,
  collapsible = true,
}: {
  containers: ContainerStatus[];
  defaultOpen?: boolean;
  criticalMetric?: "cpu" | "mem";
  /**
   * Quando false, o bloco NÃO tem colapso próprio (sem chevron): o cabeçalho vira
   * um rótulo estático e a lista fica sempre visível. Use dentro de um contêiner que
   * já controla a expansão (ex.: a linha expansível da tabela de /hosts) para não ter
   * dois chevrons aninhados. Default true (card do host, onde o colapso é o único controle).
   */
  collapsible?: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const [query, setQuery] = useState("");
  const [scrollTop, setScrollTop] = useState(0);
  // Sem colapso próprio, a lista está sempre aberta (a expansão é do contêiner pai).
  const isOpen = collapsible ? open : true;

  const crit = containers.filter((c) => c.status === "crit").length;
  const warn = containers.filter((c) => c.status === "warn").length;
  const problems = crit + warn;
  const summaryColor = crit > 0 ? "--crit" : "--warn";

  const ordered = useMemo(() => sortContainers(containers), [containers]);
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return q ? ordered.filter((c) => c.name.toLowerCase().includes(q)) : ordered;
  }, [ordered, query]);

  // Maiores consumidores da métrica crítica (Top 3), quando a tela sinaliza estado
  // crítico da métrica no host.
  const topConsumers = useMemo(() => {
    if (!criticalMetric) return [];
    const val = (c: ContainerStatus) => (criticalMetric === "cpu" ? c.cpu_pct ?? 0 : c.mem_used_bytes ?? c.mem_pct ?? 0);
    return [...containers].filter((c) => c.running).sort((a, b) => val(b) - val(a)).slice(0, 3);
  }, [containers, criticalMetric]);

  const total = filtered.length;
  const viewH = Math.min(VIEW_H, Math.max(ROW_H, total * ROW_H));
  const start = Math.max(0, Math.floor(scrollTop / ROW_H) - OVERSCAN);
  const end = Math.min(total, Math.ceil((scrollTop + viewH) / ROW_H) + OVERSCAN);
  const slice = filtered.slice(start, end);

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
      {collapsible ? (
        // O InfoTip fica FORA do botão de abrir/fechar. Ele é um <button> e
        // estava aninhado dentro de outro <button> — HTML inválido, que o React
        // acusa no console e que na prática rouba o clique: tocar no "?" abria e
        // fechava o bloco em vez de mostrar a explicação. Como irmãos, cada um
        // tem o seu alvo e o seu foco de teclado.
        <div className="containers-head-linha">
          <button
            type="button"
            className="containers-head"
            onClick={() => setOpen((o) => !o)}
            aria-expanded={open}
          >
            {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
            <span style={{ ...MUTED_12, display: "flex", alignItems: "center", gap: "var(--sp-1)" }}>
              Containers <strong style={{ color: "var(--text-2)" }}>{containers.length}</strong>
            </span>
            {problems > 0 && (
              <span style={{ marginLeft: "auto", fontSize: "var(--fs-12)", color: `var(${summaryColor})`, fontWeight: 600 }}>
                {problems === 1 ? "1 com problema" : `${problems} com problema`}
              </span>
            )}
          </button>
          <InfoTip title="CPU dos containers" text={help.fields["containers.cpu"]} />
        </div>
      ) : (
        // Sem colapso próprio: rótulo estático (a linha da tabela já tem o seu chevron).
        <div className="containers-head containers-head--static">
          <span style={{ ...MUTED_12, display: "flex", alignItems: "center", gap: "var(--sp-1)" }}>
            Containers <strong style={{ color: "var(--text-2)" }}>{containers.length}</strong>
            <InfoTip title="CPU dos containers" text={help.fields["containers.cpu"]} />
          </span>
          {problems > 0 && (
            <span style={{ marginLeft: "auto", fontSize: "var(--fs-12)", color: `var(${summaryColor})`, fontWeight: 600 }}>
              {problems === 1 ? "1 com problema" : `${problems} com problema`}
            </span>
          )}
        </div>
      )}

      {criticalMetric && topConsumers.length > 0 && (
        <div className="containers-top">
          <span style={MUTED_12}>Maiores de {criticalMetric === "cpu" ? "CPU" : "RAM"}:</span>
          {topConsumers.map((c) => (
            <span key={c.name} className="containers-top__item" title={c.name}>
              {c.name} <strong>{criticalMetric === "cpu" ? formatValue(c.cpu_pct, "percent") : formatBytes(c.mem_used_bytes ?? 0)}</strong>
            </span>
          ))}
        </div>
      )}

      {isOpen && (
        <>
          {containers.length > 8 && (
            <input
              type="search"
              className="dt-search"
              style={{ maxWidth: "none" }}
              placeholder={`Filtrar ${containers.length} containers…`}
              value={query}
              onChange={(e) => {
                setQuery(e.target.value);
                setScrollTop(0);
              }}
              aria-label="Filtrar containers"
            />
          )}
          {total === 0 ? (
            <div style={{ ...MUTED_12, padding: "var(--sp-2) 0" }}>Nenhum container para “{query}”.</div>
          ) : (
            <div
              className="containers-scroll"
              style={{ height: viewH, overflowY: "auto" }}
              onScroll={(e) => setScrollTop(e.currentTarget.scrollTop)}
            >
              <div style={{ height: total * ROW_H, position: "relative" }}>
                <div style={{ position: "absolute", top: start * ROW_H, left: 0, right: 0 }}>
                  {slice.map((c) => (
                    <CompactRow key={c.name} c={c} />
                  ))}
                </div>
              </div>
            </div>
          )}
        </>
      )}
    </div>
  );
}

// Linha compacta de altura FIXA para a lista virtualizada: indicador de estado, nome
// truncado (reticências + tooltip, nunca quebra no meio) e uso/estado à direita.
function CompactRow({ c }: { c: ContainerStatus }) {
  const color = `var(${CONTAINER_COLOR[c.status]})`;
  const problem = c.status === "crit" || c.status === "warn";
  const usage = containerUsage(c);
  const restarts = c.restarts > 0 ? `${c.restarts} ${c.restarts === 1 ? "reinício" : "reinícios"}` : null;
  const right = [usage, restarts].filter(Boolean).join(" · ");
  return (
    <div className="container-row" style={{ height: ROW_H }}>
      <span aria-hidden className="container-row__dot" style={{ background: color }} />
      <span className="container-row__name" title={`${c.name}${c.image ? " · " + c.image : ""}`}>
        {c.name}
      </span>
      <span
        className="container-row__state"
        style={{ color: problem ? color : "var(--text-3)", fontWeight: problem ? 600 : 400 }}
        title={containerStateText(c)}
      >
        {containerStateText(c)}
      </span>
      {right && <span className="container-row__right tabular">{right}</span>}
    </div>
  );
}

// Uma linha de container: indicador colorido por estado + nome + estado legível
// (à esquerda) e uso/reinícios (à direita). Falhas (crit/warn) coloridas e em
// negrito para saltar aos olhos; o texto do estado carrega o significado (não só a cor).
export function ContainerRow({ c }: { c: ContainerStatus }) {
  const color = `var(${CONTAINER_COLOR[c.status]})`;
  const problem = c.status === "crit" || c.status === "warn";
  const usage = containerUsage(c);
  const restarts =
    c.restarts > 0 ? `${c.restarts} ${c.restarts === 1 ? "reinício" : "reinícios"}` : null;
  const right = [usage, restarts].filter(Boolean).join(" · ");
  return (
    <div
      style={{
        display: "flex",
        alignItems: "flex-start",
        justifyContent: "space-between",
        gap: "var(--sp-2)",
      }}
    >
      <div style={{ display: "flex", alignItems: "flex-start", gap: "var(--sp-2)", minWidth: 0 }}>
        <span
          aria-hidden
          style={{
            width: 8,
            height: 8,
            marginTop: 5,
            borderRadius: 999,
            background: color,
            flexShrink: 0,
          }}
        />
        <div style={{ minWidth: 0 }}>
          <span
            title={c.image || undefined}
            style={{
              fontSize: "var(--fs-12)",
              fontWeight: 600,
              color: "var(--text-1)",
              wordBreak: "break-all",
            }}
          >
            {c.name}
          </span>
          <span
            style={{
              fontSize: "var(--fs-12)",
              color: problem ? color : "var(--text-3)",
              fontWeight: problem ? 600 : 400,
            }}
          >
            {" · "}
            {containerStateText(c)}
          </span>
        </div>
      </div>
      {right && (
        <span
          className="tabular"
          style={{ ...MUTED_12, whiteSpace: "nowrap", flexShrink: 0 }}
        >
          {right}
        </span>
      )}
    </div>
  );
}

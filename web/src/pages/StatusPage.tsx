// Status page pública (P6.4 / UX.13): rota #/status sem login, atualiza sozinha.
// ATENÇÃO: renderizada FORA do AppShell e FORA do ToastProvider — é PROIBIDO usar
// useToast aqui (não há provider, lançaria erro). Mantida leve e autossuficiente.
import { useCallback, useState, type CSSProperties } from "react";
import { usePolling } from "../hooks/usePolling";
import { Badge, Card, EmptyState, InfoTip, Skeleton, type State } from "../components";
import { BrandLogo } from "../components/BrandLogo";
import { help } from "../help";
import { publicStatus, type PublicStatus, type PublicUptime } from "../api";
import { APP_TIME_ZONE_LABEL, formatDateTime, formatUptimePercent } from "../format";

const STATE_BADGE: Record<string, State> = { UP: "ok", DEGRADADO: "warn", SUSPEITO: "warn", DOWN: "crit" };

// Prioridade de exibição: problemas (DOWN pior) antes de UP dentro de cada grupo.
function stateRank(state: string): number {
  switch (state.toUpperCase()) {
    case "DOWN":
      return 3;
    case "DEGRADADO":
    case "SUSPEITO":
      return 2;
    default:
      return 0;
  }
}

// tinta de fundo/texto por estado do banner (só tokens, nunca hex).
const TINT_BG: Record<State, string> = {
  ok: "var(--ok-bg)",
  warn: "var(--warn-bg)",
  crit: "var(--crit-bg)",
  info: "var(--bg-2)",
  neutral: "var(--bg-2)",
};
type Site = PublicStatus["sites"][number];

// agrupa os sites por `group`, preservando a ordem de aparição dos grupos.
function groupSites(sites: Site[]): [string, Site[]][] {
  const map = new Map<string, Site[]>();
  for (const s of sites) {
    const g = s.group || "Geral";
    if (!map.has(g)) map.set(g, []);
    map.get(g)!.push(s);
  }
  // Dentro de cada grupo, problemas (DOWN) primeiro — sort estável preserva a
  // ordem da API entre itens de mesma prioridade.
  for (const list of map.values()) {
    list.sort((a, b) => stateRank(b.state) - stateRank(a.state));
  }
  return [...map.entries()];
}

// coberturaTexto descreve, em uma frase, o quanto do período foi medido — mesmo
// vocabulário da tela de Websites (coberturaTexto lá), com os campos que a API
// pública devolve (ela não publica `expected`).
function coberturaTexto(u: PublicUptime): string {
  if (u.samples <= 0) return "nenhuma sondagem registrada no período";
  return `${u.samples.toLocaleString("pt-BR")} sondagens · ${formatCoverage(u.coverage)} do período medido`;
}

// Cobertura com 0 casas: é uma medida grosseira de "o quanto olhamos", não um SLA.
function formatCoverage(pct: number): string {
  const v = Number.isFinite(pct) ? Math.max(0, Math.min(100, pct)) : 0;
  return `${new Intl.NumberFormat("pt-BR", { maximumFractionDigits: 0 }).format(v)}%`;
}

// célula de uptime% com números tabulares e rótulo do período.
//
// POR QUE ISTO MUDOU: o backend passou a publicar um OBJETO
// {percent, coverage, samples} em vez de um número, e `percent` é `null` quando a
// cobertura não autoriza afirmar disponibilidade. O guarda antigo
// (`typeof value === "number"`) reprovava o objeto SEMPRE — esta página pública
// nunca mostrou uptime, só "—", inclusive com 99,91% medidos a 100% de cobertura.
// A cobertura vai ao lado do número porque 99,9% sobre 4% do período não é a mesma
// afirmação que 99,9% sobre o período inteiro.
function Uptime({ label, u }: { label: string; u: PublicUptime | null | undefined }) {
  // Ausente = o backend não publicou o período (ex.: 90d sem histórico de 90 dias).
  if (!u) {
    return (
      <span className="tabular" style={{ fontSize: "var(--fs-12)", whiteSpace: "nowrap" }} title={`Ainda não há histórico suficiente para calcular ${label}.`}>
        <strong style={{ fontWeight: 600, color: "var(--text-3)" }}>—</strong>{" "}
        <span style={{ color: "var(--text-3)" }}>{label}</span>
      </span>
    );
  }
  if (u.percent == null) {
    return (
      <span
        style={{ fontSize: "var(--fs-12)", whiteSpace: "nowrap", color: "var(--text-3)" }}
        title={`Sem dados suficientes para afirmar disponibilidade: ${coberturaTexto(u)}.`}
      >
        sem dados suficientes <span>{label}</span>
      </span>
    );
  }
  return (
    <span
      className="tabular"
      style={{ fontSize: "var(--fs-12)", whiteSpace: "nowrap" }}
      title={`Medido em ${coberturaTexto(u)}.`}
    >
      {/* Piso, nunca arredondamento para cima: um site que caiu não pode exibir "100,00%". */}
      <strong style={{ fontWeight: 600 }}>{formatUptimePercent(u.percent)}</strong>{" "}
      <span style={{ color: "var(--text-3)" }}>
        {label} · {formatCoverage(u.coverage)} medido
      </span>
    </span>
  );
}

export function StatusPage() {
  const [data, setData] = useState<PublicStatus | null>(null);
  const [error, setError] = useState(false);

  const load = useCallback(() => {
    publicStatus()
      .then((d) => {
        setData(d);
        setError(false);
      })
      // graceful: sem toast (não há provider). Mantém os últimos dados válidos
      // na tela e só marca erro visível quando nunca conseguimos carregar.
      .catch(() => setError(true));
  }, []);
  usePolling(load, 30000, [load]);

  const loading = !data && !error;
  const sites = data?.sites ?? [];
  const groups = groupSites(sites);

  // banner geral calculado a partir dos dados (não confia só em `overall`).
  const problems = sites.filter((s) => s.state !== "UP");
  const anyDown = problems.some((s) => s.state === "DOWN");
  const overallState: State = problems.length === 0 ? "ok" : anyDown ? "crit" : "warn";
  const overallLabel =
    problems.length === 0
      ? "Todos os sistemas operacionais"
      : `${problems.length} ${problems.length === 1 ? "serviço" : "serviços"} com problema`;

  const bannerStyle: CSSProperties = {
    display: "flex",
    alignItems: "center",
    gap: "var(--sp-3)",
    padding: "var(--sp-4)",
    background: TINT_BG[overallState],
    border: "1px solid var(--border)",
    borderRadius: "var(--radius)",
    marginBottom: "var(--sp-5)",
  };

  return (
    <div style={{ minHeight: "100vh", background: "var(--bg-0)", color: "var(--text-1)" }}>
      <div style={{ maxWidth: 820, margin: "0 auto", padding: "var(--sp-6) var(--sp-4)", width: "100%", boxSizing: "border-box" }}>
        {/* cabeçalho público limpo: marca + título + última atualização */}
        <header style={{ marginBottom: "var(--sp-5)" }}>
          <div style={{ display: "flex", alignItems: "center", gap: "var(--sp-3)", flexWrap: "wrap" }}>
            <BrandLogo variant="full" />
            <span style={{ color: "var(--text-3)", fontSize: "var(--fs-14)" }}>status</span>
          </div>
          <h1 style={{ fontSize: "var(--fs-28)", margin: "var(--sp-2) 0 0", lineHeight: 1.2 }}>
            Status dos serviços
          </h1>
          <p style={{ color: "var(--text-2)", fontSize: "var(--fs-14)", margin: "var(--sp-1) 0 0" }}>
            {data ? (
              <>Atualizado {formatDateTime(data.updated_at)} ({APP_TIME_ZONE_LABEL}) · atualiza automaticamente</>
            ) : loading ? (
              "Carregando estado dos serviços…"
            ) : (
              "Disponibilidade dos serviços monitorados"
            )}
          </p>
        </header>

        {/* estado de carregamento */}
        {loading && (
          <div style={{ display: "grid", gap: "var(--sp-3)" }}>
            <Skeleton height={64} />
            <Skeleton height={120} />
            <Skeleton height={120} />
          </div>
        )}

        {/* erro gracioso (sem toast) quando nunca conseguimos carregar */}
        {error && !data && (
          <div
            role="alert"
            style={{
              padding: "var(--sp-4)",
              background: "var(--crit-bg)",
              border: "1px solid var(--border)",
              borderRadius: "var(--radius)",
              color: "var(--text-1)",
            }}
          >
            <strong style={{ color: "var(--crit)" }}>Não foi possível carregar o status.</strong>
            <div style={{ color: "var(--text-2)", fontSize: "var(--fs-14)", marginTop: "var(--sp-1)" }}>
              A página tenta novamente sozinha a cada 30 segundos.
            </div>
          </div>
        )}

        {data && (
          <>
            {/* banner geral computado dos dados */}
            <div style={bannerStyle}>
              {/* o rótulo textual real é filho do Badge (o "●" era redundante com
                  o ícone do próprio Badge). */}
              <Badge state={overallState}>
                {overallLabel}
                {overallState === "ok" ? " ✓" : ""}
              </Badge>
            </div>

            {/* legenda com ajuda contextual (InfoTip é autossuficiente, seguro aqui) */}
            {sites.length > 0 && (
              <div
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: "var(--sp-4)",
                  flexWrap: "wrap",
                  marginBottom: "var(--sp-4)",
                  color: "var(--text-2)",
                  fontSize: "var(--fs-12)",
                }}
              >
                <span style={{ display: "inline-flex", alignItems: "center", gap: "var(--sp-1)" }}>
                  Estados
                  <InfoTip title={help.concepts["estados-de-site"].title} text={help.concepts["estados-de-site"].body} />
                </span>
                <span style={{ display: "inline-flex", alignItems: "center", gap: "var(--sp-1)" }}>
                  Uptime
                  <InfoTip title={help.concepts.uptime.title} text={help.concepts.uptime.body} />
                </span>
              </div>
            )}

            {/* serviços agrupados: cada grupo é um Card (empilha no mobile) */}
            {sites.length === 0 ? (
              <EmptyState
                title="Nenhum serviço monitorado ainda"
                body="Quando houver checks de site cadastrados, o estado e a disponibilidade de cada um aparecem aqui."
              />
            ) : (
              <div style={{ display: "grid", gap: "var(--sp-4)" }}>
                {groups.map(([group, list]) => (
                  <Card key={group} title={group}>
                    <div style={{ display: "grid", gap: "var(--sp-1)" }}>
                      {list.map((s, i) => (
                        <div
                          key={i}
                          style={{
                            display: "flex",
                            alignItems: "center",
                            gap: "var(--sp-3)",
                            flexWrap: "wrap",
                            padding: "var(--sp-2) 0",
                            borderTop: i > 0 ? "1px solid var(--border)" : "none",
                          }}
                        >
                          <Badge state={STATE_BADGE[s.state] ?? "neutral"}>{s.state}</Badge>
                          <span style={{ flex: "1 1 140px", minWidth: 0, overflowWrap: "anywhere" }}>{s.name}</span>
                          <span
                            style={{
                              display: "flex",
                              alignItems: "center",
                              gap: "var(--sp-3)",
                              flexWrap: "wrap",
                              color: "var(--text-2)",
                            }}
                          >
                            {/* Sitemap não sonda nada (quem sonda são as páginas-filhas):
                                mostra páginas no ar em vez de percentual. */}
                            {s.kind === "sitemap" && s.pages ? (
                              <span className="tabular" style={{ fontSize: "var(--fs-12)", whiteSpace: "nowrap" }}>
                                <strong style={{ fontWeight: 600 }}>
                                  {Math.max(0, s.pages.total - s.pages.down - s.pages.degraded)}/{s.pages.total}
                                </strong>{" "}
                                <span style={{ color: "var(--text-3)" }}>páginas no ar</span>
                              </span>
                            ) : (
                              <>
                                <Uptime label="dia" u={s.uptime_day} />
                                <Uptime label="mês" u={s.uptime_month} />
                                <Uptime label="90d" u={s.uptime_90d} />
                              </>
                            )}
                          </span>
                        </div>
                      ))}
                    </div>
                  </Card>
                ))}
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}

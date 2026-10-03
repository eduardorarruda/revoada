// Rota /tv/<token> (P3.1/P3.3): kiosk fullscreen, playlist com rotação+fade, watchdog
// (reload remoto, reload em erro de JS, heartbeat, anti burn-in), auth só por token.
import { useCallback, useEffect, useRef, useState } from "react";
import { tvQuery, tvResolve, tvStatus, type Dashboard, type TVStatus } from "../api";
import { usePolling } from "../hooks/usePolling";
import { useHostNames } from "../hooks/useHostNames";
import { fmtRelAbs, formatDuration, formatValue } from "../format";
import { metricMeta } from "../metrics/dict";
import { lastPoint, pointFreshness, type LastPoint } from "../metrics/lastPoint";
import { aggSuffix, unitForAgg } from "../metrics/resolution";
import { HealthWall } from "./HealthWall";
import { AlertBurst, Takeover } from "./tv/alertas";
import { estadoDaTV } from "./tv/estado";
import { FaiscaTV, LetreiroTV, NumeroTV, PontosTV, TendenciaTV, TopoTV } from "./tv/pecas";
import { useAlertasNovos } from "./tv/useAlertasNovos";

const BUNDLE = "0.1.0";

interface Screen {
  dashboard: Dashboard;
  duration_seconds: number;
}

export function TVView({ token }: { token: string }) {
  const [name, setName] = useState("");
  const [screens, setScreens] = useState<Screen[]>([]);
  const [wall, setWall] = useState(false); // TV do tipo Health Wall (sem dashboard)
  const [idx, setIdx] = useState(0);
  const [error, setError] = useState(false);
  const [fade, setFade] = useState(false);
  const loadedAt = useRef(Date.now());
  const failures = useRef(0);
  const [status, setStatus] = useState<TVStatus | null>(null);
  const { hostLabel } = useHostNames();

  // alert takeover + ticker: poll de status a cada 10s (pausa em aba oculta)
  const poll = useCallback(() => {
    tvStatus(token).then(setStatus).catch(() => {});
  }, [token]);
  usePolling(poll, 10000, [poll]);

  // Notificação por evento: quando um alerta NOVO dispara, mostra um overlay veemente
  // por 30s e depois some (ver useAlertasNovos).
  const { burst, pendentes } = useAlertasNovos(status);

  // resolve + watchdog de reload remoto / heartbeat (poll a cada 15s)
  const lastScreensJSON = useRef("");
  const resolve = useCallback(async () => {
    try {
      const r = await tvResolve(token, BUNDLE);
      failures.current = 0;
      setError(false);
      setName(r.name);
      setWall(!!r.wall);
      // Só troca `screens` quando o conteúdo MUDA de verdade. Sem isto, cada poll
      // (15s) criava um novo array (mesmo conteúdo) → o efeito de rotação, que
      // depende de `screens`, re-rodava e ZERAVA o setTimeout da troca de tela.
      // Telas com duration_seconds > 15 nunca avançavam (ficava travado em 1/N).
      const next = r.playlist
        ? r.playlist.screens
        : r.dashboard
          ? [{ dashboard: r.dashboard, duration_seconds: 3600 }]
          : null;
      if (next) {
        const j = JSON.stringify(next);
        if (j !== lastScreensJSON.current) {
          lastScreensJSON.current = j;
          setScreens(next);
        }
      }
      // sinal de reload remoto: se reload_at for posterior à carga da página, recarrega
      if (r.reload_at && new Date(r.reload_at).getTime() > loadedAt.current) {
        location.reload();
      }
    } catch {
      failures.current++;
      if (failures.current === 1) setError(true);
      // heartbeat: 3 falhas seguidas -> recarrega (recupera de server fora do ar)
      if (failures.current >= 3) location.reload();
    }
  }, [token]);
  usePolling(resolve, 15000, [resolve]);

  // rotação da playlist com transição por fade
  useEffect(() => {
    if (screens.length <= 1) return;
    const dur = (screens[idx]?.duration_seconds || 15) * 1000;
    const t = setTimeout(() => {
      setFade(true);
      setTimeout(() => {
        setIdx((i) => (i + 1) % screens.length);
        setFade(false);
      }, 400);
    }, dur);
    return () => clearTimeout(t);
  }, [screens, idx]);

  // watchdog: erro de JS não tratado -> recarrega; anti burn-in (deslocamento sub-pixel)
  const [shift, setShift] = useState(0);
  useEffect(() => {
    const onErr = () => setTimeout(() => location.reload(), 2000);
    window.addEventListener("error", onErr);
    document.body.style.cursor = "none";
    const burn = setInterval(() => setShift((s) => (s + 1) % 3), 60000);
    return () => {
      window.removeEventListener("error", onErr);
      document.body.style.cursor = "";
      clearInterval(burn);
    };
  }, []);

  const estado = estadoDaTV(status);
  const atrasado = error ? "sem conexão com o painel, tentando de novo" : null;
  const comLetreiro = !!status && (status.warnings.length > 0 || status.deploys.length > 0);

  if (error && !wall && screens.length === 0) {
    return (
      <div className="tv">
        <TopoTV nome={name} estado={{ tom: "neutro", texto: "Sem conexão" }} atrasado="reconectando…" />
        <div className="tv-espera">
          <h2 className="tv-espera__titulo">Acesso revogado ou servidor indisponível</h2>
          <p className="tv-espera__sub">A TV tenta de novo sozinha a cada 15 s e volta assim que o painel responder.</p>
        </div>
      </div>
    );
  }
  if (!wall && screens.length === 0)
    return (
      <div className="tv">
        <TopoTV nome={name} estado={estado} />
        <div className="tv-espera">
          <h2 className="tv-espera__titulo">Carregando a primeira tela…</h2>
        </div>
      </div>
    );

  // O overlay de alerta novo aparece POR CIMA da playlist, com veemência, por 30s;
  // depois some. UX-02: um crítico já na tela SEMPRE vence o burst, não
  // sobrepomos o overlay durante um takeover (nada mais grave é escondido por algo
  // menos grave). O overlay só entra sobre a playlist/wall normais.
  const overlay = burst ? <AlertBurst alert={burst} hostLabel={hostLabel} pendentes={pendentes} /> : null;

  // Takeover: um crítico ativo interrompe a playlist (o burst é suprimido).
  if (status && status.criticals.length > 0) {
    return <Takeover nome={name} estado={estado} status={status} atrasado={atrasado} />;
  }

  // TV do tipo Health Wall: renderiza a wall fullscreen (busca por token), com o mesmo
  // overlay de alerta novo por cima. Sem dashboard/rotação.
  if (wall) {
    return (
      <>
        <HealthWall tv token={token} nomeTV={name} />
        {status && <LetreiroTV warnings={status.warnings} deploys={status.deploys} />}
        {overlay}
      </>
    );
  }

  const screen = screens[idx] ?? screens[0];
  const allPanels = screen.dashboard.model.panels;
  const panels = allPanels.slice(0, 6);
  const hiddenPanels = allPanels.length - panels.length; // UX-23: não sumir em silêncio
  const duracao = screens.length > 1 ? screen.duration_seconds || 15 : undefined;
  return (
    <>
      <div
        className={`tv${fade ? " tv--trocando" : ""}${comLetreiro ? " tv--com-letreiro" : ""}`}
        style={{ transform: `translate(${shift}px, ${shift}px)` }}
      >
        <TopoTV
          nome={name}
          sub={
            <>
              <span>{screen.dashboard.title}</span>
              <PontosTV total={screens.length} atual={idx} />
            </>
          }
          estado={estado}
          atrasado={atrasado}
          progresso={duracao}
          chaveProgresso={idx}
        />
        {/* minmax(0, 1fr) no CSS: sem o `0`, cada coluna cresce até o min-content do
            NÚMERO e o terceiro painel saía da tela (medido em 1280px: 111px fora). */}
        <div className="tv-grade">
          {/* `p.query` pode faltar num painel salvo incompleto: antes era um TypeError,
              capturado pelo watchdog, que recarregava a página em laço. Agora o painel
              aparece dizendo o que há de errado. */}
          {panels.map((p) =>
            p.query?.metric ? (
              <TVBlock key={`${idx}-${p.id}`} token={token} title={p.title} metric={p.query.metric} filters={p.query.filters} agg={p.query.agg} />
            ) : (
              <div key={`${idx}-${p.id}`} className="tv-bloco tv-bloco--incompleto">
                <div className="tv-bloco__titulo">{p.title}</div>
                <div className="tv-bloco__aviso">Painel sem métrica configurada</div>
              </div>
            ),
          )}
        </div>
        {hiddenPanels > 0 && (
          <div className="tv-ocultos">
            +{hiddenPanels} {hiddenPanels === 1 ? "painel não exibido" : "painéis não exibidos"}, a TV mostra até 6 por tela
          </div>
        )}
      </div>
      {status && <LetreiroTV warnings={status.warnings} deploys={status.deploys} />}
      {overlay}
    </>
  );
}


// Passo da consulta dos blocos (1 min) e a janela curta que eles pedem.
const STEP = 60;
// Janela CURTA: este bloco desenha um número e uma mini-série, não um gráfico. Pedir
// 6 h a cada 5 s custava 8.192 linhas / 954 KB por consulta; 10 passos dão folga
// sobre a validade do ponto (3× o passo) sem carregar histórico que a parede não usa.
const JANELA_S = STEP * 10;

function TVBlock({
  token,
  title,
  metric,
  filters,
  agg,
}: {
  token: string;
  title: string;
  metric: string;
  filters?: Record<string, string>;
  agg?: string;
}) {
  // Guardamos o ÚLTIMO PONTO com o carimbo de tempo dele. Antes o bloco varria as 6
  // horas inteiras da janela e só marcava "sem sinal" quando as 6 h eram nulas, ou
  // seja, a TV do NOC mostrava o valor de 2 horas atrás em fonte de 120px como se
  // fosse de agora.
  const [ponto, setPonto] = useState<LastPoint | null>(null);
  // Os pontos da janela curta: a mesma resposta que já chegava, agora desenhada como
  // mini-série e seta de tendência. Nenhuma consulta a mais.
  const [serie, setSerie] = useState<(number | null)[]>([]);
  const [erro, setErro] = useState(false);
  const meta = metricMeta(metric);
  // A unidade depende da AGREGAÇÃO: rede em "rate" é "32 KB/s", não "32 KB", a
  // diferença entre um link ocioso e um saturado.
  const unidade = unitForAgg(meta.unit, agg);

  const load = useCallback(async () => {
    try {
      const to = new Date();
      const from = new Date(to.getTime() - JANELA_S * 1000);
      const resp = await tvQuery(token, { metric, filters, from: from.toISOString(), to: to.toISOString(), step: STEP, agg });
      const valores = resp.series[0]?.values ?? [];
      setPonto(lastPoint(resp.ts, valores));
      setSerie(valores);
      setErro(false);
    } catch {
      setErro(true);
    }
  }, [token, metric, filters, agg]);
  usePolling(load, 5000, [load]);

  const fresco = ponto ? pointFreshness(ponto, STEP) : { state: "nodata" as const, ageSeconds: null };
  const mostrar = fresco.state === "fresh";
  // Aviso honesto: distingue "nunca chegou / falha de rede" de "chegou, mas é velho".
  const aviso = erro
    ? "sem conexão com o servidor"
    : fresco.state === "nodata"
      ? `sem dado nos últimos ${formatDuration(JANELA_S)}`
      : fresco.state === "stale" && ponto?.ts != null
        ? `último dado ${fmtRelAbs(ponto.ts * 1000).rel}`
        : "";

  return (
    <div className={`tv-bloco${mostrar ? "" : " tv-bloco--sem-dado"}`}>
      {/* O título carrega o sufixo da agregação (", taxa", ", máximo"): sem ele um pico
          (máximo) era lido como o valor típico. `avg` não recebe sufixo. */}
      <div className="tv-bloco__titulo">
        {title}
        {aggSuffix(agg)}
      </div>
      {/* nowrap no CSS: o espaço em "5,2 GB" era ponto de quebra e a 3 m "5,2" em cima
          e "GB" embaixo liam-se como dois números. */}
      <div className="tv-bloco__valor">
        <NumeroTV texto={mostrar ? formatValue(ponto?.value, unidade) : "—"} />
        {mostrar && <TendenciaTV valores={serie} />}
      </div>
      {aviso && <div className="tv-bloco__aviso">{aviso}</div>}
      <FaiscaTV valores={mostrar ? serie : []} />
    </div>
  );
}

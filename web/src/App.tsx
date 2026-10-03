// Roteamento por hash + porta de autenticação. Rotas autenticadas ficam dentro
// do AppShell (sidebar/topbar); rotas públicas (dev/status/tv) ficam fora dele.
import { Bando } from "./motion";
import { lazy, Suspense, useEffect, useState } from "react";
import { PageTransition } from "./motion";
import { ErrorBoundary } from "./components/ErrorBoundary";
// Rotas leves e a tela de Login permanecem estáticas (entram no bundle inicial).
import { DevKit } from "./pages/DevKit";
import { Dashboards } from "./pages/Dashboards";
import { Hosts } from "./pages/Hosts";
import { Alerts } from "./pages/Alerts";
import { Notify } from "./pages/Notify";
import { HealthWall } from "./pages/HealthWall";
import { Login } from "./pages/Login";
import { Home } from "./pages/Home";
import { Help } from "./pages/Help";
import { AppShell } from "./components/AppShell";
import { ToastProvider } from "./components";
import { ReauthModal } from "./components/ReauthModal";
import { CorrelationDrawer } from "./components/CorrelationDrawer";
import { StatusPage } from "./pages/StatusPage";
import { isAuthenticated, restoreSession, loadMe } from "./api";

// Páginas pesadas carregadas sob demanda (React.lazy) — tiram do bundle inicial
// as libs de gráfico: echarts (Heatmap, via DevPanels) e uplot (via panels), além
// das telas grandes de Traces/Logs/Dashboards/Websites/TV. Os módulos usam export
// nomeado, então adaptamos para `default` que o lazy exige.
const DevPanels = lazy(() => import("./pages/DevPanels").then((m) => ({ default: m.DevPanels })));
const DashboardView = lazy(() => import("./pages/DashboardView").then((m) => ({ default: m.DashboardView })));
const DashboardEdit = lazy(() => import("./pages/DashboardEdit").then((m) => ({ default: m.DashboardEdit })));
const DashboardVersions = lazy(() => import("./pages/DashboardVersions").then((m) => ({ default: m.DashboardVersions })));
const Explore = lazy(() => import("./pages/Explore").then((m) => ({ default: m.Explore })));
const HostDetail = lazy(() => import("./pages/HostDetail").then((m) => ({ default: m.HostDetail })));
const Logs = lazy(() => import("./pages/Logs").then((m) => ({ default: m.Logs })));
const Traces = lazy(() => import("./pages/Traces").then((m) => ({ default: m.Traces })));
const Websites = lazy(() => import("./pages/Websites").then((m) => ({ default: m.Websites })));
const TVConsole = lazy(() => import("./pages/TVConsole").then((m) => ({ default: m.TVConsole })));
const TVView = lazy(() => import("./pages/TVView").then((m) => ({ default: m.TVView })));
const Users = lazy(() => import("./pages/Users").then((m) => ({ default: m.Users })));
const Audit = lazy(() => import("./pages/Audit").then((m) => ({ default: m.Audit })));
const Seguranca = lazy(() => import("./pages/Seguranca").then((m) => ({ default: m.Seguranca })));
const Agentes = lazy(() => import("./pages/Agentes").then((m) => ({ default: m.Agentes })));
const Deploys = lazy(() => import("./pages/Deploys").then((m) => ({ default: m.Deploys })));
const Mcp = lazy(() => import("./pages/Mcp").then((m) => ({ default: m.Mcp })));
const ModoMigracao = lazy(() => import("./modos/migracao/ModoMigracao").then((m) => ({ default: m.ModoMigracao })));
const AgentUpdates = lazy(() => import("./pages/AgentUpdates").then((m) => ({ default: m.AgentUpdates })));

// Fallback acessível enquanto um chunk sob demanda é buscado.
function PageLoading() {
  return (
    <div
      role="status"
      aria-live="polite"
      aria-busy="true"
      style={{ display: "grid", placeItems: "center", alignContent: "center", gap: "var(--sp-3)", minHeight: "40vh", color: "var(--text-2)" }}
    >
      <Bando rotulo="Carregando a tela" />
      Carregando…
    </div>
  );
}

// UID do dashboard de fábrica "Visão do Host (genérico)" (espelha
// dashboards.GenericHostUID no backend): é o único "host-…" que NÃO é um servidor.
const GENERIC_HOST_UID = "host-visao-geral";

// hostDashboardRedirect resolve o destino de um dashboard de host para a nova
// Visão do Servidor (#/hosts/<host>), muito mais legível. Os dashboards por-servidor
// são auto-gerados (UID "host-<hostname>") e o genérico só escolhe um host — a página
// de detalhe entrega o mesmo conteúdo (CPU/RAM/disco/rede/containers/alertas) num
// layout com abas e painel lateral. Devolve o caminho de destino ou null (não é
// dashboard de host → segue como dashboard normal). NÃO afeta /edit nem /versions
// (tratados antes) — editar o modelo continua acessível.
export function hostDashboardRedirect(uid: string, query: string): string | null {
  if (!uid.startsWith("host-")) return null;
  if (uid === GENERIC_HOST_UID) {
    // Genérico: ?var-host=<host> (deep link das notificações) abre direto o servidor;
    // sem host escolhido, manda para a lista de servidores para o usuário escolher.
    const h = new URLSearchParams(query).get("var-host") ?? "";
    return h ? `/hosts/${encodeURIComponent(h)}` : "/hosts";
  }
  const hostname = uid.slice("host-".length);
  return hostname ? `/hosts/${encodeURIComponent(hostname)}` : null;
}

// HashRedirect troca a rota atual por outra sem empilhar no histórico (replace), para
// que o botão "voltar" não caia de volta no redirect. Mostra o fallback enquanto troca.
function HashRedirect({ to }: { to: string }) {
  useEffect(() => {
    window.location.replace(`${window.location.pathname}${window.location.search}#${to}`);
  }, [to]);
  return <PageLoading />;
}

function useHashRoute(): string {
  const [route, setRoute] = useState(window.location.hash.slice(1) || "/");
  useEffect(() => {
    const onHash = () => setRoute(window.location.hash.slice(1) || "/");
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);
  return route;
}

// Resolve a rota autenticada para a página correspondente. `query` é a parte após
// "?" do hash (ex.: "host=web-01"), usada por telas que aceitam filtros na URL.
function renderPage(route: string, query = "") {
  // Rotas de dashboard: /d/{uid}, /d/{uid}/edit, /d/{uid}/versions.
  if (route.startsWith("/d/")) {
    const rest = route.slice(3);
    if (rest.endsWith("/edit")) return <DashboardEdit uid={rest.slice(0, -"/edit".length)} />;
    if (rest.endsWith("/versions")) return <DashboardVersions uid={rest.slice(0, -"/versions".length)} />;
    // Dashboards de host (UID "host-…") abrem a Visão do Servidor (#/hosts/<host>), bem
    // mais legível — vale para todos os servidores, não só um. Só a visualização é
    // redirecionada; editar/versões seguem no dashboard (tratados acima).
    const hostTarget = hostDashboardRedirect(rest, query);
    if (hostTarget) return <HashRedirect to={hostTarget} />;
    // ?var-host=<host> pré-seleciona o servidor no dashboard genérico (deep link das notificações).
    return <DashboardView uid={rest} initialHost={new URLSearchParams(query).get("var-host") ?? ""} />;
  }
  // Detalhe de um host: /hosts/<hostname> (o hostname pode conter pontos/traços).
  if (route.startsWith("/hosts/")) {
    const hostname = decodeURIComponent(route.slice("/hosts/".length));
    if (hostname) return <HostDetail hostname={hostname} />;
  }
  switch (route) {
    case "/wall":
      return <HealthWall />;
    case "/hosts":
      return <Hosts />;
    case "/dashboards":
      return <Dashboards />;
    case "/explore":
      return <Explore />;
    case "/alerts":
      return <Alerts />;
    case "/logs":
      return <Logs initialHost={new URLSearchParams(query).get("host") ?? ""} />;
    case "/traces":
      return <Traces />;
    case "/notify":
      return <Notify />;
    case "/websites":
      return <Websites />;
    case "/admin/tvs":
      return <TVConsole />;
    case "/users":
      return <Users />;
    case "/audit":
      return <Audit />;
    case "/seguranca":
      return <Seguranca />;
    case "/agentes":
      return <Agentes />;
    case "/mcp":
      return <Mcp />;
    case "/deploys":
      return <Deploys />;
    case "/admin/agent-updates":
      return <AgentUpdates />;
    case "/help":
      return <Help />;
    case "/":
      return <Home />;
    default:
      return <Home />;
  }
}

function isPublicRoute(route: string): boolean {
  return route === "/dev/kit" || route === "/dev/panels" || route === "/status" || route.startsWith("/tv/");
}

export function App() {
  const route = useHashRoute();
  const [, forceRender] = useState(0);
  // No boot, tenta recuperar a sessão pelo cookie de refresh antes de decidir
  // mostrar o login — assim um F5 não derruba o usuário para a tela de login.
  const [booting, setBooting] = useState(() => !isAuthenticated() && !isPublicRoute(window.location.hash.slice(1) || "/"));

  useEffect(() => {
    if (!booting) return;
    let alive = true;
    // Restaura a sessão e, se deu certo, carrega papel + escopo de servidores (/api/me)
    // antes de renderizar — assim o menu admin e o gating de botões já saem corretos.
    restoreSession()
      .then((ok) => (ok ? loadMe().catch(() => undefined) : undefined))
      .finally(() => {
        if (alive) {
          setBooting(false);
          forceRender((n) => n + 1); // reflete o accessToken recém-obtido
        }
      });
    return () => {
      alive = false;
    };
  }, [booting]);

  // Sessão perdida: quando o refresh falha de vez, a api.ts emite "revoada:auth-lost"
  // após limpar o token. Aqui só forçamos um re-render — como isAuthenticated()
  // passa a ser falso, a árvore autenticada (e todo o polling dela) desmonta e a
  // tela de login volta, encerrando o loop de 401 em tela vazia.
  useEffect(() => {
    const onAuthLost = () => forceRender((n) => n + 1);
    window.addEventListener("revoada:auth-lost", onAuthLost);
    return () => window.removeEventListener("revoada:auth-lost", onAuthLost);
  }, []);

  // Rotas públicas (sem login e sem AppShell).
  if (route === "/dev/kit") return <DevKit />;
  if (route === "/dev/panels")
    return (
      <Suspense fallback={<PageLoading />}>
        <DevPanels />
      </Suspense>
    );
  if (route === "/status") return <StatusPage />;
  if (route.startsWith("/tv/"))
    return (
      <Suspense fallback={<PageLoading />}>
        <TVView token={route.slice(4)} />
      </Suspense>
    );

  if (!isAuthenticated()) {
    // Enquanto reautentica, mostra um splash em vez de piscar o login.
    if (booting) {
      return (
        <div style={{ display: "grid", placeItems: "center", minHeight: "100vh", color: "var(--text-2)" }}>
          Carregando…
        </div>
      );
    }
    return <Login onDone={() => forceRender((n) => n + 1)} />;
  }

  // Separa o caminho da query (ex.: "/logs?host=web-01") para casar a rota e
  // destacar o menu pelo caminho puro, sem que o filtro na URL quebre nada.
  const qIndex = route.indexOf("?");
  const routePath = qIndex >= 0 ? route.slice(0, qIndex) : route;
  const routeQuery = qIndex >= 0 ? route.slice(qIndex + 1) : "";

  // Modo Migração: layout próprio, fora do menu do painel (ARQUITETURA §9).
  if (routePath.startsWith("/migracao")) {
    return (
      <ToastProvider>
        <ErrorBoundary key={routePath}>
          <Suspense fallback={<PageLoading />}>
            <ModoMigracao rota={routePath} />
          </Suspense>
        </ErrorBoundary>
        <ReauthModal />
      </ToastProvider>
    );
  }

  // ToastProvider envolve tudo para que useToast() das páginas funcione.
  return (
    <ToastProvider>
      <AppShell route={routePath}>
        {/* A barreira é por rota (key): trocar de tela limpa o erro da anterior. */}
        <ErrorBoundary key={routePath}>
          <Suspense fallback={<PageLoading />}>
            <PageTransition rota={routePath}>{renderPage(routePath, routeQuery)}</PageTransition>
          </Suspense>
        </ErrorBoundary>
      </AppShell>
      <CorrelationDrawer />
      <ReauthModal />
    </ToastProvider>
  );
}

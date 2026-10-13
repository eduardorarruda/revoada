// Roteamento por hash + porta de autenticação. Rotas autenticadas ficam dentro
// do AppShell (sidebar/topbar); rotas públicas (status/tv, e /dev/* só em dev) ficam fora dele.
import { Bando } from "./motion";
import { lazy, Suspense, useEffect, useState } from "react";
import { PageTransition } from "./motion";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { chaveDaTela } from "./chaveTela";
// Só a tela de Login (primeira tela sem sessão) e a Home ("/" após o login) ficam
// estáticas no bundle inicial; todo o resto é carregado sob demanda abaixo.
import { Login } from "./pages/Login";
import { Home } from "./pages/Home";
import { AppShell } from "./components/AppShell";
import { ToastProvider } from "./components";
import { ReauthModal } from "./components/ReauthModal";
import { CorrelationDrawer } from "./components/CorrelationDrawer";
import { isAuthenticated, restoreSession, loadMe } from "./api";

// Páginas carregadas sob demanda (React.lazy) — tiram do bundle inicial a lib de
// gráfico uplot (via panels) e as telas que o login não usa. Os módulos usam export
// nomeado, então adaptamos para `default` que o lazy exige.
//
// Vitrines de desenvolvimento (#/dev/kit e #/dev/panels): só existem em `vite dev`.
// No build de produção import.meta.env.DEV vira `false` e o import() some do bundle.
const DevKit = import.meta.env.DEV ? lazy(() => import("./pages/DevKit").then((m) => ({ default: m.DevKit }))) : null;
const DevPanels = import.meta.env.DEV
  ? lazy(() => import("./pages/DevPanels").then((m) => ({ default: m.DevPanels })))
  : null;
const Dashboards = lazy(() => import("./pages/Dashboards").then((m) => ({ default: m.Dashboards })));
const Hosts = lazy(() => import("./pages/Hosts").then((m) => ({ default: m.Hosts })));
const Alerts = lazy(() => import("./pages/Alerts").then((m) => ({ default: m.Alerts })));
const Notify = lazy(() => import("./pages/Notify").then((m) => ({ default: m.Notify })));
const HealthWall = lazy(() => import("./pages/HealthWall").then((m) => ({ default: m.HealthWall })));
const Help = lazy(() => import("./pages/Help").then((m) => ({ default: m.Help })));
const StatusPage = lazy(() => import("./pages/StatusPage").then((m) => ({ default: m.StatusPage })));
const Privacidade = lazy(() => import("./pages/Privacidade").then((m) => ({ default: m.Privacidade })));
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
const PaginaIa = lazy(() => import("./pages/ia/Ia").then((m) => ({ default: m.PaginaIa })));

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
    if (hostname) return <HostDetail hostname={hostname} initialWindow={new URLSearchParams(query).get("janela") ?? ""} />;
  }
  // Agentes de IA: /ia, /ia/execucoes, /ia/execucoes/<trace_id>, /ia/ferramentas, /ia/precos.
  if (route === "/ia" || route.startsWith("/ia/")) return <PaginaIa rota={route} query={query} />;
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
      return <Traces initialTrace={new URLSearchParams(query).get("trace") ?? ""} />;
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
  if (import.meta.env.DEV && (route === "/dev/kit" || route === "/dev/panels")) return true;
  return route === "/status" || route === "/privacidade" || route.startsWith("/tv/");
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

  // Rotas públicas (sem login e sem AppShell). As vitrines /dev/* só existem em dev.
  if (DevKit && route === "/dev/kit")
    return (
      <Suspense fallback={<PageLoading />}>
        <DevKit />
      </Suspense>
    );
  if (DevPanels && route === "/dev/panels")
    return (
      <Suspense fallback={<PageLoading />}>
        <DevPanels />
      </Suspense>
    );
  if (route === "/status")
    return (
      <Suspense fallback={<PageLoading />}>
        <StatusPage />
      </Suspense>
    );
  // Política de privacidade (LGPD): pública — quem ainda não entrou também pode ler.
  if (route === "/privacidade")
    return (
      <Suspense fallback={<PageLoading />}>
        <Privacidade />
      </Suspense>
    );
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

  // Abas de uma mesma seção (#/ia/*) não remontam a página: ver chaveTela.ts.
  const chaveTela = chaveDaTela(routePath);

  // ToastProvider envolve tudo para que useToast() das páginas funcione.
  return (
    <ToastProvider>
      <AppShell route={routePath}>
        {/* A barreira é por tela (key): trocar de tela limpa o erro da anterior. */}
        <ErrorBoundary key={chaveTela}>
          <Suspense fallback={<PageLoading />}>
            <PageTransition rota={chaveTela}>{renderPage(routePath, routeQuery)}</PageTransition>
          </Suspense>
        </ErrorBoundary>
      </AppShell>
      <CorrelationDrawer />
      <ReauthModal />
    </ToastProvider>
  );
}

// AppShell (UX.1) — sidebar agrupada + topbar + drawer mobile + toggle de tema.
// Importado direto em App.tsx (não vai no index.tsx). Rotas públicas ficam fora.
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ComponentType,
  type ReactNode,
} from "react";
import { m } from "motion/react";
import {
  House,
  LayoutGrid,
  Server,
  LayoutDashboard,
  Compass,
  ScrollText,
  Waypoints,
  BellRing,
  Send,
  Globe,
  Tv,
  ClipboardList,
  ArrowUpCircle,
  CircleHelp,
  Sun,
  Moon,
  Menu,
  User,
  Users,
  LogOut,
  PanelLeftClose,
  PanelLeftOpen,
  Search,
  ShieldAlert,
  ShieldCheck,
  Network,
  ArrowRightLeft,
  Bot,
  Rocket,
} from "lucide-react";
import { CommandPalette, type ItemPaleta } from "./CommandPalette";
import { FreshnessIndicator } from "./index";
import { BrandLogo } from "./BrandLogo";
import {
  NOME_PAPEL,
  getAccessToken,
  getMe,
  getRole,
  logout,
  sondarConexao,
  type MeResponse,
} from "../api";
import { getTheme, setTheme, type Theme } from "../theme";
import { useConexao } from "../hooks/useConexao";
import { IDADE_PARA_SONDAR, idadeUltimoSucesso } from "../conexao";
import "./appshell.css";

type IconType = ComponentType<{
  size?: number | string;
  "aria-hidden"?: boolean;
}>;
export interface NavItem {
  label: string;
  href: string; // com "#" na frente
  icon: IconType;
  match?: (route: string) => boolean; // destaque para rotas dinâmicas
  adminOnly?: boolean; // só aparece para administradores
}
export interface NavGroup {
  label?: string;
  items: NavItem[];
}

// Exportado para teste: o menu é a PRIMEIRA das camadas que escondem uma tela de
// administração, e uma camada sem teste é uma camada que alguém tira sem perceber.
export const NAV: NavGroup[] = [
  {
    items: [
      { label: "Início", href: "#/", icon: House, match: (r) => r === "/" },
    ],
  },
  {
    label: "Observar",
    items: [
      { label: "Mural de Saúde", href: "#/wall", icon: LayoutGrid },
      {
        label: "Infraestrutura",
        href: "#/hosts",
        icon: Server,
        // O detalhe de um servidor é parte da Infraestrutura: o menu continua marcado
        // e o topo diz onde a pessoa está (antes, o topo dizia "Início").
        match: (r) => r === "/hosts" || r.startsWith("/hosts/"),
      },
      {
        label: "Dashboards",
        href: "#/dashboards",
        icon: LayoutDashboard,
        match: (r) => r === "/dashboards" || r.startsWith("/d/"),
      },
      { label: "Explore", href: "#/explore", icon: Compass },
    ],
  },
  {
    label: "Operar",
    items: [
      { label: "Migração de dados", href: "#/migracao", icon: ArrowRightLeft },
      { label: "Agentes", href: "#/agentes", icon: Network },
      { label: "Deploys", href: "#/deploys", icon: Rocket },
    ],
  },
  {
    label: "Investigar",
    items: [
      { label: "Logs", href: "#/logs", icon: ScrollText },
      { label: "Traces", href: "#/traces", icon: Waypoints },
      {
        label: "Agentes de IA",
        href: "#/ia",
        icon: Bot,
        // Execuções, replay, ferramentas e preços são abas da mesma tela.
        match: (r) => r === "/ia" || r.startsWith("/ia/"),
      },
    ],
  },
  {
    label: "Reagir",
    items: [
      { label: "Alertas", href: "#/alerts", icon: BellRing },
      { label: "Canais de alerta", href: "#/notify", icon: Send },
    ],
  },
  {
    label: "Sintéticos",
    items: [{ label: "Websites & Jornadas", href: "#/websites", icon: Globe }],
  },
  {
    label: "Administração",
    items: [
      {
        label: "Usuários & Acessos",
        href: "#/users",
        icon: Users,
        adminOnly: true,
      },
      {
        label: "MCP (agentes de IA)",
        href: "#/mcp",
        icon: Bot,
        adminOnly: true,
      },
      {
        label: "Atualização dos agentes",
        href: "#/admin/agent-updates",
        icon: ArrowUpCircle,
        adminOnly: true,
      },
      {
        label: "Auditoria",
        href: "#/audit",
        icon: ClipboardList,
        adminOnly: true,
      },
      { label: "TVs & Playlists", href: "#/admin/tvs", icon: Tv },
      { label: "Guia do Painel", href: "#/help", icon: CircleHelp },
    ],
  },
];

// filtrarNav monta o menu que a pessoa vê: aplica a busca e, sobretudo, ESCONDE os
// itens admin-only de quem não é admin. Função pura e exportada de propósito — é
// aqui que se prova, em teste, que a tela de Atualização dos agentes não aparece
// para quem não pode operá-la. (O servidor também barra; isto evita mostrar um
// caminho que terminaria em 403.)
export function filtrarNav(
  nav: NavGroup[],
  admin: boolean,
  busca: string,
): NavGroup[] {
  const q = busca.trim().toLowerCase();
  return nav
    .map((group) => ({
      ...group,
      items: group.items.filter(
        (it) =>
          (!it.adminOnly || admin) &&
          (q === "" || it.label.toLowerCase().includes(q)),
      ),
    }))
    .filter((group) => group.items.length > 0);
}

// Caminho da rota a partir do href do item ("#/hosts" -> "/hosts").
function pathOf(href: string): string {
  return href.slice(1);
}

function isActive(item: NavItem, route: string): boolean {
  if (item.match) return item.match(route);
  return route === pathOf(item.href);
}

// Título mostrado na topbar para a rota atual.
function titleFor(route: string): string {
  if (route.startsWith("/d/")) {
    if (route.endsWith("/edit")) return "Editar dashboard";
    if (route.endsWith("/versions")) return "Histórico de versões";
    return "Dashboard";
  }
  for (const g of NAV) {
    for (const it of g.items) {
      if (isActive(it, route)) return it.label;
    }
  }
  return TITULOS_FORA_DO_MENU[route]?.titulo ?? "Início";
}

// Rotas fora do menu lateral (abertas pelo menu do usuário ou por links).
const TITULOS_FORA_DO_MENU: Record<string, { titulo: string; grupo: string }> = {
  "/seguranca": { titulo: "Segurança da conta", grupo: "Conta" },
};

// Grupo do menu da rota atual ("Observar", "Reagir"…), para o caminho no topo.
function groupFor(route: string): string | null {
  for (const g of NAV) {
    for (const it of g.items) {
      if (isActive(it, route)) return g.label ?? null;
    }
  }
  if (TITULOS_FORA_DO_MENU[route]) return TITULOS_FORA_DO_MENU[route].grupo;
  return route.startsWith("/d/") ? "Observar" : null;
}

// Mac mostra ⌘; o resto do mundo, Ctrl.
const TECLA_MOD =
  typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform)
    ? "⌘"
    : "Ctrl";

// Nome do usuário a partir do JWT em memória; se não der, cai no papel.
function currentUserLabel(): string {
  const t = getAccessToken();
  if (t) {
    try {
      const part = t.split(".")[1];
      const json = atob(part.replace(/-/g, "+").replace(/_/g, "/"));
      const payload = JSON.parse(json) as Record<string, unknown>;
      const name = payload.username ?? payload.sub ?? payload.name;
      if (typeof name === "string" && name) return name;
    } catch {
      /* token não decodificável — segue para o papel */
    }
  }
  return getRole() ?? "Usuário";
}

export function AppShell({
  route,
  children,
}: {
  route: string;
  children: ReactNode;
}) {
  const [collapsed, setCollapsed] = useState<boolean>(() => {
    try {
      return localStorage.getItem("painel-sidebar") === "collapsed";
    } catch {
      return false;
    }
  });
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [theme, setThemeState] = useState<Theme>(() => getTheme());
  const [menuOpen, setMenuOpen] = useState(false);
  const [menuQuery, setMenuQuery] = useState("");
  const menuRef = useRef<HTMLDivElement>(null);

  // Estado real da conexão + relógio próprio para o "há Xs" do selo. Sem o
  // relógio, a idade só mudaria quando a CONEXÃO mudasse de estado — ou seja,
  // ficaria parada em "ao vivo · 3s" por horas. Pausa com a aba escondida, como
  // todo o resto do painel (hooks/usePolling.ts).
  const conexao = useConexao();
  const [, redesenhar] = useState(0);
  useEffect(() => {
    const t = setInterval(() => {
      if (document.visibilityState === "hidden") return;
      // Tela parada (Segurança, um formulário…) não gera tráfego: sem a sonda o
      // selo diria "ao vivo · 300s", afirmando o que ninguém mediu.
      const idade = idadeUltimoSucesso();
      if (idade == null || idade >= IDADE_PARA_SONDAR) void sondarConexao().catch(() => {});
      redesenhar((n) => n + 1);
    }, 10000);
    return () => clearInterval(t);
  }, []);
  const idadeSucesso = idadeUltimoSucesso();

  // Filtra os grupos/itens do menu pela busca (padrão do painel exemplo.com.br) e
  // esconde itens admin-only de quem não é admin (o backend também barra, isto é só
  // para não mostrar um menu que levaria a 403).
  const isAdmin = getRole() === "admin";
  const filteredNav = filtrarNav(NAV, isAdmin, menuQuery);

  // Persiste o estado colapsado da sidebar.
  function toggleCollapsed() {
    setCollapsed((c) => {
      const next = !c;
      try {
        localStorage.setItem("painel-sidebar", next ? "collapsed" : "expanded");
      } catch {
        /* ignore */
      }
      return next;
    });
  }

  // Fecha o drawer ao navegar.
  useEffect(() => {
    setDrawerOpen(false);
  }, [route]);

  // Esc fecha drawer e menu do usuário.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setDrawerOpen(false);
        setMenuOpen(false);
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  // Clique fora fecha o menu do usuário.
  useEffect(() => {
    if (!menuOpen) return;
    const onClick = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node))
        setMenuOpen(false);
    };
    document.addEventListener("mousedown", onClick);
    return () => document.removeEventListener("mousedown", onClick);
  }, [menuOpen]);

  const toggleTheme = useCallback(() => {
    setThemeState((atual) => {
      const next: Theme = atual === "dark" ? "light" : "dark";
      setTheme(next);
      return next;
    });
  }, []);

  // As telas do menu entram na paleta (Ctrl+K) com o mesmo filtro de admin do menu.
  const navPaleta = useMemo<ItemPaleta[]>(
    () =>
      filtrarNav(NAV, isAdmin, "").flatMap((g) =>
        g.items.map((it) => ({
          id: `nav:${it.href}`,
          rotulo: it.label,
          grupo: "Telas",
          icone: it.icon,
          chaves: g.label ?? "",
          detalhe: g.label,
          acao: () => {
            window.location.hash = it.href.slice(1);
            window.dispatchEvent(new Event("revoada:paleta-fechar"));
          },
        })),
      ),
    [isAdmin],
  );
  const grupoAtual = groupFor(route);

  const sidebarClass = [
    "sidebar",
    collapsed ? "sidebar--collapsed" : "",
    drawerOpen ? "sidebar--drawer-open" : "",
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <div className="shell">
      {drawerOpen && (
        <div
          className="sidebar__backdrop"
          onClick={() => setDrawerOpen(false)}
          aria-hidden="true"
        />
      )}

      <aside className={sidebarClass}>
        <div className="sidebar__brand">
          <BrandLogo variant="full" className="sidebar__brand-logo" />
          <button
            type="button"
            className="sidebar__collapse-btn"
            aria-label={collapsed ? "Expandir menu" : "Recolher menu"}
            title={collapsed ? "Expandir menu" : "Recolher menu"}
            onClick={toggleCollapsed}
          >
            {collapsed ? (
              <PanelLeftOpen size={18} />
            ) : (
              <PanelLeftClose size={18} />
            )}
          </button>
        </div>

        <input
          className="field sidebar__search"
          type="search"
          placeholder="Pesquisar no menu…"
          aria-label="Pesquisar no menu"
          value={menuQuery}
          onChange={(e) => setMenuQuery(e.target.value)}
        />

        <nav className="sidebar__nav" aria-label="Navegação principal">
          {filteredNav.map((group, gi) => (
            <div key={gi}>
              {group.label && (
                <div className="sidebar__group-label">{group.label}</div>
              )}
              {group.items.map((item) => {
                const active = isActive(item, route);
                const Icon = item.icon;
                return (
                  <a
                    key={item.href}
                    href={item.href}
                    className={`sidebar__item${active ? " sidebar__item--active" : ""}`}
                    aria-current={active ? "page" : undefined}
                    // O title vale SEMPRE, não só com o menu recolhido: com o
                    // rótulo cortado por reticências, ele é o único jeito de ler
                    // o nome inteiro.
                    title={item.label}
                  >
                    {/* A pílula da marca é UM elemento que desliza de um item para
                        o outro (layoutId): o olho acompanha para onde foi. */}
                    {active && (
                      <m.span
                        className="sidebar__item-pill"
                        layoutId="nav-ativo"
                        transition={{
                          type: "spring",
                          stiffness: 520,
                          damping: 42,
                        }}
                        aria-hidden="true"
                      />
                    )}
                    <span className="sidebar__item-icon">
                      <Icon size={17} aria-hidden={true} />
                    </span>
                    <span className="sidebar__item-label">{item.label}</span>
                  </a>
                );
              })}
            </div>
          ))}
          {filteredNav.length === 0 && (
            <div className="sidebar__empty">
              Nenhum item corresponde à busca.
            </div>
          )}
        </nav>

        {/* Rodapé do menu: estado REAL da conexão, alimentado pelo tráfego da
            própria app (ver web/src/conexao.ts). Antes eram duas mentiras fixas
            no código — um selo "ao vivo" que nunca mudava, mesmo com o servidor
            fora do ar, e um "v0.7.0" escrito à mão que envelheceu sozinho e há
            versões não corresponde ao que está instalado. Indicador que só sabe
            dizer "tudo bem" engana justamente quando alguém precisa dele. */}
        <div className="sidebar__foot">
          <FreshnessIndicator
            status={conexao === "no-ar" ? "live" : "reconnecting"}
            ageSeconds={
              conexao === "no-ar" ? (idadeSucesso ?? undefined) : undefined
            }
          />
          <span className="sidebar__foot-text">
            {conexao === "no-ar"
              ? "Painel respondendo normalmente"
              : "Sem resposta do painel, tentando de novo"}
          </span>
        </div>
      </aside>

      <div className="shell__main">
        <header className="topbar">
          <button
            type="button"
            className="shell-iconbtn topbar__hamburger"
            aria-label="Abrir menu"
            onClick={() => setDrawerOpen(true)}
          >
            <Menu size={18} />
          </button>
          <span className="topbar__title">
            {grupoAtual && <span className="topbar__crumb">{grupoAtual}</span>}
            <span className="topbar__page">{titleFor(route)}</span>
          </span>
          <span className="topbar__spacer" />

          <button
            type="button"
            className="topbar__search"
            onClick={() => window.dispatchEvent(new Event("revoada:paleta"))}
            aria-label="Ir para uma tela ou servidor"
            aria-keyshortcuts="Control+K Meta+K"
          >
            <Search size={15} aria-hidden={true} />
            <span className="topbar__search-text">Ir para…</span>
            <kbd className="topbar__kbd">{TECLA_MOD} K</kbd>
          </button>

          <button
            type="button"
            className="shell-iconbtn"
            aria-label={
              theme === "dark"
                ? "Mudar para tema claro"
                : "Mudar para tema escuro"
            }
            title={theme === "dark" ? "Tema claro" : "Tema escuro"}
            onClick={toggleTheme}
          >
            {theme === "dark" ? <Sun size={18} /> : <Moon size={18} />}
          </button>

          <div className="usermenu" ref={menuRef}>
            <button
              type="button"
              className="usermenu__trigger"
              // No celular o nome some (só o avatar cabe); o rótulo acessível fica.
              aria-label={`Conta: ${currentUserLabel()}`}
              aria-haspopup="menu"
              aria-expanded={menuOpen}
              onClick={() => setMenuOpen((o) => !o)}
            >
              <span className="usermenu__avatar">
                <User size={15} aria-hidden={true} />
              </span>
              <span className="usermenu__label">{currentUserLabel()}</span>
            </button>
            {menuOpen && (
              <div className="usermenu__panel" role="menu">
                <div className="usermenu__info">
                  Papel: {NOME_PAPEL[getRole() ?? ""] ?? "—"}
                </div>
                <a
                  className="usermenu__action"
                  role="menuitem"
                  href="#/seguranca"
                  onClick={() => setMenuOpen(false)}
                >
                  <ShieldCheck size={16} aria-hidden={true} />
                  Segurança da conta
                </a>
                <button
                  type="button"
                  className="usermenu__action"
                  role="menuitem"
                  onClick={() =>
                    logout().then(() => (window.location.hash = "/"))
                  }
                >
                  <LogOut size={16} aria-hidden={true} />
                  Sair
                </button>
              </div>
            )}
          </div>
        </header>

        <main className="shell__content">
          <AvisoMFA rota={route} />
          {children}
        </main>
      </div>
      <CommandPalette
        navegacao={navPaleta}
        tema={theme}
        alternarTema={toggleTheme}
      />
    </div>
  );
}

// AvisoMFA lembra administradores e operadores sem 2FA de que ele é obrigatório para
// as ações críticas — com o atalho para resolver na hora.
function AvisoMFA({ rota }: { rota: string }) {
  const [me, setMe] = useState<MeResponse | null>(getMe());
  useEffect(() => {
    const atualizar = () => setMe(getMe());
    window.addEventListener("revoada:me", atualizar);
    window.addEventListener("revoada:mfa-necessario", atualizar);
    return () => {
      window.removeEventListener("revoada:me", atualizar);
      window.removeEventListener("revoada:mfa-necessario", atualizar);
    };
  }, []);
  if (!me?.mfa_obrigatorio || me.mfa_ativo || rota === "/seguranca")
    return null;
  return (
    <div className="aviso-mfa" role="status">
      <ShieldAlert size={18} aria-hidden={true} />
      <span>
        <strong>Ative a verificação em duas etapas.</strong> Ela é obrigatória
        para o seu papel e libera ações como executar migrações e deploys.
      </span>
      <a className="btn btn--primary" href="#/seguranca">
        Ativar agora
      </a>
    </div>
  );
}

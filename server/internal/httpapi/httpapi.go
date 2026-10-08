// Package httpapi monta o roteador do server (auth + query + metadados).
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/agents"
	"github.com/eduardorarruda/revoada/server/internal/alerting"
	"github.com/eduardorarruda/revoada/server/internal/audit"
	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/authz"
	"github.com/eduardorarruda/revoada/server/internal/canal"
	"github.com/eduardorarruda/revoada/server/internal/config"
	"github.com/eduardorarruda/revoada/server/internal/dashboards"
	"github.com/eduardorarruda/revoada/server/internal/deploys"
	"github.com/eduardorarruda/revoada/server/internal/discovery"
	"github.com/eduardorarruda/revoada/server/internal/hostadmin"
	"github.com/eduardorarruda/revoada/server/internal/ia"
	"github.com/eduardorarruda/revoada/server/internal/installer"
	"github.com/eduardorarruda/revoada/server/internal/inventory"
	"github.com/eduardorarruda/revoada/server/internal/journey"
	"github.com/eduardorarruda/revoada/server/internal/live"
	"github.com/eduardorarruda/revoada/server/internal/logs"
	"github.com/eduardorarruda/revoada/server/internal/mcpsrv"
	"github.com/eduardorarruda/revoada/server/internal/migracao"
	"github.com/eduardorarruda/revoada/server/internal/notify"
	"github.com/eduardorarruda/revoada/server/internal/provision"
	"github.com/eduardorarruda/revoada/server/internal/query"
	"github.com/eduardorarruda/revoada/server/internal/sitecheck"
	"github.com/eduardorarruda/revoada/server/internal/statuspage"
	"github.com/eduardorarruda/revoada/server/internal/traces"
	"github.com/eduardorarruda/revoada/server/internal/tv"
	"github.com/eduardorarruda/revoada/server/internal/useradmin"
)

type Deps struct {
	Canal      *canal.Handler
	Migracao   *migracao.Handler
	MCP        *mcpsrv.Servidor
	Deploys    *deploys.Handler
	Log        *slog.Logger
	Auth       *auth.Handler
	Query      *query.Handler
	Dashboards *dashboards.Handler
	Inventory  *inventory.Handler
	Live       *live.Handler
	TV         *tv.Handler
	Alerting   *alerting.Handler
	Notify     *notify.Handler
	SiteCheck  *sitecheck.Handler
	Logs       *logs.Handler
	Traces     *traces.Handler
	IA         *ia.Handler
	Discovery  *discovery.Handler
	Journey    *journey.Handler
	Status     *statuspage.Handler
	Agents     *agents.Handler
	// AgentUpdate atende a auto-atualização dos agentes. Autenticado pela SERVERKEY
	// do agente, não por sessão — logo não passa por admin()/protected(), do mesmo
	// jeito que a inscrição. nil desliga a rota (e a frota fica só com o rollout por SSH).
	AgentUpdate *agents.UpdateHandler
	Installer   *installer.Handler
	Provision   *provision.Handler
	HostAdmin   *hostadmin.Handler
	UserAdmin   *useradmin.Handler
	Audit       *audit.Recorder // grava a trilha; nil desliga a auditoria
	AuditAPI    *audit.Handler  // consulta da trilha (tela Auditoria, admin-only)
	Authz       *authz.Resolver
	DeployToken string
	ReadyCH     func(ctx context.Context) error
	ReadyPG     func(ctx context.Context) error
}

// New monta o handler HTTP completo.
func New(d Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		checks := map[string]string{"clickhouse": "ok", "postgres": "ok"}
		ready := true
		if err := d.ReadyCH(ctx); err != nil {
			checks["clickhouse"] = err.Error()
			ready = false
		}
		if err := d.ReadyPG(ctx); err != nil {
			checks["postgres"] = err.Error()
			ready = false
		}
		code := 200
		if !ready {
			code = 503
		}
		writeJSON(w, code, map[string]any{"ready": ready, "checks": checks})
	})

	// Auth (públicos + refresh/logout usam o cookie). Rate-limit POR IP para conter
	// brute-force/credential-stuffing no login e criação em massa no register.
	authRL := newIPRateLimiter(10, time.Minute) // 10 tentativas/min por IP
	// Cadastro de usuário NÃO passa pelos wrappers admin/protected (a checagem de
	// papel é interna ao handler), então a auditoria entra aqui explicitamente —
	// senão criar usuário por esta rota ficaria fora da trilha. OptionalAuth
	// identifica o admin quando ele manda o Bearer; no bootstrap (1º usuário do
	// sistema) não há token e o registro fica como anônimo, que é o correto.
	var register http.Handler = http.HandlerFunc(authRL.wrap(d.Auth.Register))
	if d.Audit != nil {
		register = d.Auth.OptionalAuth(d.Audit.Middleware(register))
	}
	mux.Handle("POST /api/auth/register", register)
	mux.HandleFunc("POST /api/auth/login", authRL.wrap(d.Auth.Login))
	mux.HandleFunc("GET /api/auth/estado", authRL.wrap(d.Auth.Estado))

	// Inscrição de servidor (instalador universal): PÚBLICA por necessidade — a
	// máquina que está entrando ainda não tem credencial nenhuma. O que a protege é
	// o token de inscrição no corpo, o limite por IP (contém quem fica adivinhando)
	// e a trilha de auditoria, que registra cada servidor que entra. Mesmo cuidado
	// do register: como não passa pelos wrappers admin/protected, a auditoria entra
	// aqui explicitamente, senão a inscrição ficaria fora do rastro.
	if d.Agents != nil {
		var enroll http.Handler = http.HandlerFunc(authRL.wrap(d.Agents.Enroll))
		if d.Audit != nil {
			enroll = d.Audit.Middleware(enroll)
		}
		mux.Handle("POST /api/enroll", enroll)
	}

	// Auto-atualização: o agente pergunta de hora em hora se há versão nova. Sem
	// sessão — a credencial é a serverkey no corpo, como na inscrição — então o
	// freio também é por IP. Sem esta rota registrada, todo o mecanismo de
	// atualização existe e fica INERTE: o agente pergunta e leva 404 para sempre,
	// e a frota continua sendo atualizada só por SSH, um host de cada vez.
	//
	// BALDE PRÓPRIO, e não o do login. Vários agentes saem pelo MESMO IP quando estão
	// atrás de um NAT — é a topologia normal de um cliente com dez servidores —, e
	// dividir o balde de 10/min do login tinha duas consequências medidas: do 11º
	// agente em diante a resposta era 429 (inclusive para a ordem de desinstalação, que
	// o painel mostrava como "aguardando"), e o admin daquele mesmo IP não conseguia
	// entrar no painel porque a frota tinha comido o orçamento. São perguntas de
	// natureza diferente: aqui o limite existe contra abuso, não contra adivinhação de
	// senha, e cada agente pergunta ~1×/h (12×/h no pior caso da consulta antecipada).
	agentRL := newIPRateLimiter(120, time.Minute)
	if d.AgentUpdate != nil {
		mux.HandleFunc("POST /api/agent/update-check", agentRL.wrap(d.AgentUpdate.Check))
	}
	// 2ª etapa do login (código do app ou de recuperação): mesmo freio por IP do login.
	mux.HandleFunc("POST /api/auth/mfa/verificar", authRL.wrap(d.Auth.VerificarMFA))
	mux.HandleFunc("POST /api/auth/refresh", authRL.wrap(d.Auth.Refresh))
	mux.HandleFunc("POST /api/auth/logout", d.Auth.Logout)

	// Protegidos (Bearer access token) + rate-limit por usuário. O limite precisa
	// acomodar o padrão real da UI: um dashboard multi-host + Explore + polling dispara
	// dezenas de /api/query e /api/metrics por ciclo. 120/min (2/s) era baixo demais e
	// gerava 429 em uso normal; 1200/min (20/s) dá folga e ainda barra abuso de um único
	// usuário autenticado (login/register têm limite por IP à parte).
	rl := newRateLimiter(1200, time.Minute)
	// withScope resolve o escopo de servidores do usuário (a partir das claims já
	// injetadas por RequireAuth) e o coloca no contexto. Todos os construtores de SQL do
	// ClickHouse e consultas Postgres host-scoped leem esse escopo para filtrar. Em erro
	// de banco, NEGA o acesso (fail-closed) — melhor 500 que vazar dados de outro grupo.
	withScope := func(h http.Handler) http.Handler {
		if d.Authz == nil {
			return h
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := auth.ClaimsFrom(r.Context())
			if !ok {
				http.Error(w, "não autenticado", http.StatusUnauthorized)
				return
			}
			scope, err := d.Authz.Resolve(r.Context(), claims.Sub, claims.Role)
			if err != nil {
				http.Error(w, "erro ao resolver permissões", http.StatusInternalServerError)
				return
			}
			h.ServeHTTP(w, r.WithContext(authz.WithScope(r.Context(), scope)))
		})
	}
	// audita embrulha um handler com o registro da trilha de auditoria. Fica DENTRO
	// do RequireAuth (as claims já estão no contexto → sabemos quem fez) e envolve o
	// handler final (sabemos o status da resposta). Só grava métodos de escrita.
	// Aplicado nos wrappers abaixo, cobre TODA rota de alteração — inclusive as que
	// forem criadas depois, sem ninguém precisar lembrar de instrumentar.
	audita := func(h http.Handler) http.Handler {
		if d.Audit == nil {
			return h
		}
		return d.Audit.Middleware(h)
	}
	protected := func(h http.HandlerFunc) http.Handler {
		return d.Auth.RequireAuth(rl.middleware(withScope(audita(http.HandlerFunc(h)))))
	}
	// admin: escrita host-scoped e objetos globais são admin-only na v1 (plano §1.6/§6).
	// O antigo wrapper editor() foi removido — os papéis hoje são só admin|user, então
	// exigir "editor" já equivalia a exigir admin; o flag can_edit por servidor fica
	// gravado (Scope.CanEdit) para quando a edição for aberta a usuário comum.
	admin := func(h http.HandlerFunc) http.Handler {
		return d.Auth.RequireAuth(rl.middleware(auth.RequireRole("admin", withScope(audita(http.HandlerFunc(h))))))
	}
	// exige protege rotas pela matriz de permissões (ARQUITETURA §14). Nas permissões
	// críticas também cobra 2FA (admin/operador) e reautenticação recente.
	exige := func(p auth.Permissao, h http.HandlerFunc) http.Handler {
		return d.Auth.RequireAuth(rl.middleware(auth.Exige(p, withScope(audita(http.HandlerFunc(h))))))
	}
	mux.Handle("GET /api/me", protected(d.Auth.Me))
	mux.Handle("POST /api/auth/change-password", protected(d.Auth.ChangePassword))
	// Segurança da conta: 2FA e reautenticação. Freio por IP além do por usuário —
	// errar o código conta como falha de login (bloqueio progressivo da conta).
	mux.Handle("POST /api/auth/mfa/iniciar", protected(d.Auth.IniciarMFA))
	mux.Handle("POST /api/auth/mfa/confirmar", protected(authRL.wrap(d.Auth.ConfirmarMFA)))
	mux.Handle("POST /api/auth/mfa/desativar", protected(d.Auth.DesativarMFA))
	mux.Handle("POST /api/auth/reautenticar", protected(authRL.wrap(d.Auth.Reautenticar)))
	mux.Handle("POST /api/auth/sair-de-tudo", protected(d.Auth.SairDeTudo))
	if d.Live != nil {
		mux.Handle("GET /api/live", d.Live) // auth por query-param (WS)
	}
	mux.Handle("POST /api/query", protected(d.Query.Query))
	mux.Handle("GET /api/metrics", protected(d.Query.Metrics))
	mux.Handle("GET /api/label-values", protected(d.Query.LabelValues))

	// Dashboards: leitura para qualquer autenticado (dados filtrados via /api/query);
	// escrita é admin-only na v1.
	if d.Dashboards != nil {
		mux.Handle("GET /api/dashboards", protected(d.Dashboards.List))
		mux.Handle("GET /api/dashboards/{uid}", protected(d.Dashboards.Get))
		mux.Handle("GET /api/dashboards/{uid}/versions", protected(d.Dashboards.Versions))
		mux.Handle("POST /api/dashboards", admin(d.Dashboards.Create))
		mux.Handle("PUT /api/dashboards/{uid}", admin(d.Dashboards.Update))
		mux.Handle("DELETE /api/dashboards/{uid}", admin(d.Dashboards.Delete))
		mux.Handle("POST /api/dashboards/{uid}/rollback", admin(d.Dashboards.Rollback))
		mux.Handle("POST /api/dashboards/starter", admin(d.Dashboards.Starter))
	}

	// Inventário (P2.6/P2.7): hosts detalhados + timeline de eventos.
	if d.Inventory != nil {
		mux.Handle("GET /api/hosts", protected(d.Inventory.Hosts))
		mux.Handle("GET /api/host-metrics", protected(d.Inventory.HostMetrics))
		mux.Handle("PATCH /api/hosts/{hostname}", admin(d.Inventory.UpdateHost))
		mux.Handle("GET /api/events", protected(d.Inventory.Timeline))
		mux.Handle("GET /api/health-wall", protected(d.Inventory.HealthWall))
	}

	// TV/Kiosk (P3.1): admin gerencia tokens; resolve/query são públicos (auth por token).
	// Limiares de saúde (Fase A): config global + override por host. Admin, pois
	// muda a semântica do semáforo do Wall/TV para todos os usuários.
	if d.Inventory != nil {
		mux.Handle("GET /api/host-thresholds", admin(d.Inventory.ListHostThresholds))
		mux.Handle("PUT /api/host-thresholds", admin(d.Inventory.UpdateHostThresholds))
	}
	// Apagar servidor por completo (agente via SSH + dados PG/CH). Destrutivo → admin.
	if d.HostAdmin != nil {
		mux.Handle("POST /api/hosts/{hostname}/delete", admin(d.HostAdmin.Delete))
	}

	// Trilha de auditoria: quem alterou o quê e quando. Leitura admin-only — é o
	// registro do que os usuários fizeram, não pode ficar exposto a todo mundo.
	if d.AuditAPI != nil {
		mux.Handle("GET /api/audit", admin(d.AuditAPI.List))
		mux.Handle("GET /api/audit/integridade", admin(d.AuditAPI.Integridade))
	}

	// Canal com os agentes (ARQUITETURA §7): inscrição, presença e tarefas ao vivo.
	if d.Canal != nil {
		mux.Handle("GET /api/agentes", protected(d.Canal.ListarAgentes))
		mux.Handle("POST /api/agentes/tokens", exige(auth.PermGerenciarAgentes, d.Canal.CriarToken))
		mux.Handle("POST /api/agentes/{id}/revogar", exige(auth.PermGerenciarAgentes, d.Canal.Revogar))
		mux.Handle("GET /api/tarefas", protected(d.Canal.ListarTarefas))
		mux.Handle("GET /api/tarefas/{id}", protected(d.Canal.Tarefa))
		mux.Handle("POST /api/tarefas/{id}/ao-vivo", protected(d.Canal.AoVivo))
		mux.Handle("POST /api/tarefas/{id}/controle", exige(auth.PermExecutarMigracao, d.Canal.Controlar))
		mux.Handle("POST /api/tarefas/diagnostico", exige(auth.PermGerenciarAgentes, d.Canal.CriarDiagnostico))
	}

	// Migração de dados (ARQUITETURA §9): conexões (senha no cofre), captura de schema pelo
	// agente, projetos e versões do mapeamento.
	if d.Deploys != nil {
		// Deploy multi-SO (Etapa 10). A GitHub Action usa o token de deploy (conta de
		// serviço); a tela usa sessão com rodar_deploy + 2FA + reautenticação.
		if d.DeployToken != "" {
			mux.Handle("POST /api/deploys/acao", serviceAuth(d.DeployToken, d.Deploys.CriarPelaAction))
			mux.Handle("GET /api/deploys/acao/{id}", serviceAuth(d.DeployToken, d.Deploys.Status))
		}
		mux.Handle("POST /api/deploys", exige(auth.PermRodarDeploy, d.Deploys.CriarPelaTela))
		mux.Handle("GET /api/deploys", exige(auth.PermVer, d.Deploys.Listar))
		mux.Handle("GET /api/deploys/{id}", exige(auth.PermVer, d.Deploys.Status))
	}
	if d.MCP != nil {
		// /mcp tem autenticação própria (token MCP com escopo, não sessão de usuário).
		mux.Handle("/mcp", d.MCP.Handler())
		mux.Handle("GET /api/mcp/tokens", admin(d.MCP.ListarTokens)) // ler a lista: admin; criar/revogar: 2FA + reauth
		mux.Handle("POST /api/mcp/tokens", exige(auth.PermGerenciarUsuarios, d.MCP.CriarToken))
		mux.Handle("POST /api/mcp/tokens/{id}/revogar", exige(auth.PermGerenciarUsuarios, d.MCP.RevogarToken))
	}
	if d.Migracao != nil {
		m := d.Migracao
		mux.Handle("GET /api/migracao/conexoes", exige(auth.PermVer, m.ListarConexoes))
		mux.Handle("POST /api/migracao/conexoes", exige(auth.PermGerenciarConexoes, m.CriarConexao))
		mux.Handle("DELETE /api/migracao/conexoes/{id}", exige(auth.PermGerenciarConexoes, m.ApagarConexao))
		mux.Handle("POST /api/migracao/conexoes/{id}/capturar", exige(auth.PermEditarMapeamento, m.Capturar))
		mux.Handle("GET /api/migracao/conexoes/{id}/esquema", exige(auth.PermVer, m.UltimoEsquema))
		mux.Handle("GET /api/migracao/projetos", exige(auth.PermVer, m.ListarProjetos))
		mux.Handle("POST /api/migracao/projetos", exige(auth.PermEditarMapeamento, m.CriarProjeto))
		mux.Handle("GET /api/migracao/projetos/{id}", exige(auth.PermVer, m.Projeto))
		mux.Handle("GET /api/migracao/projetos/{id}/versoes/{n}", exige(auth.PermVer, m.Versao))
		mux.Handle("POST /api/migracao/projetos/{id}/sugerir", exige(auth.PermEditarMapeamento, m.Sugerir))
		mux.Handle("PUT /api/migracao/projetos/{id}/mapeamento", exige(auth.PermEditarMapeamento, m.SalvarMapeamento))
		mux.Handle("POST /api/migracao/projetos/{id}/aprovar", exige(auth.PermExecutarMigracao, m.Aprovar))
		mux.Handle("GET /api/migracao/projetos/{id}/execucoes", exige(auth.PermVer, m.Execucoes))
		mux.Handle("POST /api/migracao/projetos/{id}/simular", exige(auth.PermExecutarMigracao, m.Simular))
		mux.Handle("POST /api/migracao/projetos/{id}/executar", exige(auth.PermExecutarMigracao, m.Executar))
		mux.Handle("POST /api/migracao/projetos/{id}/reverter", exige(auth.PermExecutarMigracao, m.Reverter))
		mux.Handle("POST /api/migracao/projetos/{id}/verificar", exige(auth.PermExecutarMigracao, m.Verificar))
		mux.Handle("GET /api/migracao/tarefas/{id}/checkpoints", exige(auth.PermVer, m.Checkpoints))
		mux.Handle("GET /api/migracao/projetos/{id}/upgrade", exige(auth.PermVer, m.SituacaoUpgrade))
		mux.Handle("POST /api/migracao/projetos/{id}/diagnosticar", exige(auth.PermExecutarMigracao, m.Diagnosticar))
		mux.Handle("POST /api/migracao/projetos/{id}/atualizar", exige(auth.PermExecutarMigracao, m.Atualizar))
		mux.Handle("POST /api/migracao/projetos/{id}/descartar", exige(auth.PermExecutarMigracao, m.DescartarUpgrade))
	}

	// Usuários, grupos de servidores e permissões por servidor (Fase G2). Tudo admin-only:
	// é o painel que decide quem vê/edita/recebe alerta de cada servidor.
	if d.UserAdmin != nil {
		mux.Handle("GET /api/users", admin(d.UserAdmin.ListUsers))
		mux.Handle("POST /api/users", exige(auth.PermGerenciarUsuarios, d.UserAdmin.CreateUser))
		mux.Handle("PATCH /api/users/{id}", exige(auth.PermGerenciarUsuarios, d.UserAdmin.PatchUser))
		mux.Handle("DELETE /api/users/{id}", exige(auth.PermGerenciarUsuarios, d.UserAdmin.DeleteUser))
		mux.Handle("GET /api/users/{id}/perms", admin(d.UserAdmin.GetUserPerms))
		mux.Handle("PUT /api/users/{id}/perms", exige(auth.PermGerenciarUsuarios, d.UserAdmin.SetUserPerms))
		mux.Handle("GET /api/server-groups", admin(d.UserAdmin.ListGroups))
		mux.Handle("POST /api/server-groups", admin(d.UserAdmin.CreateGroup))
		mux.Handle("PUT /api/server-groups/{id}", admin(d.UserAdmin.UpdateGroup))
		mux.Handle("DELETE /api/server-groups/{id}", admin(d.UserAdmin.DeleteGroup))
	}

	// Rotas públicas de TV: autenticadas só pelo token da TV, portanto FORA do
	// rl.middleware (que é por usuário autenticado). Sem um freio próprio elas ficavam
	// sem nenhum: medido em dev, 400 POSTs seguidos em /api/tv/query passaram com ZERO
	// 429, enquanto a rota autenticada equivalente barra a partir de 1200/min. Um link
	// de kiosk vazado era, além de leitura, um gerador de carga ilimitada no ClickHouse.
	//
	// A chave do balde é o TOKEN (hash), não o IP: várias TVs saem pelo mesmo NAT do
	// escritório e não podem se estrangular umas às outras; e um token vazado gasta o
	// próprio balde, não o de todo mundo. Sem token, cai no IP.
	//
	// 600/min por token dá folga larga para o padrão real (um mural com ~20 painéis
	// recarregando a cada 5 s faz ~240/min) e ainda corta uma enxurrada.
	tvRL := newIPRateLimiter(600, time.Minute)
	tvPublic := func(h http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !tvRL.allow(tvBucketKey(r)) {
				http.Error(w, "muitas requisições", http.StatusTooManyRequests)
				return
			}
			h(w, r)
		})
	}

	if d.TV != nil {
		mux.Handle("POST /api/tv/tokens", admin(d.TV.Create))
		mux.Handle("GET /api/tv/tokens", admin(d.TV.List))
		mux.Handle("POST /api/tv/tokens/revoke", admin(d.TV.Revoke))
		mux.Handle("DELETE /api/tv/tokens/{id}", admin(d.TV.DeleteToken))
		mux.Handle("POST /api/tv/tokens/reload", admin(d.TV.Reload))
		mux.Handle("POST /api/tv/tokens/regenerate", admin(d.TV.Regenerate))
		mux.Handle("POST /api/tv/tokens/set-playlist", admin(d.TV.SetPlaylist))
		mux.Handle("POST /api/tv/playlists", admin(d.TV.CreatePlaylist))
		mux.Handle("GET /api/tv/playlists", admin(d.TV.ListPlaylists))
		mux.Handle("DELETE /api/tv/playlists/{id}", admin(d.TV.DeletePlaylist))
		mux.Handle("GET /api/tv/resolve", tvPublic(d.TV.Resolve))
		mux.Handle("POST /api/tv/query", tvPublic(d.TV.Query))
		mux.Handle("GET /api/tv/status", tvPublic(d.TV.Status))
		mux.Handle("GET /api/tv/wall", tvPublic(d.TV.Wall))
	}

	// Chaves de agente (serverkeys): gestão restrita a admin (guardam credencial
	// de ingestão). O agente coleta e envia; a chave o autentica no gateway.
	if d.Agents != nil {
		mux.Handle("GET /api/agents", admin(d.Agents.List))
		mux.Handle("POST /api/agents", admin(d.Agents.Create))
		mux.Handle("POST /api/agents/revoke", admin(d.Agents.Revoke))
		mux.Handle("POST /api/agents/delete", admin(d.Agents.Delete))
		// Revelar UMA serverkey. É POST (e não GET) para cair na trilha de auditoria: a
		// listagem passou a vir mascarada, então este é o único caminho até a chave em
		// claro depois da criação — e ele deixa rastro de quem pegou qual.
		mux.Handle("POST /api/agents/reveal", admin(d.Agents.Reveal))

		// Freio da auto-atualização. Global (desligar/fixar versão) e por servidor
		// (segurar um host). Admin: decide se a frota inteira troca de binário.
		mux.Handle("GET /api/agent/update-policy", admin(d.Agents.UpdatePolicy))
		mux.Handle("PUT /api/agent/update-policy", admin(d.Agents.SetUpdatePolicy))
		mux.Handle("POST /api/agents/update-hold", admin(d.Agents.SetUpdateHold))

		// Tokens do instalador universal. Vivem separados das chaves de ingestão de
		// propósito: um cria chaves, o outro envia telemetria.
		mux.Handle("GET /api/enroll-tokens", admin(d.Agents.ListEnrollTokens))
		mux.Handle("POST /api/enroll-tokens", admin(d.Agents.CreateEnrollToken))
		mux.Handle("POST /api/enroll-tokens/revoke", admin(d.Agents.RevokeEnrollToken))
		mux.Handle("POST /api/enroll-tokens/delete", admin(d.Agents.DeleteEnrollToken))
	}

	// Instalador pronto do agente: escolhe-se o sistema operacional e baixa-se UM
	// arquivo com a chave de ingestão já dentro. É POST porque cria a chave — uma
	// escrita, e a trilha de auditoria só registra escritas.
	if d.Installer != nil {
		mux.Handle("POST /api/agent-installer", admin(d.Installer.Baixar))
	}

	// Onboarding SSH + cofre cifrado (Fase G): provisiona/atualiza agente via SSH,
	// com progresso via SSE. Admin: manuseia credenciais e faz SSH de saída.
	if d.Provision != nil {
		mux.Handle("POST /api/provision/start", admin(d.Provision.Start))
		mux.Handle("GET /api/provision/targets", admin(d.Provision.Targets))
		mux.Handle("POST /api/provision/targets/{id}/update", admin(d.Provision.Update))
		// Limites de recurso do agente (cerca da unit systemd + soft caps): admin edita
		// os defaults aplicados a novos (re)provisionamentos e ao comando de instalação.
		mux.Handle("GET /api/agent/resource-limits", admin(d.Provision.ResourceLimits))
		mux.Handle("PUT /api/agent/resource-limits", admin(d.Provision.SetResourceLimits))
	}

	// Anotação de deploy (P4.5): service account (token de deploy), não JWT.
	if d.Inventory != nil && d.DeployToken != "" {
		mux.Handle("POST /api/events/deploy", serviceAuth(d.DeployToken, d.Inventory.Deploy))
	}

	// Alerting (P4.1): leitura para autenticados (filtrada por escopo); escrita/ack é
	// admin-only na v1 (abrir a usuário comum com can_edit é a evolução prevista no plano §6).
	if d.Alerting != nil {
		mux.Handle("GET /api/alert-rules", protected(d.Alerting.ListRules))
		mux.Handle("POST /api/alert-rules", admin(d.Alerting.CreateRule))
		mux.Handle("PUT /api/alert-rules/{id}", admin(d.Alerting.UpdateRule))
		mux.Handle("DELETE /api/alert-rules/{id}", admin(d.Alerting.DeleteRule))
		mux.Handle("POST /api/alert-rules/preview", admin(d.Alerting.Preview))
		mux.Handle("GET /api/alerts", protected(d.Alerting.ListAlerts))
		mux.Handle("POST /api/alerts/{id}/ack", admin(d.Alerting.Ack))
		mux.Handle("POST /api/alerts/{id}/resolve", admin(d.Alerting.Resolve))
		// Containers ignorados: parados de propósito, deixam de alertar (admin).
		mux.Handle("GET /api/alerts/ignored-containers", admin(d.Alerting.ListIgnored))
		mux.Handle("POST /api/alerts/ignored-containers", admin(d.Alerting.Ignore))
		mux.Handle("DELETE /api/alerts/ignored-containers/{host}/{container}", admin(d.Alerting.Unignore))
	}

	// Notificações (P4.2): canais + integração WhatsApp. Rotas de notificação,
	// plantão, escalonamento, silêncios e histórico de envios foram DESCONTINUADAS
	// (o produto passou a entregar alertas direto nos canais). Escrita/teste de canal
	// exige admin (canais guardam segredos: SMTP, bot token).
	if d.Notify != nil {
		// Lista de canais é admin-only na v1: é o admin quem cria e vincula os canais
		// pessoais. Evolução prevista (plano §5): usuário comum listar SÓ os próprios
		// canais pessoais, para conferir o destino dos seus alertas.
		mux.Handle("GET /api/notify/channels", admin(d.Notify.ListChannels))
		mux.Handle("POST /api/notify/channels", admin(d.Notify.CreateChannel))
		mux.Handle("PUT /api/notify/channels/{id}", admin(d.Notify.UpdateChannel))
		mux.Handle("DELETE /api/notify/channels/{id}", admin(d.Notify.DeleteChannel))
		mux.Handle("POST /api/notify/channels/{id}/test", admin(d.Notify.TestChannel))
		mux.Handle("GET /api/notify/whatsapp-integration", admin(d.Notify.GetWhatsAppIntegration))
		mux.Handle("PUT /api/notify/whatsapp-integration", admin(d.Notify.UpdateWhatsAppIntegration))
		// Histórico de envios (auditoria): admin-only. O registro não carrega o host do
		// alerta, então não dá para filtrar por escopo sem vazar assuntos de servidores que
		// o usuário não vê — restringir a admin é a escolha segura da v1 (plano §5).
		mux.Handle("GET /api/notify/log", admin(d.Notify.Log))
	}

	// Checks de website/LP (P4.4): leitura para autenticados (sites são globais);
	// escrita é admin-only na v1.
	if d.SiteCheck != nil {
		mux.Handle("GET /api/site-checks", protected(d.SiteCheck.List))
		mux.Handle("GET /api/site-checks/overview", protected(d.SiteCheck.Overview))
		mux.Handle("POST /api/site-checks", admin(d.SiteCheck.Create))
		mux.Handle("PUT /api/site-checks/{id}", admin(d.SiteCheck.Update))
		mux.Handle("DELETE /api/site-checks/{id}", admin(d.SiteCheck.Delete))
		mux.Handle("GET /api/site-checks/{id}/history", protected(d.SiteCheck.History))
		mux.Handle("GET /api/site-checks/probes", protected(d.SiteCheck.Probes))
	}

	// Logs (P5.2): busca/histograma/contexto/padrões + live tail (auth por query) +
	// métricas derivadas de busca (admin para criar, na v1).
	if d.Logs != nil {
		// Conteúdo cru de log é filtrado por host (escopo do usuário): qualquer
		// autenticado busca/contextualiza, mas só dos servidores que pode ver. Admin vê
		// tudo (predicado vazio). O medidor (storage) também aplica o escopo no handler.
		mux.Handle("GET /api/logs/search", protected(d.Logs.Search))
		mux.Handle("GET /api/logs/histogram", protected(d.Logs.Histogram))
		mux.Handle("GET /api/logs/context", protected(d.Logs.Context))
		mux.Handle("GET /api/logs/patterns", protected(d.Logs.Patterns))
		mux.Handle("GET /api/logs/services", protected(d.Logs.Services))
		mux.Handle("GET /api/logs/sources", protected(d.Logs.Sources))
		mux.Handle("GET /api/logs/tail", http.HandlerFunc(d.Logs.Tail)) // WS: auth por access token na query (papel checado no handler)
		mux.Handle("GET /api/logs/metrics", protected(d.Logs.ListMetrics))
		mux.Handle("POST /api/logs/metrics", admin(d.Logs.CreateMetric))
		mux.Handle("DELETE /api/logs/metrics/{id}", admin(d.Logs.DeleteMetric))
		// Armazenamento de logs: medidor (leitura para qualquer autenticado) +
		// limite/expurgo (admin — destrutivo, sempre preso a labels['host']).
		mux.Handle("GET /api/logs/storage", protected(d.Logs.Storage))
		mux.Handle("PUT /api/logs/storage-limit", admin(d.Logs.SetStorageLimit))
		// Preview do expurgo é POST: o trecho procurado (`body_like`) É o dado vazado, e
		// em GET ele ia na URL — que o nginx registra em `$request`, que o agente coleta
		// do stdout do container e devolve para dentro da tabela `logs`. Caçar o
		// vazamento recriava o vazamento. Ver o cabeçalho de logs/purge.go.
		mux.Handle("POST /api/logs/purge/preview", admin(d.Logs.PurgePreview))
		mux.Handle("POST /api/logs/purge", admin(d.Logs.Purge))
		mux.Handle("GET /api/logs/purge/status", admin(d.Logs.PurgeStatus))
		// Destravar uma limpeza que falhou de forma permanente (KILL MUTATION). Sem
		// isto, um 409 "já existe uma limpeza em andamento" ficava para sempre.
		mux.Handle("POST /api/logs/purge/cancel", admin(d.Logs.PurgeCancel))
	}

	// Traces (P5.3): busca, waterfall, service map, exemplars.
	if d.Traces != nil {
		mux.Handle("GET /api/traces/search", protected(d.Traces.Search))
		mux.Handle("GET /api/traces/service-map", protected(d.Traces.ServiceMap))
		mux.Handle("GET /api/traces/exemplars", protected(d.Traces.Exemplars))
		mux.Handle("GET /api/correlate", protected(d.Traces.Correlate))
		mux.Handle("GET /api/traces/{trace_id}", protected(d.Traces.Get))
		mux.Handle("GET /api/traces/{trace_id}/logs", protected(d.Traces.Logs))
		// Expurgo total de traces (TRUNCATE spans). Destrutivo → admin-only.
		mux.Handle("POST /api/traces/purge", admin(d.Traces.Purge))
	}

	// Agentes de IA (ADR 008): custo, replay, ferramentas e tabela de preços. Ler o
	// conteúdo (prompt/resposta) tem permissão própria — leitor vê custo, não o texto.
	if d.IA != nil {
		mux.Handle("GET /api/ia/resumo", protected(d.IA.ResumoHTTP))
		mux.Handle("GET /api/ia/execucoes", protected(d.IA.ExecucoesHTTP))
		mux.Handle("GET /api/ia/execucoes/{trace_id}", protected(d.IA.ExecucaoHTTP))
		mux.Handle("GET /api/ia/execucoes/{trace_id}/conteudo", exige(auth.PermVerConteudoIA, d.IA.ConteudoHTTP))
		mux.Handle("GET /api/ia/ferramentas", protected(d.IA.FerramentasHTTP))
		mux.Handle("GET /api/ia/precos", protected(d.IA.PrecosHTTP))
		mux.Handle("POST /api/ia/precos", admin(d.IA.CriarPrecoHTTP))
		mux.Handle("DELETE /api/ia/precos/{id}", admin(d.IA.ApagarPrecoHTTP))
		mux.Handle("POST /api/ia/purge", admin(d.IA.PurgeHTTP))
	}

	// Status page pública (P6.4): sem autenticação, cache curto.
	if d.Status != nil {
		mux.Handle("GET /api/status", http.HandlerFunc(d.Status.Status))
	}

	// Jornadas de browser (P6.4): leitura autenticada; escrita admin-only na v1.
	if d.Journey != nil {
		mux.Handle("GET /api/journeys", protected(d.Journey.List))
		mux.Handle("POST /api/journeys", admin(d.Journey.Create))
		mux.Handle("DELETE /api/journeys/{id}", admin(d.Journey.Delete))
	}

	// Auto-discovery (P6.2): sugestões filtradas por escopo; aplicar é admin-only na v1.
	if d.Discovery != nil {
		mux.Handle("GET /api/discovery", protected(d.Discovery.List))
		mux.Handle("GET /api/discovery/suggestions", protected(d.Discovery.Suggestions))
		mux.Handle("POST /api/discovery/apply", admin(d.Discovery.Apply))
	}

	return mux
}

// serviceAuth protege endpoints de service account por um Bearer token fixo
// (comparação em tempo constante), independente do JWT de usuário.
func serviceAuth(token string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const p = "Bearer "
		authz := r.Header.Get("Authorization")
		if !strings.HasPrefix(authz, p) || subtle.ConstantTimeCompare([]byte(authz[len(p):]), []byte(token)) != 1 {
			http.Error(w, "não autorizado", http.StatusUnauthorized)
			return
		}
		h(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// rateLimiter simples: N requisições por janela por usuário (claims.Sub).
type rateLimiter struct {
	mu     sync.Mutex
	counts map[int64]counter
	limit  int
	window time.Duration
}

type counter struct {
	n     int
	reset time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{counts: map[int64]counter{}, limit: limit, window: window}
}

func (rl *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := auth.ClaimsFrom(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if !rl.allow(c.Sub) {
			http.Error(w, "muitas requisições", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (rl *rateLimiter) allow(user int64) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cur := rl.counts[user]
	if now.After(cur.reset) {
		cur = counter{n: 0, reset: now.Add(rl.window)}
	}
	if cur.n >= rl.limit {
		rl.counts[user] = cur
		return false
	}
	cur.n++
	rl.counts[user] = cur
	return true
}

// ipRateLimiter limita por IP de origem (para endpoints não autenticados como login).
// Varre entradas expiradas a cada chamada, para o mapa não crescer com IPs rotativos.
type ipRateLimiter struct {
	mu     sync.Mutex
	counts map[string]counter
	limit  int
	window time.Duration
}

func newIPRateLimiter(limit int, window time.Duration) *ipRateLimiter {
	return &ipRateLimiter{counts: map[string]counter{}, limit: limit, window: window}
}

// trustedProxyNets são as redes cujo X-Forwarded-For pode ser acreditado. Lidas UMA
// vez (a lista vem de env e não muda em runtime); entrada inválida é ignorada.
var trustedProxyNets = parseCIDRs(config.TrustedProxies())

func parseCIDRs(list []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(list))
	for _, c := range list {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// ipIsTrustedProxy diz se um IP pertence a alguma rede de proxy confiável.
func ipIsTrustedProxy(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range trustedProxyNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// peerIsTrusted diz se o peer DIRETO da conexão é um proxy declarado como confiável.
func peerIsTrusted(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	return ipIsTrustedProxy(net.ParseIP(host))
}

// clientIP identifica o cliente para o rate-limit por IP (login/registro/refresh).
//
// O X-Forwarded-For é escrito por QUEM quiser: se ele for lido sem antes conferir de
// onde a conexão veio, o balde deixa de existir. Medido em dev, na versão anterior
// desta função: 15 logins errados do mesmo IP deram 401×10 + 429×5; as 6 tentativas
// seguintes, só trocando `X-Forwarded-For: 203.0.113.N`, deram 401×6 e NENHUM 429 —
// brute-force ilimitado com um header. O gateway já fazia certo (isTrusted antes de
// olhar o XFF); o server, não.
//
// Regra agora, a mesma do gateway: o XFF só é olhado quando o peer direto pertence a
// REVOADA_TRUSTED_PROXIES. Peer não confiável ⇒ o header é ignorado por completo e o
// balde é o RemoteAddr, que o cliente não escolhe.
//
// Sendo confiável, lemos da DIREITA para a ESQUERDA descartando os saltos que também
// são proxies confiáveis (a cadeia real em produção é cliente → Traefik → nginx →
// server, e cada salto ANEXA o anterior, então os últimos elementos são IPs internos).
// O primeiro que NÃO é proxy confiável é o cliente que a borda observou; o que o
// cliente tenha inventado fica à esquerda disso e nunca é alcançado.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !peerIsTrusted(r.RemoteAddr) {
		return host
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ip := net.ParseIP(strings.TrimSpace(parts[i]))
			if ip == nil {
				continue
			}
			if ipIsTrustedProxy(ip) {
				continue // salto interno da própria cadeia de proxies
			}
			return ip.String()
		}
	}
	return host
}

// tvBucketKey devolve a chave do balde das rotas públicas de TV. Usa o HASH do token
// (nunca o token em claro, que não deve virar chave de mapa nem sair em log/pprof) e
// cai no IP quando a requisição chega sem token — que é o caso de quem está sondando.
func tvBucketKey(r *http.Request) string {
	if tok := r.URL.Query().Get("token"); tok != "" {
		return "tv:" + auth.HashToken(tok)
	}
	return "ip:" + clientIP(r)
}

func (rl *ipRateLimiter) wrap(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !rl.allow(clientIP(r)) {
			http.Error(w, "muitas tentativas, tente novamente em instantes", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

func (rl *ipRateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	for k, c := range rl.counts { // sweep das entradas expiradas
		if now.After(c.reset) {
			delete(rl.counts, k)
		}
	}
	cur := rl.counts[ip]
	if now.After(cur.reset) {
		cur = counter{n: 0, reset: now.Add(rl.window)}
	}
	if cur.n >= rl.limit {
		rl.counts[ip] = cur
		return false
	}
	cur.n++
	rl.counts[ip] = cur
	return true
}

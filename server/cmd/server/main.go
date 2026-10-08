// Comando server: API de query, autenticação e metadados do Revoada.
//
//	REVOADA_SERVER_ADDR   porta HTTP            (default :8091)
//	REVOADA_PG_DSN        PostgreSQL            (usuários/sessões)
//	REVOADA_CH_*          ClickHouse            (leitura de telemetria)
//	REVOADA_JWT_SECRET    segredo dos tokens    (OBRIGATÓRIO em produção)
package main

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"github.com/eduardorarruda/revoada/server/internal/blindagem"
	"golang.org/x/crypto/acme/autocert"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/agents"
	"github.com/eduardorarruda/revoada/server/internal/alerting"
	"github.com/eduardorarruda/revoada/server/internal/audit"
	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/authz"
	"github.com/eduardorarruda/revoada/server/internal/canal"
	"github.com/eduardorarruda/revoada/server/internal/chquery"
	"github.com/eduardorarruda/revoada/server/internal/config"
	"github.com/eduardorarruda/revoada/server/internal/dashboards"
	"github.com/eduardorarruda/revoada/server/internal/deploys"
	"github.com/eduardorarruda/revoada/server/internal/discovery"
	"github.com/eduardorarruda/revoada/server/internal/hostadmin"
	"github.com/eduardorarruda/revoada/server/internal/httpapi"
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
	"github.com/eduardorarruda/revoada/server/internal/segredos"
	"github.com/eduardorarruda/revoada/server/internal/sitecheck"
	"github.com/eduardorarruda/revoada/server/internal/statuspage"
	"github.com/eduardorarruda/revoada/server/internal/store"
	"github.com/eduardorarruda/revoada/server/internal/traces"
	"github.com/eduardorarruda/revoada/server/internal/tv"
	"github.com/eduardorarruda/revoada/server/internal/useradmin"
	"github.com/eduardorarruda/revoada/server/internal/webui"
)

func painel() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if len(os.Args) > 1 && os.Args[1] == "migracoes" {
		os.Exit(comandoMigracoes(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		// Ponte stdio → /mcp do painel (Claude Desktop/Code, IDEs). Não abre banco:
		// tudo passa pelas regras e pela auditoria do painel.
		if err := mcpsrv.Ponte(context.Background(), os.Getenv("REVOADA_URL"), os.Getenv("REVOADA_MCP_TOKEN"), os.Stdin, os.Stdout, nil); err != nil {
			fmt.Fprintln(os.Stderr, "revoada mcp:", err)
			os.Exit(1)
		}
		return
	}

	// Fail-fast: fora de desenvolvimento, um REVOADA_JWT_SECRET inseguro aborta o boot.
	if err := config.ValidateSecrets(); err != nil {
		log.Error("CONFIGURAÇÃO INSEGURA", "err", err)
		os.Exit(1)
	}
	// Em dev, ainda avisa de forma proeminente sobre segredos no default inseguro.
	for _, warn := range config.InsecureDefaults() {
		log.Warn("CONFIGURAÇÃO INSEGURA", "aviso", warn)
	}
	// Cofre de credenciais SSH sem chave dedicada: rotacionar o JWT (operação de
	// rotina) tornaria TODA credencial guardada indecifrável, em silêncio — e o erro
	// só apareceria meses depois, no dia de reprovisionar um servidor. Não aborta o
	// boot de propósito: a produção de hoje não define a variável, e derrubar o painel
	// seria trocar um risco latente por uma queda certa. Ligar a variável depois é
	// seguro (o cofre ainda lê o que foi cifrado com a chave antiga).
	if aviso := provision.AvisoChaveDeCofre(); aviso != "" && !config.IsDevEnv() {
		log.Warn("CONFIGURAÇÃO INSEGURA", "aviso", aviso)
	}

	ctx, stop := signal.NotifyContext(contextoRaiz, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Connect(ctx, config.Postgres())
	if err != nil {
		log.Error("PostgreSQL", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	// Semeia o dashboard turnkey "Visão do Host (genérico)" (idempotente): garante
	// que todo servidor já tenha uma tela pronta, sem o usuário criar nada à mão.
	// Painéis gerados antes de 25/09/2026 nasceram com travessão no título
	// ("Visão do Host — srv"); o padrão agora é vírgula. Idempotente.
	if n, err := dashboards.NormalizarTravessao(ctx, st); err != nil {
		log.Warn("não foi possível normalizar os títulos dos painéis de sistema", "err", err)
	} else if n > 0 {
		log.Info("títulos dos painéis de sistema normalizados", "painéis", n)
	}

	if created, err := dashboards.EnsureGeneric(ctx, st); err != nil {
		log.Warn("não foi possível semear o dashboard genérico", "err", err)
	} else if created {
		log.Info("dashboard genérico 'Visão do Host' semeado", "uid", dashboards.GenericHostUID)
	}

	chAddr, chUser, chPass, chDB := config.ClickHouse()
	ch := chquery.New(chAddr, chUser, chPass, chDB)
	qh := query.NewHandler(ch)

	// Resolver de escopo por usuário (permissões por servidor). TTL de 30s: uma mudança
	// de permissão vale no máximo em 30s, ou na hora quando o useradmin invalida o cache.
	authzResolver := authz.NewResolver(st, 30*time.Second)

	// Deep links de host nas notificações caem no dashboard genérico por padrão
	// (?var-host=<host>). REVOADA_HOST_DASHBOARD_UID sobrescreve se houver outro.
	hostDashUID := config.HostDashboardUID()
	if hostDashUID == "" {
		hostDashUID = dashboards.GenericHostUID
	}
	// Router de notificações: entrega alertas direto nos canais habilitados. Rotas de
	// notificação, escalonamento e plantão foram descontinuados — não há mais loop de
	// agrupamento (router.Run) nem escalonador para subir.
	router := notify.NewRouter(st, log, config.PublicURL(), hostDashUID)

	// Vigia do WhatsApp: avisa por e-mail (e WhatsApp, quando ainda der) se o wuzapi
	// cair ou parar de entregar. Só para quem está em REVOADA_VIGIA_*.
	vigia := notify.NewVigia(notify.VigiaConfigDoAmbiente(os.Getenv), st, st, log)
	router.ObservarEntregas(vigia.RegistrarEnvio)
	go vigia.Run(ctx)

	evaluator := alerting.New(st, qh, ch, log, router)
	go evaluator.Run(ctx)

	go sitecheck.NewChecker(st, ch, router, log).Run(ctx)
	go journey.NewRunner(st, ch, router, log).Run(ctx)

	// Retenção automática das tabelas de série (site_check_results, notification_log).
	retDays, ok := config.RetentionDays()
	if !ok {
		log.Warn("REVOADA_RETENTION_DAYS inválido; usando default", "dias", retDays)
	}
	go st.RunRetention(ctx, retDays, log)

	logsH := logs.NewHandler(ch, st, config.JWTSecret(), authzResolver)
	go logs.NewMetricsRunner(st, ch, log).Run(ctx)

	// Runner SSH compartilhado entre provisionar (instalar) e apagar (desinstalar).
	sshRunner := provision.NewSSHRunner()

	// Artefatos publicados (binários do agente + VERSION), lidos do MESMO diretório
	// que o instalador serve. O checksum que o agente verifica sai daqui, calculado
	// do arquivo realmente servido — nunca de um valor digitado à mão, que é como
	// uma atualização passa a apontar para um binário que não é o que se pensa.
	artefatos := installer.NewArtefatos(os.DirFS(config.AgentDistDir()))

	// Emissor de eventos de ciclo de vida (provision/update) para a timeline do host.
	// Fecho sobre o chquery já construído: mantém o pacote provision desacoplado do
	// ClickHouse. Best-effort — loga e segue; nunca aborta o provisionamento.
	provisionEvent := func(ctx context.Context, host, kind, title string) {
		if err := ch.InsertEvent(ctx, "default", kind, title, "", map[string]string{"host": host}); err != nil {
			log.Warn("provision: gravar evento de ciclo de vida na timeline falhou",
				"kind", kind, "host", host, "err", err)
		}
	}

	// A exclusão de servidor deixa uma varredura de rescaldo agendada (ver
	// hostadmin/rescaldo.go). Retomá-la no boot é o que faz a limpeza sobreviver a um
	// reinício dentro da janela — deploy automático acontece o tempo todo por aqui.
	hostAdmin := hostadmin.NewHandler(st, ch, sshRunner, log)
	hostAdmin.ResumeSweeps(ctx)
	// Faxina das ordens de auto-desinstalação: apaga a chave depois que o agente
	// confirmou (com carência) e desiste da ordem que nunca foi cumprida.
	go hostAdmin.RunUninstallJanitor(ctx)

	// Cofre de segredos (criptografia em envelope; chave mestra fora do banco). Sem ele
	// o painel não sobe: guardar segredo em texto puro não é uma opção (ARQUITETURA §13).
	cofreSegredos, err := segredos.Abrir(log)
	if err != nil {
		log.Error("cofre de segredos indisponível", "err", err)
		os.Exit(1)
	}
	authHandler := auth.NewHandler(st, config.JWTSecret())
	authHandler.UsarCofre(cofreSegredos)

	// Canal com os agentes (ARQUITETURA §7): CA interna, inscrição, gRPC sobre mTLS, presença
	// e tarefas. A CA e a chave que assina as tarefas ficam no banco, cifradas pelo cofre.
	autoridade, err := canal.CarregarAutoridade(ctx, st, cofreSegredos, config.CanalHosts(), time.Now())
	if err != nil {
		log.Error("canal: autoridade certificadora", "err", err)
		os.Exit(1)
	}
	canalSvc := canal.NovoServico(st, autoridade, log)
	canalSvc.AoMudarPresenca = avisoPresencaAgente(router, ch, log)
	// As tarefas de migração levam a senha das conexões selada para o agente que roda
	// (ligado ANTES de o canal subir: a reconciliação já pode reenviar tarefas).
	migHandler := migracao.NovoHandler(st, cofreSegredos, canalSvc)
	gravadorAuditoria := audit.NewRecorder(ctx, st, log)
	canalSvc.Seladora = migHandler.Selar
	canalSvc.AoFinalizar = aoFinalizarTarefa(router, ch, log)
	go canalSvc.MonitorarPresenca(ctx, 5*time.Second)
	go func() {
		if err := canalSvc.Servir(ctx, config.CanalAddr()); err != nil {
			log.Error("canal: servidor dos agentes parou", "err", err)
		}
	}()

	// Agentes de IA: semeia a tabela de preços de referência (uma vez por origem) e
	// fecha por minuto as séries llm.* que os alertas comuns observam.
	iaH := ia.New(ch, st, gravadorAuditoria)
	if n, err := iaH.SemearReferencia(ctx); err != nil {
		log.Warn("ia: semeando tabela de preços de referência", "err", err)
	} else if n > 0 {
		log.Info("ia: tabela de preços de referência importada", "precos", n)
	}
	go ia.NewRunner(iaH, log).Run(ctx)

	handler := httpapi.New(httpapi.Deps{
		Log:        log,
		Auth:       authHandler,
		Query:      qh,
		Dashboards: dashboards.NewHandler(st),
		Inventory:  inventory.NewHandler(st, ch),
		Live:       live.NewHandler(log, qh, config.JWTSecret(), authzResolver),
		TV:         tv.NewHandler(st, qh, ch),
		Alerting:   alerting.NewHandler(st, evaluator),
		Notify:     notify.NewHandler(st, router),
		SiteCheck:  sitecheck.NewHandler(st),
		Logs:       logsH,
		Traces:     traces.NewHandler(ch),
		IA:         iaH,
		Discovery:  discovery.NewHandler(st),
		Journey:    journey.NewHandler(st),
		Status:     statuspage.NewHandler(st),
		Agents:     agents.NewHandler(st, log),
		// A FonteDePolitica era `nil` aqui — e nil significa "atualize sempre que houver
		// versão nova". Como o deploy do painel é automático, o gatilho para trocar o
		// binário de TODA a frota era um `git push`: em ~1h todo host com panel_url
		// baixava a versão nova, sem canário e sem botão de pânico. Ligada ao banco, a
		// política passa a existir de verdade — desligamento global, versão fixada e
		// "segure este host" (ver agents/policy.go e as rotas /api/agent/update-policy).
		AgentUpdate: agents.NewUpdateHandler(st, artefatos, config.PublicURL(), agents.NewPoliticaStore(st), st, log),
		Installer:   installer.NewHandler(st, config.AgentDistDir(), config.GatewayPublicURL(), config.PublicURL(), log),
		Provision:   provision.NewHandler(st, sshRunner, config.AgentInstallURL(), config.GatewayPublicURL(), log, provisionEvent),
		HostAdmin:   hostAdmin,
		UserAdmin:   useradmin.NewHandler(st, log, authzResolver.Invalidate),
		Audit:       gravadorAuditoria,
		Deploys:     deploys.NovoHandler(canalSvc),
		MCP:         mcpsrv.Novo(st, canalSvc, migHandler, iaH, gravadorAuditoria, log),
		AuditAPI:    audit.NewHandler(st),
		Canal:       canal.NovoHandler(canalSvc, config.CanalEnderecoPublico()),
		Migracao:    migHandler,
		Authz:       authzResolver,
		DeployToken: config.DeployToken(),
		ReadyCH:     ch.Ping,
		ReadyPG:     st.Ping,
	})

	// Rotas de operação fora do mux da API: /metrics (Prometheus, com token) envolto
	// pela mesma blindagem do resto.
	raiz := http.NewServeMux()
	raiz.Handle("/", comInterface(handler, log))
	raiz.HandleFunc("GET /metrics", metricasProtegidas(config.MetricasToken()))
	certArq, chaveArq := config.TLSArquivos()
	dominio := config.TLSDominio()
	comTLS := (certArq != "" && chaveArq != "") || dominio != ""
	blindado := blindagem.Envolver(raiz, blindagem.Config{
		OrigemPainel: config.PublicURL(), OrigensCORS: config.CORSOrigens(),
		HTTPSAtras: comTLS || config.HTTPSNoProxy(), PermitirLocalhost: config.IsDevEnv(), Log: log,
	})
	srv := &http.Server{
		Addr: config.HTTPAddr(), Handler: blindado,
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute,
		// Sem WriteTimeout: SSE e WebSocket ficam abertos por minutos de propósito.
	}
	go servirHTTP(srv, log, certArq, chaveArq, dominio)

	<-ctx.Done()
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
	log.Info("server encerrado")
}

// avisoPresencaAgente transforma "agente caiu/voltou" em alerta pelos canais de
// notificação existentes (e-mail, WhatsApp, Telegram, webhook) e em evento na linha
// do tempo do servidor (ARQUITETURA §15: o painel avisa quando um agente cai).
func avisoPresencaAgente(router *notify.Router, ch *chquery.Client, log *slog.Logger) func(store.Agente, string, string) {
	regra := store.AlertRule{Name: "Agente do Revoada sem contato", Severity: "critical"}
	return func(a store.Agente, de, para string) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		labels := map[string]string{"host": a.Hostname, "agente": a.ID}
		switch {
		case para == canal.Offline:
			router.Notify(ctx, alerting.Notification{Rule: regra, Labels: labels, State: "firing", Since: time.Now(),
				Evidence: "o agente parou de mandar sinal de vida há mais de 30 segundos (rede caiu, servidor desligado ou serviço parado)"})
		case de == canal.Offline && para == canal.Online:
			router.Notify(ctx, alerting.Notification{Rule: regra, Labels: labels, State: "resolved", Since: time.Now()})
		default:
			return
		}
		titulo := "Agente sem contato"
		if para == canal.Online {
			titulo = "Agente reconectado"
		}
		if err := ch.InsertEvent(ctx, "default", "agente", titulo, "", labels); err != nil {
			log.Warn("canal: evento de presença na linha do tempo", "agente", a.ID, "err", err)
		}
	}
}

// aoFinalizarTarefa: deploy terminado vira anotação nos gráficos do servidor (linha do
// tempo do ClickHouse) e, se falhou ou foi revertido, notificação pelos canais.
func aoFinalizarTarefa(router *notify.Router, ch *chquery.Client, log *slog.Logger) func(store.Agente, store.Tarefa) {
	regra := store.AlertRule{Name: "Deploy falhou", Severity: "warning"}
	return func(a store.Agente, t store.Tarefa) {
		if t.Tipo != deploys.Tarefa {
			return
		}
		var esp struct{ Aplicacao, Versao, Ambiente string }
		_ = json.Unmarshal(t.Especificacao, &esp)
		var res struct{ Revertido bool }
		_ = json.Unmarshal(t.Resumo, &res)
		situacao := map[string]string{"sucesso": "no ar", "falha": "falhou", "cancelada": "cancelado", "recusada": "recusado"}[t.Estado]
		if res.Revertido {
			situacao = "falhou e foi revertido"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		labels := map[string]string{"host": a.Hostname, "service": esp.Aplicacao, "version": esp.Versao, "origem": t.Origem}
		if esp.Ambiente != "" {
			labels["environment"] = esp.Ambiente
		}
		titulo := "deploy " + esp.Aplicacao + " " + esp.Versao + ": " + situacao
		if err := ch.InsertEvent(ctx, "default", "deploy", titulo, t.Erro, labels); err != nil {
			log.Warn("deploy: anotação na linha do tempo", "tarefa", t.ID, "err", err)
		}
		if t.Estado != "sucesso" {
			router.Notify(ctx, alerting.Notification{Rule: regra, Labels: labels, State: "firing", Since: time.Now(), Evidence: titulo + " — " + t.Erro})
		}
	}
}

// comandoMigracoes: revoada-painel migracoes [listar | aplicar | desfazer <versão>]
// "desfazer 3" roda os rollbacks de tudo ACIMA da versão 3. Faça backup antes.
func comandoMigracoes(args []string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	st, err := store.ConectarSemMigrar(ctx, config.Postgres())
	if err != nil {
		fmt.Fprintln(os.Stderr, "PostgreSQL:", err)
		return 1
	}
	defer st.Close()
	acao := "listar"
	if len(args) > 0 {
		acao = args[0]
	}
	switch acao {
	case "listar":
		est, err := st.EstadoMigracoes(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		for _, e := range est {
			situacao := "pendente"
			if e.Aplicada {
				situacao = "aplicada em " + e.AplicadaEm.Format(time.RFC3339)
			}
			fmt.Printf("%04d  %-14s  %s\n", e.Versao, e.Nome, situacao)
		}
	case "aplicar":
		feitas, err := st.Migrar(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("aplicadas:", feitas)
	case "desfazer":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "uso: revoada-painel migracoes desfazer <versão que deve ficar> (ex.: 3)")
			return 2
		}
		alvo, err := strconv.Atoi(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "versão inválida:", args[1])
			return 2
		}
		desfeitas, err := st.Desfazer(ctx, alvo)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("desfeitas:", desfeitas)
	default:
		fmt.Fprintln(os.Stderr, "uso: revoada-painel migracoes [listar | aplicar | desfazer <versão>]")
		return 2
	}
	return 0
}

// servirHTTP sobe o painel com HTTPS próprio (certificado em arquivo ou Let's Encrypt
// automático) ou em HTTP atrás de um proxy com HTTPS (item 1 da lista de segurança).
func servirHTTP(srv *http.Server, log *slog.Logger, cert, chave, dominio string) {
	var err error
	switch {
	case dominio != "":
		m := &autocert.Manager{
			Prompt: autocert.AcceptTOS, HostPolicy: autocert.HostWhitelist(dominio),
			Cache: autocert.DirCache(filepath.Join(config.DataDir(), "acme")),
		}
		srv.TLSConfig = m.TLSConfig()
		srv.TLSConfig.MinVersion = tls.VersionTLS12
		// :80 só responde ao desafio do Let's Encrypt e manda o resto para HTTPS.
		go func() { _ = http.ListenAndServe(":80", m.HTTPHandler(nil)) }() //nolint:gosec // só redireciona
		log.Info("painel ouvindo com HTTPS (Let's Encrypt)", "addr", srv.Addr, "dominio", dominio)
		err = srv.ListenAndServeTLS("", "")
	case cert != "" && chave != "":
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		log.Info("painel ouvindo com HTTPS", "addr", srv.Addr)
		err = srv.ListenAndServeTLS(cert, chave)
	default:
		if !config.HTTPSNoProxy() && !config.IsDevEnv() {
			log.Warn("CONFIGURAÇÃO INSEGURA", "aviso", "painel em HTTP puro: configure REVOADA_TLS_CERT/REVOADA_TLS_CHAVE, REVOADA_TLS_DOMINIO ou um proxy com HTTPS (REVOADA_HTTPS_NO_PROXY=1)")
		}
		log.Info("painel ouvindo", "addr", srv.Addr)
		err = srv.ListenAndServe()
	}
	if err != nil && err != http.ErrServerClosed {
		log.Error("listen", "err", err)
		os.Exit(1)
	}
}

// metricasProtegidas exige o token de métricas (Bearer); sem token configurado, só
// aceita chamadas da própria máquina.
func metricasProtegidas(token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
				http.Error(w, "token de métricas inválido", http.StatusUnauthorized)
				return
			}
		} else if host, _, _ := net.SplitHostPort(r.RemoteAddr); host != "127.0.0.1" && host != "::1" {
			http.Error(w, "defina REVOADA_METRICAS_TOKEN para ler as métricas de fora", http.StatusForbidden)
			return
		}
		blindagem.Metricas(w, r)
	}
}

// comInterface põe a interface web embutida (Etapa 9: binário único) na frente da API:
// GET fora de /api, /mcp e das sondas vai para a SPA. REVOADA_WEB_EMBUTIDA=false
// desliga (quando um nginx serve o web/dist, como no compose de produção).
func comInterface(api http.Handler, log *slog.Logger) http.Handler {
	if strings.EqualFold(os.Getenv("REVOADA_WEB_EMBUTIDA"), "false") {
		return api
	}
	if !webui.Embutida() {
		log.Info("interface web não embutida neste binário (só a API); veja server/internal/webui")
	}
	ui := webui.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && !webui.ÉDaAPI(r.URL.Path) {
			ui.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	})
}

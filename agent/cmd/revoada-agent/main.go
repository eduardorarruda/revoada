// Comando revoada-agent: coleta métricas do host e envia via OTLP ao gateway.
//
//	revoada-agent [-config /etc/revoada/agent.yaml]   # loop de coleta contínuo (default; systemd)
//	revoada-agent once                             # coleta métricas UMA vez e sai (cron/cPanel)
//	revoada-agent discover                         # envia descoberta de serviços uma vez e sai (cron opcional)
//	revoada-agent doctor                           # diagnóstico
//	revoada-agent version
//	revoada-agent enroll -token <TOKEN> -panel <URL>   # troca um token de inscrição pela chave e a imprime
//	revoada-agent inscrever -painel <host:7443> -token <rvd1...>   # inscreve no canal (mTLS) e grava a identidade
//
// Há ainda um modo sem subcomando: quando o painel gera um INSTALADOR, ele cola
// uma receita (chave + gateway) no fim deste binário. Rodado sem argumentos, o
// agente detecta a receita e se instala sozinho. Ver internal/selfinstall.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/eduardorarruda/revoada/agent/internal/buffer"
	"github.com/eduardorarruda/revoada/agent/internal/canal"
	"github.com/eduardorarruda/revoada/agent/internal/collect"
	"github.com/eduardorarruda/revoada/agent/internal/config"
	"github.com/eduardorarruda/revoada/agent/internal/deploy"
	"github.com/eduardorarruda/revoada/agent/internal/discover"
	"github.com/eduardorarruda/revoada/agent/internal/logtail"
	"github.com/eduardorarruda/revoada/agent/internal/migracao/captura"
	"github.com/eduardorarruda/revoada/agent/internal/migracao/copia"
	"github.com/eduardorarruda/revoada/agent/internal/migracao/upgrade"
	"github.com/eduardorarruda/revoada/agent/internal/otlpsend"
	"github.com/eduardorarruda/revoada/agent/internal/probe"
	"github.com/eduardorarruda/revoada/agent/internal/selfinstall"
	"github.com/eduardorarruda/revoada/agent/internal/selfupdate"
	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
)

// applyResourceLimits aplica, no boot do loop contínuo, os tetos de recurso que o
// próprio agente consegue impor: GOMEMLIMIT (soft, via debug.SetMemoryLimit) e
// GOMAXPROCS. São editáveis pelo agent.yaml (bloco `resources:`) + restart, sem
// reinstalar. Complementam a cerca dura do systemd (MemoryMax/CPUQuota na unit).
// Valores 0 = não mexe (mantém o comportamento padrão do runtime).
func applyResourceLimits(log *slog.Logger, r config.ResourceLimits) {
	if r.MemoryLimitMB > 0 {
		limit := int64(r.MemoryLimitMB) * 1024 * 1024
		debug.SetMemoryLimit(limit)
		log.Info("limite de memória aplicado (GOMEMLIMIT)", "mb", r.MemoryLimitMB)
	}
	if r.MaxProcs > 0 {
		prev := runtime.GOMAXPROCS(r.MaxProcs)
		log.Info("limite de CPU aplicado (GOMAXPROCS)", "procs", r.MaxProcs, "antes", prev)
	}
}

// A 0.8.0 é MENOR, não patch: mudou a semântica de métricas que já existiam.
// `system.cpu.utilization` passou a ser a média do intervalo entre coletas (era um
// instantâneo de 500ms), `container.cpu.utilization` passou à escala da máquina (era
// a do Docker, onde 100% = um núcleo) e a rede passou a somar só interfaces físicas
// (era tudo, inclusive loopback e veths). Quem comparar histórico precisa saber a
// partir de onde os números mudaram de significado.
//
// A 0.8.1 muda de novo o significado de `system.cpu.utilization`: agora ela EXCLUI
// o steal, que sai só em `system.cpu.steal`. As duas séries viraram disjuntas e a
// soma delas é a leitura antiga. Pela regra do parágrafo acima isso pediria um
// número MENOR (0.9.0); ficou como patch por decisão do operador, e é este
// comentário que registra a quebra para quem for comparar histórico depois.
//
// A 0.8.2 não muda nenhum significado de métrica: ela ganha a auto-desinstalação
// (o painel manda o agente se remover quando o servidor é apagado — ver o pacote
// selfuninstall). O número PRECISA subir mesmo sem mudança de medida: a frota só
// baixa binário quando a versão publicada é maior que a que ela roda, então
// deixar 0.8.1 aqui manteria a feature para sempre no dist, sem chegar a ninguém.
//
// A 0.8.3 também não muda medida nenhuma: a descoberta de serviços passa a
// reconhecer Apache (httpd/apache2) e PHP-FPM, com as versões do PHP lidas do
// cmdline do processo mestre (ver discover.go). Sobe pelo mesmo motivo da 0.8.2.
//
// A 0.8.4 não muda medida nenhuma: corrige o travamento do agente com uma linha de
// log gigante de container (44 MB de uma vez, de um Postgres registrando os
// parâmetros de uma consulta lenta). O demux do Docker passa a cortar a linha no
// teto e descartar o resto, como o tail de arquivo, e o lote de logs fecha também
// por bytes (1 MiB).
var version = "0.8.4"

// defaultConfigPath é onde cada sistema guarda o agent.yaml. O serviço sempre
// passa -config explicitamente; este default é para quem roda um comando à mão
// (doctor, once) e não quer digitar o caminho.
func defaultConfigPath() string {
	if runtime.GOOS == "windows" {
		return `C:\revoada\agent.yaml`
	}
	return "/etc/revoada/agent.yaml"
}

func main() {
	cfgPath := flag.String("config", defaultConfigPath(), "caminho do agent.yaml")
	flag.Parse()

	switch flag.Arg(0) {
	case "version":
		fmt.Println("revoada-agent", version)
		return
	case "doctor":
		doctor(*cfgPath)
		return
	case "once":
		once(*cfgPath)
		return
	case "discover":
		discoverOnce(*cfgPath)
		return
	case "inscrever":
		inscrever(*cfgPath, flag.Args()[1:])
		return
	case "enroll":
		enroll(flag.Args()[1:])
		return
	case "servico":
		os.Exit(servico(*cfgPath, flag.Args()[1:]))
	}
	if rodandoComoServico(*cfgPath) {
		return
	}
	// Sem subcomando e sem -config explícito: pode ser um instalador gerado pelo
	// painel (binário com receita colada no fim). O serviço instalado sempre passa
	// -config, então nunca cai aqui e nunca se reinstala sozinho.
	if flag.Arg(0) == "" && !flagDefinida("config") && instalarSeForInstalador() {
		return
	}
	run(*cfgPath)
}

// enroll troca um token de inscrição pela chave de ingestão deste servidor e a
// imprime na saída padrão — nada mais. É assim que os instaladores em shell do
// Linux e do macOS obtêm a chave sem precisar interpretar JSON com sed:
//
//	KEY="$(revoada-agent enroll -token ABC -panel https://painel.exemplo)"
//
// Usa um conjunto de flags próprio para aceitar `enroll -token X` (o flag padrão
// do Go pararia de interpretar flags no primeiro argumento posicional).
func enroll(args []string) {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	token := fs.String("token", "", "token de inscrição (do instalador universal)")
	panel := fs.String("panel", "", "endereço do painel, ex.: https://painel.exemplo")
	host := fs.String("hostname", "", "nome a registrar (default: o hostname desta máquina)")
	_ = fs.Parse(args)

	if *token == "" || *panel == "" {
		fmt.Fprintln(os.Stderr, "uso: revoada-agent enroll -token <TOKEN> -panel <URL> [-hostname <NOME>]")
		os.Exit(2)
	}
	nome := *host
	if nome == "" {
		h, err := os.Hostname()
		if err != nil || h == "" {
			fmt.Fprintf(os.Stderr, "não consegui descobrir o nome desta máquina: %v\n", err)
			os.Exit(1)
		}
		nome = h
	}
	key, err := selfinstall.Enroll(context.Background(), *panel, *token, nome)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Só a chave, sem adornos: a saída é consumida por `$(...)` no shell.
	fmt.Println(key)
}

// flagDefinida diz se a flag foi passada na linha de comando (em vez de ficar no
// valor default). É a diferença entre "rodaram o instalador" e "o serviço subiu o
// agente".
func flagDefinida(nome string) bool {
	visto := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == nome {
			visto = true
		}
	})
	return visto
}

// instalarSeForInstalador roda a auto-instalação quando este binário carrega uma
// receita embutida. Devolve true quando tratou o caso (com sucesso ou com erro
// reportado) — aí o main não deve seguir para o loop de coleta.
func instalarSeForInstalador() bool {
	rec, fimBinario, err := selfinstall.Embedded()
	if errors.Is(err, selfinstall.ErrSemReceita) {
		return false // agente comum: segue o fluxo normal
	}
	if err == nil {
		err = selfinstall.Install(rec, fimBinario, os.Stdout)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nA instalação não foi concluída: %v\n", err)
		esperarEnter()
		os.Exit(1)
	}
	esperarEnter()
	return true
}

// esperarEnter segura a janela aberta no Windows. Sem isto, quem der duplo clique
// no instalador vê o console piscar e sumir — inclusive quando deu erro. Só age
// se a saída é um terminal de verdade (chamada por script/CI não trava).
func esperarEnter() {
	if runtime.GOOS != "windows" {
		return
	}
	st, err := os.Stdin.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice == 0 {
		return
	}
	fmt.Print("\nPressione Enter para fechar.")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

func run(cfgPath string) { executar(context.Background(), cfgPath) }

// executar é o loop do agente; termina quando `pai` é cancelado (serviço parando), por
// sinal do sistema ou quando o atualizador pede reinício.
func executar(pai context.Context, cfgPath string) {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	applyResourceLimits(log, cfg.ResourceLimits)
	// Sem arquivo de estado aqui de propósito: no loop contínuo a leitura
	// anterior já vive em memória entre os ticks, e gravar a cada 15s seria
	// escrita em disco de fundo (4x/min por host) sem nada em troca.
	buf, err := buffer.New(cfg.BufferDir, cfg.BufferFiles())
	if err != nil {
		log.Error("buffer", "err", err)
		os.Exit(1)
	}
	sender := buildSender(cfg, buf)

	// Salvaguarda de não-sobrecarga do caminho de logs: limita linhas/s (todas as
	// fontes somadas). Definido antes de subir qualquer coletor.
	logtail.SetLogRateLimit(cfg.LogRateLimit())
	// E o teto de VOLUME, ao lado. O de linhas sozinho garantia a coisa errada: uma
	// aplicação com JSON de 4 KB por linha cabe folgada em 5000 linhas/s e manda
	// 20 MB/s — 1,7 TB/dia saindo do link do servidor do cliente, com o agente
	// convencido de estar dentro do limite. Byte é a unidade que o link e a franquia
	// cobram; linha não é unidade de nada.
	logtail.SetLogByteRateLimit(cfg.LogByteRateLimit())

	// Onde o tail lembra até onde já leu de cada arquivo. Sem isto o ponteiro vive só
	// na memória e `seedOffsets` recomeça no FIM a cada boot: tudo que a aplicação
	// escreveu enquanto o agente esteve parado — justamente a janela do incidente que
	// derrubou o serviço — nunca chega ao painel, e o painel diz "sem erro". O
	// buffer_dir é a escolha certa porque é o único diretório que o agente
	// comprovadamente escreve, e o buffer só enxerga `.otlp`, então o estado não é
	// confundido com um lote. Definido antes de subir qualquer coletor.
	logtail.SetLogStateDir(cfg.BufferDir)

	ctxBase, stop := signal.NotifyContext(pai, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Parada limpa em duas origens: o sinal do sistema (SIGTERM do systemd) e o
	// pedido do atualizador. O agente NÃO consegue sobrescrever o próprio binário
	// — ele roda como revoada e o binário é de root, dentro de ProtectSystem=strict.
	// Então, quando o binário novo já está estagiado e verificado, a única coisa
	// que falta é sair: o ExecStartPre=+ da unit promove o arquivo como root e o
	// Restart=always reergue o serviço já com a versão nova. Sair é a metade do
	// agente nesta troca; nada aqui escreve em /usr/local/bin.
	ctx, reiniciar := context.WithCancel(ctxBase)
	defer reiniciar()
	iniciarCanal(ctx, log, cfg)
	var saindoParaAtualizar atomic.Bool

	// atz fica visível fora do if porque o laço de coleta precisa avisá-lo de cada
	// ciclo aceito pelo gateway: é essa prova de trabalho — e não "ficou 2 minutos de
	// pé" — que autoriza declarar a versão sã e desarmar o desfazimento do promotor
	// root. Nil quando a auto-atualização está desligada; CicloEnviado trata nil.
	var atz *selfupdate.Atualizador
	if cfg.AutoUpdateEnabled() {
		// O atualizador segue o ctxBase (não o ctx): ele é quem cancela o ctx, e
		// pendurá-lo no próprio cancelamento o mataria antes de terminar de logar.
		atz = selfupdate.New(selfupdate.Config{
			PanelURL:   cfg.PanelURL,
			Key:        cfg.Key,
			Versao:     version,
			Hostname:   cfg.Hostname,
			Dir:        cfg.UpdateDirOrDefault(),
			Intervalo:  cfg.UpdateInterval(),
			Modo:       selfupdate.Modo(cfg.UpdateApplyMode()),
			ConfigPath: cfgPath,
		}, log, func() {
			saindoParaAtualizar.Store(true)
			reiniciar()
		})
		go atz.Run(ctxBase)
	} else {
		log.Info("auto-atualização desligada", "motivo", motivoSemAutoUpdate(cfg))
	}

	// Modo sonda (P6.3): sonda URLs estáticas + as designadas pelo servidor.
	if cfg.Probe {
		loc := cfg.ProbeLocation
		if loc == "" {
			loc = cfg.Hostname
		}
		go probe.NewReporter(cfg.GatewayURL, cfg.Key, loc, cfg.ProbeURLs).Run(ctx)
		log.Info("modo sonda ativo", "location", loc, "urls", len(cfg.ProbeURLs))
	}

	// Coleta de logs opcional (P6.1): segue os arquivos e envia via /ingest/logs.
	if cfg.CollectLogs && len(cfg.LogPaths) > 0 {
		go logtail.New(cfg.GatewayURL, cfg.Key, cfg.Hostname, cfg.LogPaths).Run(ctx)
		log.Info("coleta de logs ativa", "paths", len(cfg.LogPaths))
	}

	// Coletores do servidor inteiro (Fase D): opt-in por toggle, best-effort. Cada
	// fonte marca os registros com um label `source`. Só rodam no loop contínuo.
	// Os coletores de stream (journald/docker/kmsg) morrem no 1º erro de leitura;
	// Supervise os reinicia com backoff (recupera de restart do dockerd, overrun do
	// kmsg, etc.) e desiste se o recurso não existe. O Tailer (syslog) já tem loop
	// próprio resiliente, então roda direto.
	if cfg.CollectJournald {
		j := logtail.NewJournald(cfg.GatewayURL, cfg.Key, cfg.Hostname, log)
		go logtail.Supervise(ctx, log, "journald", j.Run)
		log.Info("coleta journald ativa (source=journald)")
	}
	if cfg.CollectDockerLogs {
		d := logtail.NewDockerLogs(cfg.GatewayURL, cfg.Key, cfg.Hostname, log)
		go logtail.Supervise(ctx, log, "docker", d.Run)
		log.Info("coleta docker logs ativa (source=docker)")
	}
	if cfg.CollectSyslog {
		go logtail.NewSyslog(cfg.GatewayURL, cfg.Key, cfg.Hostname).Run(ctx)
		log.Info("coleta syslog ativa (source=syslog)")
	}
	if cfg.CollectDmesg {
		k := logtail.NewKmsg(cfg.GatewayURL, cfg.Key, cfg.Hostname, log)
		go logtail.Supervise(ctx, log, "kernel", k.Run)
		log.Info("coleta dmesg/kernel ativa (source=kernel)")
	}

	log.Info("revoada-agent iniciado", "host", cfg.Hostname, "gateway", cfg.GatewayURL, "interval", cfg.Interval())
	t := time.NewTicker(cfg.Interval())
	defer t.Stop()
	// cicloOK repassa ao atualizador o único sinal que prova que esta versão funciona:
	// um ciclo coletado E aceito pelo gateway.
	cicloOK := func(ok bool) {
		if ok {
			atz.CicloEnviado()
			return
		}
		// Envio recusado por credencial (401/403): pergunta ao painel AGORA, sem
		// esperar o ciclo horário. Ou a chave foi revogada e existe uma ordem de
		// desinstalação à espera deste agente, ou alguém a apagou — nos dois casos,
		// até uma hora de processo órfão martelando o gateway é tempo demais.
		// ConsultarAgora tem limitador próprio; chamar a cada ciclo é seguro.
		if sender.ChaveRecusada() {
			atz.ConsultarAgora()
		}
	}
	cicloOK(collectAndSend(ctx, log, sender, cfg))
	sendDiscovery(ctx, log, cfg) // descoberta inicial

	disco := time.NewTicker(5 * time.Minute)
	defer disco.Stop()
	for {
		select {
		case <-ctx.Done():
			if saindoParaAtualizar.Load() {
				// Saída com código 0 de propósito: `Restart=always` reergue o serviço
				// em qualquer código de saída, e sair "com erro" só sujaria o
				// `systemctl status` de um host que está fazendo exatamente o certo.
				log.Info("revoada-agent saindo para o systemd promover o binário novo — a coleta volta no reinício")
				return
			}
			log.Info("revoada-agent encerrado")
			return
		case <-t.C:
			cicloOK(collectAndSend(ctx, log, sender, cfg))
		case <-disco.C:
			sendDiscovery(ctx, log, cfg)
		}
	}
}

// motivoSemAutoUpdate explica no log por que o agente não vai se atualizar.
// "Auto-atualização desligada" sem motivo é indistinguível de um atualizador
// quebrado — e a diferença entre as duas coisas é o que o operador precisa
// saber quando a frota não sai do lugar.
func motivoSemAutoUpdate(cfg config.Config) string {
	if cfg.PanelURL == "" {
		return "panel_url não configurado no agent.yaml (agente instalado antes desta função; reinstale pelo painel para ligá-la)"
	}
	return "auto_update: false no agent.yaml"
}

// buildSender monta o Sender OTLP com os labels estáticos do host (versão, sonda).
func buildSender(cfg config.Config, buf *buffer.Buffer) *otlpsend.Sender {
	extra := map[string]string{"agent.version": version}
	if cfg.Probe {
		extra["probe"] = "true"
		if cfg.ProbeLocation != "" {
			extra["probe_location"] = cfg.ProbeLocation
		}
	}
	return otlpsend.New(cfg.GatewayURL, cfg.Key, cfg.Hostname, extra, buf)
}

// once executa um único ciclo de coleta+envio de métricas e sai. Feito para ser
// chamado por cron (ex.: hospedagem compartilhada/cPanel, sem systemd). O buffer em
// disco (buffer_dir) garante o reenvio no próximo tick se o gateway estiver fora.
func once(cfgPath string) {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	// Cada execução por cron é um processo novo: sem lembrar a leitura anterior,
	// toda medida de CPU cairia na janela curta — o defeito que o medidor corrige.
	// O host `mail.exemplo.com.br` roda por cron E tem containers: sem lembrar a leitura
	// anterior de cada container, `container.cpu.utilization` sumiria do painel dele.
	collect.UseContainerStateFile(containerStateFile(cfg))
	collect.UseCPUStateFile(cpuStateFile(cfg), func(err error) {
		log.Warn("não consegui gravar o estado de CPU — a medida volta a ser feita numa janela de 500ms, que é imprecisa; verifique dono e permissão do diretório",
			"arquivo", cpuStateFile(cfg), "err", err)
	})
	buf, err := buffer.New(cfg.BufferDir, cfg.BufferFiles())
	if err != nil {
		log.Error("buffer", "err", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	collectAndSend(ctx, log, buildSender(cfg, buf), cfg)
}

// cpuStateFile é onde o agente lembra a última leitura de CPU entre execuções.
// Fica DENTRO do buffer_dir — o único diretório que o agente comprovadamente
// consegue escrever (o buffer.New o cria), que o instalador já dá ao usuário do
// serviço e que a desinstalação apaga. O buffer só enxerga arquivos `.otlp`,
// então o estado nunca é confundido com um lote nem varrido pela rotação.
func cpuStateFile(cfg config.Config) string {
	return filepath.Join(cfg.BufferDir, "cpu.state")
}

// containerStateFile guarda a última leitura de CPU de CADA container, pelo mesmo
// motivo e no mesmo lugar do cpu.state.
func containerStateFile(cfg config.Config) string {
	return filepath.Join(cfg.BufferDir, "containers.state")
}

// discoverOnce envia a descoberta de serviços uma vez e sai. Como o scan de
// processos/portas é pesado, na instalação por cron rode com frequência menor
// (ex.: a cada 15–30 min) que o `once` de métricas.
func discoverOnce(cfgPath string) {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sendDiscovery(ctx, log, cfg)
}

func sendDiscovery(ctx context.Context, log *slog.Logger, cfg config.Config) {
	rep := discover.Run(ctx, cfg.Hostname)
	if err := discover.Send(ctx, cfg.GatewayURL, cfg.Key, rep); err != nil {
		log.Warn("descoberta não enviada", "err", err)
		return
	}
	log.Info("descoberta enviada", "servicos", len(rep.Services), "docker", rep.DockerPresent, "containers", len(rep.DockerContainers))
}

// collectAndSend devolve true quando o ciclo inteiro deu certo E o gateway ACEITOU o
// envio. O booleano não é decoração: é ele que alimenta selfupdate.CicloEnviado, ou
// seja, é a prova de que ESTE binário está fazendo o trabalho para o qual existe.
// Sem ela, "versão sã" voltaria a significar só "o processo não morreu ainda".
func collectAndSend(ctx context.Context, log *slog.Logger, sender *otlpsend.Sender, cfg config.Config) bool {
	// O inventário é lido UMA vez por ciclo e repassado a Host: é dele que sai o
	// `system.uptime`. Host chamava host.Info de novo, o que no Linux varre /proc
	// inteiro (e faz fork+exec do lsb_release em AlmaLinux/CloudLinux) para obter
	// um número que já está aqui na mão.
	info, _ := collect.Info(ctx)
	// Cada grupo é carimbado com a hora em que acabou de ser lido, não com a do
	// envio (que vem depois dos containers): validado contra /proc, o carimbo do
	// envio deixava contador de rede e load average até segundos atrasados.
	pts := collect.Carimbar(collect.Host(ctx, cfg.AllFilesystems(), info), time.Now())
	if cfg.Containers() {
		pts = append(pts, collect.Carimbar(collect.Containers(ctx, cfg.MaxContainers), time.Now())...)
	}
	if cfg.SelfMetricsEnabled() {
		pts = append(pts, collect.Carimbar(collect.Self(ctx), time.Now())...) // footprint do próprio agente (agent.self.*)
	}
	if err := sender.Send(ctx, pts, info); err != nil {
		log.Warn("envio falhou (bufferizado)", "err", err, "pontos", len(pts))
		return false
	}
	// Debug (não Info): em operação normal isto dispara a cada tick (15s) — ~5,7 mil
	// linhas/dia por host no journald, e re-ingeridas na tabela `logs` quando o
	// próprio journald é coletado. O caminho de sucesso não precisa ser ruidoso; as
	// falhas seguem em Warn e o footprint real está nas métricas agent.self.*.
	log.Debug("enviado", "pontos", len(pts))
	return true
}

// doctor imprime um diagnóstico legível e sai com código != 0 se algo está errado.
func doctor(cfgPath string) {
	fmt.Println("revoada-agent doctor")
	fmt.Println("=================")
	ok := true

	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Printf("✗ config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ config %s\n", cfgPath)
	fmt.Printf("  gateway=%s host=%s interval=%s buffer=%s probe=%v\n", cfg.GatewayURL, cfg.Hostname, cfg.Interval(), cfg.BufferDir, cfg.Probe)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	info, errInfo := collect.Info(ctx)
	if errInfo != nil {
		fmt.Printf("✗ coleta host: %v\n", errInfo)
		ok = false
	} else {
		fmt.Printf("✓ host %s (%s %s, uptime %ds)\n", info.Hostname, info.Platform, info.Arch, info.Uptime)
	}
	// A medição de CPU depende de lembrar a leitura anterior. No modo cron, se o
	// estado não gravar, ela silenciosamente volta a ser uma janela de 500ms — e
	// as linhas do cron mandam a saída para /dev/null. Este diagnóstico é o único
	// lugar onde o operador consegue ver isso.
	var falhaEstado error
	estado := cpuStateFile(cfg)
	collect.UseCPUStateFile(estado, func(err error) { falhaEstado = err })

	pts := collect.Host(ctx, cfg.AllFilesystems(), info)
	fmt.Printf("✓ %d métricas coletadas\n", len(pts))
	for _, p := range pts {
		switch p.Name {
		case "system.cpu.utilization":
			fmt.Printf("✓ CPU %.1f%%\n", p.Value)
		case "system.cpu.steal":
			// Steal alto significa que a CPU medida acima NÃO é toda do servidor: o
			// hipervisor a entregou a outro inquilino. É a diferença entre otimizar
			// o software e trocar de plano, e o operador precisa ver os dois juntos.
			fmt.Printf("✓ CPU roubada pelo hipervisor (steal) %.1f%%\n", p.Value)
		}
	}
	switch idade, temEstado := collect.CPUStateInfo(estado); {
	case falhaEstado != nil:
		fmt.Printf("✗ estado de CPU %s: %v\n", estado, falhaEstado)
		fmt.Printf("  a CPU volta a ser medida numa janela de 500ms, que é imprecisa — dê permissão de escrita ao usuário do agente nesse diretório\n")
		ok = false
	case !temEstado:
		fmt.Printf("⚠ estado de CPU ainda não gravado em %s (normal na primeira execução)\n", estado)
	default:
		fmt.Printf("✓ estado de CPU em %s (última leitura há %s)\n", estado, idade.Round(time.Second))
	}

	if buf, err := buffer.New(cfg.BufferDir, cfg.BufferFiles()); err != nil {
		fmt.Printf("✗ buffer %s: %v (permissão?)\n", cfg.BufferDir, err)
		ok = false
	} else if files, _ := buf.List(); len(files) > 0 {
		fmt.Printf("⚠ %d lote(s) pendente(s) no buffer (gateway esteve fora?)\n", len(files))
	} else {
		fmt.Printf("✓ buffer vazio\n")
	}

	buf, _ := buffer.New(cfg.BufferDir, cfg.BufferFiles())
	sender := otlpsend.New(cfg.GatewayURL, cfg.Key, cfg.Hostname, nil, buf)
	if err := sender.Ping(ctx); err != nil {
		fmt.Printf("✗ gateway %s inacessível/credencial inválida: %v\n", cfg.GatewayURL, err)
		ok = false
	} else {
		fmt.Printf("✓ gateway acessível e chave aceita\n")
	}

	if !ok {
		fmt.Println("\nRESULTADO: problemas encontrados.")
		os.Exit(1)
	}
	fmt.Println("\nRESULTADO: tudo certo.")
}

// inscrever troca o token de uso único do painel pela identidade do agente no canal
// (certificado mTLS + chaves) e grava em canal.dir. Depois disso o serviço conecta
// sozinho a cada início.
func inscrever(cfgPath string, args []string) {
	fs := flag.NewFlagSet("inscrever", flag.ExitOnError)
	painel := fs.String("painel", "", "endereço do canal do painel (host:porta, ex.: painel.empresa.com:7443)")
	token := fs.String("token", "", "token de inscrição gerado no painel (rvd1....)")
	dir := fs.String("dir", "", "onde gravar a identidade (padrão: canal.dir do agent.yaml)")
	_ = fs.Parse(args)
	if *painel == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "uso: revoada-agent inscrever -painel host:7443 -token rvd1....")
		os.Exit(2)
	}
	destino := *dir
	if destino == "" {
		cfg, err := config.Load(cfgPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "não deu para ler a configuração (use -dir):", err)
			os.Exit(1)
		}
		destino = cfg.Canal.Dir
	}
	host, _ := os.Hostname()
	id, err := canal.Inscrever(context.Background(), *painel, *token, canal.Apresentacao{
		Hostname: host, SO: runtime.GOOS, Arch: runtime.GOARCH, Versao: version,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "inscrição falhou:", err)
		os.Exit(1)
	}
	if err := id.Salvar(destino); err != nil {
		fmt.Fprintln(os.Stderr, "inscrito, mas não deu para gravar a identidade:", err)
		os.Exit(1)
	}
	fmt.Printf("Agente inscrito no painel %s com o id %s.\nIdentidade gravada em %s.\n", *painel, id.AgenteID, destino)
	fmt.Println("Lembre de liberar os tipos de tarefa em canal.tarefas_permitidas no agent.yaml e reiniciar o serviço.")
}

// iniciarCanal sobe o cliente do canal se o agente já foi inscrito. Sem identidade,
// o agente segue só com a coleta de métricas (como antes).
func iniciarCanal(ctx context.Context, log *slog.Logger, cfg config.Config) {
	cli, err := canal.NovoCliente(log, cfg.Canal.Dir, canal.Apresentacao{
		Hostname: cfg.Hostname, SO: runtime.GOOS, Arch: runtime.GOARCH, Versao: version,
	}, cfg.Canal.TarefasPermitidas)
	if errors.Is(err, canal.ErrSemIdentidade) {
		log.Info("canal: agente não inscrito; só coleta de métricas", "dir", cfg.Canal.Dir)
		return
	}
	if err != nil {
		log.Error("canal: não deu para iniciar", "err", err)
		return
	}
	cli.UsarLeitorEsquema(func(ctx context.Context, p canal.PedidoBanco) ([]byte, error) {
		e, err := captura.Capturar(ctx, captura.Conexao{Motor: p.Motor, Endereco: p.Endereco, Banco: p.Banco,
			Usuario: p.Usuario, Senha: p.Senha, Opcoes: p.Opcoes})
		if err != nil {
			return nil, err
		}
		return json.Marshal(e)
	})
	tarefas := copia.Tarefas()
	for tipo, m := range upgrade.Tarefas() {
		tarefas[tipo] = m
	}
	if len(cfg.Canal.Deploy) > 0 {
		motor := deploy.NovoMotor(cfg.Canal.Deploy, filepath.Join(cfg.Canal.Dir, "deploy"))
		tarefas[deploy.Tarefa] = func(ctx context.Context, b []byte, r copia.Relator) (any, error) { return motor.Aplicar(ctx, b, r) }
	}
	for tipo, m := range tarefas {
		cli.Executor().Registrar(tipo, func(ctx context.Context, t *agentev1.Tarefa, r canal.Relator) (any, error) {
			return m(ctx, t.GetEspecificacao(), r)
		})
	}
	log.Info("canal: iniciando", "tarefas_permitidas", cfg.Canal.TarefasPermitidas)
	go cli.Rodar(ctx)
}

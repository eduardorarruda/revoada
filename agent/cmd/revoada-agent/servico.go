package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/kardianos/service"
)

// Serviço nativo do sistema (Etapa 9): systemd no Linux, launchd no macOS e Serviço
// do Windows (com reinício em falha — antes era tarefa agendada). Os instaladores em
// shell (deploy/agent/install*.sh) continuam sendo o caminho com a cerca de recursos
// mais apertada no Linux; este subcomando é o caminho comum aos três sistemas.
//
//	revoada-agent -config <arquivo> servico instalar|remover|iniciar|parar|reiniciar|status

const esperaParada = 20 * time.Second

type programa struct {
	cfg    string
	cancel context.CancelFunc
	fim    chan struct{}
}

func (p *programa) Start(service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel, p.fim = cancel, make(chan struct{})
	go func() {
		defer close(p.fim)
		executar(ctx, p.cfg)
		if ctx.Err() == nil {
			// saiu sozinho (o atualizador pediu reinício): código ≠ 0 para o gerenciador
			// de serviços subir de novo pela regra de recuperação
			os.Exit(1)
		}
	}()
	return nil
}

func (p *programa) Stop(service.Service) error {
	if p.cancel != nil {
		p.cancel()
		select {
		case <-p.fim:
		case <-time.After(esperaParada):
		}
	}
	return nil
}

func configServico(cfgPath string) *service.Config {
	abs, err := filepath.Abs(cfgPath)
	if err != nil {
		abs = cfgPath
	}
	return &service.Config{
		Name:        "revoada-agent",
		DisplayName: "Revoada Agent",
		Description: "Agente do Revoada: métricas, logs e tarefas (migração, upgrade) pedidas pelo painel.",
		Arguments:   []string{"-config", abs},
		Option: service.KeyValue{
			"Restart":                "on-failure", // systemd
			"OnFailure":              "restart",    // Windows: recuperação
			"OnFailureDelayDuration": "10s",
			"KeepAlive":              true, // launchd
			"RunAtLoad":              true,
		},
	}
}

func servico(cfgPath string, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "uso: revoada-agent -config <arquivo> servico instalar|remover|iniciar|parar|reiniciar|status")
		return 2
	}
	s, err := service.New(&programa{cfg: cfgPath}, configServico(cfgPath))
	if err != nil {
		fmt.Fprintln(os.Stderr, "serviço:", err)
		return 1
	}
	acoes := map[string]string{"instalar": "install", "remover": "uninstall", "iniciar": "start", "parar": "stop", "reiniciar": "restart"}
	if args[0] == "status" {
		st, err := s.Status()
		if err != nil {
			fmt.Fprintln(os.Stderr, "status:", err)
			return 1
		}
		fmt.Println(map[service.Status]string{service.StatusRunning: "rodando", service.StatusStopped: "parado"}[st])
		return 0
	}
	acao, ok := acoes[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "ação %q desconhecida\n", args[0])
		return 2
	}
	if err := service.Control(s, acao); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v (rode como administrador/root)\n", args[0], err)
		return 1
	}
	fmt.Printf("serviço revoada-agent: %s ok (%s)\n", args[0], s.Platform())
	return 0
}

// rodandoComoServico: no Windows, quem chama é o Gerenciador de Serviços — o processo
// precisa conversar com ele (senão o Windows mata o agente em 30 s). Nos outros
// sistemas o systemd/launchd só executa o binário e o caminho normal já serve.
func rodandoComoServico(cfgPath string) bool {
	if runtime.GOOS != "windows" || service.Interactive() {
		return false
	}
	s, err := service.New(&programa{cfg: cfgPath}, configServico(cfgPath))
	if err != nil {
		return false
	}
	if err := s.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "serviço:", err)
		os.Exit(1)
	}
	return true
}

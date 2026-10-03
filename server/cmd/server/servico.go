package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kardianos/service"
)

// Serviço nativo do painel (Etapa 9): systemd, launchd ou Serviço do Windows.
//
//	revoada-painel servico instalar --env /etc/revoada/painel.env
//	revoada-painel servico iniciar|parar|reiniciar|remover|status
//
// A configuração vem de um arquivo KEY=VALOR com permissão 0600 (o mesmo formato do
// .env do compose) — e NÃO das variáveis da unit: arquivo de unit é legível por
// todos, e a configuração tem a senha do banco e o segredo do JWT.

const nomeServico = "revoada-painel"

func configServicoPainel(arquivoEnv string) *service.Config {
	args := []string{}
	if arquivoEnv != "" {
		if abs, err := filepath.Abs(arquivoEnv); err == nil {
			arquivoEnv = abs
		}
		args = append(args, "--env", arquivoEnv)
	}
	return &service.Config{
		Name: nomeServico, DisplayName: "Revoada (painel)",
		Description: "Painel do Revoada: API, MCP, canal dos agentes e interface web.",
		Arguments:   args,
		Option: service.KeyValue{"Restart": "on-failure", "OnFailure": "restart", "OnFailureDelayDuration": "10s",
			"KeepAlive": true, "RunAtLoad": true},
	}
}

// carregarEnv lê KEY=VALOR (sem sobrescrever o que já veio do ambiente).
func carregarEnv(arquivo string) error {
	st, err := os.Stat(arquivo)
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		fmt.Fprintf(os.Stderr, "aviso: %s tem permissão %v; deixe 0600 (tem segredos)\n", arquivo, st.Mode().Perm())
	}
	f, err := os.Open(arquivo)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimPrefix(l, "export "), "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`)
		if _, existe := os.LookupEnv(k); !existe {
			_ = os.Setenv(k, v)
		}
	}
	return sc.Err()
}

// argumentoEnv tira "--env arquivo" dos argumentos e carrega o arquivo.
func argumentoEnv(args []string) ([]string, string) {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--env" || args[i] == "-env" {
			return append(append([]string{}, args[:i]...), args[i+2:]...), args[i+1]
		}
	}
	return args, ""
}

func comandoServico(args []string) int {
	args, env := argumentoEnv(args)
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "uso: revoada-painel servico instalar --env <arquivo> | iniciar | parar | reiniciar | remover | status")
		return 2
	}
	s, err := service.New(&programaPainel{}, configServicoPainel(env))
	if err != nil {
		fmt.Fprintln(os.Stderr, "serviço:", err)
		return 1
	}
	if args[0] == "status" {
		st, err := s.Status()
		if err != nil {
			fmt.Fprintln(os.Stderr, "status:", err)
			return 1
		}
		fmt.Println(map[service.Status]string{service.StatusRunning: "rodando", service.StatusStopped: "parado"}[st])
		return 0
	}
	acao, ok := map[string]string{"instalar": "install", "remover": "uninstall", "iniciar": "start", "parar": "stop", "reiniciar": "restart"}[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "ação %q desconhecida\n", args[0])
		return 2
	}
	if acao == "install" && env == "" {
		fmt.Fprintln(os.Stderr, "instalar precisa de --env <arquivo> com a configuração (permissão 0600)")
		return 2
	}
	if err := service.Control(s, acao); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v (rode como administrador/root)\n", args[0], err)
		return 1
	}
	fmt.Printf("serviço %s: %s ok (%s)\n", nomeServico, args[0], s.Platform())
	return 0
}

// contextoRaiz é a raiz do contexto do painel: o Stop do serviço do Windows cancela
// aqui (no Windows não dá para mandar sinal ao próprio processo).
var contextoRaiz, pararRaiz = context.WithCancel(context.Background())

// programaPainel liga o painel ao Gerenciador de Serviços do Windows.
type programaPainel struct{ fim chan struct{} }

func (p *programaPainel) Start(service.Service) error {
	p.fim = make(chan struct{})
	go func() {
		defer close(p.fim)
		painel()
	}()
	return nil
}

func (p *programaPainel) Stop(service.Service) error {
	pararRaiz()
	if p.fim != nil {
		select {
		case <-p.fim:
		case <-time.After(30 * time.Second):
		}
	}
	return nil
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "servico" {
		os.Exit(comandoServico(os.Args[2:]))
	}
	args, env := argumentoEnv(os.Args[1:])
	if env != "" {
		if err := carregarEnv(env); err != nil {
			fmt.Fprintln(os.Stderr, "lendo", env+":", err)
			os.Exit(1)
		}
	}
	os.Args = append([]string{os.Args[0]}, args...)
	if runtime.GOOS == "windows" && !service.Interactive() {
		s, err := service.New(&programaPainel{}, configServicoPainel(env))
		if err == nil {
			err = s.Run()
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "serviço:", err)
			os.Exit(1)
		}
		return
	}
	painel()
}

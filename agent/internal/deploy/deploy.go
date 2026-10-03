// Package deploy implanta uma aplicação neste servidor a pedido do painel (ARQUITETURA §10)
// — sem runner self-hosted: o agente que já está no servidor executa.
//
// Segurança: o painel manda só o NOME da aplicação e a VERSÃO. O que roda (compose ou
// script, em qual pasta, qual health check) vem da configuração LOCAL do agente
// (canal.deploy), que é do dono do servidor. Um painel comprometido não consegue
// mandar comando nenhum.
package deploy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/eduardorarruda/revoada/agent/internal/config"
	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
)

// Tarefa é o tipo de tarefa do deploy.
const Tarefa = "deploy.aplicar"

const (
	esperaPadrao   = 60 * time.Second
	intervaloSaude = 2 * time.Second
	linhasResumo   = 60
)

var versaoValida = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

// Relator é o que o deploy usa do canal (eventos para a tela ao vivo).
type Relator interface {
	Evento(etapa string, nivel agentev1.EventoTarefa_Nivel, msg string, progresso float64, metricas map[string]float64)
}

// Especificacao é o JSON da tarefa (montado pelo painel).
type Especificacao struct {
	Aplicacao string `json:"aplicacao"`
	Versao    string `json:"versao"`
	Ambiente  string `json:"ambiente,omitempty"`
	Commit    string `json:"commit,omitempty"`
}

// Resumo é o resultado da tarefa.
type Resumo struct {
	Aplicacao string   `json:"aplicacao"`
	Versao    string   `json:"versao"`
	Anterior  string   `json:"anterior,omitempty"`
	Saudavel  bool     `json:"saudavel"`
	Revertido bool     `json:"revertido"`
	Saida     []string `json:"saida,omitempty"`
	DuracaoMS int64    `json:"duracao_ms"`
}

type estado struct {
	Atual    string    `json:"atual"`
	Anterior string    `json:"anterior"`
	Em       time.Time `json:"em"`
}

// Motor guarda a configuração e onde fica o estado (versão atual/anterior por app).
type Motor struct {
	apps    map[string]config.AplicacaoDeploy
	dir     string
	cliente *http.Client
	mu      sync.Mutex // um deploy por vez neste servidor
}

func NovoMotor(apps map[string]config.AplicacaoDeploy, dirEstado string) *Motor {
	return &Motor{apps: apps, dir: dirEstado, cliente: &http.Client{Timeout: 5 * time.Second}}
}

func (m *Motor) arquivoEstado(app string) string { return filepath.Join(m.dir, "deploy-"+app+".json") }

func (m *Motor) lerEstado(app string) estado {
	var e estado
	if b, err := os.ReadFile(m.arquivoEstado(app)); err == nil {
		_ = json.Unmarshal(b, &e)
	}
	return e
}

func (m *Motor) gravarEstado(app string, e estado) error {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(e)
	return os.WriteFile(m.arquivoEstado(app), b, 0o600)
}

// Aplicar é o manipulador da tarefa deploy.aplicar.
func (m *Motor) Aplicar(ctx context.Context, bruto []byte, r Relator) (any, error) {
	var esp Especificacao
	if err := json.Unmarshal(bruto, &esp); err != nil {
		return nil, fmt.Errorf("especificação inválida: %w", err)
	}
	app, ok := m.apps[esp.Aplicacao]
	if !ok {
		return nil, fmt.Errorf("a aplicação %q não está liberada neste servidor (canal.deploy no agent.yaml)", esp.Aplicacao)
	}
	if !versaoValida.MatchString(esp.Versao) {
		return nil, errors.New("versão inválida (letras, números, . _ + -; até 128)")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	inicio := time.Now()
	st := m.lerEstado(esp.Aplicacao)
	res := &Resumo{Aplicacao: esp.Aplicacao, Versao: esp.Versao, Anterior: st.Atual}
	saida := &saida{r: r}
	defer func() { res.Saida, res.DuracaoMS = saida.ultimas(), time.Since(inicio).Milliseconds() }()

	r.Evento("deploy", agentev1.EventoTarefa_INFO, fmt.Sprintf("implantando %s %s (antes: %s)", esp.Aplicacao, esp.Versao, valor(st.Atual)), 5, nil)
	err := m.rodar(ctx, app, esp.Versao, st.Atual, false, saida)
	if err == nil {
		r.Evento("saude", agentev1.EventoTarefa_INFO, "esperando a aplicação responder "+app.Saude, 70, nil)
		err = m.esperarSaude(ctx, app)
	}
	if err == nil {
		res.Saudavel = true
		if e := m.gravarEstado(esp.Aplicacao, estado{Atual: esp.Versao, Anterior: st.Atual, Em: time.Now()}); e != nil {
			r.Evento("deploy", agentev1.EventoTarefa_AVISO, "implantado, mas não deu para gravar o estado: "+e.Error(), 99, nil)
		}
		r.Evento("deploy", agentev1.EventoTarefa_INFO, fmt.Sprintf("%s %s no ar e saudável", esp.Aplicacao, esp.Versao), 100, nil)
		return res, nil
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	// Rollback automático: volta para a versão que estava no ar.
	r.Evento("rollback", agentev1.EventoTarefa_AVISO, "deploy falhou ("+err.Error()+"); voltando para "+valor(st.Atual), 80, nil)
	if st.Atual == "" && app.Tipo == "compose" {
		return res, fmt.Errorf("deploy falhou e não há versão anterior registrada para voltar: %w", err)
	}
	if rb := m.rodar(ctx, app, st.Atual, esp.Versao, true, saida); rb != nil {
		return res, fmt.Errorf("deploy falhou (%v) e o ROLLBACK também falhou: %w — intervenha", err, rb)
	}
	if sa := m.esperarSaude(ctx, app); sa != nil {
		return res, fmt.Errorf("deploy falhou (%v); rollback rodou, mas a aplicação não respondeu: %w", err, sa)
	}
	res.Revertido = true
	r.Evento("rollback", agentev1.EventoTarefa_INFO, "rollback concluído: "+valor(st.Atual)+" de volta e saudável", 100, nil)
	return res, fmt.Errorf("deploy de %s falhou e foi revertido para %s: %w", esp.Versao, valor(st.Atual), err)
}

func valor(s string) string {
	if s == "" {
		return "(nenhuma registrada)"
	}
	return s
}

// rodar executa o deploy (ou o rollback) conforme o tipo da aplicação. `versao` é a
// que deve ficar no ar; no rollback, `outra` é a que falhou.
func (m *Motor) rodar(ctx context.Context, app config.AplicacaoDeploy, versao, outra string, rollback bool, s *saida) error {
	env := append(os.Environ(), "REVOADA_VERSAO="+versao)
	switch app.Tipo {
	case "compose":
		arq := app.Arquivo
		if arq == "" {
			arq = "docker-compose.yml"
		}
		for _, args := range [][]string{{"compose", "-f", arq, "pull"}, {"compose", "-f", arq, "up", "-d", "--remove-orphans"}} {
			if err := executar(ctx, app.Diretorio, env, s, "docker", args...); err != nil {
				return err
			}
		}
		return nil
	case "script":
		script := app.Script
		if rollback {
			if app.Rollback == "" {
				return errors.New("sem script de rollback configurado")
			}
			// rollback: REVOADA_VERSAO = a que falhou; REVOADA_VERSAO_ANTERIOR = a que volta
			script = app.Rollback
			env = append(os.Environ(), "REVOADA_VERSAO="+outra, "REVOADA_VERSAO_ANTERIOR="+versao)
		}
		caminho, err := dentroDe(app.Diretorio, script)
		if err != nil {
			return err
		}
		prog, args := interpretador(caminho)
		return executar(ctx, app.Diretorio, env, s, prog, args...)
	}
	return fmt.Errorf("tipo de deploy %q desconhecido (use compose ou script)", app.Tipo)
}

// dentroDe garante que o script fica dentro da pasta da aplicação.
func dentroDe(dir, rel string) (string, error) {
	abs := filepath.Clean(filepath.Join(dir, rel))
	base := filepath.Clean(dir) + string(filepath.Separator)
	if !strings.HasPrefix(abs, base) {
		return "", fmt.Errorf("o script %q sai da pasta da aplicação", rel)
	}
	return abs, nil
}

// interpretador escolhe como rodar o script em cada sistema.
func interpretador(caminho string) (string, []string) {
	switch strings.ToLower(filepath.Ext(caminho)) {
	case ".ps1":
		return "powershell", []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", caminho}
	case ".cmd", ".bat":
		return "cmd", []string{"/C", caminho}
	}
	if runtime.GOOS == "windows" {
		return "powershell", []string{"-NoProfile", "-NonInteractive", "-File", caminho}
	}
	return "sh", []string{caminho}
}

func executar(ctx context.Context, dir string, env []string, s *saida, prog string, args ...string) error {
	cmd := exec.CommandContext(ctx, prog, args...)
	cmd.Dir, cmd.Env = dir, env
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", prog, err)
	}
	fim := make(chan struct{})
	go func() {
		defer close(fim)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			s.linha(sc.Text())
		}
	}()
	err := cmd.Wait()
	pw.Close()
	<-fim
	if err != nil {
		return fmt.Errorf("%s %s: %w", prog, strings.Join(args, " "), err)
	}
	return nil
}

// esperarSaude chama o health check até responder 2xx ou estourar o tempo.
func (m *Motor) esperarSaude(ctx context.Context, app config.AplicacaoDeploy) error {
	if app.Saude == "" {
		return nil
	}
	espera := esperaPadrao
	if app.EsperaS > 0 {
		espera = time.Duration(app.EsperaS) * time.Second
	}
	limite := time.Now().Add(espera)
	var ultimo string
	for time.Now().Before(limite) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, app.Saude, nil)
		if err != nil {
			return err
		}
		resp, err := m.cliente.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode/100 == 2 {
				return nil
			}
			ultimo = resp.Status
		} else {
			ultimo = err.Error()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(intervaloSaude):
		}
	}
	return fmt.Errorf("health check sem resposta 2xx em %v (último: %s)", espera, ultimo)
}

// saida manda as linhas para a tela (no máximo uma a cada 500 ms) e guarda as últimas.
type saida struct {
	r      Relator
	mu     sync.Mutex
	linhas []string
	ultimo time.Time
}

func (s *saida) linha(l string) {
	s.mu.Lock()
	s.linhas = append(s.linhas, l)
	if len(s.linhas) > linhasResumo {
		s.linhas = s.linhas[len(s.linhas)-linhasResumo:]
	}
	enviar := time.Since(s.ultimo) > 500*time.Millisecond
	if enviar {
		s.ultimo = time.Now()
	}
	s.mu.Unlock()
	if enviar && s.r != nil {
		s.r.Evento("saida", agentev1.EventoTarefa_INFO, l, 0, nil)
	}
}

func (s *saida) ultimas() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.linhas...)
}

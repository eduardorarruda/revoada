package selfinstall

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Pastas e nomes da instalação no Windows. São os mesmos caminhos que o Guia do
// painel documenta para a instalação manual — quem instalou à mão antes e roda o
// instalador agora acaba com exatamente o mesmo layout.
const (
	winDir       = `C:\revoada`
	winExe       = `C:\revoada\revoada-agent.exe`
	winCfg       = `C:\revoada\agent.yaml`
	winBuffer    = `C:\revoada\buffer`
	winTaskName  = "Revoada Agent"
	winTaskLabel = "tarefa agendada"
)

// Install pega a receita embutida e deixa o agente rodando: escreve a
// configuração, instala o binário e registra o serviço que o mantém de pé.
//
// Só o Windows é atendido aqui. Linux e macOS têm instaladores em shell
// (install.sh / install-macos.sh) que fazem bem mais do que este caminho
// conseguiria — detectam journald, Docker, grupos do sistema, e montam a unit
// systemd ou o launchd com a cerca de recurso. Duplicar isso em Go seria pior em
// todos os aspectos; então nesses sistemas o painel entrega o script, não o
// binário com receita.
func Install(rec Recipe, fimBinario int64, out io.Writer) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("este executável é o instalador do Windows; em %s use o instalador em shell que o painel gera (Infraestrutura → Instalar agente)", runtime.GOOS)
	}
	return installWindows(rec, fimBinario, out)
}

func installWindows(rec Recipe, fimBinario int64, out io.Writer) error {
	say := func(format string, a ...any) { fmt.Fprintf(out, format+"\n", a...) }

	if !ehAdminWindows() {
		return fmt.Errorf("é preciso rodar como administrador: feche esta janela, clique com o botão direito no instalador e escolha \"Executar como administrador\"")
	}

	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return fmt.Errorf("não consegui descobrir o nome deste computador: %w", err)
	}

	// Instalador universal: a receita traz um token de inscrição em vez da chave.
	// Pedimos a chave ANTES de mexer em qualquer coisa no disco — se o painel
	// estiver fora do ar ou o token tiver sido revogado, é melhor parar aqui do que
	// deixar meio agente instalado.
	if rec.Key == "" {
		say("==> pedindo a chave deste computador ao painel")
		chave, err := Enroll(context.Background(), rec.PanelURL, rec.EnrollToken, host)
		if err != nil {
			return err
		}
		rec.Key = chave
	}

	say("==> instalando o agente do Revoada em %s", winDir)
	for _, d := range []string{winDir, winBuffer} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("criando %s: %w", d, err)
		}
	}

	// A tarefa é parada ANTES de trocar o binário: no Windows um executável em
	// execução fica travado e a cópia falharia com "acesso negado". Parar uma
	// tarefa que não existe é inofensivo.
	_ = exec.Command("schtasks", "/End", "/TN", winTaskName).Run()

	say("==> instalando o binário em %s", winExe)
	if err := copiarBinario(winExe, fimBinario); err != nil {
		return err
	}

	say("==> escrevendo %s", winCfg)
	if err := os.WriteFile(winCfg, []byte(yamlWindows(rec, host)), 0o600); err != nil {
		return fmt.Errorf("escrevendo %s: %w", winCfg, err)
	}

	say("==> registrando a %s \"%s\" (inicia junto com o Windows)", winTaskLabel, winTaskName)
	cmd := exec.Command("schtasks", "/Create",
		"/TN", winTaskName,
		"/TR", fmt.Sprintf(`"%s" -config "%s"`, winExe, winCfg),
		"/SC", "ONSTART",
		"/RU", "SYSTEM",
		"/RL", "HIGHEST",
		"/F",
	)
	if saida, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("registrando a %s: %w — %s", winTaskLabel, err, strings.TrimSpace(string(saida)))
	}
	if saida, err := exec.Command("schtasks", "/Run", "/TN", winTaskName).CombinedOutput(); err != nil {
		return fmt.Errorf("iniciando a %s: %w — %s", winTaskLabel, err, strings.TrimSpace(string(saida)))
	}

	say("")
	say("Pronto. Este computador aparece como \"%s\" no painel em até um minuto.", host)
	if rec.PanelURL != "" {
		say("Acompanhe em %s/#/hosts", strings.TrimRight(rec.PanelURL, "/"))
	}
	say("")
	say("Para conferir agora:  \"%s\" -config \"%s\" doctor", winExe, winCfg)
	say("Para desinstalar:     schtasks /Delete /TN \"%s\" /F  e apague %s", winTaskName, winDir)
	return nil
}

// copiarBinario grava em dest os primeiros `fim` bytes do executável em curso —
// isto é, o agente sem a receita colada no fim. O agente instalado fica assim
// idêntico ao binário oficial, e a chave de ingestão existe em um lugar só (o
// agent.yaml, com permissão restrita), em vez de ficar também dentro do .exe.
func copiarBinario(dest string, fim int64) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("localizando o próprio executável: %w", err)
	}
	if mesmoCaminho(exe, dest) {
		// O administrador copiou o instalador para dentro da pasta de instalação e
		// rodou dali. Não dá para se sobrescrever em execução, e nem precisa: o
		// binário já está no lugar certo.
		return nil
	}
	src, err := os.Open(exe)
	if err != nil {
		return fmt.Errorf("abrindo %s: %w", exe, err)
	}
	defer func() { _ = src.Close() }()

	// Escreve em temporário e renomeia: se algo falhar no meio, o agente que já
	// estava instalado continua íntegro em vez de virar um arquivo pela metade.
	tmp := dest + ".novo"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("criando %s: %w", tmp, err)
	}
	if _, err := io.Copy(dst, io.LimitReader(src, fim)); err != nil {
		_ = dst.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("copiando o binário: %w", err)
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fechando %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("instalando em %s: %w", dest, err)
	}
	return nil
}

// mesmoCaminho compara dois caminhos do Windows: sem diferença de maiúsculas e
// com as barras normalizadas.
func mesmoCaminho(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// ehAdminWindows responde se o processo tem privilégio administrativo. `net
// session` só funciona elevado — é o teste clássico, não precisa de dependência
// nova e falha do lado seguro (na dúvida, dizemos que não é admin e o
// administrador recebe uma instrução clara em vez de um erro críptico do
// schtasks).
func ehAdminWindows() bool {
	return exec.Command("net", "session").Run() == nil
}

// yamlWindows monta o agent.yaml da instalação no Windows.
//
// Os coletores de logs do sistema (journald, docker, syslog, dmesg) ficam de fora
// porque são recursos de Linux — no Windows o agente coleta CPU, RAM, disco, rede
// e processos. Aspas simples nos caminhos: em YAML elas não interpretam escapes,
// então a contrabarra do Windows chega literal.
func yamlWindows(rec Recipe, host string) string {
	intervalo := rec.IntervalSeconds
	if intervalo <= 0 {
		intervalo = 15
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Gerado pelo instalador do Revoada. Contém a chave de ingestão deste host.\n")
	fmt.Fprintf(&b, "gateway_url: %s\n", rec.GatewayURL)
	fmt.Fprintf(&b, "key: %s\n", rec.Key)
	fmt.Fprintf(&b, "hostname: %s\n", host)
	fmt.Fprintf(&b, "interval_seconds: %d\n", intervalo)
	fmt.Fprintf(&b, "buffer_dir: '%s'\n", winBuffer)
	fmt.Fprintf(&b, "probe: %t\n", rec.Probe)
	fmt.Fprintf(&b, "collect_containers: false\n")
	if rec.MemoryLimitMB > 0 || rec.MaxProcs > 0 {
		fmt.Fprintf(&b, "resources:\n")
		fmt.Fprintf(&b, "  memory_limit_mb: %d\n", rec.MemoryLimitMB)
		fmt.Fprintf(&b, "  max_procs: %d\n", rec.MaxProcs)
	}
	return b.String()
}

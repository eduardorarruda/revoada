// Package selfuninstall remove o agente da própria máquina, quando o painel manda.
//
// # Por que existe
//
// Apagar um servidor pelo painel tirava os dados, mas deixava o agente instalado e
// rodando. Quem não tem SSH guardado ficava com um processo órfão batendo no gateway
// e levando 401 até alguém entrar na máquina à mão — que é exatamente o que a pessoa
// não tinha como fazer. A ordem chega pelo canal que já existe (a consulta de
// auto-atualização); a execução mora aqui.
//
// # Cada sistema tem um caminho, e um deles é indireto
//
// No LINUX o agente roda como o usuário `revoada`, sem privilégio: ele não consegue
// parar o próprio serviço, apagar a unit nem remover o binário de /usr/local/bin. Mas
// a instalação já tem um componente root — o promotor da auto-atualização, que roda
// no ExecStartPre da unit e é quem troca o binário. A desinstalação usa o MESMO
// caminho: o agente deixa um bilhete no diretório de estágio (que lhe pertence) e
// pede o reinício; no start seguinte o promotor, como root, lê o bilhete e remove
// tudo. Nenhum privilégio novo, nenhum sudoers, nenhuma superfície extra.
//
// No macOS e no Windows o agente já roda privilegiado (launchd como root, tarefa
// agendada como SYSTEM), então ele mesmo executa a remoção e o painel recebe a
// confirmação de que ACABOU, não de que começou.
package selfuninstall

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Estados relatados ao painel. São strings porque viajam no mesmo campo de texto do
// relato de auto-atualização — um canal, um formato.
const (
	// EstadoConcluido: acabou nesta chamada; o agente pode morrer em seguida.
	EstadoConcluido = "desinstalado"
	// EstadoEmCurso: o pedido foi aceito e termina no reinício seguinte (Linux). O
	// painel NÃO pode tratar isso como "removido" — só como "começou".
	EstadoEmCurso = "desinstalando"
	// EstadoFalhou: não deu, e o motivo vai junto. Melhor um erro visível no painel
	// do que um agente que ninguém sabe que continua lá.
	EstadoFalhou = "desinstalar_falhou"
)

// Resultado é o que o agente relata de volta.
type Resultado struct {
	Estado string
	Erro   string
	// ReiniciarParaConcluir diz ao chamador que a remoção só se completa quando o
	// serviço reiniciar (o promotor root faz o resto).
	ReiniciarParaConcluir bool
}

// nomeDoBilhete é o arquivo que o agente deixa para o promotor root. Nome literal
// dos dois lados — o promotor (deploy/agent/promote-update.sh) procura por este
// mesmo nome. Mudar aqui exige mudar lá.
const nomeDoBilhete = "desinstalar"

// Executar remove o agente da máquina. `updateDir` é o diretório de estágio da
// auto-atualização (o mesmo do agent.yaml), usado só no Linux.
func Executar(updateDir string) Resultado {
	switch runtime.GOOS {
	case "linux":
		return pedirAoPromotor(updateDir)
	case "darwin":
		return removerMacOS()
	case "windows":
		return removerWindows()
	default:
		return Resultado{Estado: EstadoFalhou, Erro: "sistema não suportado: " + runtime.GOOS}
	}
}

// caminhoPromotor é onde o install.sh escreve o componente root. Literal aqui e lá.
// É variável (e não constante) só para o teste poder apontar para um arquivo de
// mentira — nada em produção escreve nela.
var caminhoPromotor = "/usr/local/lib/revoada/promote-update.sh"

// pedirAoPromotor deixa o bilhete e devolve "em curso". O conteúdo é só carimbo de
// tempo — o promotor não precisa de mais nada, e menos dado no arquivo é menos
// superfície num script que roda como root.
func pedirAoPromotor(updateDir string) Resultado {
	if strings.TrimSpace(updateDir) == "" {
		return Resultado{Estado: EstadoFalhou,
			Erro: "sem diretório de estágio configurado; a desinstalação precisa do promotor root"}
	}
	// O PROMOTOR DESTA MÁQUINA SABE LER O BILHETE?
	//
	// O promotor é escrito pelo install.sh, não pela auto-atualização — trocar o
	// binário do agente NÃO troca o script root. Numa máquina instalada antes desta
	// função existir, o bilhete seria deixado e ninguém o leria: o agente relataria
	// "desinstalando", o painel encerraria a ordem como concluída e o agente
	// continuaria rodando para sempre. Prefiro a falha visível: ela vira uma linha
	// no painel dizendo o que fazer, em vez de um servidor fantasma que ninguém
	// procura mais.
	if !promotorSabeDesinstalar(caminhoPromotor) {
		return Resultado{Estado: EstadoFalhou,
			Erro: "o componente root desta máquina é anterior à auto-desinstalação; reinstale o agente pelo painel (ou rode o instalador com --uninstall) para removê-lo"}
	}
	if err := os.MkdirAll(updateDir, 0o755); err != nil {
		return Resultado{Estado: EstadoFalhou, Erro: "não consegui preparar o diretório de estágio: " + err.Error()}
	}
	bilhete := filepath.Join(updateDir, nomeDoBilhete)
	conteudo := fmt.Sprintf("ordenado_em=%s\n", time.Now().UTC().Format(time.RFC3339))
	// Escreve em temporário e renomeia: o promotor pode estar lendo o diretório neste
	// instante, e um arquivo pela metade viraria uma remoção pela metade.
	tmp := bilhete + ".parcial"
	if err := os.WriteFile(tmp, []byte(conteudo), 0o644); err != nil {
		return Resultado{Estado: EstadoFalhou, Erro: "não consegui escrever o pedido de desinstalação: " + err.Error()}
	}
	if err := os.Rename(tmp, bilhete); err != nil {
		_ = os.Remove(tmp)
		return Resultado{Estado: EstadoFalhou, Erro: "não consegui publicar o pedido de desinstalação: " + err.Error()}
	}
	return Resultado{Estado: EstadoEmCurso, ReiniciarParaConcluir: true}
}

// promotorSabeDesinstalar lê o script root e diz se ele conhece o bilhete. A checagem
// é por conteúdo (e não por versão) porque não existe versão do promotor: ele é um
// arquivo que o instalador escreve, e o que importa é se ESTE arquivo tem o trecho.
//
// Lê no máximo 64 KiB: o promotor tem ~4 KiB, e um arquivo gigante no lugar dele é
// motivo para desconfiar, não para carregar na memória.
func promotorSabeDesinstalar(caminho string) bool {
	f, err := os.Open(caminho)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	b := make([]byte, 64<<10)
	n, _ := f.Read(b)
	return strings.Contains(string(b[:n]), nomeDoBilhete)
}

// Caminhos da instalação — espelham deploy/agent/install-macos.sh e o instalador do
// Windows (agent/internal/selfinstall). Literais dos dois lados pelo mesmo motivo do
// bilhete: caminho deduzido de um lado só some do outro sem ninguém notar.
const (
	macLabel = "digital.revoada.painel.agent"
	macPlist = "/Library/LaunchDaemons/digital.revoada.painel.agent.plist"
	macBin   = "/usr/local/bin/revoada-agent"
	macConf  = "/etc/revoada"
	macState = "/var/lib/revoada-agent"

	winDir      = `C:\revoada`
	winTaskName = "Revoada Agent"
)

func removerMacOS() Resultado {
	if os.Geteuid() != 0 {
		return Resultado{Estado: EstadoFalhou,
			Erro: "o agente não está rodando como root; não consigo remover o daemon do launchd"}
	}
	// bootout descarrega o daemon; o erro é ignorado de propósito: se ele já não
	// estiver carregado, o que importa é o resto sair.
	_ = exec.Command("launchctl", "bootout", "system/"+macLabel).Run()
	var falhas []string
	for _, p := range []string{macPlist, macBin} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			falhas = append(falhas, p)
		}
	}
	for _, d := range []string{macConf, macState} {
		if err := os.RemoveAll(d); err != nil {
			falhas = append(falhas, d)
		}
	}
	if len(falhas) > 0 {
		return Resultado{Estado: EstadoFalhou, Erro: "não consegui remover: " + strings.Join(falhas, ", ")}
	}
	return Resultado{Estado: EstadoConcluido}
}

func removerWindows() Resultado {
	// A tarefa agendada some primeiro: enquanto ela existir, o Windows pode subir o
	// agente de novo no meio da remoção dos arquivos.
	if saida, err := exec.Command("schtasks", "/Delete", "/TN", winTaskName, "/F").CombinedOutput(); err != nil {
		return Resultado{Estado: EstadoFalhou,
			Erro: "não consegui apagar a tarefa agendada (é preciso administrador): " + resumo(saida, err)}
	}
	// O diretório inteiro sai, MENOS o executável em uso — o Windows não deixa apagar
	// o binário do processo que está rodando. Ele fica para o próprio serviço de
	// limpeza do sistema; o que importa é que nada mais o inicia.
	entradas, err := os.ReadDir(winDir)
	if err != nil {
		if os.IsNotExist(err) {
			return Resultado{Estado: EstadoConcluido}
		}
		return Resultado{Estado: EstadoFalhou, Erro: "não consegui ler " + winDir + ": " + err.Error()}
	}
	meu, _ := os.Executable()
	var restaram []string
	for _, e := range entradas {
		p := filepath.Join(winDir, e.Name())
		if mesmoArquivo(p, meu) {
			continue
		}
		if err := os.RemoveAll(p); err != nil {
			restaram = append(restaram, e.Name())
		}
	}
	if len(restaram) > 0 {
		return Resultado{Estado: EstadoFalhou, Erro: "não consegui remover: " + strings.Join(restaram, ", ")}
	}
	return Resultado{Estado: EstadoConcluido}
}

func mesmoArquivo(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ra, err1 := filepath.Abs(a)
	rb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return strings.EqualFold(ra, rb)
}

func resumo(saida []byte, err error) string {
	s := strings.TrimSpace(string(saida))
	if s == "" {
		return err.Error()
	}
	const max = 200
	if len(s) > max {
		s = s[:max]
	}
	return s
}

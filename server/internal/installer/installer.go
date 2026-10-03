// Package installer monta o instalador do agente já com a chave de ingestão
// dentro: o administrador escolhe o sistema operacional, baixa UM arquivo, leva
// para o servidor e roda. Nada de editar YAML, colar chave ou montar comando.
//
// O que sai de cada sistema, e por quê:
//
//   - Linux e macOS recebem um SCRIPT (.sh / .command) com os argumentos já
//     preenchidos, seguido do instalador oficial (install.sh / install-macos.sh)
//     na íntegra. Esses scripts fazem muito mais do que escrever um YAML —
//     detectam journald, Docker, grupos do sistema, montam a unit systemd ou o
//     launchd com a cerca de recurso. Reimplementar isso seria pior; então o
//     painel só cola o cabeçalho com a receita em cima do script que já existe e
//     já está testado.
//
//   - Windows recebe um EXECUTÁVEL (.exe): o binário do agente com a receita
//     colada no fim (ver trailer.go). Não há instalador em shell equivalente no
//     Windows, e um .exe que se instala sozinho ao ser executado como
//     administrador é o que mais se aproxima de "só rodar".
//
// Os artefatos de origem (binários e scripts) são lidos do diretório publicado
// pelo deploy — o MESMO que o nginx serve — para que o instalador gerado nunca
// fique defasado em relação ao que está no ar.
package installer

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

// SO é o sistema operacional de destino escolhido no painel.
type SO string

const (
	SOLinux   SO = "linux"
	SOWindows SO = "windows"
	SOMacOS   SO = "macos"
)

// ParseSO valida o que veio do front. Tolera as grafias mais prováveis para que
// um "macOS" ou "Windows" digitado não vire um 400 sem explicação.
func ParseSO(v string) (SO, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "linux":
		return SOLinux, nil
	case "windows", "win":
		return SOWindows, nil
	case "macos", "mac", "darwin", "osx":
		return SOMacOS, nil
	}
	return "", fmt.Errorf("sistema operacional %q não é suportado (use linux, windows ou macos)", v)
}

// Nomes dos artefatos no diretório publicado pelo deploy.
const (
	arqInstallLinux = "install.sh"
	arqInstallMacOS = "install-macos.sh"
	arqAgenteWin    = "revoada-agent.exe"
)

// Opts é tudo que o instalador precisa saber. Vem do handler: a chave recém-criada
// (ou reaproveitada), os endereços públicos e a cerca de recurso configurada.
type Opts struct {
	SO SO
	// Identificacao é o nome que o administrador deu ao servidor (ou ao lote, no
	// instalador universal). Só nomeia o arquivo baixado — quem define o hostname
	// reportado é o próprio host, na instalação, igual ao instalador de 1 linha.
	Identificacao string
	// Key é a chave de ingestão do instalador POR SERVIDOR.
	Key string
	// EnrollToken é o token do instalador UNIVERSAL: no lugar da chave pronta, o
	// arquivo leva um token e cada máquina pede a SUA chave ao instalar. Exatamente
	// um dos dois deve vir preenchido.
	EnrollToken string
	GatewayURL  string
	PanelURL    string
	Probe       bool
	Limites     store.AgentResourceLimits
}

// universal diz se estas opções descrevem o instalador que serve a vários
// servidores. É a presença do token que define — não um sinalizador à parte, que
// poderia contradizer o conteúdo.
func (o Opts) universal() bool { return o.EnrollToken != "" }

// Arquivo é o que vai para o navegador.
type Arquivo struct {
	Nome  string
	Tipo  string
	Bytes []byte
}

// ErrArtefatoAusente indica que o diretório publicado não tem o script/binário
// pedido. É a falha esperada quando o painel roda sem o dist montado (ex.: dev
// sem `make agent-windows`), e o handler a traduz numa mensagem acionável.
var ErrArtefatoAusente = errors.New("artefato do agente não encontrado")

// Gerar monta o arquivo de instalação. `dist` é o diretório publicado pelo deploy
// (o mesmo que o nginx serve), passado como fs.FS para ser testável sem disco.
func Gerar(dist fs.FS, o Opts) (Arquivo, error) {
	if strings.TrimSpace(o.Key) == "" && strings.TrimSpace(o.EnrollToken) == "" {
		return Arquivo{}, errors.New("instalador sem chave de ingestão nem token de inscrição")
	}
	if strings.TrimSpace(o.GatewayURL) == "" {
		return Arquivo{}, errors.New("endereço do gateway não configurado (REVOADA_GATEWAY_PUBLIC_URL)")
	}
	if o.universal() && strings.TrimSpace(o.PanelURL) == "" {
		return Arquivo{}, errors.New("endereço do painel não configurado (REVOADA_PUBLIC_URL), sem ele a máquina não sabe onde pedir a chave")
	}
	base := nomeBase(o)
	switch o.SO {
	case SOLinux:
		b, err := gerarScript(dist, arqInstallLinux, o)
		return Arquivo{Nome: base + ".sh", Tipo: "application/octet-stream", Bytes: b}, err
	case SOMacOS:
		b, err := gerarScript(dist, arqInstallMacOS, o)
		// .command em vez de .sh: no macOS o Finder abre um .command com duplo
		// clique, dentro do Terminal. Um .sh ele só oferece "abrir com…".
		return Arquivo{Nome: base + ".command", Tipo: "application/octet-stream", Bytes: b}, err
	case SOWindows:
		b, err := gerarWindows(dist, o)
		return Arquivo{Nome: base + ".exe", Tipo: "application/octet-stream", Bytes: b}, err
	}
	return Arquivo{}, fmt.Errorf("sistema operacional %q não é suportado", o.SO)
}

// gerarScript cola um cabeçalho com os argumentos já preenchidos em cima do
// instalador oficial. O truque é o `set --`: ele define os parâmetros posicionais
// do shell, e o script que vem abaixo os lê no seu próprio laço de flags, sem
// saber que não veio ninguém digitando na linha de comando.
func gerarScript(dist fs.FS, nome string, o Opts) ([]byte, error) {
	corpo, err := lerArtefato(dist, nome)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# ─────────────────────────────────────────────────────────────────────\n")
	if o.universal() {
		b.WriteString("#  Instalador do agente do Revoada, UNIVERSAL\n")
	} else {
		b.WriteString("#  Instalador do agente do Revoada\n")
	}
	if id := strings.TrimSpace(o.Identificacao); id != "" {
		if o.universal() {
			fmt.Fprintf(&b, "#  Lote:     %s\n", umaLinha(id))
		} else {
			fmt.Fprintf(&b, "#  Servidor: %s\n", umaLinha(id))
		}
	}
	if o.PanelURL != "" {
		fmt.Fprintf(&b, "#  Painel:   %s\n", umaLinha(o.PanelURL))
	}
	b.WriteString("#\n")
	b.WriteString("#  Rode no servidor que será monitorado, como root:\n")
	b.WriteString("#      sudo sh " + nomeBase(o) + extDe(o.SO) + "\n")
	b.WriteString("#\n")
	if o.universal() {
		b.WriteString("#  Este mesmo arquivo serve para QUANTOS SERVIDORES você quiser: ele\n")
		b.WriteString("#  não carrega uma chave, e sim um token de inscrição. Ao rodar, cada\n")
		b.WriteString("#  máquina pede ao painel a chave DELA, revogar uma não derruba as\n")
		b.WriteString("#  outras. Guarde o arquivo como uma senha: quem o tiver consegue\n")
		b.WriteString("#  cadastrar servidores no painel. Para estancar, revogue o token em\n")
		b.WriteString("#  Infraestrutura → Instalar agente.\n")
	} else {
		b.WriteString("#  A chave de ingestão deste servidor já está embutida logo abaixo.\n")
		b.WriteString("#  Trate este arquivo como uma senha: quem o tiver consegue enviar\n")
		b.WriteString("#  dados em nome deste servidor. Apague-o depois de instalar.\n")
	}
	b.WriteString("# ─────────────────────────────────────────────────────────────────────\n")
	b.WriteString(argumentos(o) + "\n\n")
	b.Write(corpo)
	return []byte(b.String()), nil
}

// argumentos monta a linha `set -- …` com tudo que o instalador oficial espera.
// A chave vai entre aspas simples e escapada, para que um caractere inesperado
// não escape do argumento e vire comando.
func argumentos(o Opts) string {
	var args []string
	if o.universal() {
		// Sem chave: o script pede a dele ao painel, então precisa saber o endereço.
		args = append(args, "--enroll-token "+aspas(o.EnrollToken), "--panel "+aspas(o.PanelURL))
	} else {
		args = append(args, "--key "+aspas(o.Key))
		// O endereço do painel vai junto mesmo sem inscrição: é dele que o agente
		// pergunta se existe versão nova. O install.sh consegue derivá-lo da
		// --agent-url, mas depender dessa dedução significaria que uma mudança no
		// layout do dist deixaria hosts instalados sem auto-atualização — e o
		// sintoma seria nenhum: o host simplesmente ficaria para trás em silêncio.
		if p := strings.TrimSpace(o.PanelURL); p != "" {
			args = append(args, "--panel "+aspas(p))
		}
	}
	args = append(args,
		"--gateway "+aspas(o.GatewayURL),
		"--agent-url "+aspas(agentURL(o.PanelURL, o.SO)),
	)
	if o.Probe {
		args = append(args, "--probe")
	}
	// Valores numéricos vindos das configurações; sem aspas, igual ao comando de
	// uma linha que o painel já mostra.
	l := o.Limites
	args = append(args, fmt.Sprintf("--mem-max %dM --mem-high %dM --cpu-quota %d%% --nice %d --tasks-max %d --mem-soft %d --max-procs %d",
		l.MemoryMaxMB, l.MemoryHighMB, l.CPUQuotaPct, l.Nice, l.TasksMax, l.MemSoftMB, l.MaxProcs))
	return "set -- " + strings.Join(args, " ")
}

// gerarWindows cola a receita no fim do binário do agente.
func gerarWindows(dist fs.FS, o Opts) ([]byte, error) {
	bin, err := lerArtefato(dist, arqAgenteWin)
	if err != nil {
		return nil, err
	}
	return AppendRecipe(bin, Recipe{
		GatewayURL:    o.GatewayURL,
		Key:           o.Key,
		EnrollToken:   o.EnrollToken,
		PanelURL:      o.PanelURL,
		Probe:         o.Probe,
		MemoryLimitMB: o.Limites.MemSoftMB,
		MaxProcs:      o.Limites.MaxProcs,
	})
}

func lerArtefato(dist fs.FS, nome string) ([]byte, error) {
	if dist == nil {
		return nil, fmt.Errorf("%w: %s", ErrArtefatoAusente, nome)
	}
	b, err := fs.ReadFile(dist, nome)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrArtefatoAusente, nome)
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("%w: %s está vazio", ErrArtefatoAusente, nome)
	}
	return b, nil
}

// agentURL é de onde o script baixa o binário do agente: sempre o mesmo host do
// painel.
//
// No macOS a URL é um PREFIXO, não o arquivo final: Apple Silicon e Intel exigem
// binários diferentes e só o Mac sabe qual é o seu, então o install-macos.sh
// acrescenta "-arm64" ou "-amd64" depois de olhar o `uname -m`.
func agentURL(panelURL string, so SO) string {
	base := strings.TrimRight(panelURL, "/") + "/revoada-agent"
	if so == SOMacOS {
		return base + "-darwin"
	}
	return base
}

// nomeBase monta o nome do arquivo sem extensão. O "universal" no nome não é
// enfeite: é o que impede alguém de confundir, meses depois, o arquivo que serve a
// frota inteira com o de um servidor só.
func nomeBase(o Opts) string {
	if o.universal() {
		return "instalar-revoada-universal-" + slug(o.Identificacao)
	}
	return "instalar-revoada-" + slug(o.Identificacao)
}

func extDe(s SO) string {
	if s == SOMacOS {
		return ".command"
	}
	return ".sh"
}

// aspas envolve um valor em aspas simples de shell. Dentro delas nada é
// interpretado; a única sequência necessária é a que fecha, escapa e reabre a
// aspa para representar a própria aspa simples.
func aspas(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

// umaLinha achata quebras de linha para que um nome esquisito não consiga sair do
// bloco de comentários do cabeçalho e virar comando.
func umaLinha(v string) string {
	return strings.NewReplacer("\n", " ", "\r", " ").Replace(v)
}

var naoSlug = regexp.MustCompile(`[^a-z0-9]+`)

// slug transforma a identificação do servidor num pedaço de nome de arquivo
// seguro: minúsculas, sem acento nem espaço, sem caminho. Vazio vira "agente".
func slug(v string) string {
	s := naoSlug.ReplaceAllString(strings.ToLower(semAcento(v)), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "agente"
	}
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}

// semAcento troca as letras acentuadas do português pelas equivalentes simples —
// o bastante para "São Paulo 01" virar "sao-paulo-01" em vez de "s-o-paulo-01".
func semAcento(v string) string {
	return strings.NewReplacer(
		"á", "a", "à", "a", "ã", "a", "â", "a", "ä", "a",
		"é", "e", "ê", "e", "ë", "e",
		"í", "i", "ï", "i",
		"ó", "o", "ô", "o", "õ", "o", "ö", "o",
		"ú", "u", "ü", "u",
		"ç", "c", "ñ", "n",
	).Replace(strings.ToLower(v))
}

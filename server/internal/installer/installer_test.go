package installer

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

const corpoInstallSh = "#!/bin/sh\nset -eu\n# instalador oficial de mentira\necho instalando \"$@\"\n"

func distFalso() fstest.MapFS {
	return fstest.MapFS{
		arqInstallLinux: {Data: []byte(corpoInstallSh)},
		arqInstallMacOS: {Data: []byte("#!/bin/sh\n# macos\n")},
		arqAgenteWin:    {Data: []byte("MZ isto finge ser o revoada-agent.exe")},
	}
}

func opts(so SO) Opts {
	return Opts{
		SO:            so,
		Identificacao: "App Prod 01",
		Key:           "chave-secreta",
		GatewayURL:    "https://painel.exemplo",
		PanelURL:      "https://painel.exemplo",
		Limites:       store.DefaultAgentResourceLimits(),
	}
}

func TestLinuxSaiPronedoParaRodar(t *testing.T) {
	arq, err := Gerar(distFalso(), opts(SOLinux))
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	if arq.Nome != "instalar-revoada-app-prod-01.sh" {
		t.Errorf("nome = %q", arq.Nome)
	}
	s := string(arq.Bytes)
	if !strings.HasPrefix(s, "#!/bin/sh\n") {
		t.Errorf("script sem shebang na primeira linha:\n%s", s[:60])
	}
	// A receita tem de estar toda na linha `set --`, e ANTES do corpo do
	// instalador — é assim que o script oficial a lê no seu laço de flags.
	iSet := strings.Index(s, "set -- ")
	iCorpo := strings.Index(s, "echo instalando")
	if iSet < 0 || iCorpo < 0 || iSet > iCorpo {
		t.Fatalf("ordem errada: set em %d, corpo em %d", iSet, iCorpo)
	}
	for _, esperado := range []string{
		"--key 'chave-secreta'",
		"--gateway 'https://painel.exemplo'",
		"--agent-url 'https://painel.exemplo/revoada-agent'",
		"--mem-max 256M", "--mem-soft 220", "--max-procs 2",
	} {
		if !strings.Contains(s, esperado) {
			t.Errorf("faltou %q na linha de argumentos", esperado)
		}
	}
	// O instalador oficial vai INTEIRO, sem edição: é ele que sabe detectar
	// journald/Docker e montar a unit. Qualquer recorte aqui seria uma segunda
	// implementação silenciosamente divergente.
	if !strings.Contains(s, corpoInstallSh) {
		t.Errorf("o corpo do install.sh não foi embutido na íntegra")
	}
}

func TestSondaSoEntraQuandoPedida(t *testing.T) {
	o := opts(SOLinux)
	if s := string(mustGerar(t, o).Bytes); strings.Contains(s, "--probe") {
		t.Errorf("--probe apareceu sem ter sido pedido")
	}
	o.Probe = true
	if s := string(mustGerar(t, o).Bytes); !strings.Contains(s, "--probe") {
		t.Errorf("--probe não apareceu mesmo pedido")
	}
}

func TestMacOSSaiComoCommandParaAbrirNoFinder(t *testing.T) {
	arq := mustGerar(t, opts(SOMacOS))
	if !strings.HasSuffix(arq.Nome, ".command") {
		t.Errorf("nome = %q, queria terminar em .command (duplo clique no Finder)", arq.Nome)
	}
	// No macOS a URL do binário é um PREFIXO: quem completa com -arm64/-amd64 é o
	// script, depois de olhar o processador. Mandar a URL do Linux instalaria um
	// binário que não roda.
	if s := string(arq.Bytes); !strings.Contains(s, "--agent-url 'https://painel.exemplo/revoada-agent-darwin'") {
		t.Errorf("prefixo do binário do macOS errado:\n%s", primeiraLinhaSet(s))
	}
}

func TestWindowsSaiComOBinarioIntactoMaisAReceita(t *testing.T) {
	dist := distFalso()
	original := dist[arqAgenteWin].Data
	arq := mustGerar(t, opts(SOWindows))
	if arq.Nome != "instalar-revoada-app-prod-01.exe" {
		t.Errorf("nome = %q", arq.Nome)
	}
	// O executável precisa continuar sendo o mesmo executável: os bytes do
	// binário original vêm primeiro, byte a byte.
	if !bytes.HasPrefix(arq.Bytes, original) {
		t.Fatalf("o binário original não está intacto no começo do arquivo")
	}
	rec, fim := lerTrailer(t, arq.Bytes)
	if fim != int64(len(original)) {
		t.Errorf("fim do binário = %d, queria %d", fim, len(original))
	}
	if rec.Key != "chave-secreta" || rec.GatewayURL != "https://painel.exemplo" {
		t.Errorf("receita = %+v", rec)
	}
	// A cerca suave viaja junto: o agente do Windows aplica GOMEMLIMIT/GOMAXPROCS
	// sozinho, já que lá não existe a unit systemd que segura o resto.
	if rec.MemoryLimitMB != 220 || rec.MaxProcs != 2 {
		t.Errorf("limites não vieram na receita: %+v", rec)
	}
}

// lerTrailer decodifica o trailer do jeito que o AGENTE decodifica (ver
// agent/internal/selfinstall). Duplicado aqui de propósito: se um dos lados mudar
// o formato sem o outro, este teste falha — é o alarme de divergência entre os
// dois módulos.
func lerTrailer(t *testing.T, arq []byte) (Recipe, int64) {
	t.Helper()
	if len(arq) < len(Magic)+8 {
		t.Fatalf("arquivo curto demais para ter trailer")
	}
	foot := arq[len(arq)-len(Magic)-8:]
	if string(foot[:len(Magic)]) != Magic {
		t.Fatalf("magic ausente no fim do arquivo")
	}
	n := binary.BigEndian.Uint64(foot[len(Magic):])
	fim := int64(len(arq)) - int64(len(Magic)) - 8 - int64(n)
	var rec Recipe
	if err := json.Unmarshal(arq[fim:fim+int64(n)], &rec); err != nil {
		t.Fatalf("JSON da receita ilegível: %v", err)
	}
	return rec, fim
}

// universal monta as opções do instalador que serve a vários servidores: token de
// inscrição no lugar da chave.
func universal(so SO) Opts {
	o := opts(so)
	o.Key = ""
	o.EnrollToken = "token-de-inscricao"
	o.Identificacao = "Mutirão agosto"
	return o
}

func TestUniversalLevaTokenEmVezDeChave(t *testing.T) {
	arq := mustGerar(t, universal(SOLinux))
	// "universal" no nome do arquivo: meses depois, ninguém deve confundir o
	// instalador da frota inteira com o de um servidor só.
	if arq.Nome != "instalar-revoada-universal-mutirao-agosto.sh" {
		t.Errorf("nome = %q", arq.Nome)
	}
	s := string(arq.Bytes)
	if strings.Contains(primeiraLinhaSet(s), "--key ") {
		t.Errorf("o instalador universal não pode carregar chave de ingestão:\n%s", primeiraLinhaSet(s))
	}
	for _, esperado := range []string{
		"--enroll-token 'token-de-inscricao'",
		// Sem --panel a máquina não sabe a quem pedir a chave; o script recusa.
		"--panel 'https://painel.exemplo'",
		"--gateway 'https://painel.exemplo'",
	} {
		if !strings.Contains(s, esperado) {
			t.Errorf("faltou %q:\n%s", esperado, primeiraLinhaSet(s))
		}
	}
	// O cabeçalho tem de dizer que o arquivo serve para várias máquinas e como
	// estancar — é a única documentação que acompanha quem recebe o arquivo.
	for _, frase := range []string{"UNIVERSAL", "QUANTOS SERVIDORES", "revogue o token"} {
		if !strings.Contains(s, frase) {
			t.Errorf("cabeçalho sem %q", frase)
		}
	}
}

func TestUniversalNoWindowsViajaNaReceita(t *testing.T) {
	arq := mustGerar(t, universal(SOWindows))
	rec, _ := lerTrailer(t, arq.Bytes)
	if rec.EnrollToken != "token-de-inscricao" || rec.Key != "" {
		t.Errorf("receita = %+v; queria token sem chave", rec)
	}
	// O .exe precisa saber ONDE pedir a chave; sem PanelURL ele instalaria mudo.
	if rec.PanelURL == "" {
		t.Errorf("receita universal sem endereço do painel: %+v", rec)
	}
}

func TestUniversalSemEnderecoDoPainelNaoGera(t *testing.T) {
	// REVOADA_PUBLIC_URL não configurado: o arquivo sairia sem para onde pedir a
	// chave e falharia em TODA máquina. Recusar aqui, onde alguém lê o motivo.
	o := universal(SOLinux)
	o.PanelURL = ""
	if _, err := Gerar(distFalso(), o); err == nil {
		t.Fatalf("gerou instalador universal sem endereço do painel")
	}
}

func TestArtefatoAusenteViraErroReconhecivel(t *testing.T) {
	// Dist sem o .exe: acontece de verdade quando o deploy ainda não publicou o
	// binário do Windows. O handler traduz isto numa instrução para o admin, então
	// o erro precisa ser identificável — não uma string qualquer.
	dist := fstest.MapFS{arqInstallLinux: {Data: []byte(corpoInstallSh)}}
	if _, err := Gerar(dist, opts(SOWindows)); !errors.Is(err, ErrArtefatoAusente) {
		t.Fatalf("erro = %v, queria ErrArtefatoAusente", err)
	}
}

func TestSemDistNaoExplode(t *testing.T) {
	if _, err := Gerar(nil, opts(SOLinux)); !errors.Is(err, ErrArtefatoAusente) {
		t.Fatalf("erro = %v, queria ErrArtefatoAusente", err)
	}
}

func TestNomeDoArquivoNaoEscapaDoDiretorio(t *testing.T) {
	// A identificação vem digitada por gente; ela batiza o arquivo baixado e nunca
	// pode virar caminho nem cabeçalho HTTP com quebra de linha.
	casos := map[string]string{
		"São Paulo 01":      "instalar-revoada-sao-paulo-01.sh",
		"../../etc/passwd":  "instalar-revoada-etc-passwd.sh",
		`servidor"; rm -rf`: "instalar-revoada-servidor-rm-rf.sh",
		"   ":               "instalar-revoada-agente.sh",
	}
	for entrada, esperado := range casos {
		o := opts(SOLinux)
		o.Identificacao = entrada
		arq := mustGerar(t, o)
		if arq.Nome != esperado {
			t.Errorf("identificação %q => nome %q, queria %q", entrada, arq.Nome, esperado)
		}
	}
}

func TestChaveComAspaSimplesNaoEscapaDoArgumento(t *testing.T) {
	// Defesa em profundidade: a chave é gerada pelo painel e nunca teria aspa,
	// mas o script é executado como root — o custo de estar errado é alto demais
	// para depender disso.
	o := opts(SOLinux)
	o.Key = `a'; touch /tmp/x; echo '`
	s := string(mustGerar(t, o).Bytes)
	if !strings.Contains(s, `--key 'a'\''; touch /tmp/x; echo '\'''`) {
		t.Errorf("aspa simples não foi escapada:\n%s", primeiraLinhaSet(s))
	}
}

func TestIdentificacaoNaoQuebraOCabecalhoDeComentarios(t *testing.T) {
	o := opts(SOLinux)
	o.Identificacao = "servidor\nrm -rf /"
	s := string(mustGerar(t, o).Bytes)
	if strings.Contains(s, "\nrm -rf /") {
		t.Errorf("quebra de linha vazou do comentário para o corpo do script")
	}
}

func TestParseSOAceitaAsGrafiasProvaveis(t *testing.T) {
	casos := map[string]SO{
		"linux": SOLinux, "Windows": SOWindows, "win": SOWindows,
		"macos": SOMacOS, "macOS": SOMacOS, "darwin": SOMacOS, " osx ": SOMacOS,
	}
	for entrada, quero := range casos {
		got, err := ParseSO(entrada)
		if err != nil || got != quero {
			t.Errorf("ParseSO(%q) = %q, %v; queria %q", entrada, got, err, quero)
		}
	}
	if _, err := ParseSO("solaris"); err == nil {
		t.Errorf("ParseSO(\"solaris\") deveria recusar")
	}
}

func TestSemChaveNaoGera(t *testing.T) {
	o := opts(SOLinux)
	o.Key = ""
	if _, err := Gerar(distFalso(), o); err == nil {
		t.Fatalf("gerou instalador sem chave de ingestão")
	}
}

func mustGerar(t *testing.T, o Opts) Arquivo {
	t.Helper()
	arq, err := Gerar(distFalso(), o)
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	return arq
}

func primeiraLinhaSet(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "set -- ") {
			return l
		}
	}
	return ""
}

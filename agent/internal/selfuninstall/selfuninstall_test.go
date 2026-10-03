package selfuninstall

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestBilheteParaOPromotor trava o contrato do Linux: o agente NÃO remove nada por
// conta própria — ele roda como `revoada`, sem privilégio — e sim deixa um bilhete que
// o promotor root lê no start seguinte. Se este arquivo mudar de nome ou de lugar, a
// desinstalação vira silêncio: o agente diz "estou saindo" e continua lá para sempre.
func TestBilheteParaOPromotor(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("o caminho do bilhete só existe no Linux")
	}
	dir := t.TempDir()
	comPromotorCapaz(t, dir)

	res := Executar(dir)

	if res.Estado != EstadoEmCurso {
		t.Errorf("no Linux a remoção só termina no restart: estado = %q", res.Estado)
	}
	if !res.ReiniciarParaConcluir {
		t.Error("o agente precisa pedir o reinício, senão o promotor nunca roda")
	}
	bilhete := filepath.Join(dir, "desinstalar")
	if _, err := os.Stat(bilhete); err != nil {
		t.Fatalf("o bilhete que o promotor procura não foi criado: %v", err)
	}
	// Nada de parcial deixado para trás: o promotor lê o diretório e um arquivo pela
	// metade viraria uma remoção pela metade.
	if _, err := os.Stat(bilhete + ".parcial"); !os.IsNotExist(err) {
		t.Error("sobrou o arquivo temporário no diretório de estágio")
	}
}

// TestSemDiretorioDeEstagioFalhaExplicito: um agente sem diretório de estágio (modo
// cron, instalação antiga) NÃO tem promotor root. Dizer "desinstalando" aí seria
// mentir para o painel, que marcaria o servidor como resolvido enquanto o agente
// continua de pé.
func TestSemDiretorioDeEstagioFalhaExplicito(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("o caminho do bilhete só existe no Linux")
	}
	comPromotorCapaz(t, t.TempDir())
	res := Executar("   ")
	if res.Estado != EstadoFalhou {
		t.Errorf("sem promotor a resposta tem de ser falha explícita: %+v", res)
	}
	if res.Erro == "" {
		t.Error("a falha precisa explicar o motivo — é o que vai aparecer no painel")
	}
}

// TestPromotorAntigoFalhaEmVezDeMentir cobre a frota que já está instalada.
//
// O promotor root é escrito pelo INSTALADOR, e a auto-atualização troca só o binário
// do agente — então um host instalado antes desta feature roda um agente novo com um
// promotor velho. Se o agente deixasse o bilhete assim mesmo, ele relataria
// "desinstalando", o painel encerraria a ordem como concluída, e o agente seguiria
// rodando para sempre num servidor que ninguém procura mais. Falha visível é melhor.
func TestPromotorAntigoFalhaEmVezDeMentir(t *testing.T) {
	dir := t.TempDir()
	antigo := filepath.Join(dir, "promote-antigo.sh")
	if err := os.WriteFile(antigo, []byte("#!/bin/sh\n# só promove binário\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if promotorSabeDesinstalar(antigo) {
		t.Error("um promotor sem o trecho do bilhete não pode ser dado como capaz")
	}
	if promotorSabeDesinstalar(filepath.Join(dir, "nao-existe.sh")) {
		t.Error("promotor ausente (modo cron, instalação manual) também não é capaz")
	}

	novo := filepath.Join(dir, "promote-novo.sh")
	corpo := "#!/bin/sh\nDESINSTALAR=\"$DIR/" + nomeDoBilhete + "\"\n"
	if err := os.WriteFile(novo, []byte(corpo), 0o755); err != nil {
		t.Fatal(err)
	}
	if !promotorSabeDesinstalar(novo) {
		t.Error("o promotor atual precisa ser reconhecido, senão a feature nunca roda")
	}
}

// comPromotorCapaz aponta a checagem para um promotor de mentira que conhece o
// bilhete, para o teste exercitar o caminho feliz sem precisar de /usr/local.
func comPromotorCapaz(t *testing.T, dir string) {
	t.Helper()
	falso := filepath.Join(dir, "promote-falso.sh")
	corpo := "#!/bin/sh\nDESINSTALAR=\"$DIR/" + nomeDoBilhete + "\"\n"
	if err := os.WriteFile(falso, []byte(corpo), 0o755); err != nil {
		t.Fatal(err)
	}
	original := caminhoPromotor
	caminhoPromotor = falso
	t.Cleanup(func() { caminhoPromotor = original })
}

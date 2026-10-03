package logtail

import (
	"io"
	"strings"
	"testing"
)

// Provas do teto ÚNICO por linha.
//
// Antes: 256 KiB no tail de arquivo, 1 MiB no demux do Docker, 4 MiB no scanner do
// journald — e 1 MiB na documentação. Quatro respostas para a mesma pergunta,
// dependendo de qual coletor produziu a linha.

// PROVA: uma linha gigante é cortada no teto do pacote e MARCADA.
func TestLerLinhasCortaNoTetoUnico(t *testing.T) {
	gigante := strings.Repeat("a", 4<<20) // 4 MiB, o antigo teto do journald
	var lidas []string
	lerLinhas(strings.NewReader(gigante+"\n"), func(l []byte) bool {
		lidas = append(lidas, string(l))
		return true
	})
	if len(lidas) != 1 {
		t.Fatalf("uma linha comprida virou %d linhas — o painel mostraria mensagens que não existem", len(lidas))
	}
	if !strings.HasSuffix(lidas[0], truncMark) {
		t.Fatal("cortou sem marcar: uma linha cortada em silêncio é lida como a linha inteira")
	}
	if n := len(lidas[0]) - len(truncMark); n != maxLineBytes {
		t.Fatalf("cortou em %d bytes, o teto do pacote é %d", n, maxLineBytes)
	}
}

// PROVA: o fluxo CONTINUA depois da linha comprida.
//
// É a razão de não usar bufio.Scanner: ele aborta a varredura inteira ao estourar o
// buffer (ErrTooLong). Nos coletores de stream isso não é "uma linha perdida", é o
// coletor morrendo — e o Supervise o reergueria para morrer na mesma linha, um laço
// de reinício disparado por UMA linha comprida de uma aplicação qualquer.
func TestLinhaGiganteNaoDerrubaOFluxo(t *testing.T) {
	fluxo := "antes\n" + strings.Repeat("b", 2<<20) + "\ndepois\n"
	var lidas []string
	lerLinhas(strings.NewReader(fluxo), func(l []byte) bool {
		lidas = append(lidas, string(l))
		return true
	})
	if len(lidas) != 3 {
		t.Fatalf("o fluxo parou na linha comprida: %d linha(s) lidas", len(lidas))
	}
	if lidas[0] != "antes" || lidas[2] != "depois" {
		t.Fatalf("linhas fora de ordem/perdidas: %q, %q", lidas[0], lidas[2])
	}
}

// PROVA: a cauda sem \n ainda é entregue. É a última mensagem de um serviço que
// morreu sem quebrar linha — justamente a que explica por que ele morreu.
func TestCaudaSemQuebraDeLinhaEEntregue(t *testing.T) {
	var lidas []string
	lerLinhas(strings.NewReader("uma\nduas sem fim"), func(l []byte) bool {
		lidas = append(lidas, string(l))
		return true
	})
	if len(lidas) != 2 || lidas[1] != "duas sem fim" {
		t.Fatalf("perdeu a cauda sem \\n: %q", lidas)
	}
}

// PROVA: o \r do CRLF não vaza para o corpo (log de aplicação Windows num container).
func TestLerLinhasRemoveCRLF(t *testing.T) {
	var lidas []string
	lerLinhas(strings.NewReader("com crlf\r\n"), func(l []byte) bool {
		lidas = append(lidas, string(l))
		return true
	})
	if len(lidas) != 1 || lidas[0] != "com crlf" {
		t.Fatalf("CRLF não tratado: %q", lidas)
	}
}

// PROVA: devolver false interrompe a leitura (é como o cancelamento do contexto
// chega até aqui, sem deixar a goroutine do coletor pendurada).
func TestLerLinhasParaQuandoOConsumidorPede(t *testing.T) {
	n := 0
	lerLinhas(io.MultiReader(strings.NewReader(strings.Repeat("x\n", 1000))), func([]byte) bool {
		n++
		return n < 3
	})
	if n != 3 {
		t.Fatalf("não parou quando pedido: leu %d linhas", n)
	}
}

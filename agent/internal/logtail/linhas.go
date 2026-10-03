package logtail

import (
	"bufio"
	"bytes"
	"io"
)

// Leitura de linhas com teto ÚNICO, compartilhada pelos coletores de stream.
//
// # Por que existe
//
// O teto por linha estava escrito em três lugares e com três valores: 256 KiB no tail
// de arquivo, 1 MiB no demux do Docker e 4 MiB no scanner do journald — enquanto a
// documentação prometia 1 MiB. Três garantias diferentes sob o mesmo nome não é um
// detalhe de estilo: significa que a resposta para "quanto o agente pode mandar por
// linha?" depende de qual coletor a produziu, e ninguém que opera o painel sabe disso.
// Agora o valor é um só (maxLineBytes, em logtail.go) e vale para todo mundo.
//
// # Por que não bufio.Scanner
//
// O Scanner ABORTA a varredura ao encontrar uma linha maior que o seu buffer
// (bufio.ErrTooLong). Nos coletores de stream isso não é "uma linha perdida": é o
// coletor inteiro morrendo — o journald e o docker seriam reerguidos pelo Supervise e
// tornariam a morrer na mesma linha, um laço de reinício disparado por UMA linha
// comprida de uma aplicação qualquer. Ler com bufio.Reader e TRUNCAR mantém o fluxo
// vivo: a linha comprida chega cortada e marcada, e o resto do log continua.
type acumulador struct {
	buf      []byte
	estourou bool
}

// anexar copia o fragmento respeitando o teto. O que passar do teto é contado como
// estouro e descartado — mas a linha continua sendo lida até o \n, senão o resto dela
// viraria "linhas" falsas no painel.
func (a *acumulador) anexar(frag []byte) {
	if len(a.buf) >= maxLineBytes {
		if len(frag) > 0 {
			a.estourou = true
		}
		return
	}
	cabe := maxLineBytes - len(a.buf)
	if len(frag) > cabe {
		a.buf = append(a.buf, frag[:cabe]...)
		a.estourou = true
		return
	}
	a.buf = append(a.buf, frag...)
}

// fechar devolve a linha pronta (sem \r\n final, com o marcador se foi cortada) e
// reinicia o acumulador. A fatia devolvida é reaproveitada na linha seguinte: quem
// recebe deve consumi-la na hora (converter para string, fazer o parse) e nunca
// guardá-la.
func (a *acumulador) fechar() []byte {
	linha := bytes.TrimRight(a.buf, "\r\n")
	if a.estourou {
		linha = append(linha, truncMark...)
	}
	return linha
}

func (a *acumulador) reiniciar() {
	a.buf = a.buf[:0]
	a.estourou = false
}

// lerLinhas lê r linha a linha e chama fn para cada uma. Para quando fn devolve
// false, quando o fluxo acaba ou quando dá erro de leitura (que é como um stream
// cancelado pelo contexto chega até aqui). Nunca aborta por causa de uma linha longa.
func lerLinhas(r io.Reader, fn func([]byte) bool) {
	br := bufio.NewReaderSize(r, 64*1024)
	var acc acumulador
	for {
		frag, err := br.ReadSlice('\n')
		acc.anexar(frag)
		switch err {
		case nil:
			if !fn(acc.fechar()) {
				return
			}
			acc.reiniciar()
		case bufio.ErrBufferFull:
			// Linha maior que o buffer de leitura: continua acumulando (o teto de
			// maxLineBytes é aplicado dentro do anexar) até achar o \n.
			continue
		default:
			// Fim do fluxo. A cauda sem \n ainda é uma linha: descartá-la perderia a
			// última mensagem de um serviço que morreu sem quebrar linha — justamente a
			// que explica por que ele morreu.
			if len(acc.buf) > 0 {
				fn(acc.fechar())
			}
			return
		}
	}
}

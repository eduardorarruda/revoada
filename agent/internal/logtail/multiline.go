package logtail

import (
	"fmt"
	"regexp"
	"strings"
)

// Heurística de costura de stack traces multiline. Uma linha é continuação da
// entrada anterior quando começa com espaço/tab, ou casa um padrão típico de
// frame de stack trace (Java `at`/`Caused by`/`... N more`, Python `File "…"`),
// ou é a linha da exceção que fecha um traceback Python já aberto.
var (
	// Nota: frames Java indentados ("\tat …") já são pegos pelo teste de indentação
	// em continuation(); aqui NÃO incluímos `at ` sem indentação, senão uma linha
	// legítima como "at 03:00 backup finished" seria colada na entrada anterior.
	reContHead   = regexp.MustCompile(`^(Caused by:|\s*\.\.\.|\s+File ")`)
	reTraceStart = regexp.MustCompile(`^Traceback \(most recent call last\):`)
)

// Teto de uma entrada costurada. Existe porque a costura não tinha limite algum: um
// processo em loop de exceção (ou um `print` de milhares de frames) entregava um
// único registro com dezenas de milhares de linhas. Medido neste repositório, num
// i5-7200U: 20.000 linhas custavam 5,9 s de CPU e 7,1 GB alocados; 60.000 linhas,
// 52 s e 64 GB — dentro de um ciclo de 10 s, ou seja, o agente deixava de coletar
// para montar uma string. O rate limiter não protege disso: ele conta REGISTROS, e
// a bomba acontece antes, ao montar o registro.
//
// 200 linhas é mais que o suficiente para diagnosticar um stack trace real (um
// traceback Java profundo passa raspando de 60 frames); 64 KiB é o segundo teto,
// para o caso de poucas linhas muito longas.
const (
	maxStitchLines = 200
	maxStitchBytes = 64 << 10
)

// continuation reporta se `line` continua a entrada anterior. `tb` indica que
// estamos dentro de um traceback Python (as linhas indentadas seguintes contam
// como continuação, e a primeira linha NÃO indentada é a mensagem da exceção que
// encerra o traceback). Função pura, base do stitching de arquivos e docker.
func continuation(line string, tb bool) (cont, nextTB bool) {
	switch {
	case line == "":
		return false, false
	case line[0] == ' ' || line[0] == '\t':
		return true, tb
	case reContHead.MatchString(line):
		return true, tb
	case reTraceStart.MatchString(line):
		return true, true
	case tb:
		// Linha não indentada dentro de um traceback = linha da exceção
		// (ex.: "ValueError: boom"): é continuação e encerra o traceback.
		return true, false
	default:
		return false, false
	}
}

// stitchBuf acumula uma entrada costurada. Usa strings.Builder porque a versão
// anterior fazia `out[len(out)-1] += "\n" + line`: cada continuação realocava e
// COPIAVA a entrada inteira, dando custo quadrático no número de linhas. Com o
// Builder o custo é linear e a memória, amortizada.
type stitchBuf struct {
	b       strings.Builder
	open    bool
	lines   int
	omitted int
}

func (s *stitchBuf) start(line string) {
	s.b.Reset()
	s.b.WriteString(line)
	s.open = true
	s.lines = 1
	s.omitted = 0
}

// add anexa uma continuação, ou apenas conta a omissão se algum teto já estourou.
// A primeira linha nunca é recusada: ela já vem limitada por maxLineBytes na
// leitura, e descartá-la esconderia a mensagem do erro — que é o que interessa.
func (s *stitchBuf) add(line string) {
	if s.lines >= maxStitchLines || s.b.Len()+len(line)+1 > maxStitchBytes {
		s.omitted++
		return
	}
	s.b.WriteByte('\n')
	s.b.WriteString(line)
	s.lines++
}

// done fecha a entrada. O corte é ANUNCIADO no próprio corpo: um stack trace
// cortado em silêncio lê como "é só isso que existe", e quem depura conclui que o
// frame que causou o problema não estava lá.
func (s *stitchBuf) done() string {
	txt := s.b.String()
	if s.omitted > 0 {
		txt += fmt.Sprintf("\n…(stack truncada, %d linhas omitidas)", s.omitted)
	}
	s.open = false
	return txt
}

// stitchLines junta linhas de continuação de stack trace à entrada anterior
// (função pura, testável): dado []string devolve []string agrupado, com as
// continuações unidas por "\n" e limitadas por maxStitchLines/maxStitchBytes.
// Aplicada aos batches de arquivo/syslog.
func stitchLines(in []string) []string {
	var out []string
	var cur stitchBuf
	tb := false
	flush := func() {
		if cur.open {
			out = append(out, cur.done())
		}
	}
	for _, line := range in {
		if cur.open {
			if cont, next := continuation(line, tb); cont {
				cur.add(line)
				tb = next
				continue
			}
		}
		flush()
		cur.start(line)
		tb = reTraceStart.MatchString(line)
	}
	flush()
	return out
}

// dline é uma linha demultiplexada do stream de logs de um container Docker.
type dline struct {
	id        string // ID do container: é a chave do cursor de retomada (cursor.go)
	container string
	stream    string // stdout | stderr
	text      string
	// ts é o timestamp que o Docker prefixa em cada linha (`timestamps=1`), já
	// separado do corpo. É o que o coletor persiste para retomar de onde parou depois
	// de uma parada do agente, em vez de recomeçar em "agora" e abrir um buraco.
	ts string
}

// stitchDocker aplica a mesma heurística de continuation a um lote de linhas
// Docker, só unindo linhas do mesmo container e mesmo stream (uma continuação
// nunca cruza container/stream). Reaproveita a função pura continuation e os
// mesmos tetos de linhas/bytes por entrada.
func stitchDocker(in []dline) []dline {
	var out []dline
	var cur stitchBuf
	var head dline
	tb := false
	flush := func() {
		if cur.open {
			head.text = cur.done()
			out = append(out, head)
		}
	}
	for _, dl := range in {
		if cur.open {
			if cont, next := continuation(dl.text, tb); cont && head.container == dl.container && head.stream == dl.stream {
				cur.add(dl.text)
				tb = next
				continue
			}
		}
		flush()
		head = dl
		cur.start(dl.text)
		tb = reTraceStart.MatchString(dl.text)
	}
	flush()
	return out
}

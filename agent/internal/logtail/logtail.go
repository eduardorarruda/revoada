// Package logtail coleta logs do servidor e os envia ao gateway via NDJSON
// (/ingest/logs). Além de seguir arquivos por glob (`log_paths`, source=file),
// oferece coletores do servidor inteiro — journald, docker, syslog e kernel
// (dmesg) — cada um num arquivo próprio, best-effort e opt-in por config (P6/Fase D).
//
// Todos os coletores produzem o MESMO registro NDJSON
// (`{service,severity,body,labels}`) e compartilham o mecanismo de envio (sink);
// só variam os labels — sempre com um `source` ∈ {file,journald,docker,syslog,kernel}.
package logtail

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Salvaguardas de não-sobrecarga na leitura de arquivos:
//   - maxReadPerCycle: teto de bytes lidos por arquivo POR CICLO (10s). Um burst
//     (app despeja centenas de MB de uma vez) não é materializado inteiro na
//     memória do host — o excedente é lido nos ciclos seguintes, avançando o offset
//     aos poucos. Antes, readNew lia todo o delta de uma vez (pico de RAM/I/O).
//   - maxLineBytes: uma linha gigante é TRUNCADA (não descartada nem abortando o
//     scan como o bufio.Scanner fazia ao estourar seu buffer).
const (
	maxReadPerCycle = 8 << 20 // 8 MiB por arquivo por ciclo
	// maxLineBytes é o teto por linha de TODOS os coletores do pacote — arquivo,
	// syslog, docker e journald.
	//
	// Era a mesma ideia escrita em três lugares e com três valores: 256 KiB aqui,
	// 1 MiB no demux do Docker e 4 MiB no scanner do journald, enquanto a
	// documentação prometia 1 MiB. Quatro respostas para "quanto o agente manda por
	// linha?", dependendo de qual coletor produziu a linha — e nenhuma delas era a
	// documentada. Uma constante só, aplicada por lerLinhas (linhas.go) e por
	// addLine, é o que torna a garantia verificável.
	maxLineBytes = 256 << 10 // 256 KiB por linha, em todo o pacote
)

// cicloDeLeitura é o intervalo entre duas varreduras do Tailer. Cada varredura
// manda num lote tudo o que os arquivos ganharam no ciclo — por isso os baldes de
// taxa (ratelimit.go) precisam comportar um ciclo inteiro de cota.
const cicloDeLeitura = 10 * time.Second

// truncMark é o marcador de linha cortada. Uma linha cortada em silêncio é lida
// como a linha inteira — o operador conclui que o JSON estava malformado ou que a
// mensagem terminava ali. Compartilhado com o caminho de containers (dockerlogs.go),
// que cortava sem marcar nenhum.
const truncMark = " …(linha truncada)"

// Tailer segue arquivos de log (glob) e envia as linhas novas ao gateway. É
// reutilizado tanto para `log_paths` (source=file) quanto para o syslog
// (source=syslog, ver syslog.go): a lógica de tail/offset/rotação é idêntica.
type Tailer struct {
	sink     *sink
	source   string               // label source dos registros (file | syslog)
	patterns []string             // globs/arquivos seguidos
	offsets  map[string]fileState // caminho → (inode, byte já lido): dedupe + rotação
	dirty    bool                 // offsets mudaram desde a última gravação
	notes    []record             // avisos a injetar no stream (ex.: rotação detectada)
	now      func() time.Time     // injetável no teste (expiração de offset por idade)
}

// New cria um Tailer de arquivos de aplicação (source=file).
func New(gatewayURL, key, host string, patterns []string) *Tailer {
	return &Tailer{
		sink:     newSink(gatewayURL, key, host),
		source:   "file",
		patterns: patterns,
		offsets:  map[string]fileState{},
		now:      time.Now,
	}
}

// Run varre os arquivos a cada 10s e envia as linhas novas.
func (t *Tailer) Run(ctx context.Context) {
	// Retoma de onde parou na execução anterior. Sem isso, o seedOffsets abaixo
	// posicionava TODO arquivo no fim e o que foi escrito enquanto o agente esteve
	// parado (restart, atualização, reboot) nunca chegava ao painel.
	t.loadOffsets()
	// Arquivo nunca visto começa no fim (só linhas novas), como um tail -F: no
	// primeiro boot não faz sentido despejar meses de histórico no ClickHouse.
	t.seedOffsets()
	t.saveOffsets()
	tk := time.NewTicker(cicloDeLeitura)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			// Grava antes de sair: o encerramento limpo é justamente quando dá para
			// não perder nada.
			t.saveOffsets()
			return
		case <-tk.C:
			t.scan(ctx)
			t.saveOffsets()
		}
	}
}

func (t *Tailer) files() []string {
	var out []string
	for _, p := range t.patterns {
		if m, err := filepath.Glob(p); err == nil {
			out = append(out, m...)
		}
	}
	return out
}

// seedOffsets posiciona no fim os arquivos que o coletor ainda NÃO conhece. O
// `continue` é o ponto todo: um arquivo com offset já carregado do disco não pode
// ser reposicionado no fim, senão o estado persistido não serviria para nada.
func (t *Tailer) seedOffsets() {
	for _, f := range t.files() {
		if _, ok := t.offsets[f]; ok {
			continue
		}
		if fi, err := os.Stat(f); err == nil {
			t.offsets[f] = fileState{Ino: fileInode(fi), Off: fi.Size(), Seen: t.agora().Unix()}
			t.dirty = true
		}
	}
}

func (t *Tailer) scan(ctx context.Context) {
	files := t.files()
	t.pruneOffsets(files)
	for _, f := range files {
		lines := t.readNew(f)
		if len(lines) == 0 {
			continue
		}
		// Costura stack traces multiline num único registro antes de enviar.
		t.post(ctx, f, stitchLines(lines))
	}
	t.flushNotes(ctx)
}

// flushNotes envia os avisos do próprio coletor (rotação detectada). Vão pelo mesmo
// stream de logs porque é onde o operador olha: uma rotação que fez o coletor
// reler do zero explica linhas repetidas no painel, e sem o aviso a explicação
// simplesmente não existe.
func (t *Tailer) flushNotes(ctx context.Context) {
	if len(t.notes) == 0 {
		return
	}
	notes := t.notes
	t.notes = nil
	t.sink.post(ctx, notes)
}

// offsetTTL é quanto tempo o offset de um arquivo AUSENTE ainda vale. Existe porque
// a poda antiga apagava na primeira ausência, e ausência de um ciclo é rotina
// (logrotate renomeia e recria; glob por data some e volta na virada; operador move
// o arquivo por um instante). O offset apagado fazia o arquivo voltar como
// desconhecido — e como seedOffsets só roda no boot, o readNew começava do byte 0 e
// REENVIAVA o arquivo inteiro. Foi medido assim: toda linha chegando ao ClickHouse
// com vezes = 2.
//
// 24h é folgado de propósito: o custo de guardar uma entrada de ~40 bytes por um dia
// é irrelevante perto do custo de reenviar um log de gigabytes; e é maior que
// qualquer janela de rotação diária, que é o caso comum.
const offsetTTL = 24 * time.Hour

// offsetSeenSkew é a granularidade com que Seen é atualizado. Regravar o timestamp a
// cada ciclo de 10s marcaria o estado como sujo e faria uma escrita em disco a cada
// 10s sem nenhuma informação nova.
const offsetSeenSkew = 60 * time.Second

func (t *Tailer) agora() time.Time {
	if t.now == nil {
		return time.Now()
	}
	return t.now()
}

// pruneOffsets marca os arquivos presentes e descarta os que estão AUSENTES há mais
// que offsetTTL — evita crescimento ilimitado em agentes longevos (rotação cria
// nomes novos para sempre) sem confundir um sumiço de um ciclo com fim de vida.
func (t *Tailer) pruneOffsets(current []string) {
	if len(t.offsets) == 0 {
		return
	}
	now := t.agora()
	live := make(map[string]struct{}, len(current))
	for _, f := range current {
		live[f] = struct{}{}
	}
	for path, st := range t.offsets {
		if _, ok := live[path]; ok {
			if now.Sub(time.Unix(st.Seen, 0)) >= offsetSeenSkew {
				st.Seen = now.Unix()
				t.offsets[path] = st
				t.dirty = true
			}
			continue
		}
		// Entrada vinda de um estado antigo (sem Seen): dá a ela o relógio de agora em
		// vez de apagar, senão a primeira varredura após a atualização do agente
		// reproduziria exatamente o defeito que este código corrige.
		if st.Seen == 0 {
			st.Seen = now.Unix()
			t.offsets[path] = st
			t.dirty = true
			continue
		}
		if now.Sub(time.Unix(st.Seen, 0)) > offsetTTL {
			delete(t.offsets, path)
			t.dirty = true
		}
	}
}

// readNew lê as linhas adicionadas desde o último offset (trata rotação).
func (t *Tailer) readNew(path string) []string {
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	st := t.offsets[path]
	last := st.Off
	ino := fileInode(fi)

	// Rotação. O critério de tamanho sozinho não bastava: no `copytruncate` o
	// logrotate copia o arquivo, trunca o original e a aplicação volta a escrever —
	// se o arquivo já tiver crescido além do offset antigo quando o coletor olhar, o
	// tamanho NÃO é menor e ele continua lendo de um deslocamento que agora aponta
	// para o meio de outra linha. Foi o que se mediu ao vivo: 11 linhas perdidas e a
	// 12ª entregue cortada ao meio. O inode fecha esse buraco para rotação por
	// rename; o tamanho continua cobrindo o truncamento com inode preservado.
	// st.Ino == 0 é arquivo ainda sem estado (ou SO sem inode): não é rotação.
	var resto []string
	trocouInode := ino != 0 && st.Ino != 0 && ino != st.Ino
	if rotacionado := fi.Size() < last || trocouInode || reescrito(path, st, fi.Size()); rotacionado {
		if trocouInode {
			resto = restoDoRotacionado(path, st)
		}
		t.noteRotation(path, st, fi.Size(), ino, len(resto))
		last = 0
	}
	if fi.Size() == last && ino == st.Ino {
		return resto
	}
	// A impressão digital é uma janela ROLANTE: continua a do ciclo anterior quando a
	// leitura segue do mesmo offset (ciclos com poucos bytes não a encurtam).
	var semente []byte
	if last == st.Off {
		semente = st.Rabo
	}
	lines, consumed, rabo, ok := lerLinhasDe(path, last, fi.Size(), semente)
	if !ok {
		return resto
	}
	// Seen é preservado: quem cuida da idade é o pruneOffsets, e zerá-lo aqui faria a
	// entrada parecer "sem Seen" logo depois de ser lida.
	t.offsets[path] = fileState{Ino: ino, Off: last + consumed, Seen: st.Seen, Rabo: rabo}
	t.dirty = true
	return append(resto, lines...)
}

// maxEntradasRotacao limita a varredura do diretório atrás do arquivo rotacionado:
// diretório de log com milhares de arquivos não pode virar custo por ciclo.
const maxEntradasRotacao = 2000

// restoDoRotacionado devolve as linhas que o arquivo ANTIGO ganhou depois da última
// leitura e antes da rotação.
//
// No logrotate `create` (o padrão) o arquivo é RENOMEADO (app.log → app.log.1) e
// um novo é criado; a aplicação continua escrevendo no antigo até ser recarregada.
// O coletor via o inode novo, recomeçava do zero no arquivo novo e as linhas
// escritas no antigo entre a última varredura e a rotação sumiam sem rastro —
// medido na validação de 02/10/2026: 5 de 5 linhas perdidas, num cenário que
// acontece em TODA rotação diária de um serviço ativo. O arquivo antigo continua no
// mesmo diretório com o mesmo inode: basta achá-lo e ler de onde o offset parou.
func restoDoRotacionado(path string, st fileState) []string {
	dir := filepath.Dir(path)
	entradas, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for i, e := range entradas {
		if i >= maxEntradasRotacao {
			return nil
		}
		if e.IsDir() || filepath.Join(dir, e.Name()) == path {
			continue
		}
		cand := filepath.Join(dir, e.Name())
		fi, err := os.Stat(cand)
		if err != nil || fileInode(fi) != st.Ino {
			continue
		}
		if fi.Size() <= st.Off {
			return nil // achou o antigo e não há nada novo nele
		}
		lines, _, _, _ := lerLinhasDe(cand, st.Off, fi.Size(), nil)
		return lines
	}
	return nil
}

// tamanhoRabo é quantos bytes antes do offset ficam guardados como impressão
// digital do arquivo (fileState.Rabo).
const tamanhoRabo = 64

// reescrito diz se o arquivo foi truncado e reescrito com MAIS bytes do que o
// offset antigo entre duas varreduras (copytruncate com a aplicação escrevendo
// rápido). Inode igual e tamanho maior: o único sinal é o conteúdo. Os bytes logo
// antes do offset têm de ser os mesmos que lemos da última vez; se mudaram, o
// offset aponta para o meio de outro conteúdo e continuar dali entregaria a
// primeira linha cortada e perderia todas as anteriores.
func reescrito(path string, st fileState, size int64) bool {
	n := int64(len(st.Rabo))
	if n == 0 || size <= st.Off || st.Off < n {
		return false
	}
	fh, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = fh.Close() }()
	atual := make([]byte, n)
	if _, err := fh.ReadAt(atual, st.Off-n); err != nil {
		return false
	}
	return !bytes.Equal(atual, st.Rabo)
}

// lerLinhasDe lê as linhas COMPLETAS de path entre off e size (no máximo
// maxReadPerCycle bytes) e devolve quantos bytes elas ocupam e os últimos
// tamanhoRabo bytes consumidos (continuando `semente`, os que precediam off). Uma
// cauda parcial (arquivo sendo escrito) não é emitida pela metade: fica para
// quando fechar.
func lerLinhasDe(path string, off, size int64, semente []byte) (lines []string, consumed int64, rabo []byte, ok bool) {
	rabo = append([]byte(nil), semente...)
	fh, err := os.Open(path)
	if err != nil {
		return nil, 0, nil, false
	}
	defer func() { _ = fh.Close() }()
	if _, err := fh.Seek(off, 0); err != nil {
		return nil, 0, nil, false
	}
	guarda := func(b []byte) {
		rabo = append(rabo, b...)
		if len(rabo) > tamanhoRabo {
			rabo = append([]byte(nil), rabo[len(rabo)-tamanhoRabo:]...)
		}
	}
	toRead := size - off
	if toRead > maxReadPerCycle {
		toRead = maxReadPerCycle
	}
	br := bufio.NewReaderSize(io.LimitReader(fh, toRead), 64*1024)
	for {
		b, rerr := br.ReadBytes('\n')
		if len(b) > 0 && b[len(b)-1] == '\n' {
			consumed += int64(len(b))
			guarda(b)
			addLine(&lines, b)
		} else if rerr != nil {
			// Cauda sem \n no fim da janela/arquivo. Caso patológico: uma única linha
			// gigante encheu TODO o teto do ciclo sem quebrar — trunca e avança, senão
			// o offset nunca progride e relê o mesmo trecho para sempre.
			if consumed == 0 && toRead == maxReadPerCycle && int64(len(b)) >= toRead {
				consumed += int64(len(b))
				guarda(b)
				addLine(&lines, b)
			}
			break
		}
		if rerr != nil {
			break
		}
	}
	return lines, consumed, rabo, true
}

// noteRotation enfileira um aviso de rotação. Registrar importa porque a rotação é
// o único momento em que o coletor volta ao início de um arquivo: sem o aviso, uma
// releitura aparece no painel como duplicata sem causa, e uma rotação que apagou
// linhas antes de o coletor passar aparece como silêncio sem causa.
func (t *Tailer) noteRotation(path string, old fileState, size int64, ino uint64, recuperadas int) {
	motivo := "arquivo truncado"
	if ino != 0 && old.Ino != 0 && ino != old.Ino {
		motivo = "arquivo substituído (inode mudou)"
		if recuperadas > 0 {
			motivo += fmt.Sprintf("; %d linha(s) finais do arquivo antigo recuperadas", recuperadas)
		}
	}
	t.notes = append(t.notes, record{
		service:  "revoada-agent",
		severity: "INFO",
		body: fmt.Sprintf("rotação de log detectada em %s (%s): leitura recomeça do início; offset anterior %d, tamanho atual %d",
			path, motivo, old.Off, size),
		labels: map[string]string{"source": t.source, "file": path},
	})
}

// addLine acrescenta a linha (sem \r\n final) a lines, truncando em maxLineBytes e
// marcando o corte. Linhas vazias são ignoradas.
func addLine(lines *[]string, b []byte) {
	line := strings.TrimRight(string(b), "\r\n")
	if line == "" {
		return
	}
	if len(line) > maxLineBytes {
		line = line[:maxLineBytes] + truncMark
	}
	*lines = append(*lines, line)
}

var reErr = regexp.MustCompile(`(?i)\b(error|erro|fatal|panic|exception)\b`)

// reErrStruct pega o que o \b de reErr não pega: exceções em CamelCase (ex.:
// NullPointerException, OutOfMemoryError) — o sufixo "Exception"/"Error" está
// grudado na classe, sem fronteira de palavra antes — e frames de stack trace
// costurados no corpo ("  at ...", "Traceback"). CamelCase é case-sensitive (exige
// inicial maiúscula) para não casar "terror"/"errors" comuns. `(?m)` faz `^` valer
// por linha dentro do corpo já costurado.
var reErrStruct = regexp.MustCompile(`(?m)[A-Z][A-Za-z0-9_]*(Exception|Error)\b|^[ \t]+at\s|^Traceback \(most recent call last\):`)
var reWarn = regexp.MustCompile(`(?i)\b(warn|warning|aviso)\b`)

// severity classifica uma linha (ou entrada costurada) usada para
// arquivos/syslog/docker, onde não há PRIORITY estruturado. journald/kernel usam
// severityFromPriority.
//
// Primeiro o nível DECLARADO em JSON (severity.go): a regex de palavra não enxerga
// `{"level":50}` nem `"level":"critical"`, e num serviço com pino isso deixou 49 de
// 51 linhas sem classificação — um serviço que só imprimia erro aparecia no painel
// com zero erros. Só depois a regex, que é adivinhação sobre texto livre.
//
// O default é UNKNOWN, e não INFO. A regex acerta o que reconhece e não sabe nada
// sobre o resto: chamar de INFO tudo que ela não reconheceu transforma ignorância
// em afirmação — uma linha de falha num formato que o padrão não cobre (mensagem em
// outro idioma, código de erro sem a palavra "error") entra no painel classificada
// como rotina e some dos filtros de erro. UNKNOWN diz a verdade: não foi possível
// classificar.
func severity(line string) string {
	if s, ok := severityFromJSON(line); ok {
		return s
	}
	switch {
	case reErr.MatchString(line) || reErrStruct.MatchString(line):
		return "ERROR"
	case reWarn.MatchString(line):
		return "WARN"
	default:
		return "UNKNOWN"
	}
}

func (t *Tailer) post(ctx context.Context, file string, lines []string) {
	service := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	recs := make([]record, 0, len(lines))
	for _, l := range lines {
		recs = append(recs, record{
			service:  service,
			severity: severity(l),
			body:     l,
			labels:   map[string]string{"source": t.source, "file": file},
		})
	}
	t.sink.post(ctx, recs)
}

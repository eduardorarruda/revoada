package logtail

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// dockerSock é o socket unix padrão do Docker (mesmo caminho de internal/collect).
const dockerSock = "/var/run/docker.sock"

// arqCursorDocker guarda o último timestamp já enviado de CADA container, para o
// follow retomar dali em vez de recomeçar em "agora" a cada boot do agente.
const arqCursorDocker = "dockerlogs.cursor"

// DockerLogs coleta os logs de todos os containers em execução (source=docker) via
// a API do Docker (`GET /containers/{id}/logs?follow=1&stdout=1&stderr=1&tail=0&timestamps=1`).
// Descobre containers periodicamente, mantém uma goroutine de follow por container
// e as encerra quando o container some. Best-effort: sem socket, emite warn e sai.
type DockerLogs struct {
	sink   *sink
	log    *slog.Logger
	list   *http.Client // client com timeout curto para descoberta
	stream *http.Client // client sem timeout para os streams de follow
	// cur guarda o último timestamp já enviado por container (ver cursor.go). Sem
	// ele o follow abria com `tail=0` e tudo que os containers registraram enquanto o
	// agente esteve parado — inclusive durante a auto-atualização, que é rotina —
	// nunca chegava ao painel.
	cur *cursorStore
}

func NewDockerLogs(gatewayURL, key, host string, log *slog.Logger) *DockerLogs {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", dockerSock)
	}
	return &DockerLogs{
		sink:   newSink(gatewayURL, key, host),
		log:    log,
		list:   &http.Client{Transport: &http.Transport{DialContext: dial}, Timeout: 3 * time.Second},
		stream: &http.Client{Transport: &http.Transport{DialContext: dial}}, // sem timeout: cancela pelo ctx
		cur:    novoCursor(arqCursorDocker),
	}
}

func (d *DockerLogs) Run(ctx context.Context) {
	if _, err := os.Stat(dockerSock); err != nil {
		d.log.Warn("docker logs: socket ausente, coletor desativado", "path", dockerSock)
		return
	}
	lines := make(chan dline, 4096)
	go d.consume(ctx, lines)

	// following é acessado apenas nesta goroutine (scan): sem necessidade de mutex.
	following := map[string]context.CancelFunc{}
	scan := func() {
		running := d.listRunning(ctx)
		for id, name := range running { // novos containers → inicia follow
			if _, ok := following[id]; ok {
				continue
			}
			fctx, cancel := context.WithCancel(ctx)
			following[id] = cancel
			go d.follow(fctx, id, name, lines)
		}
		for id, cancel := range following { // containers que sumiram → encerra
			if _, ok := running[id]; !ok {
				cancel()
				delete(following, id)
			}
		}
		// Container removido não volta: guardar o timestamp dele para sempre faria o
		// arquivo de cursor crescer sem limite num host que recria containers a cada
		// deploy (o nosso caso). Mesma poda que podarCPUState faz do lado das métricas.
		if len(running) > 0 {
			d.cur.podar(running)
		}
		d.cur.salvar()
	}

	tk := time.NewTicker(15 * time.Second)
	defer tk.Stop()
	scan()
	for {
		select {
		case <-ctx.Done():
			for _, cancel := range following {
				cancel()
			}
			// Grava a posição antes de sair: o encerramento limpo é exatamente o
			// momento em que dá para não perder nada — e o encerramento limpo mais
			// frequente é a auto-atualização.
			d.cur.salvar()
			return
		case <-tk.C:
			scan()
		}
	}
}

// listRunning devolve os containers em execução (id → nome). Best-effort.
func (d *DockerLogs) listRunning(ctx context.Context) map[string]string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/json", nil)
	if err != nil {
		return nil
	}
	resp, err := d.list.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var list []struct {
		ID    string   `json:"Id"`
		Names []string `json:"Names"`
		Image string   `json:"Image"`
	}
	if json.NewDecoder(resp.Body).Decode(&list) != nil {
		return nil
	}
	out := make(map[string]string, len(list))
	for _, ct := range list {
		name := ct.Image
		if len(ct.Names) > 0 {
			name = strings.TrimPrefix(ct.Names[0], "/")
		}
		out[ct.ID] = name
	}
	return out
}

// follow segue o stream de logs de um container até o ctx ser cancelado (container
// removido) ou o stream cair. Demultiplexa o framing do Docker.
func (d *DockerLogs) follow(ctx context.Context, id, name string, out chan<- dline) {
	url := "http://docker/containers/" + id + "/logs?follow=1&stdout=1&stderr=1&timestamps=1" + d.desde(id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	resp, err := d.stream.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	if d.hasTTY(ctx, id) {
		readRawLines(ctx, resp.Body, id, name, out) // TTY: stream cru, sem header
		return
	}
	demux(ctx, resp.Body, id, name, out)
}

// desde monta o trecho de URL que diz ao Docker onde recomeçar.
//
// Sem cursor guardado é `tail=0`: um container que o agente nunca viu começa do
// momento atual, como um `tail -F`, senão a primeira coleta despejaria meses de
// histórico de todo container do host no ClickHouse.
//
// COM cursor guardado é `since=<último timestamp já enviado>+1ns` e `tail=all`. O
// `+1ns` evita reenviar a última linha (o `since` do Docker é inclusivo), e o
// `tail=all` é obrigatório junto com `since`: `tail=0` significa "as últimas 0 linhas"
// e devolveria NADA, ou seja, manteria exatamente o buraco que este cursor existe
// para fechar.
func (d *DockerLogs) desde(id string) string {
	ts := d.cur.ler(id)
	if ts == "" {
		return "&tail=0"
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return "&tail=0"
	}
	t = t.Add(time.Nanosecond)
	return fmt.Sprintf("&tail=all&since=%d.%09d", t.Unix(), t.Nanosecond())
}

// hasTTY inspeciona o container para saber se tem TTY alocado (nesse caso o Docker
// NÃO multiplexa o stream com o header de 8 bytes).
func (d *DockerLogs) hasTTY(ctx context.Context, id string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/"+id+"/json", nil)
	if err != nil {
		return false
	}
	resp, err := d.list.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var insp struct {
		Config struct {
			Tty bool `json:"Tty"`
		} `json:"Config"`
	}
	if json.NewDecoder(resp.Body).Decode(&insp) != nil {
		return false
	}
	return insp.Config.Tty
}

// demux desmultiplexa o stream de logs do Docker (containers sem TTY): cada frame
// tem um cabeçalho de 8 bytes — byte0 = stream (1=stdout, 2=stderr) e bytes 4–7 =
// tamanho do payload (big-endian). Acumula por stream e emite linha a linha.
func demux(ctx context.Context, r io.Reader, id, container string, out chan<- dline) {
	br := bufio.NewReader(r)
	header := make([]byte, 8)
	var acc [3][]byte // buffer de linha por tipo de stream (índices 1 e 2)
	for {
		if _, err := io.ReadFull(br, header); err != nil {
			return
		}
		st := header[0]
		n := binary.BigEndian.Uint32(header[4:8])
		if n == 0 {
			continue
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(br, payload); err != nil {
			return
		}
		if st != 1 && st != 2 {
			continue
		}
		acc[st] = append(acc[st], payload...)
		for {
			i := bytes.IndexByte(acc[st], '\n')
			if i < 0 {
				// Sem newline: um container que escreve muito sem quebrar linha
				// (saída binária, JSON gigante numa linha) faria acc crescer sem
				// limite. Ao passar do teto, corta como uma linha e segue — com o
				// mesmo marcador do caminho de arquivo, senão a linha cortada chega
				// ao painel indistinguível de uma linha que terminava ali.
				if len(acc[st]) > maxLineBytes {
					line := string(acc[st][:maxLineBytes]) + truncMark
					acc[st] = acc[st][:0]
					if !emit(ctx, out, id, container, streamName(st), line) {
						return
					}
				}
				break
			}
			line := string(acc[st][:i])
			acc[st] = acc[st][i+1:]
			if !emit(ctx, out, id, container, streamName(st), line) {
				return
			}
		}
	}
}

// readRawLines lê um stream cru (container com TTY): linhas por \n, tudo stdout.
// Usa lerLinhas (teto único do pacote, ver linhas.go) em vez de bufio.Scanner: o
// Scanner aborta o stream inteiro numa linha maior que o buffer, e o Supervise
// reergueria o coletor para ele morrer na mesma linha.
func readRawLines(ctx context.Context, r io.Reader, id, container string, out chan<- dline) {
	lerLinhas(r, func(linha []byte) bool {
		return emit(ctx, out, id, container, "stdout", string(linha))
	})
}

func streamName(st byte) string {
	if st == 2 {
		return "stderr"
	}
	return "stdout"
}

// reDockerTS casa o prefixo de timestamp que `timestamps=1` adiciona a CADA linha
// (inclusive frames de stack trace). É removido do corpo para não quebrar a costura
// multiline (a linha de continuação precisa manter sua indentação original) — mas o
// valor é GUARDADO: é ele que vira o cursor `since=` do container no próximo boot do
// agente (ver cursor.go e DockerLogs.desde).
var reDockerTS = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\S+)\s`)

func emit(ctx context.Context, out chan<- dline, id, container, stream, line string) bool {
	line = strings.TrimRight(line, "\r")
	ts := ""
	if m := reDockerTS.FindStringSubmatch(line); m != nil {
		ts = m[1]
		line = line[len(m[0]):]
	}
	select {
	case out <- dline{id: id, container: container, stream: stream, text: line, ts: ts}:
		return true
	case <-ctx.Done():
		return false
	}
}

// consume agrupa as linhas demultiplexadas, costura stack traces multiline
// (stitchDocker) e envia em lotes (flush por tamanho/tempo). stderr vira o label
// stream=stderr (pista de erro); a severidade continua sendo por regex no texto.
func (d *DockerLogs) consume(ctx context.Context, lines <-chan dline) {
	tk := time.NewTicker(1500 * time.Millisecond)
	defer tk.Stop()
	var buf []dline
	flush := func() {
		if len(buf) == 0 {
			return
		}
		stitched := stitchDocker(buf)
		recs := make([]record, 0, len(stitched))
		for _, dl := range stitched {
			recs = append(recs, record{
				service:  dl.container,
				severity: severity(dl.text),
				body:     dl.text,
				labels:   map[string]string{"source": "docker", "container": dl.container, "stream": dl.stream},
			})
		}
		// O cursor só avança DEPOIS de o gateway aceitar o lote, e pelas linhas CRUAS
		// (antes da costura): é a última linha realmente lida do daemon que define de
		// onde retomar.
		//
		// A ordem importa e foi medida: com o cursor avançando ANTES do envio, matar o
		// agente no meio do ciclo (SIGTERM do systemd, ou seja, TODA auto-atualização)
		// deixava o cursor à frente das linhas que nunca chegaram — e o laboratório
		// mostrou exatamente uma linha faltando na retomada. Confirmar primeiro troca
		// esse buraco por, no pior caso, algumas linhas repetidas: repetição o painel
		// mostra e o operador entende; silêncio, não.
		if d.sink.post(ctx, recs) {
			for _, dl := range buf {
				d.cur.gravar(dl.id, dl.ts)
			}
			d.cur.salvar()
		}
		buf = buf[:0]
	}
	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case dl, ok := <-lines:
			if !ok {
				flush()
				return
			}
			buf = append(buf, dl)
			if len(buf) >= 200 {
				flush()
			}
		case <-tk.C:
			flush()
		}
	}
}

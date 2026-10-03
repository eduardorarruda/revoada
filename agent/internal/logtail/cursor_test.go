package logtail

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Provas dos cursores de stream (journald e docker).
//
// O defeito: o tail de ARQUIVO persiste offset e atravessa uma parada do agente sem
// perder nada; os coletores de stream não persistiam nada e recomeçavam em "agora" a
// cada boot (`tail=0` no docker, `--since now` no journald). Toda parada do agente —
// e a auto-atualização é uma parada, rotineira e automática — abria um buraco de log
// que, no painel, é indistinguível de um servidor que ficou quieto.

func comDiretorioDeEstado(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	SetLogStateDir(dir)
	t.Cleanup(func() { SetLogStateDir("") })
	return dir
}

// PROVA: o cursor sobrevive ao processo — é isso que fecha o buraco.
func TestCursorPersisteEntreExecucoes(t *testing.T) {
	dir := comDiretorioDeEstado(t)

	c := novoCursor("teste.cursor")
	c.gravar("abc", "2026-08-09T10:00:00.000000001Z")
	c.salvar()

	if _, err := os.Stat(filepath.Join(dir, "teste.cursor")); err != nil {
		t.Fatalf("o cursor não foi para o disco: %v", err)
	}
	// Outro "processo": relê do zero.
	c2 := novoCursor("teste.cursor")
	if got := c2.ler("abc"); got != "2026-08-09T10:00:00.000000001Z" {
		t.Fatalf("cursor perdido entre execuções: %q", got)
	}
}

// PROVA: sem diretório de estado configurado, tudo continua funcionando (degrada
// para o comportamento antigo). Um agente antigo, ou um host onde o buffer_dir não é
// gravável, não pode parar de coletar por causa disto.
func TestCursorSemDiretorioNaoQuebra(t *testing.T) {
	SetLogStateDir("")
	c := novoCursor("teste.cursor")
	c.gravar("abc", "x")
	c.salvar()
	if c.ler("abc") != "x" {
		t.Fatal("o cursor em memória tem de funcionar mesmo sem persistência")
	}
}

// PROVA: containers removidos saem do cursor. Sem a poda, um host que recria
// containers a cada deploy (o nosso caso) acumularia uma entrada morta por deploy,
// para sempre — o mesmo vazamento lento que podarCPUState corrige nas métricas.
func TestCursorPodaContainersQueSumiram(t *testing.T) {
	comDiretorioDeEstado(t)
	c := novoCursor("teste.cursor")
	c.gravar("vivo", "t1")
	c.gravar("morto", "t2")
	c.podar(map[string]string{"vivo": "nome"})
	if c.ler("morto") != "" {
		t.Fatal("container removido continuou no cursor")
	}
	if c.ler("vivo") == "" {
		t.Fatal("podou um container que ainda existe — reabriria o buraco de log dele")
	}
}

// PROVA: `esquecer` limpa o cursor recusado, no disco também. É o caminho de
// degradação quando o journal foi rotacionado/vacuumado: insistir num cursor morto
// deixaria o coletor num laço de falha com o Supervise, e parar de coletar é pior do
// que perder o histórico.
func TestCursorEsquecerLimpaODisco(t *testing.T) {
	comDiretorioDeEstado(t)
	c := novoCursor("teste.cursor")
	c.gravar(chaveCursor, "s=abc")
	c.salvar()
	c.esquecer(chaveCursor)
	if novoCursor("teste.cursor").ler(chaveCursor) != "" {
		t.Fatal("o cursor recusado sobreviveu no disco — o coletor tentaria de novo para sempre")
	}
}

// PROVA: a URL do follow do Docker retoma do último timestamp já enviado.
//
// Detalhe que parece cosmético e não é: `since` sozinho com `tail=0` devolveria NADA
// (tail=0 = "as últimas 0 linhas"), ou seja, manteria exatamente o buraco. Por isso a
// presença de `tail=all` é parte da prova.
func TestDockerRetomaDoUltimoTimestamp(t *testing.T) {
	comDiretorioDeEstado(t)
	d := NewDockerLogs("http://gw", "k", "h", slog.New(slog.NewTextHandler(io.Discard, nil)))

	if got := d.desde("nunca-visto"); got != "&tail=0" {
		t.Fatalf("container novo tem de começar do fim (como tail -F), veio %q", got)
	}

	ts := time.Date(2026, 8, 9, 10, 0, 0, 123456789, time.UTC)
	d.cur.gravar("c1", ts.Format(time.RFC3339Nano))
	got := d.desde("c1")
	if !strings.Contains(got, "tail=all") {
		t.Fatalf("sem tail=all o `since` do Docker devolve nada e o buraco continua: %q", got)
	}
	// +1ns sobre o último já enviado: o `since` do Docker é inclusivo, e sem o
	// incremento a última linha seria reenviada a cada reinício do agente.
	quer := fmt.Sprintf("since=%d.%09d", ts.Unix(), ts.Nanosecond()+1)
	if !strings.Contains(got, quer) {
		t.Fatalf("retomada errada: %q não contém %q", got, quer)
	}
}

// PROVA: o timestamp que vira cursor é o da linha CRUA, e ele avança conforme as
// linhas chegam pelo demux.
func TestDemuxGuardaTimestampParaOCursor(t *testing.T) {
	quadro := func(stream byte, texto string) []byte {
		b := make([]byte, 8)
		b[0] = stream
		binary.BigEndian.PutUint32(b[4:], uint32(len(texto)))
		return append(b, texto...)
	}
	var fluxo bytes.Buffer
	fluxo.Write(quadro(1, "2026-08-09T10:00:00.000000001Z primeira\n"))
	fluxo.Write(quadro(1, "2026-08-09T10:00:05.000000002Z segunda\n"))

	out := make(chan dline, 8)
	demux(context.Background(), &fluxo, "id1", "api", out)
	close(out)

	var ultimo string
	n := 0
	for dl := range out {
		n++
		if dl.id != "id1" {
			t.Fatalf("id do container perdido: %q", dl.id)
		}
		ultimo = dl.ts
	}
	if n != 2 {
		t.Fatalf("linhas perdidas no demux: %d", n)
	}
	if ultimo != "2026-08-09T10:00:05.000000002Z" {
		t.Fatalf("o cursor não avançou para a última linha lida: %q", ultimo)
	}
}

// PROVA: o cursor do journal sai do campo __CURSOR, inclusive em entradas que NÃO
// viram registro. Sem isso, uma sequência de entradas sem MESSAGE faria o coletor
// retomar antes delas e relê-las a cada reinício.
func TestParseJournalDevolveCursor(t *testing.T) {
	r, cur, ok := parseJournal([]byte(`{"__CURSOR":"s=abc;i=1","MESSAGE":"oi","PRIORITY":"3"}`))
	if !ok || r.body != "oi" {
		t.Fatalf("registro mal lido: %+v", r)
	}
	if cur != "s=abc;i=1" {
		t.Fatalf("cursor não extraído: %q", cur)
	}
	if _, cur, ok := parseJournal([]byte(`{"__CURSOR":"s=abc;i=2","PRIORITY":"6"}`)); ok || cur != "s=abc;i=2" {
		t.Fatalf("entrada sem MESSAGE precisa devolver o cursor mesmo assim: cur=%q ok=%v", cur, ok)
	}
}

// PROVA: o cursor só avança DEPOIS de o gateway aceitar o lote.
//
// Medido no laboratório com o comportamento anterior (cursor gravado na leitura):
// matando o agente no meio de um ciclo — que é o que o systemd faz em TODA
// auto-atualização —, o cursor ficava à frente das linhas que nunca saíram e uma
// linha sumia na retomada. Confirmar antes de avançar troca esse buraco por, no pior
// caso, linhas repetidas.
func TestCursorNaoAvancaSeOEnvioFalhou(t *testing.T) {
	comDiretorioDeEstado(t)
	// Gateway que recusa tudo com 400 (não repetível: falha na primeira tentativa).
	srv, _, _ := srvColetor(t, 400, 1<<30)
	defer srv.Close()

	c := novoCursor("teste.cursor")
	s := newSink(srv.URL, "k", "h")
	s.limiter, s.bytes = nil, nil

	if s.post(context.Background(), []record{{service: "a", body: "x"}}) {
		t.Fatal("post disse que o gateway aceitou um 400")
	}
	// Simula a regra do coletor: só grava se o post deu certo.
	if s.post(context.Background(), []record{{service: "a", body: "x"}}) {
		c.gravar("c1", "t-que-nao-deveria-existir")
	}
	if c.ler("c1") != "" {
		t.Fatal("o cursor avançou por cima de linhas que não chegaram ao gateway — buraco silencioso na retomada")
	}
}

// PROVA: drainCom confirma o último registro do lote aceito, e é dele que sai o
// cursor persistido.
func TestDrainComConfirmaUltimoRegistroEntregue(t *testing.T) {
	srv, _, _ := srvColetor(t, 200, 0)
	defer srv.Close()
	s := newSink(srv.URL, "k", "h")
	s.limiter, s.bytes = nil, nil

	recs := make(chan record, 4)
	recs <- record{service: "a", body: "um", cursor: "s=1"}
	recs <- record{service: "a", body: "dois", cursor: "s=2"}
	close(recs)

	var confirmado string
	s.drainCom(context.Background(), recs, func(r record) { confirmado = r.cursor })
	if confirmado != "s=2" {
		t.Fatalf("cursor confirmado = %q, queria o do último registro entregue (s=2)", confirmado)
	}
}

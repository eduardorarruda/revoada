package logtail

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSeverity(t *testing.T) {
	cases := map[string]string{
		"tudo ok por aqui":            "UNKNOWN",
		"ERROR falha ao gravar":       "ERROR",
		"deu uma Exception feia":      "ERROR",
		"WARNING fila cheia":          "WARN",
		"aviso: disco quase cheio":    "WARN",
		"pedido 42 processado em 5ms": "UNKNOWN",
	}
	for line, want := range cases {
		if got := severity(line); got != want {
			t.Errorf("severity(%q)=%s, quer %s", line, got, want)
		}
	}
}

func TestReadNewSeguindoOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("l1\nl2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tl := New("http://x", "k", "h", []string{path})
	// seed no fim: as 2 primeiras linhas são ignoradas.
	tl.seedOffsets()
	if got := tl.readNew(path); len(got) != 0 {
		t.Fatalf("após seed não deveria haver linhas novas, veio %v", got)
	}
	// acrescenta 2 linhas → devem ser lidas exatamente uma vez.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("l3\nl4\n")
	f.Close()
	got := tl.readNew(path)
	if len(got) != 2 || got[0] != "l3" || got[1] != "l4" {
		t.Fatalf("esperava [l3 l4], veio %v", got)
	}
	// segunda leitura sem novas linhas → vazio (não reenvia).
	if got := tl.readNew(path); len(got) != 0 {
		t.Fatalf("não deveria reler, veio %v", got)
	}
}

func TestReadNewRotacao(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	os.WriteFile(path, []byte("linha antiga\n"), 0o644)
	tl := New("http://x", "k", "h", []string{path})
	tl.seedOffsets()
	// rotação: arquivo truncado e reescrito menor → recomeça do zero.
	os.WriteFile(path, []byte("nova\n"), 0o644)
	got := tl.readNew(path)
	if len(got) != 1 || got[0] != "nova" {
		t.Fatalf("após rotação esperava [nova], veio %v", got)
	}
}

// TestReadNewRotacaoCopytruncatePorInode prova o defeito que o critério de tamanho
// sozinho não pegava: o arquivo é SUBSTITUÍDO (inode novo) e já nasce maior que o
// offset antigo. Sem olhar o inode, o coletor seguia lendo a partir de um
// deslocamento que agora cai no meio de outra linha — foi assim que se perderam 11
// linhas e a 12ª chegou cortada ao meio.
func TestReadNewRotacaoCopytruncatePorInode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sem inode no Windows: a detecção cai para o critério de tamanho")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	antigo := strings.Repeat("linha antiga bem comprida para ocupar bytes\n", 20)
	if err := os.WriteFile(path, []byte(antigo), 0o644); err != nil {
		t.Fatal(err)
	}
	tl := New("http://x", "k", "h", []string{path})
	tl.seedOffsets()

	// logrotate por rename+create: o caminho passa a apontar para um arquivo NOVO,
	// e o conteúdo novo é maior que o offset guardado.
	os.Rename(path, path+".1")
	novo := strings.Repeat("linha nova depois da rotacao com bastante texto tambem\n", 30)
	if err := os.WriteFile(path, []byte(novo), 0o644); err != nil {
		t.Fatal(err)
	}

	got := tl.readNew(path)
	if len(got) != 30 {
		t.Fatalf("esperava as 30 linhas do arquivo novo, veio %d", len(got))
	}
	if got[0] != "linha nova depois da rotacao com bastante texto tambem" {
		t.Fatalf("primeira linha veio cortada/errada: %q", got[0])
	}
	if len(tl.notes) != 1 || !strings.Contains(tl.notes[0].body, "inode mudou") {
		t.Errorf("a rotação deveria ter sido registrada no stream: %#v", tl.notes)
	}
}

// TestOffsetsPersistemEntreExecucoes prova a perda silenciosa de linha por
// reinício: o agente parava, o arquivo continuava sendo escrito, e no boot seguinte
// o seedOffsets posicionava no FIM — tudo escrito na janela de indisponibilidade
// nunca chegava ao painel.
func TestOffsetsPersistemEntreExecucoes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("antiga\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	SetLogStateDir(dir)
	defer SetLogStateDir("")

	// 1ª execução: começa no fim e grava o estado.
	t1 := New("http://x", "k", "h", []string{path})
	t1.loadOffsets()
	t1.seedOffsets()
	t1.saveOffsets()
	if _, err := os.Stat(filepath.Join(dir, "logtail-file.state")); err != nil {
		t.Fatalf("estado não foi gravado: %v", err)
	}

	// agente parado: a aplicação continua escrevendo.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("durante a parada 1\ndurante a parada 2\n")
	f.Close()

	// 2ª execução (processo novo): tem de retomar do offset gravado.
	t2 := New("http://x", "k", "h", []string{path})
	t2.loadOffsets()
	t2.seedOffsets()
	got := t2.readNew(path)
	if len(got) != 2 || got[0] != "durante a parada 1" || got[1] != "durante a parada 2" {
		t.Fatalf("linhas escritas com o agente parado foram perdidas: %v", got)
	}
}

// TestSeedNaoMexeEmArquivoConhecido: seedOffsets só pode posicionar no fim arquivo
// que o coletor nunca viu — do contrário anularia o estado recém-carregado.
func TestSeedNaoMexeEmArquivoConhecido(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	os.WriteFile(path, []byte("a\nb\nc\n"), 0o644)
	tl := New("http://x", "k", "h", []string{path})
	tl.offsets[path] = fileState{Off: 2} // como se viesse do estado em disco
	tl.seedOffsets()
	if got := tl.offsets[path].Off; got != 2 {
		t.Fatalf("seedOffsets sobrescreveu offset conhecido: %d", got)
	}
}

// TestOffsetSobreviveAAusenciaDeUmCiclo prova o reenvio do arquivo inteiro: o
// arquivo some por um ciclo (rotação, `mv` de operador, glob por data na virada), a
// poda antiga apagava o offset na hora e, quando o arquivo voltava, o readNew lia do
// byte 0 — todas as linhas chegavam ao painel pela segunda vez (medido: vezes = 2).
func TestOffsetSobreviveAAusenciaDeUmCiclo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	conteudo := "l1\nl2\nl3\n"
	if err := os.WriteFile(path, []byte(conteudo), 0o644); err != nil {
		t.Fatal(err)
	}
	tl := New("http://x", "k", "h", []string{path})
	tl.seedOffsets()
	off := tl.offsets[path].Off

	// ciclo em que o arquivo não existe (janela do logrotate entre rename e create).
	tl.pruneOffsets(nil)
	if _, ok := tl.offsets[path]; !ok {
		t.Fatalf("offset apagado na primeira ausência: o arquivo voltaria a ser lido do zero")
	}

	// arquivo volta com uma linha nova: só ela pode ser lida.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("l4\n")
	f.Close()
	tl.pruneOffsets([]string{path})
	got := tl.readNew(path)
	if len(got) != 1 || got[0] != "l4" {
		t.Fatalf("arquivo reenviado do zero após ausência de um ciclo: %v (offset era %d)", got, off)
	}
}

// TestOffsetExpiraPorIdade: guardar para sempre o offset de todo arquivo que já
// existiu faria o estado crescer sem limite num agente longevo (a rotação cria nomes
// novos indefinidamente). A entrada morre por IDADE de ausência, não na primeira.
func TestOffsetExpiraPorIdade(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	os.WriteFile(path, []byte("a\n"), 0o644)
	tl := New("http://x", "k", "h", []string{path})
	agora := time.Now()
	tl.now = func() time.Time { return agora }
	tl.seedOffsets()

	// ainda dentro do TTL: sobrevive.
	agora = agora.Add(offsetTTL - time.Minute)
	tl.pruneOffsets(nil)
	if _, ok := tl.offsets[path]; !ok {
		t.Fatalf("offset expirou antes do TTL")
	}
	// passado o TTL: some.
	agora = agora.Add(2 * time.Minute)
	tl.pruneOffsets(nil)
	if _, ok := tl.offsets[path]; ok {
		t.Fatalf("offset de arquivo ausente há mais que o TTL não foi podado")
	}
}

// TestEstadoAntigoSemSeenNaoEhPodadoNaHora: estado gravado por uma versão anterior
// não tem o campo Seen. Tratá-lo como "ausente desde a época zero" faria a primeira
// varredura após a atualização do agente reproduzir exatamente o reenvio em dobro.
func TestEstadoAntigoSemSeenNaoEhPodadoNaHora(t *testing.T) {
	tl := New("http://x", "k", "h", []string{"/nao/existe/*.log"})
	tl.offsets["/nao/existe/app.log"] = fileState{Off: 100} // sem Seen, como no formato antigo
	tl.pruneOffsets(nil)
	st, ok := tl.offsets["/nao/existe/app.log"]
	if !ok {
		t.Fatalf("estado antigo (sem Seen) foi podado na primeira varredura")
	}
	if st.Seen == 0 {
		t.Fatalf("Seen deveria ter sido adotado com o relógio atual, veio %d", st.Seen)
	}
}

// TestEstadoCorrompidoNaoImpedeColeta: estado ilegível é best-effort — volta ao
// comportamento de começar do fim, nunca derruba a coleta.
func TestEstadoCorrompidoNaoImpedeColeta(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	os.WriteFile(path, []byte("a\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "logtail-file.state"), []byte("{lixo"), 0o600)
	SetLogStateDir(dir)
	defer SetLogStateDir("")

	tl := New("http://x", "k", "h", []string{path})
	tl.loadOffsets()
	tl.seedOffsets()
	if got := tl.readNew(path); len(got) != 0 {
		t.Fatalf("com estado corrompido deveria começar do fim, veio %v", got)
	}
}

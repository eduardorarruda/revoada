package logtail

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func acrescentar(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

// PROVA (validação de 02/10/2026): no logrotate `create`, as linhas escritas no
// arquivo antigo entre a última varredura e o rename chegam — antes sumiam (5 de 5
// perdidas ao vivo).
func TestRotacaoPorRenameRecuperaFimDoArquivoAntigo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sem inode no Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	acrescentar(t, path, "a1\na2\n")
	tl := New("http://x", "k", "h", []string{path})
	if got := tl.readNew(path); len(got) != 2 {
		t.Fatalf("primeira leitura: %v", got)
	}

	acrescentar(t, path, "b1\nb2\nb3\n") // escrito ANTES da rotação, ainda não lido
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	acrescentar(t, path, "c1\nc2\n") // arquivo novo

	got := tl.readNew(path)
	want := []string{"b1", "b2", "b3", "c1", "c2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("esperava %v (fim do antigo + novo, em ordem), veio %v", want, got)
	}
	if len(tl.notes) != 1 || !strings.Contains(tl.notes[0].body, "3 linha(s) finais do arquivo antigo recuperadas") {
		t.Errorf("o aviso de rotação deveria contar as linhas recuperadas: %#v", tl.notes)
	}
	// Nada é relido no ciclo seguinte.
	if got := tl.readNew(path); len(got) != 0 {
		t.Fatalf("releu depois da rotação: %v", got)
	}
}

// PROVA: rotação em que o antigo foi apagado (ou comprimido) não quebra nada —
// segue lendo o novo do zero.
func TestRotacaoSemArquivoAntigoLeSoONovo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sem inode no Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	acrescentar(t, path, "a1\n")
	tl := New("http://x", "k", "h", []string{path})
	tl.readNew(path)
	acrescentar(t, path, "perdida\n")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	acrescentar(t, path, "c1\n")
	got := tl.readNew(path)
	if strings.Join(got, ",") != "c1" {
		t.Fatalf("esperava [c1], veio %v", got)
	}
}

// PROVA: copytruncate seguido de escrita MAIOR que o offset antigo, antes da
// varredura. Mesmo inode e tamanho maior — antes o coletor seguia do offset velho,
// entregava a primeira linha cortada e perdia as anteriores (ao vivo: 29 de 30
// recebidas, a primeira cortada).
func TestTruncamentoSeguidoDeEscritaMaiorRecomecaDoZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	acrescentar(t, path, "t3-0\nt3-1\nt3-2\n")
	tl := New("http://x", "k", "h", []string{path})
	if got := tl.readNew(path); len(got) != 3 {
		t.Fatalf("primeira leitura: %v", got)
	}
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	var novas []string
	for i := 0; i < 30; i++ {
		novas = append(novas, "t4-"+strings.Repeat("y", 40)+string(rune('a'+i%26)))
	}
	acrescentar(t, path, strings.Join(novas, "\n")+"\n")

	got := tl.readNew(path)
	if strings.Join(got, "|") != strings.Join(novas, "|") {
		t.Fatalf("esperava as 30 linhas íntegras; veio %d, primeira %q", len(got), first(got))
	}
	if len(tl.notes) != 1 || !strings.Contains(tl.notes[0].body, "arquivo truncado") {
		t.Errorf("o truncamento deveria ser avisado no stream: %#v", tl.notes)
	}
}

// PROVA (controle): crescimento normal NÃO é confundido com reescrita — nenhuma
// linha é reenviada.
func TestCrescimentoNormalNaoEhReescrita(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	tl := New("http://x", "k", "h", []string{path})
	acrescentar(t, path, "x1\n")
	tl.readNew(path)
	for i := 0; i < 5; i++ {
		acrescentar(t, path, "mais uma linha\n")
		if got := tl.readNew(path); len(got) != 1 {
			t.Fatalf("ciclo %d: esperava 1 linha nova, veio %v", i, got)
		}
	}
	if len(tl.notes) != 0 {
		t.Fatalf("crescimento normal gerou aviso de rotação: %#v", tl.notes)
	}
	// Cauda parcial (sem \n) não muda a impressão digital nem vira reescrita.
	acrescentar(t, path, "parcial")
	if got := tl.readNew(path); len(got) != 0 {
		t.Fatalf("cauda parcial emitida: %v", got)
	}
	acrescentar(t, path, " completa\n")
	if got := tl.readNew(path); len(got) != 1 || got[0] != "parcial completa" {
		t.Fatalf("esperava a linha completada, veio %v", got)
	}
	if len(tl.notes) != 0 {
		t.Fatalf("cauda parcial gerou aviso de rotação: %#v", tl.notes)
	}
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// PROVA: arquivo reescrito com EXATAMENTE o mesmo tamanho do offset. Acontece de
// verdade no ext4: apagar e recriar o log reaproveita o inode na hora, e se a linha
// nova tem o tamanho da antiga o coletor via "mesmo inode, nada novo" e a perdia
// (o CI no Ubuntu pegou isto; no btrfs o inode não se repete e passava).
func TestReescritoComMesmoTamanhoNaoPerdeALinha(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	acrescentar(t, path, "a1\n")
	tl := New("http://x", "k", "h", []string{path})
	if got := tl.readNew(path); strings.Join(got, ",") != "a1" {
		t.Fatalf("primeira leitura: %v", got)
	}
	if err := os.WriteFile(path, []byte("c1\n"), 0o644); err != nil { // trunca e reescreve
		t.Fatal(err)
	}
	if got := tl.readNew(path); strings.Join(got, ",") != "c1" {
		t.Fatalf("esperava [c1], veio %v", got)
	}
	if got := tl.readNew(path); len(got) != 0 {
		t.Fatalf("releu: %v", got)
	}
}

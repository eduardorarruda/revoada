package logtail

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// TestStitchJavaStackTrace: um stack trace Java (linha de erro + frames `at` +
// `Caused by:` + `... N more`) deve virar UM único registro.
func TestStitchJavaStackTrace(t *testing.T) {
	in := []string{
		"2026-07-20 12:00:00 ERROR Unhandled exception: java.lang.NullPointerException",
		"\tat com.foo.Bar.baz(Bar.java:10)",
		"\tat com.foo.App.main(App.java:5)",
		"Caused by: java.lang.IllegalStateException: bad state",
		"\tat com.foo.Bar.init(Bar.java:3)",
		"\t... 3 more",
		"2026-07-20 12:00:01 INFO recovered",
	}
	got := stitchLines(in)
	if len(got) != 2 {
		t.Fatalf("esperava 2 registros, veio %d: %#v", len(got), got)
	}
	first := got[0]
	for _, frag := range []string{"NullPointerException", "at com.foo.Bar.baz", "Caused by:", "... 3 more"} {
		if !strings.Contains(first, frag) {
			t.Errorf("registro do stack trace não contém %q: %q", frag, first)
		}
	}
	if strings.Count(first, "\n") != 5 {
		t.Errorf("esperava 6 linhas coladas (5 quebras) no 1º registro, veio %d", strings.Count(first, "\n"))
	}
	if got[1] != "2026-07-20 12:00:01 INFO recovered" {
		t.Errorf("2º registro deveria ser a linha INFO isolada, veio %q", got[1])
	}
}

// TestStitchPythonTraceback: um traceback Python (Traceback + frames File/código
// indentados + linha da exceção) deve virar UM único registro.
func TestStitchPythonTraceback(t *testing.T) {
	in := []string{
		"2026-07-20 12:00:00 ERROR request failed",
		"Traceback (most recent call last):",
		`  File "app.py", line 10, in handler`,
		"    do_thing()",
		"ValueError: boom",
		"2026-07-20 12:00:01 INFO next request",
	}
	got := stitchLines(in)
	if len(got) != 2 {
		t.Fatalf("esperava 2 registros, veio %d: %#v", len(got), got)
	}
	for _, frag := range []string{"request failed", "Traceback (most recent call last):", `File "app.py"`, "ValueError: boom"} {
		if !strings.Contains(got[0], frag) {
			t.Errorf("registro do traceback não contém %q: %q", frag, got[0])
		}
	}
	if got[1] != "2026-07-20 12:00:01 INFO next request" {
		t.Errorf("2º registro deveria ser a linha INFO isolada, veio %q", got[1])
	}
}

// TestStitchLinhasNormais: linhas de log normais (nenhuma indentada nem casando
// padrão de stack) NÃO devem ser coladas — permanecem uma por registro.
func TestStitchLinhasNormais(t *testing.T) {
	in := []string{
		"2026-07-20 INFO started",
		"2026-07-20 INFO handled request 1",
		"2026-07-20 WARN queue is filling",
		"2026-07-20 ERROR failed to connect",
	}
	got := stitchLines(in)
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("linhas normais não deveriam ser coladas.\n quer %#v\n veio %#v", in, got)
	}
}

// TestStitchDockerRespeitaStream: no docker, uma continuação só cola em linhas do
// mesmo container e mesmo stream — nunca cruza stdout/stderr.
func TestStitchDockerRespeitaStream(t *testing.T) {
	in := []dline{
		{container: "api", stream: "stderr", text: "ERROR boom java.lang.RuntimeException"},
		{container: "api", stream: "stderr", text: "\tat com.foo.Bar.baz(Bar.java:1)"},
		{container: "api", stream: "stdout", text: "\tat this.is.stdout.not.a.continuation"},
	}
	got := stitchDocker(in)
	if len(got) != 2 {
		t.Fatalf("esperava 2 registros (stderr colado, stdout separado), veio %d: %#v", len(got), got)
	}
	if !strings.Contains(got[0].text, "at com.foo.Bar.baz") || got[0].stream != "stderr" {
		t.Errorf("1º registro deveria ser o stderr costurado, veio %#v", got[0])
	}
	if got[1].stream != "stdout" {
		t.Errorf("2º registro deveria ser o stdout separado, veio %#v", got[1])
	}
}

// TestStitchTetoDeLinhas: uma entrada não pode crescer sem limite (um processo em
// loop de exceção entregava dezenas de milhares de linhas num único registro, com
// custo quadrático de CPU). O corte tem de ser ANUNCIADO no corpo.
func TestStitchTetoDeLinhas(t *testing.T) {
	in := []string{"ERROR java.lang.NullPointerException: boom"}
	const frames = 5000
	for i := 0; i < frames; i++ {
		in = append(in, fmt.Sprintf("\tat com.foo.Bar.baz(Bar.java:%d)", i))
	}
	in = append(in, "2026-08-09 INFO recuperado")

	got := stitchLines(in)
	if len(got) != 2 {
		t.Fatalf("esperava 2 registros, veio %d", len(got))
	}
	linhas := strings.Count(got[0], "\n") + 1
	// maxStitchLines linhas costuradas + 1 linha do marcador de corte.
	if linhas != maxStitchLines+1 {
		t.Errorf("esperava %d linhas (teto + marcador), veio %d", maxStitchLines+1, linhas)
	}
	omitidas := frames + 1 - maxStitchLines
	if want := fmt.Sprintf("…(stack truncada, %d linhas omitidas)", omitidas); !strings.Contains(got[0], want) {
		t.Errorf("marcador de corte ausente ou errado, esperava %q em:\n%s", want, got[0][len(got[0])-120:])
	}
	if got[1] != "2026-08-09 INFO recuperado" {
		t.Errorf("a linha seguinte ao corte foi perdida: %q", got[1])
	}
}

// TestStitchTetoDeBytes: poucas linhas muito longas também têm de parar no teto de
// bytes — senão o teto de linhas sozinho deixaria passar um registro de vários MiB.
func TestStitchTetoDeBytes(t *testing.T) {
	longa := "\t" + strings.Repeat("x", 8<<10)
	in := []string{"ERROR estourou"}
	for i := 0; i < 50; i++ { // 50 × 8 KiB = 400 KiB, bem acima de maxStitchBytes
		in = append(in, longa)
	}
	got := stitchLines(in)
	if len(got) != 1 {
		t.Fatalf("esperava 1 registro, veio %d", len(got))
	}
	if len(got[0]) > maxStitchBytes+200 { // +200: folga para o marcador de corte
		t.Errorf("registro passou do teto de bytes: %d bytes", len(got[0]))
	}
	if !strings.Contains(got[0], "linhas omitidas") {
		t.Errorf("corte por bytes não foi anunciado no corpo")
	}
}

// TestStitchDockerTeto: o mesmo teto vale para o caminho de containers, e os labels
// do primeiro frame (container/stream) precisam sobreviver ao corte.
func TestStitchDockerTeto(t *testing.T) {
	in := []dline{{container: "api", stream: "stderr", text: "ERROR boom"}}
	for i := 0; i < 1000; i++ {
		in = append(in, dline{container: "api", stream: "stderr", text: "\tat frame"})
	}
	got := stitchDocker(in)
	if len(got) != 1 {
		t.Fatalf("esperava 1 registro, veio %d", len(got))
	}
	if got[0].container != "api" || got[0].stream != "stderr" {
		t.Errorf("labels perdidos no corte: %#v", got[0])
	}
	if n := strings.Count(got[0].text, "\n") + 1; n != maxStitchLines+1 {
		t.Errorf("esperava %d linhas (teto + marcador), veio %d", maxStitchLines+1, n)
	}
	if !strings.Contains(got[0].text, "…(stack truncada,") {
		t.Errorf("corte não anunciado: %q", got[0].text)
	}
}

// BenchmarkStitchBomba mede o caminho que antes era quadrático. Referência colhida
// neste repositório (i5-7200U): com a concatenação `+=`, 20.000 linhas custavam
// 5,9 s e 7,1 GB alocados, e 60.000 linhas, 52 s e 64 GB. Com strings.Builder e o
// teto, o custo é de centenas de microssegundos e ~30 KB.
func BenchmarkStitchBomba(b *testing.B) {
	in := []string{"ERROR java.lang.NullPointerException: boom"}
	for i := 0; i < 60000; i++ {
		in = append(in, fmt.Sprintf("\tat com.foo.Bar.baz(Bar.java:%d)", i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if len(stitchLines(in)) == 0 {
			b.Fatal("vazio")
		}
	}
}

func TestSeverityFromPriority(t *testing.T) {
	cases := map[int]string{
		0: "ERROR", 1: "ERROR", 2: "ERROR", 3: "ERROR",
		4: "WARN",
		5: "INFO", 6: "INFO",
		7: "DEBUG",
		9: "INFO", // fora da faixa → default INFO
	}
	for prio, want := range cases {
		if got := severityFromPriority(prio); got != want {
			t.Errorf("severityFromPriority(%d)=%s, quer %s", prio, got, want)
		}
	}
}

func TestParseKmsg(t *testing.T) {
	// prio=3 (err) → level 3 → ERROR; prio=6 (info) → INFO.
	sev, body, ok := parseKmsg("3,404,12345678,-;EXT4-fs error (device sda1): bad")
	if !ok || sev != "ERROR" || body != "EXT4-fs error (device sda1): bad" {
		t.Fatalf("kmsg err: ok=%v sev=%s body=%q", ok, sev, body)
	}
	sev, _, ok = parseKmsg("6,10,999,-;usb 1-1: new device")
	if !ok || sev != "INFO" {
		t.Fatalf("kmsg info: ok=%v sev=%s", ok, sev)
	}
	// prio=11 = facility 1<<3 | level 3 → level 3 → ERROR (usa prio & 7).
	if sev, _, _ := parseKmsg("11,1,1,-;kernel oops"); sev != "ERROR" {
		t.Errorf("kmsg facility+level: sev=%s, quer ERROR", sev)
	}
	if _, _, ok := parseKmsg("sem ponto e virgula"); ok {
		t.Errorf("registro sem ';' deveria ser inválido")
	}
}

func TestParseJournal(t *testing.T) {
	line := []byte(`{"MESSAGE":"disk full","PRIORITY":"3","_SYSTEMD_UNIT":"nginx.service"}`)
	r, _, ok := parseJournal(line)
	if !ok || r.body != "disk full" || r.severity != "ERROR" {
		t.Fatalf("journal: ok=%v body=%q sev=%s", ok, r.body, r.severity)
	}
	if r.labels["source"] != "journald" || r.labels["unit"] != "nginx.service" || r.service != "nginx.service" {
		t.Errorf("labels/service errados: %#v service=%s", r.labels, r.service)
	}
	// Sem unit → cai para SYSLOG_IDENTIFIER e service=identifier.
	r2, _, _ := parseJournal([]byte(`{"MESSAGE":"hi","PRIORITY":"6","SYSLOG_IDENTIFIER":"sshd"}`))
	if r2.labels["unit"] != "sshd" || r2.service != "sshd" || r2.severity != "INFO" {
		t.Errorf("fallback SYSLOG_IDENTIFIER falhou: %#v service=%s sev=%s", r2.labels, r2.service, r2.severity)
	}
	// MESSAGE vazio → registro inválido.
	if _, _, ok := parseJournal([]byte(`{"PRIORITY":"6"}`)); ok {
		t.Errorf("MESSAGE ausente deveria ser inválido")
	}
	// MESSAGE como array de bytes (não-UTF8) deve ser coagido a string.
	if r3, _, ok := parseJournal([]byte(`{"MESSAGE":[104,105],"PRIORITY":"6"}`)); !ok || r3.body != "hi" {
		t.Errorf("MESSAGE array não coagido: ok=%v body=%q", ok, r3.body)
	}
}

func TestStripDockerTimestamp(t *testing.T) {
	// emit remove o timestamp que timestamps=1 antepõe, preservando a indentação.
	out := make(chan dline, 1)
	if !emit(context.Background(), out, "id-do-container", "api", "stdout", "2026-07-20T12:00:00.123456789Z \tat com.foo.Bar(x)") {
		t.Fatal("emit falhou")
	}
	got := <-out
	if got.text != "\tat com.foo.Bar(x)" {
		t.Errorf("timestamp não removido / indentação perdida: %q", got.text)
	}
	// O timestamp sai do corpo mas não é jogado fora: é ele que vira o cursor
	// `since=` do container no próximo boot. Perdê-lo aqui reabre o buraco de log
	// que toda parada do agente (inclusive a auto-atualização) causava.
	if got.ts != "2026-07-20T12:00:00.123456789Z" {
		t.Errorf("timestamp não guardado para o cursor: %q", got.ts)
	}
	if got.id != "id-do-container" {
		t.Errorf("id do container perdido — é a chave do cursor: %q", got.id)
	}
}

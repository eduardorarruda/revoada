package logtail

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Provas do teto de VOLUME (bytes/s).
//
// O defeito que ele corrige: o único teto era em LINHAS/s (5000), e a garantia
// declarada sempre foi de volume ("~1 MB/s a 200 B/linha"). Uma aplicação que loga
// JSON de 4 KB por linha cabe folgada dentro das 5000 linhas/s e manda 20 MB/s —
// 1,7 TB/dia saindo do link do servidor do cliente, com o agente convencido de estar
// dentro do limite.

func relogioFixo(t *time.Time) func() time.Time { return func() time.Time { return *t } }

func linhaDe(n int) record {
	return record{service: "app", severity: "INFO", body: strings.Repeat("x", n)}
}

// PROVA: o caso medido — linhas grandes passam pelo teto de LINHAS e são cortadas
// pelo teto de BYTES.
func TestTetoDeBytesCortaOQueOTetoDeLinhasDeixaPassar(t *testing.T) {
	agora := time.Unix(1_700_000_000, 0)

	// 100 linhas por lote é irrisório para 5000 linhas/s: o teto de linhas não corta.
	linhas := newRateLimiter(5000, relogioFixo(&agora))
	// 1 MiB/s de volume: 100 linhas de 4 KiB são 400 KiB, então o primeiro lote passa
	// e os seguintes, dentro do mesmo segundo, não.
	bytes := newByteLimiter(1<<20, relogioFixo(&agora))

	lote := make([]record, 100)
	for i := range lote {
		lote[i] = linhaDe(4 << 10)
	}

	if k := linhas.admit(len(lote)); k != len(lote) {
		t.Fatalf("o teto de linhas cortou (%d de %d) — este teste precisa provar que ele NÃO corta", k, len(lote))
	}

	total := 0
	for i := 0; i < 10; i++ { // 10 lotes = ~4 MiB no MESMO instante
		total += bytes.admit(lote)
	}
	if total >= 1000 {
		t.Fatalf("o teto de bytes deixou passar tudo (%d linhas, ~%d MiB num instante) — o link do cliente segue desprotegido", total, total*4/1024)
	}
	// No mesmo instante só existe a cota inicial (1 s = 1 MiB) e nada mais.
	if maxEsperado := 1 << 20 / (4 << 10); total > maxEsperado+2 {
		t.Fatalf("passou %d linhas (~%d KiB); o balde de 1 MiB/s comporta ~%d sem o relógio andar", total, total*4, maxEsperado)
	}
	if total == 0 {
		t.Fatal("não deixou passar nada — o limite não pode virar um bloqueio")
	}
}

// PROVA: linhas pequenas não são afetadas pelo teto de volume. Sem este controle, a
// correção poderia ter estrangulado a operação normal (um host comum produz
// kilobytes por segundo, não megabytes).
func TestTetoDeBytesNaoAtrapalhaLogNormal(t *testing.T) {
	agora := time.Unix(1_700_000_000, 0)
	bl := newByteLimiter(1<<20, relogioFixo(&agora))
	lote := make([]record, 200)
	for i := range lote {
		lote[i] = linhaDe(200) // 200 B/linha: o perfil de referência da garantia
	}
	if k := bl.admit(lote); k != len(lote) {
		t.Fatalf("cortou log de tamanho normal (%d de %d)", k, len(lote))
	}
}

// PROVA: o balde recompõe com o tempo — o limite é uma TAXA, não uma cota total.
func TestTetoDeBytesRecompoeComOTempo(t *testing.T) {
	agora := time.Unix(1_700_000_000, 0)
	bl := newByteLimiter(1<<20, relogioFixo(&agora))
	grande := []record{linhaDe(1<<20 - 64)} // esvazia a cota inicial (1 s) de uma vez
	if k := bl.admit(grande); k != 1 {
		t.Fatal("a cota inicial de 1 s não comportou 1 MiB")
	}
	if k := bl.admit([]record{linhaDe(1 << 20)}); k != 0 {
		t.Fatal("deixou passar com o balde vazio")
	}
	agora = agora.Add(3 * time.Second)
	if k := bl.admit([]record{linhaDe(512 << 10)}); k != 1 {
		t.Fatal("não recompôs depois de 3 segundos — o teto virou cota, não taxa")
	}
}

// PROVA: um único registro maior que o balde inteiro passa em vez de travar o
// coletor para sempre.
//
// Sem este escape, uma linha de 3 MiB num balde de 1 MiB/s nunca caberia: ela não
// sairia nem seria descartada, e o coletor ficaria parado nela — o pior dos dois
// mundos, porque o log some E o limite não é respeitado por nada em troca.
func TestRegistroMaiorQueOBaldeNaoTrava(t *testing.T) {
	agora := time.Unix(1_700_000_000, 0)
	bl := newByteLimiter(1<<20, relogioFixo(&agora))
	maiorQueOBalde := int(janelaDeRajada.Seconds())*(1<<20) + (1 << 20)
	if k := bl.admit([]record{linhaDe(maiorQueOBalde)}); k != 1 {
		t.Fatal("um registro maior que o balde travou o coletor")
	}
	// E o débito é cobrado: o próximo registro, no mesmo instante, não passa.
	if k := bl.admit([]record{linhaDe(100)}); k != 0 {
		t.Fatal("o excesso não foi cobrado do balde — o limite deixaria de valer na média")
	}
}

// PROVA: o descarte por volume é AVISADO no próprio stream, e o aviso diz qual chave
// ajustar. Perda silenciosa é o desfecho que todo este caminho existe para evitar:
// no painel, log descartado é indistinguível de servidor quieto.
func TestAvisoDeDescartePorVolume(t *testing.T) {
	agora := time.Unix(1_700_000_000, 0)
	bl := newByteLimiter(1024, relogioFixo(&agora))
	bl.admit([]record{linhaDe(4096), linhaDe(4096), linhaDe(4096)})
	n := bl.notice()
	if n == nil {
		t.Fatal("descartou por volume e não avisou ninguém")
	}
	if !strings.Contains(n.body, "log_max_bytes_per_sec") {
		t.Fatalf("o aviso precisa nomear a chave a ajustar, senão o operador mexe na errada: %q", n.body)
	}
	if n.severity != "WARN" {
		t.Fatalf("aviso de perda tem de ser WARN, veio %q", n.severity)
	}
}

// PROVA (fim a fim no sink): com o teto de volume ligado, o gateway recebe MENOS do
// que foi produzido — e recebe o aviso da perda.
func TestSinkAplicaTetoDeBytes(t *testing.T) {
	srv, _, corpos := srvColetor(t, 200, 0)
	defer srv.Close()

	antesLinhas, antesBytes := sharedLimiter, sharedByteLimiter
	defer func() { sharedLimiter, sharedByteLimiter = antesLinhas, antesBytes }()
	SetLogRateLimit(100000)  // teto de linhas fora do caminho de propósito
	SetLogByteRateLimit(512) // volume apertado: é ele que tem de cortar

	s := newSink(srv.URL, "k", "h")
	lote := []record{linhaDe(4096), linhaDe(4096), linhaDe(4096)}
	s.post(context.Background(), lote)

	// srvColetor guarda uma entrada por LINHA NDJSON recebida.
	recebidas := *corpos
	if len(recebidas) == 0 {
		t.Fatal("nada chegou ao gateway")
	}
	if len(recebidas) >= len(lote) {
		t.Fatalf("o teto de volume não cortou nada: %d linhas de 4 KiB chegaram ao gateway", len(recebidas))
	}
	juntas := strings.Join(recebidas, "\n")
	if !strings.Contains(juntas, "log_max_bytes_per_sec") {
		t.Fatal("cortou sem avisar — no painel isso vira silêncio sem causa")
	}
}

package otlp

import (
	"log/slog"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/gateway/internal/model"
)

// traceIDPorDecisao acha um trace_id cujo bucket determinístico dá a decisão desejada
// para a fração informada — o teste precisa controlar QUAL lado do sorteio cai.
func traceIDPorDecisao(t *testing.T, pct uint32, querManter bool) string {
	t.Helper()
	for i := 0; i < 10000; i++ {
		id := "trace-" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26))
		if (traceBucket(id) < pct) == querManter {
			return id
		}
	}
	t.Fatalf("não achei trace_id com bucket desejado")
	return ""
}

func receiverDeTeste(frac, slowMs float64) *Receiver {
	return &Receiver{log: slog.Default(), now: time.Now, sampleFrac: frac, slowMs: slowMs}
}

// TestTraceRaizNaoEhDescartadaAoChegarLoteInteressante reproduz o defeito medido:
//
//	lote 1: raiz `GET /pedido`, 50 ms, nada de interessante → o bucket mandava DESCARTAR
//	lote 2: filho `db.insert`, 900 ms → considerado lento e MANTIDO
//
// Resultado antigo: a lista devolvia `root_name=db.insert, spans=1, duration_ms=900` —
// um filho promovido a raiz (o server usa argMin(name, ts)) e a duração subestimada,
// numa tela que promete "os problemas e os lentos você sempre vê".
//
// Agora a decisão é do TRACE e pegajosa: o lote 2 é mantido, e — porque a raiz já foi
// perdida — os spans retidos vão marcados como PARCIAIS, para o painel não apresentar
// um pedaço como se fosse o todo.
func TestTraceParcialQuandoViraInteressanteDepois(t *testing.T) {
	rc := receiverDeTeste(0.2, 500) // 20%: pct=20
	tid := traceIDPorDecisao(t, 20, false)

	raiz := []model.Span{{TraceID: tid, SpanID: "1", Name: "GET /pedido", DurationMs: 50}}
	if got := rc.sampleSpans(raiz); len(got) != 0 {
		t.Fatalf("lote 1 (nada interessante, bucket fora): mantidos=%d, quer 0", len(got))
	}

	filho := []model.Span{{TraceID: tid, SpanID: "2", ParentID: "1", Name: "db.insert", DurationMs: 900}}
	got := rc.sampleSpans(filho)
	if len(got) != 1 {
		t.Fatalf("lote 2 (lento): mantidos=%d, quer 1", len(got))
	}
	if got[0].Labels[SpanPartialLabel] != "1" {
		t.Fatalf("trace incompleto não foi marcado como parcial: labels=%v", got[0].Labels)
	}
}

// TestTraceInteressanteMantemLotesSeguintes é a outra metade do MESMO defeito: a raiz
// interessante ficava, e os filhos — julgados sozinhos noutro lote, sem nada de
// interessante à vista — caíam no bucket e sumiam. O trace aparecia truncado do outro
// lado. Com a decisão pegajosa, todo lote do mesmo trace entra junto.
func TestTraceInteressanteMantemLotesSeguintes(t *testing.T) {
	rc := receiverDeTeste(0.2, 500)
	tid := traceIDPorDecisao(t, 20, false) // bucket manda descartar

	raiz := []model.Span{{TraceID: tid, SpanID: "1", Name: "GET /pedido", StatusCode: "ERROR", DurationMs: 12}}
	if got := rc.sampleSpans(raiz); len(got) != 1 {
		t.Fatalf("span com erro tem de ser mantido: mantidos=%d", len(got))
	}
	filhos := []model.Span{
		{TraceID: tid, SpanID: "2", ParentID: "1", Name: "cache.get", DurationMs: 3},
		{TraceID: tid, SpanID: "3", ParentID: "1", Name: "db.select", DurationMs: 8},
	}
	got := rc.sampleSpans(filhos)
	if len(got) != 2 {
		t.Fatalf("filhos do trace interessante foram descartados: mantidos=%d, quer 2", len(got))
	}
	for _, s := range got {
		if s.Labels[SpanPartialLabel] != "" {
			t.Fatalf("trace completo marcado como parcial: %v", s.Labels)
		}
	}
}

// TestTraceDecisaoNaoVazaMemoria: o trace_id vem do cliente, então a janela de decisão
// precisa de teto — senão ela é a superfície mais barata de estourar o gateway.
func TestTraceDecisaoNaoVazaMemoria(t *testing.T) {
	d := newTraceDecider(time.Minute, 16)
	for i := 0; i < 5000; i++ {
		d.decide("t"+string(rune(i%1000))+string(rune(i/1000)), false, true)
	}
	if d.len() > 16 {
		t.Fatalf("decisões vivas=%d acima do teto 16", d.len())
	}
}

// TestTraceDecisaoExpira: fora da janela, o trace recomeça do zero (lembrar de todos
// para sempre é o vazamento que o TTL evita).
func TestTraceDecisaoExpira(t *testing.T) {
	d := newTraceDecider(time.Minute, 100)
	agora := time.Unix(0, 0)
	d.now = func() time.Time { return agora }
	if keep, _ := d.decide("t1", false, true); !keep {
		t.Fatal("decisão inicial deveria manter")
	}
	agora = agora.Add(2 * time.Minute)
	// Fora da janela e com `fresh=false`, a decisão nova é descartar — prova que a
	// entrada antiga não foi herdada.
	if keep, _ := d.decide("t1", false, false); keep {
		t.Fatal("decisão vencida foi herdada")
	}
}

// TestTraceSemMudancaNaoMarcaParcial: um trace mantido desde o primeiro lote não pode
// ganhar a marca de parcial (senão a marca perde o sentido e a tela mente ao contrário).
func TestTraceSemMudancaNaoMarcaParcial(t *testing.T) {
	rc := receiverDeTeste(1, 500) // frac=1: tudo é mantido
	tid := "trace-completo"
	got := rc.sampleSpans([]model.Span{{TraceID: tid, SpanID: "1", Name: "GET /", DurationMs: 5}})
	if len(got) != 1 {
		t.Fatalf("frac=1 deveria manter tudo: %d", len(got))
	}
	got = rc.sampleSpans([]model.Span{{TraceID: tid, SpanID: "2", Name: "db", DurationMs: 900}})
	if len(got) != 1 || got[0].Labels[SpanPartialLabel] != "" {
		t.Fatalf("trace completo marcado como parcial: %+v", got)
	}
}

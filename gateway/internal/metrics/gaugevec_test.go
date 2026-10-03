package metrics

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestGaugeVecExpiraNaRaspagem trava o conserto do TTL que nunca era cobrado.
//
// A varredura de séries paradas só rodava dentro do Set — ou seja, quando uma série
// NOVA nascia. Numa instalação estável nenhuma série nova nasce, então nenhuma série
// velha morria: o /metrics continuava exportando o rótulo de um host que parou de
// reportar, indefinidamente. Como o /metrics do gateway é raspado para dentro do
// ClickHouse, isso fazia o nome de um servidor APAGADO pelo painel voltar a virar
// linha a cada raspagem, para sempre.
func TestGaugeVecExpiraNaRaspagem(t *testing.T) {
	relogio := time.Now()
	v := NewGaugeVec("teste_gauge_expira", "ajuda", 10, 5*time.Minute, "host")
	v.now = func() time.Time { return relogio }

	v.Set(1.5, "vai-sumir")
	if got := raspar(v); !strings.Contains(got, `host="vai-sumir"`) {
		t.Fatalf("a série deveria aparecer logo após o Set: %q", got)
	}

	// Passa o TTL SEM nenhum Set novo — é exatamente o caso da instalação estável.
	relogio = relogio.Add(6 * time.Minute)

	if got := raspar(v); strings.Contains(got, "vai-sumir") {
		t.Errorf("série parada há mais que o TTL não pode continuar sendo exportada: %q", got)
	}
	if n := v.Len(); n != 0 {
		t.Errorf("a série deveria ter saído da memória também: Len=%d", n)
	}
}

// TestGaugeVecNaoExpiraQuemAindaReporta: a varredura não pode levar junto quem
// continua vivo — seria apagar o medidor de um host que está reportando normalmente.
func TestGaugeVecNaoExpiraQuemAindaReporta(t *testing.T) {
	relogio := time.Now()
	v := NewGaugeVec("teste_gauge_vivo", "ajuda", 10, 5*time.Minute, "host")
	v.now = func() time.Time { return relogio }

	v.Set(1, "vivo")
	relogio = relogio.Add(4 * time.Minute)
	v.Set(2, "vivo")
	relogio = relogio.Add(4 * time.Minute) // 8 min do 1º Set, 4 min do último

	if got := raspar(v); !strings.Contains(got, `host="vivo"`) {
		t.Errorf("host que reportou há 4 min (TTL de 5) tem de continuar no medidor: %q", got)
	}
}

func raspar(v *GaugeVec) string {
	var b bytes.Buffer
	v.write(&b)
	return b.String()
}

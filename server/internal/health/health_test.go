package health

import (
	"math"
	"testing"
)

// TestClassifyNaNInf é a prova de que o defeito "NaN classifica como ok" morreu.
//
// Toda comparação com NaN é falsa — inclusive `NaN >= crit` e `NaN >= warn` —,
// então antes da guarda o valor caía no `default` e virava "ok" VERDE. Um valor
// impossível (0/0 em algum coletor, contador zerado, divisão por zero) aparecia no
// mural como o estado mais tranquilizador que existe.
func TestClassifyNaNInf(t *testing.T) {
	const warn, crit = 70, 90
	for _, pct := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		got := Classify(pct, warn, crit)
		if got == StateOK {
			t.Errorf("Classify(%v) = %q — valor impossível NÃO pode virar ok verde", pct, got)
		}
		if got != StateNoData {
			t.Errorf("Classify(%v) = %q; want %q", pct, got, StateNoData)
		}
	}
}

// TestWorstIgnoraNoData: "sem dado" não é severidade e não pode pintar o card
// inteiro — a ausência aparece no ladrilho da métrica, que é onde é acionável.
func TestWorstIgnoraNoData(t *testing.T) {
	if got := Worst(StateNoData, StateOK, StateOK); got != StateOK {
		t.Errorf("Worst(nodata, ok, ok) = %q; want ok", got)
	}
	if got := Worst(StateNoData, StateCrit); got != StateCrit {
		t.Errorf("Worst(nodata, crit) = %q; want crit", got)
	}
	if got := Worst(StateNoData); got != StateOK {
		t.Errorf("Worst(nodata) = %q; want ok (sem nenhuma severidade válida)", got)
	}
}

func TestClassify(t *testing.T) {
	// Usa os limiares de CPU/RAM (warn=70, crit=90) e cobre as bordas.
	const warn, crit = 70, 90
	cases := []struct {
		pct  float64
		want string
	}{
		{0, StateOK},
		{69, StateOK},
		{69.999, StateOK},
		{70, StateWarn}, // borda inferior do warn é inclusiva
		{80, StateWarn},
		{89, StateWarn},
		{89.999, StateWarn},
		{90, StateCrit}, // borda inferior do crit é inclusiva
		{91, StateCrit},
		{100, StateCrit},
		{150, StateCrit},
	}
	for _, c := range cases {
		if got := Classify(c.pct, warn, crit); got != c.want {
			t.Errorf("Classify(%v, %v, %v) = %q; want %q", c.pct, warn, crit, got, c.want)
		}
	}
}

func TestClassifyDiskThresholds(t *testing.T) {
	// Disco é mais rígido: warn=75, crit=90.
	d := Defaults["disk"]
	cases := []struct {
		pct  float64
		want string
	}{
		{74, StateOK},
		{75, StateWarn},
		{89, StateWarn},
		{90, StateCrit},
	}
	for _, c := range cases {
		if got := Classify(c.pct, d.Warn, d.Crit); got != c.want {
			t.Errorf("disk Classify(%v) = %q; want %q", c.pct, got, c.want)
		}
	}
}

func TestWorst(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{StateOK, StateOK, StateOK}, StateOK},
		{[]string{StateOK, StateWarn, StateOK}, StateWarn},
		{[]string{StateWarn, StateCrit, StateOK}, StateCrit},
		{[]string{StateCrit}, StateCrit},
		{nil, StateOK},                               // sem estados => ok
		{[]string{"", ""}, StateOK},                  // vazios ignorados
		{[]string{"", StateWarn}, StateWarn},         // vazio + warn => warn
		{[]string{StateOK, "desconhecido"}, StateOK}, // desconhecido não sobrepõe ok
		{[]string{StateWarn, StateWarn, StateOK}, StateWarn},
	}
	for _, c := range cases {
		if got := Worst(c.in...); got != c.want {
			t.Errorf("Worst(%v) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestDefaults(t *testing.T) {
	if Default("cpu").Warn != 70 || Default("cpu").Crit != 90 {
		t.Errorf("cpu defaults inesperados: %+v", Default("cpu"))
	}
	if Default("disk").Warn != 75 {
		t.Errorf("disk warn esperado 75, veio %v", Default("disk").Warn)
	}
	if Default("swap").Warn != 70 || Default("swap").Crit != 90 {
		t.Errorf("swap defaults inesperados (esperado 70/90): %+v", Default("swap"))
	}
	if (Default("inexistente") != Threshold{}) {
		t.Errorf("métrica desconhecida deve devolver zero-value")
	}
}

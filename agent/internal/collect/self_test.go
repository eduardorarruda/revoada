package collect

import (
	"context"
	"testing"
	"time"
)

// No modo `once` (cron, `* * * * *`) cada ciclo é um processo novo: a base de CPU é
// tirada e consultada microssegundos depois. O denominador fica na ordem de 10⁻⁴ s
// contra um numerador com granularidade de 1 jiffy, e a série reportava 0% eterno
// com picos ocasionais de ~2000%. Sem base utilizável, não se emite — zero seria
// uma afirmação que não temos como fazer.
func TestBaseSelfCPUValida(t *testing.T) {
	agora := time.Now()
	casos := []struct {
		nome   string
		base   time.Time
		quero  bool
		porque string
	}{
		{"sem base", time.Time{}, false, "o processo ainda não tirou a amostra-base"},
		{"base de microssegundos (modo cron)", agora.Add(-200 * time.Microsecond), false,
			"é exatamente o caso que reportava 0% eterno e picos de 2000%"},
		{"base de 10ms", agora.Add(-10 * time.Millisecond), false,
			"um jiffy inteiro de erro sobre 10ms é 100 pontos percentuais"},
		{"base de 999ms", agora.Add(-999 * time.Millisecond), false, "ainda abaixo do piso"},
		{"base de 1s", agora.Add(-time.Second), true, "quantização de ~1 ponto percentual: grosseiro, mas honesto"},
		{"base de um ciclo (15s)", agora.Add(-15 * time.Second), true, "o caso normal do modo daemon"},
		{"relógio andou para trás", agora.Add(time.Minute), false, "intervalo negativo não é intervalo"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			if got := baseSelfCPUValida(c.base, agora); got != c.quero {
				t.Errorf("baseSelfCPUValida = %v, quero %v — %s", got, c.quero, c.porque)
			}
		})
	}
}

// Duas coletas seguidas (o que o modo cron faz, em processos distintos) não podem
// produzir `agent.self.cpu.percent`: não houve intervalo entre elas.
func TestSelfNaoEmiteCPUSemBaseUtilizavel(t *testing.T) {
	Self(context.Background())
	for _, p := range Self(context.Background()) {
		if p.Name == "agent.self.cpu.percent" {
			t.Errorf("sem intervalo medível a métrica não pode ser emitida, veio %v", p.Value)
		}
	}
}

// TestSelfMetricsBasics: a coleta do próprio footprint sempre devolve ao menos
// goroutines e heap, com valores plausíveis, e nunca panica.
func TestSelfMetricsBasics(t *testing.T) {
	pts := Self(context.Background())
	got := map[string]float64{}
	for _, p := range pts {
		got[p.Name] = p.Value
	}
	if _, ok := got["agent.self.goroutines"]; !ok {
		t.Fatal("faltou agent.self.goroutines")
	}
	if got["agent.self.goroutines"] < 1 {
		t.Fatalf("goroutines deveria ser >=1, veio %v", got["agent.self.goroutines"])
	}
	if _, ok := got["agent.self.memory.heap_bytes"]; !ok {
		t.Fatal("faltou agent.self.memory.heap_bytes")
	}
	if got["agent.self.memory.heap_bytes"] <= 0 {
		t.Fatalf("heap deveria ser >0, veio %v", got["agent.self.memory.heap_bytes"])
	}
	// Segunda chamada deve continuar íntegra (CPU% pode faltar de propósito quando
	// não houve intervalo medível — ver TestSelfNaoEmiteCPUSemBaseUtilizavel).
	pts2 := Self(context.Background())
	if len(pts2) < 2 {
		t.Fatalf("segunda coleta muito curta: %d pontos", len(pts2))
	}
}

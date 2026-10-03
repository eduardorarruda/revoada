package collect

import (
	"context"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

var (
	selfOnce sync.Once
	selfProc *process.Process

	selfCPUMu sync.Mutex
	// selfCPUBase é quando a amostra-base de CPU do processo foi tirada. O gopsutil
	// guarda a leitura anterior dentro do *process.Process, mas não conta a quem
	// pergunta há quanto tempo ela é — e é justamente esse intervalo que decide se a
	// divisão tem alguma resolução.
	selfCPUBase time.Time
)

// minSelfCPUWindow é o menor intervalo entre duas leituras de CPU do próprio
// processo que ainda produz um número, e não um artefato de arredondamento.
//
// O tempo de CPU de um processo vem de /proc/self/stat em JIFFIES: com o HZ=100
// usual do Linux, a menor variação possível é 10ms. Num intervalo de 1s isso dá uma
// quantização de ~1 ponto percentual — grosseiro, mas honesto. Num intervalo de
// microssegundos, o mesmo jiffy vira milhares de porcento.
const minSelfCPUWindow = time.Second

// Self coleta o PRÓPRIO footprint do agente (métricas agent.self.*): CPU%, memória
// residente (RSS), heap Go e número de goroutines. É a peça da "garantia medida de
// não-sobrecarga": com estas séries dá para PROVAR ao vivo, num painel, que o agente
// é leve e continua leve sob carga (inclusive que os caps de log/container seguram o
// consumo). Best-effort: qualquer erro é ignorado e nunca afeta a coleta principal.
//
// CPU%: process.Percent mede o uso desde a última chamada, então precisa de uma
// amostra-base — e a base só vale se for VELHA o bastante. O código anterior tirava
// a base e consultava o valor microssegundos depois, no mesmo Self(). No modo
// daemon isso só estragava a primeira amostra; no modo `once` (cron, instalado como
// `* * * * *`) TODO ciclo é um processo novo, então TODA amostra passava por esse
// caminho: o denominador era da ordem de 10⁻⁴ s contra um numerador com granularidade
// de 1 jiffy, e a série reportava 0% eternamente, com picos ocasionais de ~2000%.
//
// Agora, sem base utilizável, a métrica NÃO é emitida. Zero seria uma afirmação —
// "o agente não consumiu CPU" — e é uma afirmação que não temos como fazer. No modo
// cron isso significa que `agent.self.cpu.percent` deixa de existir; as outras
// agent.self.* continuam, e no modo daemon (onde a medida é correta) nada muda.
//
// Pode passar de 100% em máquinas multi-core (soma dos núcleos) — é o comportamento
// esperado do gopsutil.
func Self(ctx context.Context) []Point {
	selfOnce.Do(func() {
		if p, err := process.NewProcess(int32(os.Getpid())); err == nil {
			selfProc = p
			_, _ = selfProc.PercentWithContext(ctx, 0) // baseline para o delta de CPU
			selfCPUMu.Lock()
			selfCPUBase = time.Now()
			selfCPUMu.Unlock()
		}
	})

	pts := []Point{{Name: "agent.self.goroutines", Value: float64(runtime.NumGoroutine())}}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	pts = append(pts, Point{Name: "agent.self.memory.heap_bytes", Value: float64(ms.HeapAlloc)})

	if selfProc != nil {
		if pct, ok := selfCPUPercent(ctx); ok {
			pts = append(pts, Point{Name: "agent.self.cpu.percent", Value: pct})
		}
		if mi, err := selfProc.MemoryInfoWithContext(ctx); err == nil && mi != nil {
			pts = append(pts, Point{Name: "agent.self.memory.rss_bytes", Value: float64(mi.RSS)})
		}
	}
	return pts
}

// selfCPUPercent lê o uso de CPU do processo desde a amostra-base, mas SÓ quando a
// base já é velha o bastante. A verificação vem antes da chamada de propósito:
// PercentWithContext move a base para agora, então consultar e descartar o resultado
// destruiria a base boa e condenaria também o ciclo seguinte.
func selfCPUPercent(ctx context.Context) (float64, bool) {
	selfCPUMu.Lock()
	base := selfCPUBase
	selfCPUMu.Unlock()

	if !baseSelfCPUValida(base, time.Now()) {
		return 0, false
	}
	pct, err := selfProc.PercentWithContext(ctx, 0)
	if err != nil {
		return 0, false
	}
	selfCPUMu.Lock()
	selfCPUBase = time.Now()
	selfCPUMu.Unlock()
	return pct, true
}

// baseSelfCPUValida decide se dá para medir CPU do próprio processo entre `base` e
// `agora` (função pura, testável). Recusa base ausente, intervalo negativo (relógio
// andou para trás) e intervalo curto demais para ter resolução de jiffy.
func baseSelfCPUValida(base, agora time.Time) bool {
	if base.IsZero() {
		return false
	}
	return agora.Sub(base) >= minSelfCPUWindow
}

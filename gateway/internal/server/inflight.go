package server

import (
	"net/http"
	"strconv"
	"sync/atomic"

	"github.com/eduardorarruda/revoada/gateway/internal/metrics"
)

// InFlight é o teto GLOBAL de requisições de ingestão sendo processadas ao mesmo tempo.
//
// POR QUE existe (o que acontecia sem ele): o gateway não tinha teto de carga NENHUM.
// O rate limit é POR IP — ele não sabe quantas requisições o processo inteiro está
// segurando. Sob saturação (ClickHouse em merge, Postgres lento, uma frota inteira
// drenando WAL ao mesmo tempo) o gateway continuava respondendo 200 e apenas ficava
// mais lento, até o POST estourar o timeout de 10 s do agente. Aí o agente considerava
// o lote perdido, re-bufferava e MANDAVA DE NOVO — engrossando a fila que já estava
// afogada. É o pior formato possível de sobrecarga: o gateway aceita tudo, não entrega
// nada e ainda multiplica o tráfego. E, como cada requisição em voo pode segurar até
// 16 MiB de corpo descomprimido (maxOTLPBody), a fila que cresce é memória do processo.
//
// Com o teto, a saturação vira uma resposta RÁPIDA e HONESTA: 503 + Retry-After. O
// agente já respeita Retry-After (a frente do agente fez isso no 429), então ele recua
// em vez de repetir na mesma velocidade. Dizer "não agora" em 1 ms é infinitamente
// melhor que dizer "ok" e sumir por 10 s.
//
// O teto NÃO é uma fila: quem não cabe é recusado na hora. Enfileirar só moveria a
// espera para dentro do gateway — e a espera dentro do gateway é justamente o que
// estoura o timeout do agente.
type InFlight struct {
	max  int64
	cur  atomic.Int64
	peak atomic.Int64

	// retryAfter é o valor (segundos) do header Retry-After da recusa.
	retryAfter string
}

var (
	overloaded = metrics.NewCounter(
		"revoada_ingest_overloaded_total",
		"Requisições recusadas com 503 por estouro do teto global de requisições em voo.")
)

// NewInFlight cria o limitador com o teto informado (<=0 desliga o teto).
func NewInFlight(max int) *InFlight {
	f := &InFlight{max: int64(max), retryAfter: "1"}
	metrics.NewGaugeFunc("revoada_ingest_inflight",
		"Requisições de ingestão em processamento neste instante.",
		func() float64 { return float64(f.cur.Load()) })
	metrics.NewGaugeFunc("revoada_ingest_inflight_max",
		"Teto global de requisições de ingestão em voo (REVOADA_MAX_INFLIGHT).",
		func() float64 { return float64(f.max) })
	// O PICO é o número que diz se o teto está bem dimensionado: um pico colado no
	// teto sem nenhum 503 significa que a próxima rajada vai bater.
	metrics.NewGaugeFunc("revoada_ingest_inflight_peak",
		"Maior número de requisições de ingestão simultâneas observado desde o boot.",
		func() float64 { return float64(f.peak.Load()) })
	return f
}

// Enter tenta ocupar uma vaga e CONTA a recusa; Leave devolve a vaga. São exportadas
// porque o listener OTLP/gRPC precisa do MESMO teto: dois contadores separados dariam
// ao gRPC uma cota extra de memória, que é meio teto — ou seja, nenhum.
func (f *InFlight) Enter() bool {
	if f.acquire() {
		return true
	}
	overloaded.Inc()
	return false
}

// Leave devolve a vaga ocupada por Enter.
func (f *InFlight) Leave() { f.release() }

// acquire tenta ocupar uma vaga; devolve false quando o teto está cheio.
func (f *InFlight) acquire() bool {
	if f.max <= 0 {
		return true
	}
	n := f.cur.Add(1)
	if n > f.max {
		f.cur.Add(-1)
		return false
	}
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	return true
}

func (f *InFlight) release() {
	if f.max > 0 {
		f.cur.Add(-1)
	}
}

// Wrap embrulha um handler de ingestão com o teto global.
func (f *InFlight) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !f.Enter() {
			// Retry-After é o que transforma a recusa em recuo do emissor em vez de
			// repetição imediata. Sem ele, 503 é só um 200 mais rápido.
			w.Header().Set("Retry-After", f.retryAfter)
			http.Error(w, "gateway sobrecarregado, retente", http.StatusServiceUnavailable)
			return
		}
		defer f.Leave()
		next.ServeHTTP(w, r)
	})
}

// SetRetryAfter ajusta o valor do header Retry-After (segundos).
func (f *InFlight) SetRetryAfter(seconds int) {
	if seconds < 1 {
		seconds = 1
	}
	f.retryAfter = strconv.Itoa(seconds)
}

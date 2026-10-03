package otlp

import (
	"sync"
	"time"

	"github.com/eduardorarruda/revoada/gateway/internal/metrics"
)

// A DECISÃO DE AMOSTRAGEM PRECISA SER DO TRACE, NÃO DO LOTE.
//
// O que estava errado (medido): sampleSpans decidia por REQUISIÇÃO. O `interesting`
// (erro ou span lento) era calculado só sobre os spans daquele lote; os spans do MESMO
// trace que chegassem noutra requisição eram julgados de novo, e sem o span interessante
// à vista caíam no bucket determinístico do trace_id. Prova com um trace partido em dois
// lotes: a raiz `GET /pedido` (50 ms, nada de interessante) chegou no lote 1 e foi
// DESCARTADA pelo bucket; o filho `db.insert` (900 ms) chegou no lote 2, foi considerado
// lento e MANTIDO. A lista de traces do painel devolveu
// `root_name=db.insert, spans=1, duration_ms=900`: promoveu um filho a raiz (o server usa
// argMin(name, ts)), perdeu a operação que o usuário de fato pediu e ainda subestimou a
// duração — tudo isso numa tela que promete "os problemas e os lentos você sempre vê".
//
// A correção tem duas metades:
//
//  1. DECISÃO PEGAJOSA por trace_id, numa janela curta. A primeira vez que um trace
//     aparece, decide-se; daí em diante todo lote do mesmo trace herda a decisão. Isso
//     resolve INTEIRO o caso mais comum (raiz interessante chega primeiro, filhos
//     depois): antes os filhos eram julgados sozinhos e sumiam.
//
//  2. MARCA DE PARCIAL quando a decisão MUDA para "manter" depois de já termos
//     descartado spans daquele trace. Aí não há como recuperar o que foi jogado fora, e
//     a única coisa honesta é dizer que aquele trace está incompleto — em vez de
//     apresentar um pedaço como se fosse o todo.
//
// Por que não bufferizar o trace inteiro antes de decidir: seria tail sampling de
// verdade, exige segurar spans por segundos (memória + atraso na tela) e é trabalho de
// um coletor OTel dedicado. A decisão pegajosa entrega a coerência que faltava sem
// segurar nada.

// SpanPartialLabel é o rótulo colocado nos spans de um trace que sabidamente perdeu
// spans antes de a decisão virar "manter".
//
// PARA QUEM LÊ (server/web): o valor é sempre "1" e só aparece quando o trace está
// incompleto. Como os spans vão para a coluna `labels` (Map(String,String)) da tabela
// `spans` no ClickHouse, dá para ler com `labels['revoada.trace_partial'] = '1'` —
// tipicamente com `max(labels['revoada.trace_partial'] = '1')` no GROUP BY trace_id da
// lista de traces. A tela deve marcar esse trace como "parcial": a raiz pode não ser a
// raiz de verdade e a duração total é um PISO.
const SpanPartialLabel = "revoada.trace_partial"

var (
	traceKept = metrics.NewCounter(
		"revoada_traces_sampled_kept_total",
		"Traces cuja decisão de amostragem foi MANTER (na janela de decisão).")
	traceDropped = metrics.NewCounter(
		"revoada_traces_sampled_dropped_total",
		"Traces cuja decisão de amostragem foi DESCARTAR (na janela de decisão).")
	traceUpgraded = metrics.NewCounter(
		"revoada_traces_sampled_upgraded_total",
		"Traces que passaram de DESCARTAR para MANTER ao chegar um span interessante em outro lote.")
	tracePartial = metrics.NewCounter(
		"revoada_traces_partial_total",
		"Traces marcados como PARCIAIS: viraram interessantes depois de já terem perdido spans.")
	traceDecisionsEvicted = metrics.NewCounter(
		"revoada_traces_decisions_evicted_total",
		"Decisões de amostragem despejadas por teto de memória (traces perdem a coerência entre lotes).")
)

// Dimensionamento da janela de decisão.
//
//   - traceDecisionTTL = 2 min. Um trace distribuído nasce e morre em segundos; 2 min
//     cobre com folga a chegada dos spans de todos os serviços dele (inclusive um
//     exportador com BatchSpanProcessor de 5 s e alguma fila).
//   - maxTraceDecisions = 200 000. Em regime, é o número de traces VIVOS na janela.
//     A entrada tem ~64 bytes, então o teto é da ordem de 13 MiB — e ele existe porque
//     o trace_id vem do cliente: sem teto, esta struct seria a superfície mais barata
//     de estourar a memória do gateway.
const (
	traceDecisionTTL  = 2 * time.Minute
	maxTraceDecisions = 200000
)

type traceDecision struct {
	keep       bool
	lostSpans  bool // já descartamos spans deste trace
	partial    bool // decisão virou "manter" DEPOIS de perder spans
	expiresAt  time.Time
	lastAccess time.Time
}

// traceDecider guarda a decisão de amostragem por trace_id dentro de uma janela curta.
type traceDecider struct {
	mu      sync.Mutex
	m       map[string]*traceDecision
	ttl     time.Duration
	maxKeys int
	now     func() time.Time
}

func newTraceDecider(ttl time.Duration, maxKeys int) *traceDecider {
	if ttl <= 0 {
		ttl = traceDecisionTTL
	}
	if maxKeys <= 0 {
		maxKeys = maxTraceDecisions
	}
	return &traceDecider{m: map[string]*traceDecision{}, ttl: ttl, maxKeys: maxKeys, now: time.Now}
}

// decide devolve (manter, parcial) para um trace_id.
//
// interesting: algum span DESTE lote tem erro ou passou do limiar de lento.
// fresh: a decisão a usar caso o trace ainda não seja conhecido (bucket determinístico).
func (d *traceDecider) decide(traceID string, interesting, fresh bool) (keep, partial bool) {
	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()

	e, ok := d.m[traceID]
	if ok && now.After(e.expiresAt) {
		// A janela venceu: o trace recomeça do zero (spans muito atrasados de um trace
		// antigo são raros, e lembrar de todos para sempre é o vazamento que o TTL evita).
		delete(d.m, traceID)
		ok = false
	}
	if !ok {
		d.evictLocked(now)
		keep = interesting || fresh
		e = &traceDecision{keep: keep, expiresAt: now.Add(d.ttl)}
		d.m[traceID] = e
		if keep {
			traceKept.Inc()
		} else {
			e.lostSpans = true
			traceDropped.Inc()
		}
		e.lastAccess = now
		return e.keep, e.partial
	}

	e.lastAccess = now
	switch {
	case e.keep:
		// Já era para manter: todo lote seguinte do mesmo trace entra junto. É esta
		// linha que impede o outro lado do mesmo defeito — a raiz interessante ficar e
		// os filhos, julgados sozinhos noutro lote, sumirem.
	case interesting:
		// Virou interessante depois de já termos descartado spans dele. Mantemos daqui
		// em diante, mas o trace está INCOMPLETO e precisa ser apresentado como tal.
		e.keep = true
		traceUpgraded.Inc()
		if e.lostSpans && !e.partial {
			e.partial = true
			tracePartial.Inc()
		}
	default:
		e.lostSpans = true
	}
	return e.keep, e.partial
}

// evictLocked mantém o número de decisões sob o teto: expira o que venceu e, se ainda
// estiver cheio, remove a decisão menos recentemente usada.
func (d *traceDecider) evictLocked(now time.Time) {
	if len(d.m) < d.maxKeys {
		return
	}
	var n int64
	for k, e := range d.m {
		if now.After(e.expiresAt) {
			delete(d.m, k)
			n++
		}
	}
	if len(d.m) >= d.maxKeys {
		var oldestKey string
		var oldest time.Time
		for k, e := range d.m {
			if oldestKey == "" || e.lastAccess.Before(oldest) {
				oldestKey, oldest = k, e.lastAccess
			}
		}
		if oldestKey != "" {
			delete(d.m, oldestKey)
			n++
		}
	}
	if n > 0 {
		traceDecisionsEvicted.Add(n)
	}
}

// len devolve o número de decisões vivas (testes).
func (d *traceDecider) len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.m)
}

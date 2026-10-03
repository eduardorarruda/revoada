// Package metrics é o registro ÚNICO de contadores/medidores expostos em /metrics.
//
// POR QUE existe: antes desta versão o /metrics do gateway só mostrava os 3 contadores
// do writer. Tudo que o gateway DESCARTAVA — ponto com relógio fora de faixa, lote
// perdido pelo batcher após os retries, telemetria apagada pelo DiscardOld do NATS —
// sumia sem deixar número. Descarte que não vira contador é perda silenciosa: o gráfico
// continua contínuo e errado, e ninguém tem como saber. Todo caminho de recusa deste
// serviço registra aqui.
//
// Formato de exposição Prometheus (text/plain 0.0.4), sem dependência externa: o
// gateway é um binário distroless e não vale arrastar o client_golang inteiro para
// meia dúzia de contadores.
package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Counter é um contador monotônico (acumulado desde o boot do processo).
type Counter struct{ v atomic.Int64 }

// Inc soma 1.
func (c *Counter) Inc() { c.v.Add(1) }

// Add soma n (n >= 0 por contrato de contador).
func (c *Counter) Add(n int64) { c.v.Add(n) }

// Value devolve o acumulado.
func (c *Counter) Value() int64 { return c.v.Load() }

// CounterVec é um contador com rótulos fixos (ex.: signal="metrics", reason="clock_future").
// As séries são criadas sob demanda; o conjunto de valores de rótulo é FECHADO no código
// (nunca vem do cliente), então não há risco de explosão de cardinalidade no /metrics.
type CounterVec struct {
	name, help string
	labels     []string
	mu         sync.RWMutex
	series     map[string]*labeledCounter
}

type labeledCounter struct {
	values []string
	c      Counter
}

// With devolve (criando se preciso) o contador da combinação de rótulos informada.
// A ordem dos valores segue a ordem dos nomes de rótulo passados em NewCounterVec.
func (v *CounterVec) With(values ...string) *Counter {
	k := strings.Join(values, "\x00")
	v.mu.RLock()
	lc, ok := v.series[k]
	v.mu.RUnlock()
	if ok {
		return &lc.c
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if lc, ok = v.series[k]; ok {
		return &lc.c
	}
	lc = &labeledCounter{values: append([]string(nil), values...)}
	v.series[k] = lc
	return &lc.c
}

// GaugeVec é um medidor com rótulos cujo valor de rótulo VEM DO CLIENTE (ex.: o
// hostname do agente). Por isso, e ao contrário do CounterVec, ele tem TETO de séries
// e expiração por tempo: sem isso o /metrics do gateway seria a superfície mais barata
// de estourar a memória do processo — basta um emissor inventar um hostname novo por
// requisição, exatamente a mesma falha que o balde por serverkey do rate limit tinha.
//
// A série mais antiga (menos recentemente escrita) é despejada ao encostar no teto, e
// o despejo é CONTADO em <name>_evicted_total: um medidor por host que some sem número
// vira "o host não reporta" na tela, que é a mentira que ele deveria justamente evitar.
type GaugeVec struct {
	name      string
	labels    []string
	maxSeries int
	ttl       time.Duration

	mu        sync.RWMutex
	series    map[string]*gaugeSeries
	lastSweep time.Time
	now       func() time.Time
	evicted   *Counter
}

type gaugeSeries struct {
	values  []string
	value   float64
	lastSet time.Time
}

// NewGaugeVec registra (ou recupera) um medidor rotulado com teto de séries.
// maxSeries <= 0 usa 2000; ttl <= 0 usa 1h.
func NewGaugeVec(name, help string, maxSeries int, ttl time.Duration, labelNames ...string) *GaugeVec {
	if maxSeries <= 0 {
		maxSeries = 2000
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	v := &GaugeVec{
		name: name, labels: labelNames, maxSeries: maxSeries, ttl: ttl,
		series: map[string]*gaugeSeries{}, now: time.Now,
	}
	col := &collector{name: name, help: help, typ: "gauge"}
	obj := register(name, col, v).(*GaugeVec)
	col.write = func(w io.Writer) { obj.write(w) }
	// O contador de despejo é registrado JUNTO: quem lê o medidor precisa saber, na
	// mesma raspagem, se ele está completo ou se o teto começou a cortar hosts.
	obj.evicted = NewCounter(name+"_evicted_total",
		"Séries despejadas de "+name+" por teto de cardinalidade ou expiração.")
	return obj
}

// Set grava o valor corrente da série identificada por labelValues.
func (v *GaugeVec) Set(value float64, labelValues ...string) {
	now := v.now()
	k := strings.Join(labelValues, "\x00")
	v.mu.Lock()
	defer v.mu.Unlock()
	if s, ok := v.series[k]; ok {
		s.value, s.lastSet = value, now
		return
	}
	v.sweepLocked(now)
	if len(v.series) >= v.maxSeries {
		v.evictOldestLocked()
	}
	v.series[k] = &gaugeSeries{values: append([]string(nil), labelValues...), value: value, lastSet: now}
}

// sweepLocked remove séries paradas há mais que o TTL (no máximo uma varredura por
// minuto: o caminho quente da ingestão não paga O(n) por requisição).
func (v *GaugeVec) sweepLocked(now time.Time) {
	if now.Sub(v.lastSweep) < time.Minute {
		return
	}
	v.lastSweep = now
	cutoff := now.Add(-v.ttl)
	for k, s := range v.series {
		if s.lastSet.Before(cutoff) {
			delete(v.series, k)
			v.evicted.Inc()
		}
	}
}

func (v *GaugeVec) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for k, s := range v.series {
		if oldestKey == "" || s.lastSet.Before(oldest) {
			oldestKey, oldest = k, s.lastSet
		}
	}
	if oldestKey != "" {
		delete(v.series, oldestKey)
		v.evicted.Inc()
	}
}

// Len devolve o número de séries vivas (testes/observabilidade).
func (v *GaugeVec) Len() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.series)
}

func (v *GaugeVec) write(w io.Writer) {
	// A VARREDURA TAMBÉM ACONTECE AQUI, e não só no Set.
	//
	// sweepLocked só era chamado por put(), isto é, quando uma série NOVA nascia. Numa
	// instalação estável — que é o caso normal, o mesmo punhado de hosts reportando —
	// nenhuma série nova nasce, e então nenhuma série velha morre: o TTL estava
	// documentado e nunca era cobrado. Medido em dev: um host cuja chave foi apagada
	// continuou saindo no /metrics por mais de 10 minutos com um TTL de 5.
	//
	// Onde o /metrics é raspado para dentro do ClickHouse, isso vaza para muito além
	// da memória do gateway: o rótulo `host` de um servidor APAGADO voltava a virar
	// linha a cada raspagem, para sempre, reaparecendo nas telas que listam hosts a
	// partir das métricas. (Conferido em 13/08/2026: essa raspagem existe no ambiente
	// de dev e NÃO em produção — ver o comentário de skew.go. A correção vale pelos
	// dois motivos mesmo assim: série morta ocupando memória já é motivo bastante.)
	// A raspagem é o único momento garantido de passar por aqui; o próprio sweepLocked
	// se limita a uma varredura por minuto.
	now := v.now()
	v.mu.Lock()
	v.sweepLocked(now)
	v.mu.Unlock()

	v.mu.RLock()
	lines := make([]string, 0, len(v.series))
	for _, s := range v.series {
		var b strings.Builder
		b.WriteString(v.name)
		b.WriteByte('{')
		for i, lv := range s.values {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(v.labels[i])
			b.WriteString(`="`)
			b.WriteString(escapeLabel(lv))
			b.WriteByte('"')
		}
		b.WriteString("} ")
		b.WriteString(formatFloat(s.value))
		lines = append(lines, b.String())
	}
	v.mu.RUnlock()
	sort.Strings(lines) // saída estável entre scrapes
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
}

// --- registro ---

type collector struct {
	name  string
	help  string
	typ   string // counter | gauge
	write func(w io.Writer)
}

var (
	regMu sync.Mutex
	reg   = map[string]*collector{}
	objs  = map[string]any{} // devolve o MESMO objeto em re-registro (idempotente)
)

// register é idempotente por nome: registrar duas vezes (ex.: dois Batchers criados
// no mesmo teste) devolve o objeto já existente em vez de duplicar a série.
func register(name string, c *collector, obj any) any {
	regMu.Lock()
	defer regMu.Unlock()
	if old, ok := objs[name]; ok {
		return old
	}
	reg[name] = c
	objs[name] = obj
	return obj
}

// NewCounter registra (ou recupera) um contador simples.
func NewCounter(name, help string) *Counter {
	c := &Counter{}
	col := &collector{name: name, help: help, typ: "counter"}
	obj := register(name, col, c).(*Counter)
	col.write = func(w io.Writer) {
		fmt.Fprintf(w, "%s %d\n", name, obj.Value())
	}
	return obj
}

// NewCounterVec registra (ou recupera) um contador com rótulos.
func NewCounterVec(name, help string, labelNames ...string) *CounterVec {
	v := &CounterVec{name: name, help: help, labels: labelNames, series: map[string]*labeledCounter{}}
	col := &collector{name: name, help: help, typ: "counter"}
	obj := register(name, col, v).(*CounterVec)
	col.write = func(w io.Writer) {
		obj.mu.RLock()
		lines := make([]string, 0, len(obj.series))
		for _, lc := range obj.series {
			var b strings.Builder
			b.WriteString(name)
			b.WriteByte('{')
			for i, lv := range lc.values {
				if i > 0 {
					b.WriteByte(',')
				}
				b.WriteString(obj.labels[i])
				b.WriteString(`="`)
				b.WriteString(escapeLabel(lv))
				b.WriteByte('"')
			}
			b.WriteString("} ")
			b.WriteString(strconv.FormatInt(lc.c.Value(), 10))
			lines = append(lines, b.String())
		}
		obj.mu.RUnlock()
		sort.Strings(lines) // saída estável entre scrapes
		for _, l := range lines {
			fmt.Fprintln(w, l)
		}
	}
	return obj
}

// NewGaugeFunc registra um medidor calculado na hora do scrape (ex.: profundidade
// da fila). fn deve ser barata e não bloquear.
func NewGaugeFunc(name, help string, fn func() float64) {
	col := &collector{name: name, help: help, typ: "gauge"}
	col.write = func(w io.Writer) { fmt.Fprintf(w, "%s %s\n", name, formatFloat(fn())) }
	register(name, col, col)
}

// NewCounterFunc é como NewGaugeFunc, mas declarado como counter (valor monotônico
// mantido por outro objeto — ex.: os contadores internos do writer/batcher).
func NewCounterFunc(name, help string, fn func() float64) {
	col := &collector{name: name, help: help, typ: "counter"}
	col.write = func(w io.Writer) { fmt.Fprintf(w, "%s %s\n", name, formatFloat(fn())) }
	register(name, col, col)
}

func formatFloat(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

// escapadorDeRotulo é criado UMA vez. Ele era construído a cada valor de rótulo,
// dentro do laço que monta as linhas — e esse laço roda com o lock de leitura tomado,
// segurando os Set() da ingestão. Medido neste pacote: 15,7 µs por chamada montando o
// Replacer contra 150 ns reaproveitando-o, 105× de diferença; no teto de séries isso
// era ~26 ms por raspagem em que nada mais avança.
var escapadorDeRotulo = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escapeLabel(s string) string { return escapadorDeRotulo.Replace(s) }

// Write renderiza todos os coletores registrados, em ordem de nome (saída estável).
func Write(w io.Writer) {
	regMu.Lock()
	names := make([]string, 0, len(reg))
	for n := range reg {
		names = append(names, n)
	}
	cols := make([]*collector, 0, len(names))
	sort.Strings(names)
	for _, n := range names {
		cols = append(cols, reg[n])
	}
	regMu.Unlock()

	for _, c := range cols {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", c.name, c.help, c.name, c.typ)
		c.write(w)
	}
}

// Handler expõe o registro em /metrics.
func Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		Write(w)
	}
}

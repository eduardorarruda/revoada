package logtail

import (
	"fmt"
	"sync"
	"time"
)

// rateLimiter é um token bucket (linhas/segundo) COMPARTILHADO por todos os coletores
// de log do processo. É a salvaguarda de não-sobrecarga do caminho de logs: o que passa
// do limite é DESCARTADO (logs são best-effort e não têm buffer em disco como as
// métricas) e contado, com um aviso periódico injetado no próprio stream para o operador
// enxergar a perda em vez de sofrer em silêncio. Um limitador único é o correto aqui: a
// garantia é sobre a carga TOTAL que o agente impõe ao host, somando todas as fontes.
type rateLimiter struct {
	mu      sync.Mutex
	perSec  float64
	tokens  float64
	max     float64 // teto do balde: perSec × janelaDeRajada (ver abaixo)
	last    time.Time
	dropped int
	lastMsg time.Time
	now     func() time.Time // injetável para teste
}

// janelaDeRajada é quantos segundos de cota o balde acumula. Tem de cobrir o ciclo
// MAIS LONGO entre dois envios de um coletor — o do Tailer de arquivos, que lê e
// manda tudo de uma vez a cada cicloDeLeitura (10 s).
//
// Era 2 s, e isso fazia a taxa prometida ser mentira para arquivos: o balde enchia
// até 2 s de cota e parava, mas o lote chegava com 10 s de linhas. Medido ao vivo
// (validação de 02/10/2026): uma aplicação escrevendo 1500 linhas/s — 30% do teto
// de 5000 — perdeu 33% das linhas (44.996 escritas, 30.016 no painel), com o aviso
// dizendo "limite 5000 linhas/s". A taxa efetiva para arquivos era 1000 linhas/s e
// 200 KiB/s, um quinto do que a configuração declarava.
//
// Com o balde do tamanho do ciclo, a MÉDIA continua limitada a perSec (a cota só
// recompõe nessa velocidade); o que muda é que um lote de 10 s dentro da taxa passa
// inteiro.
const janelaDeRajada = cicloDeLeitura

// novoBalde devolve (cota inicial, teto) de um token bucket de perSec por segundo.
// O teto é a janelaDeRajada inteira; a cota inicial é UM segundo — a recomposição
// conta a partir da criação (last = agora), então o primeiro lote, que só sai um
// ciclo depois do boot, já encontra o ciclo inteiro de cota. Antes `last` ficava
// zerado até o primeiro admit e o primeiro lote do processo só tinha 1 s de cota:
// uma rajada de 20 mil linhas logo após o boot chegava com 5 mil.
func novoBalde(perSec float64) (tokens, max float64) {
	return perSec, perSec * janelaDeRajada.Seconds()
}

// sharedLimiter é o limitador do processo. nil => sem limite. Definido no boot por
// SetLogRateLimit, antes de qualquer coletor começar.
var sharedLimiter *rateLimiter

// SetLogRateLimit configura o limite global de linhas/s de log (<=0 desliga). Chamado
// uma vez no boot (main.go), antes de subir os coletores.
func SetLogRateLimit(perSec int) {
	sharedLimiter = newRateLimiter(perSec, time.Now)
}

func newRateLimiter(perSec int, now func() time.Time) *rateLimiter {
	if perSec <= 0 {
		return nil // ilimitado
	}
	tokens, max := novoBalde(float64(perSec))
	return &rateLimiter{
		perSec: float64(perSec),
		tokens: tokens,
		max:    max,
		last:   now(),
		now:    now,
	}
}

// admit devolve quantas das n linhas podem ser enviadas agora (0..n), consumindo
// tokens e acumulando o descarte do excedente. Refil proporcional ao tempo decorrido.
func (rl *rateLimiter) admit(n int) int {
	if rl == nil || n <= 0 {
		return n
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	t := rl.now()
	if rl.last.IsZero() {
		rl.last = t
	}
	rl.tokens += rl.perSec * t.Sub(rl.last).Seconds()
	rl.last = t
	if rl.tokens > rl.max {
		rl.tokens = rl.max
	}
	k := n
	if float64(k) > rl.tokens {
		k = int(rl.tokens)
	}
	rl.tokens -= float64(k)
	rl.dropped += n - k
	return k
}

// notice devolve um aviso de descarte (no máximo a cada 30s) ou nil. Reseta o contador.
func (rl *rateLimiter) notice() *record {
	if rl == nil {
		return nil
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if rl.dropped == 0 {
		return nil
	}
	t := rl.now()
	if !rl.lastMsg.IsZero() && t.Sub(rl.lastMsg) < 30*time.Second {
		return nil
	}
	n := rl.dropped
	rl.dropped = 0
	rl.lastMsg = t
	return &record{
		service:  "revoada-agent",
		severity: "WARN",
		body:     fmt.Sprintf("rate limit de logs: %d linha(s) descartada(s) para proteger o host (limite %.0f linhas/s, ajuste log_max_lines_per_sec)", n, rl.perSec),
		labels:   map[string]string{"source": "revoada-agent"},
	}
}

// ─── teto de BYTES/s ─────────────────────────────────────────────────────────
//
// POR QUE existe um segundo balde: o teto acima conta LINHAS, e a garantia que o
// agente sempre declarou é de VOLUME ("~1 MB/s a 200 B/linha"). As duas coisas só
// coincidem enquanto a linha for pequena. Uma aplicação que loga JSON estruturado de
// 4 KB por linha — Spring Boot, Rails com lograge, qualquer coisa que serializa stack
// trace — cabe FOLGADA dentro de 5000 linhas/s e manda 20 MB/s: 1,7 TB por dia saindo
// do link do servidor do cliente, com o agente convencido de estar dentro do limite.
//
// Byte é a unidade que o link, a franquia do cliente e o disco do gateway cobram;
// linha não é unidade de nada. Os dois valem ao mesmo tempo e o que estourar primeiro
// corta: o de linhas protege o CAMINHO (parse, número de registros no ClickHouse), o
// de bytes protege o LINK e o DISCO.

// byteLimiter é o token bucket de bytes/s, compartilhado por todos os coletores pelo
// mesmo motivo do de linhas: a garantia é sobre a carga TOTAL do agente no host.
type byteLimiter struct {
	mu      sync.Mutex
	perSec  float64
	tokens  float64
	max     float64 // teto do balde: perSec × janelaDeRajada
	last    time.Time
	dropped int   // registros descartados
	bytes   int64 // bytes descartados (é o número que interessa ao operador)
	lastMsg time.Time
	now     func() time.Time
}

// sharedByteLimiter é o limitador de volume do processo. nil => sem limite.
var sharedByteLimiter *byteLimiter

// SetLogByteRateLimit configura o teto global de bytes/s (<=0 desliga). Chamado uma
// vez no boot (main.go), antes de subir os coletores, ao lado de SetLogRateLimit.
func SetLogByteRateLimit(perSec int) {
	sharedByteLimiter = newByteLimiter(perSec, time.Now)
}

func newByteLimiter(perSec int, now func() time.Time) *byteLimiter {
	if perSec <= 0 {
		return nil // ilimitado
	}
	tokens, max := novoBalde(float64(perSec))
	return &byteLimiter{
		perSec: float64(perSec),
		tokens: tokens,
		max:    max,
		last:   now(),
		now:    now,
	}
}

// tamanhoRegistro estima o custo de rede de um registro. É o corpo mais o serviço e
// os labels — o que de fato viaja no NDJSON. Não precisa ser exato: precisa ser
// proporcional, para que 4 KB por linha pesem 20x mais que 200 B por linha, que é
// exatamente a diferença que o teto de linhas não enxergava.
func tamanhoRegistro(r record) int {
	n := len(r.body) + len(r.service) + len(r.severity)
	for k, v := range r.labels {
		n += len(k) + len(v)
	}
	return n
}

// admit devolve quantos dos registros cabem no orçamento de bytes agora, sempre um
// PREFIXO da fatia (a ordem cronológica do log é o que o operador lê; furar o meio
// para caber uma linha curta produziria um log que não conta a história na ordem).
func (bl *byteLimiter) admit(recs []record) int {
	if bl == nil || len(recs) == 0 {
		return len(recs)
	}
	bl.mu.Lock()
	defer bl.mu.Unlock()
	t := bl.now()
	if bl.last.IsZero() {
		bl.last = t
	}
	bl.tokens += bl.perSec * t.Sub(bl.last).Seconds()
	bl.last = t
	if bl.tokens > bl.max {
		bl.tokens = bl.max
	}

	k := 0
	for _, r := range recs {
		custo := float64(tamanhoRegistro(r))
		if custo > bl.tokens {
			// Escape para não travar para sempre: um registro maior que o BALDE CHEIO
			// nunca caberia por mais que se espere, e o coletor ficaria parado nele até
			// o fim dos tempos — a linha nem sai nem é descartada, o pior dos dois
			// mundos. Nesse caso deixa passar e fica devendo; o débito é cobrado do
			// refil dos próximos segundos, então a média continua respeitando o limite.
			// (Um registro que cabe no balde cheio, mas não agora, simplesmente espera.)
			if k == 0 && custo > bl.max {
				bl.tokens -= custo
				k = 1
			}
			break
		}
		bl.tokens -= custo
		k++
	}
	for _, r := range recs[k:] {
		bl.bytes += int64(tamanhoRegistro(r))
	}
	bl.dropped += len(recs) - k
	return k
}

// notice devolve um aviso de descarte por volume (no máximo a cada 30s) ou nil.
// Separado do aviso de linhas de propósito: "descartei porque você mandou linhas
// demais" e "descartei porque você mandou MB demais" pedem ajustes em chaves
// diferentes, e um aviso genérico faria o operador mexer na errada.
func (bl *byteLimiter) notice() *record {
	if bl == nil {
		return nil
	}
	bl.mu.Lock()
	defer bl.mu.Unlock()
	if bl.dropped == 0 {
		return nil
	}
	t := bl.now()
	if !bl.lastMsg.IsZero() && t.Sub(bl.lastMsg) < 30*time.Second {
		return nil
	}
	n, kb := bl.dropped, bl.bytes/1024
	bl.dropped, bl.bytes = 0, 0
	bl.lastMsg = t
	return &record{
		service:  "revoada-agent",
		severity: "WARN",
		body: fmt.Sprintf("limite de VOLUME de logs: %d linha(s) (~%d KiB) descartada(s) para proteger o link do host (limite %.0f bytes/s, ajuste log_max_bytes_per_sec)",
			n, kb, bl.perSec),
		labels: map[string]string{"source": "revoada-agent"},
	}
}

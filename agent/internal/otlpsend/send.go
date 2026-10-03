// Package otlpsend monta e envia métricas OTLP ao gateway, com WAL em disco.
package otlpsend

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eduardorarruda/revoada/agent/internal/buffer"
	"github.com/eduardorarruda/revoada/agent/internal/collect"
	"google.golang.org/protobuf/proto"

	cpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	cmnpb "go.opentelemetry.io/proto/otlp/common/v1"
	mpb "go.opentelemetry.io/proto/otlp/metrics/v1"
	rpb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// Salvaguardas de retorno de queda ("estouro da boiada").
//
// O drain antigo reenviava o WAL INTEIRO de uma vez, em série, no mesmo goroutine
// do ticker, sem teto, sem pausa e sem jitter — medido: 32 POSTs em 18 ms assim que
// o gateway voltou. Com 5000 lotes (o teto do WAL, ≈55 MB) e 100 agentes voltando
// juntos, o gateway leva a frota inteira na cara no segundo em que se reergue, e
// derruba de novo. A fila não é urgente: ela já esperou minutos ou horas; drenar em
// alguns ciclos e chegar inteira é melhor que chegar toda junta e ser recusada.
const (
	// maxLotesPorDrain é quantos lotes atrasados saem por ciclo. A 15s de tick isso
	// é ~40 lotes/min: uma fila de 1h (240 lotes) se esvazia em ~6 min, sem pico.
	maxLotesPorDrain = 10
	// pausaEntreLotes é o respiro entre POSTs do mesmo ciclo, com jitter aplicado
	// em cima. Espalha os POSTs de um agente E, somado ao jitter, desalinha agentes
	// que voltaram no mesmo segundo.
	pausaEntreLotes = 150 * time.Millisecond
	// recuoPadrao é quanto o agente espera ao levar 429/503 SEM Retry-After. Sem
	// isto o agente reenviava o buffer inteiro no tick seguinte, ignorando o "pare"
	// que o gateway acabou de dar.
	recuoPadrao = 30 * time.Second
	// recuoMaximo evita que um Retry-After absurdo (ou um proxy confuso) deixe o
	// agente mudo por horas. Passando disto, o agente volta a tentar — bufferizando
	// enquanto isso, que é o comportamento seguro.
	recuoMaximo = 10 * time.Minute
)

type Sender struct {
	url      string
	key      string
	hostname string
	extra    map[string]string // labels extras (ex: probe location)
	http     *http.Client
	buf      *buffer.Buffer

	// Recuo pedido pelo gateway (429/503 + Retry-After). Protegido por mutex
	// porque Ping e Send podem sair de goroutines diferentes (doctor/loop).
	mu       sync.Mutex
	recuoAte time.Time

	// Injetáveis para teste: sem isto, provar o teto por ciclo e o recuo exigiria
	// esperar segundos de relógio de parede em cada caso.
	agora  func() time.Time
	dormir func(ctx context.Context, d time.Duration)
	// chaveRecusada guarda se o gateway respondeu 401/403 no último envio. Não é
	// contagem nem histórico: é o estado atual da credencial, lido pelo laço de coleta
	// para acordar a consulta ao painel (ver selfupdate.ConsultarAgora). Uma chave
	// recusada pode significar que existe uma ORDEM DE DESINSTALAÇÃO esperando por
	// este agente, e descobrir isso só na próxima consulta horária deixa um processo
	// órfão martelando o gateway por até uma hora.
	chaveRecusada atomic.Bool
	// pausa é o intervalo-base entre lotes do mesmo drain (0 desliga a pausa).
	pausa time.Duration
	// maxLotes é o teto de lotes por drain (<=0 usa maxLotesPorDrain).
	maxLotes int
}

func New(gatewayURL, key, hostname string, extra map[string]string, buf *buffer.Buffer) *Sender {
	return &Sender{
		url:      strings.TrimRight(gatewayURL, "/") + "/v1/metrics",
		key:      key,
		hostname: hostname,
		extra:    extra,
		http:     &http.Client{Timeout: 10 * time.Second},
		buf:      buf,
		agora:    time.Now,
		dormir:   dormirCtx,
		pausa:    pausaEntreLotes,
		maxLotes: maxLotesPorDrain,
	}
}

// dormirCtx espera d, mas acorda na hora se o contexto morrer — um SIGTERM durante
// a drenagem não pode ficar preso esperando a pausa acabar.
func dormirCtx(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// jitterPausa sorteia entre 50% e 150% da pausa-base. Uma pausa fixa faria a frota
// que voltou junta continuar batendo em uníssono, só que mais devagar — o pico
// continuaria existindo, apenas deslocado. crypto/rand porque math/rand sem semente
// sorteia a MESMA sequência em todo processo, que é exatamente a boiada de novo.
func jitterPausa(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	meio := int64(base / 2)
	if meio <= 0 {
		return base
	}
	n, err := rand.Int(rand.Reader, big.NewInt(meio*2))
	if err != nil {
		return base
	}
	return time.Duration(meio + n.Int64())
}

func kv(k, v string) *cmnpb.KeyValue {
	return &cmnpb.KeyValue{Key: k, Value: &cmnpb.AnyValue{Value: &cmnpb.AnyValue_StringValue{StringValue: v}}}
}

// build monta o ExportMetricsServiceRequest a partir dos pontos coletados.
func (s *Sender) build(points []collect.Point, info collect.HostInfo) *cpb.ExportMetricsServiceRequest {
	nowNs := uint64(time.Now().UnixNano())
	attrs := []*cmnpb.KeyValue{
		kv("host", s.hostname),
		kv("host.name", s.hostname),
	}
	if info.OS != "" {
		attrs = append(attrs, kv("os", info.OS), kv("platform", info.Platform), kv("kernel", info.Kernel), kv("arch", info.Arch))
	}
	// Campos ESTÁTICOS do host (viram labels de resource → não incluir nada dinâmico
	// como uptime, que explodiria a cardinalidade das métricas).
	attrs = append(attrs, kv("host.cpu.cores", strconv.Itoa(runtime.NumCPU())))
	for k, v := range s.extra {
		attrs = append(attrs, kv(k, v))
	}

	metrics := make([]*mpb.Metric, 0, len(points))
	for _, p := range points {
		ts := nowNs
		if p.At > 0 {
			ts = uint64(p.At) // hora da leitura (collect.Carimbar)
		}
		dp := &mpb.NumberDataPoint{
			TimeUnixNano: ts,
			Value:        &mpb.NumberDataPoint_AsDouble{AsDouble: p.Value},
		}
		// Atributos por datapoint (ex.: disco por montagem → mount=/var). O gateway
		// mescla dp.Attributes no Map `labels` do ClickHouse (convert.go:37).
		if len(p.Labels) > 0 {
			dp.Attributes = make([]*cmnpb.KeyValue, 0, len(p.Labels))
			for k, v := range p.Labels {
				dp.Attributes = append(dp.Attributes, kv(k, v))
			}
		}
		m := &mpb.Metric{Name: p.Name}
		if p.Counter {
			m.Data = &mpb.Metric_Sum{Sum: &mpb.Sum{
				DataPoints:             []*mpb.NumberDataPoint{dp},
				IsMonotonic:            true,
				AggregationTemporality: mpb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
			}}
		} else {
			m.Data = &mpb.Metric_Gauge{Gauge: &mpb.Gauge{DataPoints: []*mpb.NumberDataPoint{dp}}}
		}
		metrics = append(metrics, m)
	}

	return &cpb.ExportMetricsServiceRequest{
		ResourceMetrics: []*mpb.ResourceMetrics{{
			Resource:     &rpb.Resource{Attributes: attrs},
			ScopeMetrics: []*mpb.ScopeMetrics{{Metrics: metrics}},
		}},
	}
}

// Send envia um snapshot; drena o buffer primeiro. Em falha, enfileira no disco.
func (s *Sender) Send(ctx context.Context, points []collect.Point, info collect.HostInfo) error {
	data, err := proto.Marshal(s.build(points, info))
	if err != nil {
		return err
	}
	// Recuo pedido pelo gateway: nem o drain nem o ponto do ciclo saem enquanto a
	// janela não vence. O ponto vai para o WAL como se a rede tivesse falhado —
	// não se perde nada, só não se insiste contra quem acabou de dizer "pare".
	if espera, recuando := s.recuando(); recuando {
		_ = s.buf.Put(s.agora().UnixNano(), data)
		return fmt.Errorf("gateway pediu recuo; aguardando %s antes de tentar de novo (lote bufferizado)", espera.Round(time.Second))
	}
	s.drain(ctx) // tenta reenviar o que ficou pendente, com teto por ciclo
	// O drain pode ter levado o 429 agora mesmo. Insistir com o ponto do ciclo
	// logo depois seria ignorar o "pare" no mesmo segundo em que ele chegou.
	if espera, recuando := s.recuando(); recuando {
		_ = s.buf.Put(s.agora().UnixNano(), data)
		return fmt.Errorf("gateway pediu recuo; aguardando %s antes de tentar de novo (lote bufferizado)", espera.Round(time.Second))
	}
	if err := s.post(ctx, data); err != nil {
		_ = s.buf.Put(s.agora().UnixNano(), data)
		return err
	}
	return nil
}

// drain reenvia lotes bufferizados (mais antigos primeiro) até um falhar, ATÉ o
// teto de lotes do ciclo, com uma pausa jitterizada entre eles.
//
// O teto é a correção principal: sem ele, o retorno de uma queda virava 32 POSTs em
// 18 ms de UM agente (medido) — vezes a frota, um pico que derruba o gateway logo
// depois de ele voltar. Com teto e pausa, a fila leva alguns ciclos a mais para
// escoar e ninguém percebe, porque ela já estava atrasada de qualquer jeito.
func (s *Sender) drain(ctx context.Context) {
	files, err := s.buf.List()
	if err != nil {
		return
	}
	teto := s.maxLotes
	if teto <= 0 {
		teto = maxLotesPorDrain
	}
	if len(files) > teto {
		files = files[:teto]
	}
	for i, f := range files {
		if ctx.Err() != nil {
			return // parada limpa: o que sobrou continua no WAL
		}
		if i > 0 {
			s.dormir(ctx, jitterPausa(s.pausa))
		}
		data, err := s.buf.Read(f)
		if err != nil {
			_ = s.buf.Remove(f)
			continue
		}
		if err := s.post(ctx, data); err != nil {
			return // gateway ainda fora (ou pediu recuo): para e tenta no próximo ciclo
		}
		_ = s.buf.Remove(f)
	}
}

// recuando diz se ainda estamos dentro da janela de recuo pedida pelo gateway.
func (s *Sender) recuando() (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recuoAte.IsZero() {
		return 0, false
	}
	falta := s.recuoAte.Sub(s.agora())
	if falta <= 0 {
		s.recuoAte = time.Time{}
		return 0, false
	}
	return falta, true
}

// marcarRecuo agenda o recuo. Só ESTENDE: dois 429 seguidos no mesmo ciclo não
// podem encurtar a janela que o primeiro pediu.
func (s *Sender) marcarRecuo(d time.Duration) {
	if d <= 0 {
		d = recuoPadrao
	}
	if d > recuoMaximo {
		d = recuoMaximo
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ate := s.agora().Add(d); ate.After(s.recuoAte) {
		s.recuoAte = ate
	}
}

func (s *Sender) post(ctx context.Context, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("X-Revoada-Key", s.key)
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 429/503 não são "erro genérico": são o gateway dizendo explicitamente
		// "pare, e volte em X". Tratá-los como qualquer outra falha (o que o código
		// fazia) significava reenviar o buffer INTEIRO no tick seguinte, contra um
		// gateway que acabou de avisar que não aguenta — o agente vira o ataque.
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			d := retryAfter(resp.Header.Get("Retry-After"), s.agora())
			s.marcarRecuo(d)
			return fmt.Errorf("gateway status %d (recuando %s)", resp.StatusCode, s.recuoRestante().Round(time.Second))
		}
		// 401/403: a credencial não vale mais. Marca para o laço de coleta acordar a
		// consulta ao painel — é lá que uma eventual ordem de desinstalação espera.
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			s.chaveRecusada.Store(true)
		}
		return fmt.Errorf("gateway status %d", resp.StatusCode)
	}
	s.chaveRecusada.Store(false)
	return nil
}

// recuoRestante é só para a mensagem de erro.
func (s *Sender) recuoRestante() time.Duration {
	d, _ := s.recuando()
	return d
}

// retryAfter interpreta o cabeçalho nos DOIS formatos do RFC 9110: segundos
// ("120") e data HTTP ("Wed, 21 Oct 2015 07:28:00 GMT"). Valor ausente ou
// ilegível => 0, e o chamador aplica recuoPadrao.
func retryAfter(v string, agora time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if seg, err := strconv.Atoi(v); err == nil {
		if seg <= 0 {
			return 0
		}
		return time.Duration(seg) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(agora); d > 0 {
			return d
		}
	}
	return 0
}

// Ping testa a conectividade/credencial enviando um lote vazio (para o doctor).
func (s *Sender) Ping(ctx context.Context) error {
	data, _ := proto.Marshal(s.build(nil, collect.HostInfo{}))
	return s.post(ctx, data)
}

// ChaveRecusada diz se o gateway rejeitou a credencial no último envio (401/403).
// Volta a false assim que um envio é aceito.
func (s *Sender) ChaveRecusada() bool { return s.chaveRecusada.Load() }

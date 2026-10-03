// Package queue encapsula o NATS JetStream: stream de ingestão de métricas,
// publisher (usado pelo receiver) e consumer (usado pelo writer).
package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eduardorarruda/revoada/gateway/internal/config"
	"github.com/eduardorarruda/revoada/gateway/internal/metrics"
	"github.com/eduardorarruda/revoada/gateway/internal/model"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	StreamName     = "INGEST"
	SubjectMetrics = "ingest.metrics"

	// natsPayloadMargin deixa folga para overhead de protocolo/headers abaixo do
	// max_payload negociado com o servidor, ao fragmentar mensagens.
	natsPayloadMargin = 16 << 10 // 16 KiB
)

// Contadores da fila expostos em /metrics.
//
// POR QUE: o stream é configurado com Discard: DiscardOld e MaxBytes de 2 GiB — ao
// encher, o NATS apaga a telemetria MAIS ANTIGA para continuar aceitando publicação.
// É a decisão certa (falhar a publicação derrubaria a ingestão), mas o descarte não era
// contado nem exposto em lugar nenhum: o gráfico continuava contínuo e ERRADO, sem
// buraco visível, exatamente onde o operador mais precisa confiar no dado (a volta de
// uma queda longa, que é quando o stream enche).
//
// revoada_nats_msgs_dropped_estimated_total é uma ESTIMATIVA, e o nome diz isso: o
// JetStream não publica um contador de descarte por DiscardOld. Ela sai da diferença
// entre as mensagens que SAÍRAM do stream (avanço de first_seq desde o boot) e as que
// o writer confirmou com ack. Mensagem que saiu sem ack foi apagada — por DiscardOld
// ou por MaxAge (24 h), e ambos são perda de telemetria.
var (
	msgsAcked = metrics.NewCounter(
		"revoada_nats_msgs_acked_total",
		"Mensagens do stream de ingestão confirmadas (ack) pelo writer.")
	oversizeDropped = metrics.NewCounter(
		"revoada_nats_publish_oversize_dropped_total",
		"Métricas isoladas descartadas por estourarem o max_payload do NATS.")

	// msgsRedelivered é o contador que faltava quando a GRAVAÇÃO EM DOBRO aconteceu.
	//
	// POR QUE: o consumer subia sem AckWait, ficando no default de 30 s do JetStream,
	// enquanto o cliente HTTP do ClickHouse espera até 60 s por um INSERT. Medido em
	// dev: `docker pause` no ClickHouse, um lote de 5 pontos distintos, HTTP 200 para o
	// agente, despausa → count()=10 e uniqExact(idx)=5 na tabela `metrics`, e cnt=2 no
	// rollup metrics_1m. O INSERT levou entre 30 s e 60 s, o JetStream considerou o ack
	// vencido e REENTREGOU o mesmo lote — que foi inserido de novo. E o pior: nada
	// contava isso. revoada_writer_errors_total era 0 (nenhum insert falhou) e
	// num_redelivered do consumer era 0 (a reentrega já tinha sido confirmada). Um
	// gráfico com o dobro do valor real e ZERO sinal em /metrics.
	//
	// Este contador é lido do NumDelivered da PRÓPRIA mensagem no momento em que ela
	// chega ao writer: qualquer mensagem entregue mais de uma vez aparece aqui, mesmo
	// que o ciclo termine bem. Deixou de ser invisível.
	msgsRedelivered = metrics.NewCounter(
		"revoada_nats_msgs_redelivered_total",
		"Mensagens do stream entregues ao writer mais de uma vez (risco de gravação em dobro).")
	// publishDuplicates conta lotes que o JetStream RECONHECEU como repetidos e não
	// gravou (dedup por Nats-Msg-Id). É o número que prova que o reenvio do agente
	// depois de um 503 não virou linha em dobro.
	publishDuplicates = metrics.NewCounter(
		"revoada_nats_publish_duplicates_total",
		"Lotes recusados como duplicata pela janela de dedup do JetStream (Nats-Msg-Id).")

	// activeQueue é a Queue viva observada pelos medidores do /metrics. Os medidores
	// são registrados UMA vez (init) e leem daqui, e não de um `q` capturado: registrar
	// dentro de Connect prenderia o /metrics à primeira Queue criada, e o registro é
	// idempotente por nome — a segunda Connect (teste, reconexão) ficaria invisível.
	activeQueue atomic.Pointer[Queue]
)

func init() {
	gauge := func(name, help string, fn func(*Queue) float64) {
		metrics.NewGaugeFunc(name, help, func() float64 {
			q := activeQueue.Load()
			if q == nil {
				return 0
			}
			return fn(q)
		})
	}
	gauge("revoada_nats_stream_msgs", "Mensagens vivas no stream de ingestão.",
		func(q *Queue) float64 { return float64(q.stMsgs.Load()) })
	gauge("revoada_nats_stream_bytes", "Bytes ocupados pelo stream de ingestão.",
		func(q *Queue) float64 { return float64(q.stBytes.Load()) })
	gauge("revoada_nats_stream_max_bytes", "Teto de bytes do stream (MaxBytes).",
		func(q *Queue) float64 { return float64(q.maxBytes) })
	gauge("revoada_nats_stream_info_ok", "1 quando a última leitura do estado do stream teve sucesso.",
		func(q *Queue) float64 {
			if q.stOK.Load() {
				return 1
			}
			return 0
		})
	metrics.NewCounterFunc("revoada_nats_msgs_removed_total",
		"Mensagens que SAÍRAM do stream desde o boot (avanço de first_seq).",
		func() float64 {
			q := activeQueue.Load()
			if q == nil {
				return 0
			}
			return float64(q.stRemoved.Load())
		})
	metrics.NewCounterFunc("revoada_nats_msgs_dropped_estimated_total",
		"ESTIMATIVA de mensagens perdidas sem ack (DiscardOld ao encher, ou MaxAge).",
		func() float64 {
			q := activeQueue.Load()
			if q == nil {
				return 0
			}
			d := q.stRemoved.Load() - msgsAcked.Value()
			if d < 0 {
				d = 0 // ack de mensagem removida antes do boot: não inventa perda
			}
			return float64(d)
		})
}

// streamWatchEvery é o intervalo de amostragem do estado do stream. O /metrics não
// pode fazer uma chamada de rede a cada scrape (um scrape lento viraria um timeout no
// Prometheus e, pior, na página de saúde), então o estado é amostrado aqui e lido de
// variáveis atômicas.
const streamWatchEvery = 15 * time.Second

// watchStream amostra o estado do stream até o ctx ser cancelado. É o que dá NÚMERO ao
// descarte por DiscardOld: sem ele, a telemetria apagada pelo NATS ao encher some sem
// deixar rastro e o gráfico continua contínuo e errado.
func (q *Queue) watchStream(ctx context.Context) {
	q.sampleStream(ctx)
	t := time.NewTicker(streamWatchEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			q.sampleStream(ctx)
		}
	}
}

func (q *Queue) sampleStream(ctx context.Context) {
	st, err := q.js.Stream(ctx, StreamName)
	if err != nil {
		q.stOK.Store(false)
		return
	}
	info, err := st.Info(ctx)
	if err != nil || info == nil {
		q.stOK.Store(false)
		return
	}
	q.stMsgs.Store(int64(info.State.Msgs))
	q.stBytes.Store(int64(info.State.Bytes))
	first := info.State.FirstSeq
	q.stFirstSeq.Store(first)
	// A base é o first_seq do PRIMEIRO ciclo: o stream pode já ter história de antes
	// deste processo, e contar essa história como "removida agora" inventaria uma
	// perda gigante no boot.
	q.stBaseSeq.CompareAndSwap(0, first)
	if base := q.stBaseSeq.Load(); first >= base {
		q.stRemoved.Store(int64(first - base))
	}
	q.stOK.Store(true)
}

// Queue é a conexão com o NATS + JetStream.
type Queue struct {
	nc       *nats.Conn
	js       jetstream.JetStream
	log      *slog.Logger
	msgLimit int // teto (bytes serializados) de UMA mensagem publicada
	maxBytes int64

	// Estado do stream amostrado periodicamente por WatchStream (o /metrics não pode
	// fazer uma chamada de rede a cada scrape, nem o caminho de contrapressão).
	stMsgs     atomic.Int64
	stBytes    atomic.Int64
	stFirstSeq atomic.Uint64
	stBaseSeq  atomic.Uint64 // first_seq observado no boot (base do "removidas")
	stRemoved  atomic.Int64
	stOK       atomic.Bool
}

// connOptions são as opções da conexão com o NATS. Extraídas em função para que o
// teste consiga AFIRMAR o que elas valem — em particular o ReconnectBufSize, que é a
// diferença entre um 503 honesto e um 503 que mente (ver abaixo).
func connOptions() []nats.Option {
	return []nats.Option{
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
		// ReconnectBufSize(-1) DESLIGA o buffer de reconexão.
		//
		// POR QUE: com o buffer default de 8 MiB, publicar com o NATS fora não falhava
		// de verdade — a mensagem ia para o buffer e era ENTREGUE ao reconectar, mas o
		// PubAck nunca chegava a tempo e o gateway respondia 503. Medido em dev:
		// `docker stop revoada-nats` → POST /v1/metrics → 503 para o agente →
		// `docker start` → o lote "que falhou" ENTROU (count=1); o agente, obedecendo o
		// 503, reenviou o mesmo lote → count=2. O gateway dizia "não entrou" para um
		// lote que ia entrar, e a linha nascia duplicada por obediência do agente.
		//
		// Com o buffer desligado, publicar durante a reconexão devolve
		// ErrReconnectBufExceeded na hora: 503 volta a significar "não entrou".
		nats.ReconnectBufSize(-1),
	}
}

// Connect abre a conexão e garante o stream.
func Connect(ctx context.Context, url string) (*Queue, error) {
	nc, err := nats.Connect(url, connOptions()...)
	if err != nil {
		return nil, err
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, err
	}
	// MaxBytes limita o disco ocupado pelo stream; DiscardOld faz o stream perder
	// as mensagens MAIS ANTIGAS ao encher, em vez de FALHAR a publicação (o que
	// derrubaria a ingestão). CreateOrUpdateStream aplica isso a um stream já existente.
	//
	// Duplicates liga a DEDUPLICAÇÃO por Nats-Msg-Id no servidor. Cada lote é publicado
	// com um id derivado do conteúdo (ver PublishMetrics), então o reenvio do MESMO lote
	// dentro da janela é reconhecido e NÃO vira segunda cópia no stream. É a rede de
	// segurança do caso que nenhum código de erro resolve: a conexão cai DEPOIS que o
	// servidor gravou e ANTES do PubAck chegar — o gateway não tem como saber se entrou,
	// responde 503, o agente reenvia, e sem dedup isso é uma linha em dobro garantida.
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:       StreamName,
		Subjects:   []string{"ingest.>"},
		Retention:  jetstream.WorkQueuePolicy,
		Storage:    jetstream.FileStorage,
		MaxAge:     24 * time.Hour,
		MaxBytes:   config.NATSMaxBytes(),
		Discard:    jetstream.DiscardOld,
		Duplicates: config.NATSDedupWindow(),
	})
	if err != nil {
		nc.Close()
		return nil, err
	}

	// Teto por mensagem: o menor entre o configurado e (max_payload - margem).
	msgLimit := config.NATSMaxMsgBytes()
	if mp := int(nc.MaxPayload()); mp > 0 && mp-natsPayloadMargin < msgLimit {
		msgLimit = mp - natsPayloadMargin
	}
	if msgLimit < 1 {
		msgLimit = 1
	}
	q := &Queue{nc: nc, js: js, log: slog.Default(), msgLimit: msgLimit, maxBytes: config.NATSMaxBytes()}
	activeQueue.Store(q)
	go q.watchStream(ctx)
	return q, nil
}

// Close encerra a conexão.
func (q *Queue) Close() { q.nc.Close() }

// PublishMetrics publica um lote de métricas, FRAGMENTANDO em várias mensagens se o
// JSON serializado passar de q.msgLimit — um único Publish acima do max_payload do
// NATS falha (viraria 503 e o agente re-bufferaria). O consumer processa mensagem a
// mensagem, então múltiplas mensagens são seguras.
// Além disso, o publish é LIMITADO NO TEMPO e IDEMPOTENTE por conteúdo:
//
//   - timeout próprio (config.NATSPublishTimeout): sem ele, o ctx da requisição HTTP
//     não tem prazo e um NATS fora deixava o handler pendurado até o agente estourar o
//     próprio timeout de 10 s — o agente re-bufferava e o gateway continuava segurando
//     memória de uma requisição que não tem mais dono.
//   - Nats-Msg-Id derivado do CONTEÚDO do lote: reenvio do mesmo lote (o agente
//     obedecendo um 503, ou o WAL drenando depois de um restart) é reconhecido pela
//     janela de dedup do stream e não vira segunda cópia.
func (q *Queue) PublishMetrics(ctx context.Context, metrics []model.Metric) error {
	ctx, cancel := context.WithTimeout(ctx, config.NATSPublishTimeout())
	defer cancel()
	return chunkPublish(metrics, q.msgLimit,
		func(b []byte) error {
			ack, err := q.js.Publish(ctx, SubjectMetrics, b, jetstream.WithMsgID(msgID(b)))
			if err != nil {
				return err
			}
			if ack != nil && ack.Duplicate {
				publishDuplicates.Inc()
			}
			return nil
		},
		func(oversize []model.Metric) {
			// Um único registro que estoura o limite não pode ser publicado; logamos
			// e seguimos com o resto (nunca falha silenciosa: fica visível no log).
			oversizeDropped.Inc()
			q.log.Error("nats: métrica isolada acima do max_payload, descartada",
				"metric", oversize[0].Metric, "limit_bytes", q.msgLimit)
		},
	)
}

// msgID deriva o identificador de deduplicação do CONTEÚDO do lote (SHA-256 truncado
// em 128 bits, hex). Tem de sair do conteúdo, e não de um contador: quem reenvia é o
// AGENTE, de outro processo e possivelmente depois de um restart — só o conteúdo é
// igual dos dois lados. SHA-256 (e não FNV) porque aqui uma colisão não é uma métrica
// imprecisa: é um lote LEGÍTIMO descartado em silêncio como "duplicata".
func msgID(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// chunkPublish serializa metrics e publica via publish; se o lote exceder limit,
// divide recursivamente ao meio até caber. Um sub-lote de UM único registro que
// ainda assim estoure é entregue a onOversize (log/skip) sem derrubar o restante.
// Extraído para ser testável com um publisher fake (sem NATS real).
func chunkPublish(metrics []model.Metric, limit int, publish func([]byte) error, onOversize func([]model.Metric)) error {
	if len(metrics) == 0 {
		return nil
	}
	b, err := json.Marshal(metrics)
	if err != nil {
		return err
	}
	if len(b) <= limit {
		return publish(b)
	}
	if len(metrics) == 1 {
		if onOversize != nil {
			onOversize(metrics)
		}
		return nil
	}
	mid := len(metrics) / 2
	if err := chunkPublish(metrics[:mid], limit, publish, onOversize); err != nil {
		return err
	}
	return chunkPublish(metrics[mid:], limit, publish, onOversize)
}

// MetricsHandler processa um lote recebido; devolver erro faz o NATS reentregar (nak).
type MetricsHandler func(ctx context.Context, metrics []model.Metric) error

// Parâmetros de lote e resiliência do writer.
const (
	fetchMax    = 128                    // mensagens por ciclo (cada uma ~1 POST de métricas ≈ 10k linhas no teto)
	fetchWait   = 2 * time.Second        // flush no máximo a cada 2s
	backoffBase = 500 * time.Millisecond // atraso inicial de reentrega em falha de escrita
	backoffMax  = 30 * time.Second       // teto do backoff exponencial

	// ackWait é o prazo que o JetStream espera pelo ack antes de REENTREGAR a mensagem.
	//
	// POR QUE 90 s: o INSERT no ClickHouse pode levar até 60 s (timeout do http.Client
	// em chhttp.New). O default do JetStream é 30 s — ou seja, TODO insert entre 30 s e
	// 60 s era reentregue e gravado de novo, com o writer reportando sucesso nas duas
	// vezes. O prazo tem de ser MAIOR que o timeout do insert, com folga para o
	// serialize/rede do lote; 90 s = 1,5× o pior caso do insert.
	ackWait = 90 * time.Second

	// ackHeartbeat é o intervalo do InProgress durante um insert longo. AckWait sozinho
	// só empurra o problema: um ClickHouse em merge pesado pode passar dos 90 s e a
	// reentrega volta. O InProgress RENOVA o prazo enquanto o insert está de fato em
	// andamento, então a reentrega passa a significar "o writer morreu", que é o que ela
	// deveria significar. 20 s = AckWait/4,5, sobra folga para um heartbeat perdido.
	ackHeartbeat = 20 * time.Second
)

// inProgresser é o que o heartbeat precisa de uma mensagem do JetStream (InProgress
// renova o prazo de ack). Interface mínima para o teste conseguir contar heartbeats
// sem um servidor NATS de verdade.
type inProgresser interface{ InProgress() error }

// handleWithHeartbeat roda h e, ENQUANTO ele não retorna, manda InProgress em todas as
// mensagens do lote a cada ackHeartbeat.
//
// POR QUE: AckWait sozinho é um prazo fixo, e um INSERT longo o suficiente estoura
// qualquer prazo fixo — foi assim que um ClickHouse pausado produziu count()=10 para 5
// pontos distintos. O InProgress diz ao servidor "ainda estou nisso", então o prazo só
// vence de verdade quando o writer para de responder. É a diferença entre "insert lento"
// (não pode reentregar) e "writer morto" (tem de reentregar).
func (q *Queue) handleWithHeartbeat(ctx context.Context, h MetricsHandler, batch []model.Metric, pending []jetstream.Msg) error {
	msgs := make([]inProgresser, len(pending))
	for i, m := range pending {
		msgs[i] = m
	}
	stop := startAckHeartbeat(msgs, ackHeartbeat)
	defer stop()
	return h(ctx, batch)
}

// startAckHeartbeat dispara o heartbeat e devolve a função que o encerra.
func startAckHeartbeat(msgs []inProgresser, every time.Duration) (stop func()) {
	done := make(chan struct{})
	var once sync.Once
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				for _, m := range msgs {
					_ = m.InProgress()
				}
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}

// ConsumeMetrics registra um consumer durável (pull) e chama h com lotes agregados.
// Agrega por mensagens (fetchMax) ou tempo (fetchWait), o que vier antes. Ack só após
// h retornar nil; em falha, Nak com backoff exponencial (evita hot-loop com o store fora).
// Bloqueia até o ctx ser cancelado.
func (q *Queue) ConsumeMetrics(ctx context.Context, durable string, h MetricsHandler) error {
	cons, err := q.js.CreateOrUpdateConsumer(ctx, StreamName, jetstream.ConsumerConfig{
		Durable:       durable,
		FilterSubject: SubjectMetrics,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       ackWait, // ver ackWait: sem isto, insert > 30 s virava linha em dobro
		MaxDeliver:    -1,
	})
	if err != nil {
		return err
	}

	backoff := backoffBase
	for ctx.Err() == nil {
		msgs, err := cons.Fetch(fetchMax, jetstream.FetchMaxWait(fetchWait))
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoffBase):
			}
			continue
		}

		var batch []model.Metric
		var pending []jetstream.Msg
		for msg := range msgs.Messages() {
			var part []model.Metric
			if err := json.Unmarshal(msg.Data(), &part); err != nil {
				_ = msg.Term() // payload corrompido: descarta (não reentrega em loop)
				continue
			}
			// Uma mensagem que chega aqui pela SEGUNDA vez é reentrega: ou o writer
			// morreu no meio, ou o ack venceu com o insert ainda em andamento. Nos dois
			// casos o lote pode acabar gravado duas vezes, e isso precisa ter número.
			if md, mderr := msg.Metadata(); mderr == nil && md.NumDelivered > 1 {
				msgsRedelivered.Inc()
				q.log.Warn("nats: mensagem reentregue ao writer (risco de gravação em dobro)",
					"seq", md.Sequence.Stream, "entregas", md.NumDelivered)
			}
			batch = append(batch, part...)
			pending = append(pending, msg)
		}
		if len(pending) == 0 {
			continue // ciclo sem mensagens (timeout)
		}

		if err := q.handleWithHeartbeat(ctx, h, batch, pending); err != nil {
			for _, m := range pending {
				_ = m.NakWithDelay(backoff)
			}
			if backoff *= 2; backoff > backoffMax {
				backoff = backoffMax
			}
			continue
		}
		backoff = backoffBase
		for _, m := range pending {
			if err := m.Ack(); err == nil {
				// O ack é o denominador da estimativa de perda: mensagem que saiu do
				// stream SEM passar por aqui foi apagada (DiscardOld/MaxAge), não escrita.
				msgsAcked.Inc()
			}
		}
	}
	return nil
}

package logtail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// record é um registro de log pronto para o NDJSON /ingest/logs. O sink injeta o
// label "host" no envio; cada coletor preenche "source" e os labels específicos
// (unit, container, stream, file). O formato do wire é idêntico ao do logtail
// original: {"service","severity","body","labels"}.
type record struct {
	service  string
	severity string
	body     string
	labels   map[string]string
	// cursor é a posição do coletor de stream NESTE registro (o `__CURSOR` do
	// journald). Não vai para o wire: existe só para o cursor de retomada avançar
	// DEPOIS da entrega confirmada. Marcá-lo na leitura, como se fazia, perde a
	// última janela de registros quando o agente é encerrado no meio — que é
	// exatamente o momento que este cursor existe para cobrir.
	cursor string
}

// sink é o mecanismo de envio compartilhado por todos os coletores (file,
// journald, docker, syslog, kernel): serializa registros em NDJSON e faz POST
// {gateway}/ingest/logs com o header X-Revoada-Key.
type sink struct {
	gateway string
	key     string
	host    string
	client  *http.Client
	limiter *rateLimiter // salvaguarda de linhas/s (compartilhado; nil = ilimitado)
	bytes   *byteLimiter // salvaguarda de bytes/s (compartilhado; nil = ilimitado)
}

func newSink(gatewayURL, key, host string) *sink {
	return &sink{
		gateway: strings.TrimRight(gatewayURL, "/"),
		key:     key,
		host:    host,
		client:  &http.Client{Timeout: 10 * time.Second},
		limiter: sharedLimiter,
		bytes:   sharedByteLimiter,
	}
}

// post serializa e envia os registros. O label "host" é injetado aqui para todos os
// coletores. Devolve true quando o gateway ACEITOU o lote (ou quando não havia nada a
// mandar) — é esse booleano que autoriza o cursor de retomada a avançar; ver cursor.go.
func (s *sink) post(ctx context.Context, recs []record) bool {
	if len(recs) == 0 {
		return true
	}
	// Salvaguardas de não-sobrecarga: o excedente é DESCARTADO (best-effort) e um
	// aviso periódico é injetado no próprio stream, para a perda ficar visível ao
	// operador em vez de virar silêncio.
	//
	// Os DOIS tetos são aplicados, e nesta ordem: primeiro linhas/s (protege o
	// caminho de parse/ingestão), depois bytes/s (protege o link do cliente e o disco
	// do gateway). Só o de linhas garantia a coisa errada — 5000 linhas/s de JSON de
	// 4 KB são 20 MB/s, 1,7 TB/dia, tudo "dentro do limite". Ver ratelimit.go.
	if s.limiter != nil || s.bytes != nil {
		if s.limiter != nil {
			if k := s.limiter.admit(len(recs)); k < len(recs) {
				recs = recs[:k]
			}
		}
		if s.bytes != nil {
			if k := s.bytes.admit(recs); k < len(recs) {
				recs = recs[:k]
			}
		}
		// Os avisos entram DEPOIS do corte, e não passam pelos baldes: um aviso de
		// descarte que é ele próprio descartado deixa a perda invisível — que é o
		// desfecho exato que estes avisos existem para impedir.
		if n := s.limiter.notice(); n != nil {
			recs = append(recs, *n)
		}
		if n := s.bytes.notice(); n != nil {
			recs = append(recs, *n)
		}
		if len(recs) == 0 {
			// Tudo foi descartado pela salvaguarda. Isso conta como "resolvido": as
			// linhas não voltam, e não avançar aqui faria o coletor reler para sempre
			// um trecho que ele já decidiu não enviar.
			return true
		}
	}
	// Aviso de lotes perdidos em envios anteriores, pelo mesmo princípio do rate
	// limiter: a perda vai junto do próximo lote que der certo, para o operador ver
	// que faltou log em vez de concluir que o servidor ficou quieto.
	aviso, anunciadas := sendLoss.peek()
	if aviso != nil {
		recs = append(recs, *aviso)
	}
	// O lote vai em ENVIOS de tamanho limitado. O gateway recusa (413, sem retry)
	// requisição com mais de 20 mil linhas ou 16 MiB, e com o balde do tamanho do
	// ciclo (ratelimit.go) um único ciclo do Tailer pode passar disso: o lote INTEIRO
	// seria perdido por ter dado certo demais. Cada envio é independente — um que
	// falha não leva os outros junto, e só as linhas dele contam como perda.
	tudoOK := true
	for _, envio := range fatiarEnvios(s.host, recs, aviso != nil) {
		if s.send(ctx, envio.corpo) {
			if envio.levaAviso {
				sendLoss.ack(anunciadas)
			}
			continue
		}
		tudoOK = false
		// O envio não entrou. Conta a perda: descartar em silêncio era o pior
		// desfecho — o painel fica sem as linhas E sem nenhum sinal de que elas
		// existiram. O próprio aviso de perda não é linha do cliente: não se soma.
		perdidas := envio.registros
		if envio.levaAviso {
			perdidas--
		}
		sendLoss.add(perdidas)
	}
	return tudoOK
}

// Tetos de um envio ao gateway, com folga sobre os limites dele (maxNDJSONLines =
// 20000 e 16 MiB de corpo em gateway/internal/otlp/logs_receiver.go).
const (
	maxRegistrosPorEnvio = 5000
	maxBytesPorEnvio     = 4 << 20
)

// envio é um corpo NDJSON pronto para um POST.
type envio struct {
	corpo     []byte
	registros int
	levaAviso bool // carrega o aviso de perda anexado no fim de recs
}

// fatiarEnvios serializa recs (com redaction e o label host) em envios que
// respeitam maxRegistrosPorEnvio e maxBytesPorEnvio, preservando a ordem. Um
// registro maior que o teto de bytes vai sozinho (ele já vem limitado por
// maxLineBytes). comAviso diz que o último registro é o aviso de perda: o envio
// que o carrega fica marcado.
func fatiarEnvios(host string, recs []record, comAviso bool) []envio {
	var out []envio
	var buf bytes.Buffer
	n := 0
	fechar := func(levaAviso bool) {
		if n == 0 {
			return
		}
		out = append(out, envio{corpo: append([]byte(nil), buf.Bytes()...), registros: n, levaAviso: levaAviso})
		buf.Reset()
		n = 0
	}
	for i, r := range recs {
		linha := linhaNDJSON(host, r)
		if n > 0 && (n >= maxRegistrosPorEnvio || buf.Len()+len(linha) > maxBytesPorEnvio) {
			fechar(false)
		}
		buf.Write(linha)
		n++
		if i == len(recs)-1 {
			fechar(comAviso)
		}
	}
	return out
}

// linhaNDJSON serializa um registro com o label host e a redaction aplicada.
func linhaNDJSON(host string, r record) []byte {
	labels := make(map[string]string, len(r.labels)+1)
	labels["host"] = host
	for k, v := range r.labels {
		labels[k] = redactSecrets(v)
	}
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]any{
		"service":  r.service,
		"severity": r.severity,
		// Redaction ANTES do envio: um segredo impresso por um serviço de terceiro
		// é mascarado aqui e nunca chega ao ClickHouse (ver redact.go).
		"body":   redactSecrets(r.body),
		"labels": labels,
	})
	return buf.Bytes()
}

// postBackoff é a espera entre tentativas (logo, 3 tentativas por lote). Curta de
// propósito: quem chama é o ciclo de coleta (10s no Tailer, 1,5s no docker), e
// travar ali por muito tempo empurra a fila de linhas para trás. Duas esperas
// cobrem o caso comum de 429/503 — gateway reiniciando, pico de escrita no
// ClickHouse — sem transformar o coletor num processo bloqueado.
var postBackoff = []time.Duration{500 * time.Millisecond, 2 * time.Second}

// send faz o POST com retry. O defeito que ela corrige: a versão anterior era
// `if resp, err := s.client.Do(req); err == nil { resp.Body.Close() }` — o
// StatusCode NUNCA era lido. Um 429, cujo contrato explícito é "retente", e um 503
// de gateway reiniciando descartavam o lote inteiro sem retry, sem contagem e sem
// aviso. Reproduzido ao vivo contra um servidor que responde 429: todas as linhas
// sumiam e nada no painel indicava a falta.
func (s *sink) send(ctx context.Context, body []byte) bool {
	for attempt := 0; ; attempt++ {
		ok, retry := s.attempt(ctx, body)
		if ok || !retry || attempt >= len(postBackoff) {
			return ok
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(postBackoff[attempt]):
		}
	}
}

// attempt faz uma tentativa e diz se vale repetir. Só 429 (backpressure), 5xx
// (gateway/ClickHouse com problema) e erro de transporte são repetíveis; um 400 de
// corpo malformado ou um 401 de chave errada não melhoram com insistência — repetir
// só somaria carga a um gateway que já respondeu o que tinha a responder.
func (s *sink) attempt(ctx context.Context, body []byte) (ok, retry bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.gateway+"/ingest/logs", bytes.NewReader(body))
	if err != nil {
		return false, false
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("X-Revoada-Key", s.key)
	resp, err := s.client.Do(req)
	if err != nil {
		return false, ctx.Err() == nil // rede caiu: repetível; ctx cancelado: não
	}
	// Drena antes de fechar para a conexão poder ser reaproveitada pelo keep-alive;
	// sem isso, cada lote abre um socket novo contra o gateway.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return true, false
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return false, true
	default:
		return false, false
	}
}

// lossTracker conta linhas de log que não chegaram ao gateway e produz um aviso
// periódico, imitando o rateLimiter.notice(): a perda vira uma linha no próprio
// stream, no lugar em que o operador já está olhando.
type lossTracker struct {
	mu      sync.Mutex
	dropped int
	lastMsg time.Time
	now     func() time.Time
}

// sendLoss é o contador do processo. Único de propósito: a pergunta que ele
// responde ("quanto log deixou de chegar") é sobre o agente inteiro, não sobre uma
// fonte.
var sendLoss = &lossTracker{now: time.Now}

func (lt *lossTracker) add(n int) {
	if n <= 0 {
		return
	}
	lt.mu.Lock()
	lt.dropped += n
	lt.mu.Unlock()
}

// peek devolve o aviso a anexar ao lote atual e quantas perdas ele anuncia, SEM
// zerar o contador. Zerar só no ack (após o POST ter dado certo) evita o caso
// perverso de o aviso da perda se perder no mesmo lote que falhou — que é
// exatamente o cenário em que ele é mais provável.
func (lt *lossTracker) peek() (*record, int) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	if lt.dropped == 0 {
		return nil, 0
	}
	t := lt.now()
	if !lt.lastMsg.IsZero() && t.Sub(lt.lastMsg) < 30*time.Second {
		return nil, 0
	}
	n := lt.dropped
	return &record{
		service:  "revoada-agent",
		severity: "WARN",
		body:     fmt.Sprintf("envio de logs ao gateway falhou: %d linha(s) não chegaram (o gateway recusou ou estava indisponível; logs não têm buffer em disco, então essas linhas se perderam)", n),
		labels:   map[string]string{"source": "revoada-agent"},
	}, n
}

// ack confirma que o aviso chegou: desconta o que foi anunciado e reinicia a janela.
func (lt *lossTracker) ack(n int) {
	if n <= 0 {
		return
	}
	lt.mu.Lock()
	lt.dropped -= n
	if lt.dropped < 0 {
		lt.dropped = 0
	}
	lt.lastMsg = lt.now()
	lt.mu.Unlock()
}

// drain consome registros de um stream contínuo (journald, kernel) e os envia em
// lotes — flush por tamanho (200) ou tempo (2s) para não abrir uma requisição
// por linha. Encerra quando o canal fecha ou o contexto é cancelado.
func (s *sink) drain(ctx context.Context, recs <-chan record) { s.drainCom(ctx, recs, nil) }

// drainCom é o drain com confirmação de entrega: `confirmar` recebe o ÚLTIMO registro
// de cada lote que o gateway aceitou. É como o coletor de journald só avança o cursor
// depois de a linha estar do outro lado — sem isso, o encerramento do agente (o mais
// comum deles: a auto-atualização) perderia a última janela de registros, que é
// justamente a que o cursor existe para não perder.
func (s *sink) drainCom(ctx context.Context, recs <-chan record, confirmar func(record)) {
	tk := time.NewTicker(2 * time.Second)
	defer tk.Stop()
	var batch []record
	flush := func() {
		if len(batch) == 0 {
			return
		}
		ultimo := batch[len(batch)-1]
		if s.post(ctx, batch) && confirmar != nil {
			confirmar(ultimo)
		}
		batch = batch[:0]
	}
	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case r, ok := <-recs:
			if !ok {
				flush()
				return
			}
			batch = append(batch, r)
			if len(batch) >= 200 {
				flush()
			}
		case <-tk.C:
			flush()
		}
	}
}

// severityFromPriority mapeia a PRIORITY do syslog/journald/kmsg (0–7) para o
// rótulo textual de severidade (função pura, testável). 0–3 = emerg/alert/crit/err,
// 4 = warning, 5–6 = notice/info, 7 = debug.
func severityFromPriority(prio int) string {
	switch {
	case prio >= 0 && prio <= 3:
		return "ERROR"
	case prio == 4:
		return "WARN"
	case prio == 7:
		return "DEBUG"
	default:
		return "INFO"
	}
}

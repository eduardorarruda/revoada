// Package sitecheck sonda websites/LPs: mede o tempo decomposto (DNS→conexão→TLS
// →TTFB→total), valida asserções e classifica o diagnóstico da falha. TLS é
// sempre validado.
package sitecheck

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/safehttp"
)

// DefaultTimeout é o tempo máximo que a sonda espera por uma resposta COMPLETA.
//
// Era uma constante embutida (15s) que não aparecia em tela nem no dicionário: um
// site que responde em 16 s era publicado como "fora do ar" sem que nada dissesse
// que o corte era nosso. Agora é o default de `Target.TimeoutMS` (por check) e o
// número entra no texto do diagnóstico ("sem resposta em 20 s") — ver
// TimeoutDiagnosisText e o rótulo correspondente em web/src/pages/Websites.tsx.
//
// 15 s → 20 s em 13/08/2026, a pedido: entraram sistemas mais pesados na lista. Ao
// mexer aqui, lembre do que o número custa. Ele NÃO é só "esperar mais": o disparo
// de "fora do ar" exige duas falhas seguidas, então o alerta chega ~10 s mais tarde
// numa queda real; e o ciclo de sondagem espera o lote inteiro terminar antes do
// próximo, então um alvo pendurado atrasa TODOS os outros checks pelo mesmo tempo.
// Subir isso sem configurar `max_latency_ms` (o limiar de lentidão, por check) só
// aumenta a zona onde "lento" é publicado como "saudável".
const DefaultTimeout = 20 * time.Second

// MaxBodyBytes é o teto de leitura de corpo de TODA sonda do painel — a central
// (este pacote) e a do agente (agent/internal/probe). Este é o lado canônico: o
// agente espelha o valor.
//
// Por que um teto ÚNICO, e por que este valor:
//
// As duas sondas votam no MESMO consenso, e o teto é justamente o que decide a
// asserção de palavra-chave. Com tetos diferentes (8 MiB na central, 512 KiB no
// agente) elas discordavam por construção: medido na mesma página de 1.024.013
// bytes, com a palavra a 700 KiB, a central devolvia ok=true e o agente
// up=false diag=keyword_indeterminado truncated=true — duas falhas no consenso,
// DOWN + notificação crítica para um site no ar. O painel acusava o cliente pelo
// tamanho da própria página.
//
// Vence o valor MENOR (512 KiB), que é o do agente, por causa da banda: a sonda
// roda a cada 30s, 2.880 vezes por dia POR URL, e o tráfego sai do site do
// próprio cliente. A 8 MiB o pior caso era ~22 GiB/dia por URL (basta a URL
// apontar para um arquivo em vez de uma página); a 512 KiB cai para ~1,4 GiB/dia.
// 512 KiB cobre HTML real com folga, e o corte deixou de ser perigoso: corpo
// truncado não afirma ausência de palavra-chave (DiagKeywordIndet) e não vota
// como falha no consenso.
//
// Ao estourar, `Result.Truncated` marca que `total_ms` virou um PISO: o tempo
// medido é real, o resto da transferência não entrou na conta.
const MaxBodyBytes = 512 << 10

// maxCorpo é o nome interno histórico do mesmo teto.
const maxCorpo = MaxBodyBytes

// Result é o resultado decomposto de uma sondagem.
type Result struct {
	OK        bool   // sucesso "duro": status e palavra-chave conferem
	Degraded  bool   // respondeu, mas acima da latência máxima
	Status    int    `json:"status"`
	Diagnosis string `json:"diagnosis"` // ver diagnosis.go (tabela única)
	// Blocked: a sondagem NÃO saiu daqui — foi recusada pelo nosso guard SSRF. Não
	// é medida do alvo: não pode virar DOWN nem entrar na conta de uptime.
	Blocked bool `json:"blocked"`
	// Inconclusive: a sondagem SAIU, mas não conseguiu decidir a asserção (corpo
	// cortado no teto antes de achar a palavra-chave). Vale o mesmo que Blocked
	// para a máquina de estados e para o uptime: abstenção, não falha. Ver
	// DiagnosisIsInconclusive.
	Inconclusive bool `json:"inconclusive"`
	// Truncated: o corpo estourou maxCorpo, então total_ms é um piso e a asserção
	// de palavra-chave é indeterminada.
	Truncated    bool    `json:"truncated"`
	DNSMs        float64 `json:"dns_ms"`
	ConnectMs    float64 `json:"connect_ms"`
	TLSMs        float64 `json:"tls_ms"`
	TTFBMs       float64 `json:"ttfb_ms"`
	TotalMs      float64 `json:"total_ms"`
	CertDaysLeft int     `json:"cert_days_left"`
	// TLSOK diz que o handshake TLS terminou sem erro. Não vai para o JSON: serve
	// só para separar "TLS travou" de "conectou e a página não veio".
	TLSOK bool `json:"-"`
	// TimeoutMs é o limite que valeu para ESTA sondagem (ms), para a tela poder
	// dizer "sem resposta em N s" em vez de um "connect_timeout" sem régua.
	TimeoutMs int `json:"timeout_ms"`
}

// Target descreve o que sondar e as asserções.
type Target struct {
	URL          string
	ExpectStatus int
	Keyword      string
	MaxLatencyMS int
	// TimeoutMS: 0 = DefaultTimeout. Vale para a resposta INTEIRA (cabeçalho +
	// corpo), que é o mesmo relógio de `total_ms` e de `MaxLatencyMS`.
	TimeoutMS int
}

// Timeout devolve o limite efetivo desta sondagem.
func (t Target) Timeout() time.Duration {
	if t.TimeoutMS > 0 {
		return time.Duration(t.TimeoutMS) * time.Millisecond
	}
	return DefaultTimeout
}

// TimeoutDiagnosisText explica o corte em português, com o número que valeu.
func TimeoutDiagnosisText(d time.Duration) string {
	return fmt.Sprintf("sem resposta em %.0f s", d.Seconds())
}

// Prober executa sondagens HTTP com medição decomposta.
type Prober struct{ client *http.Client }

func NewProber() *Prober {
	// DisableKeepAlives: cada sondagem refaz DNS/conexão/TLS (medição fiel).
	// TLS validado por padrão (InsecureSkipVerify permanece false).
	// Transport endurecido contra SSRF (dialer guardado + CheckRedirect).
	tr := safehttp.Transport()
	tr.DisableKeepAlives = true
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return &Prober{client: &http.Client{
		Transport: tr,
		// Timeout fica com o contexto de cada sondagem (Target.TimeoutMS): um teto
		// fixo no client ignoraria o limite escolhido para o check.
		CheckRedirect: safehttp.CheckRedirect,
	}}
}

func (p *Prober) Probe(ctx context.Context, t Target) Result {
	var r Result
	var dnsStart, connStart, tlsStart, start time.Time
	limite := t.Timeout()
	r.TimeoutMs = int(limite.Milliseconds())

	ctx, cancel := context.WithTimeout(ctx, limite)
	defer cancel()

	// As fases são gravadas pelos ganchos do httptrace, que rodam nas goroutines do
	// transporte (a de leitura da conexão, inclusive). Quando o prazo estoura,
	// `client.Do` volta ANTES de essas goroutines terminarem: sem trava, ler as fases
	// aqui é corrida de dados — e a revisão a pegou virando `sem_resposta` em
	// `connect_timeout`, porque o primeiro byte chegou logo DEPOIS do prazo e o
	// gancho escreveu o TTFB por cima. Então: cada gancho grava sob `mu`, e no
	// instante em que a sondagem termina as fases são congeladas; o que chegar depois
	// não pertence a esta medição.
	var mu sync.Mutex
	congelado := false
	handshakeIniciado := false // lido só depois de congelar
	grava := func(f func()) {
		mu.Lock()
		defer mu.Unlock()
		if !congelado {
			f()
		}
	}
	congela := func() Result {
		mu.Lock()
		defer mu.Unlock()
		congelado = true
		return r
	}
	trace := &httptrace.ClientTrace{
		DNSStart:     func(httptrace.DNSStartInfo) { grava(func() { dnsStart = time.Now() }) },
		DNSDone:      func(httptrace.DNSDoneInfo) { grava(func() { r.DNSMs = msSince(dnsStart) }) },
		ConnectStart: func(string, string) { grava(func() { connStart = time.Now() }) },
		ConnectDone: func(_, _ string, err error) {
			grava(func() {
				r.ConnectMs = msSince(connStart)
				if err == nil {
					r.ConnectMs = aconteceu(r.ConnectMs)
				}
			})
		},
		TLSHandshakeStart: func() { grava(func() { tlsStart, handshakeIniciado = time.Now(), true }) },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			grava(func() {
				r.TLSMs = msSince(tlsStart)
				r.TLSOK = err == nil
			})
		},
		GotFirstResponseByte: func() { grava(func() { r.TTFBMs = aconteceu(msSince(start)) }) },
	}

	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, t.URL, nil)
	if err != nil {
		// Não é falha do alvo: a URL nem chegou a virar requisição. Antes isso virava
		// `http_status`, um diagnóstico sobre uma resposta que nunca existiu.
		r.Diagnosis = DiagBadURL
		return r
	}
	req.Header.Set("User-Agent", "Revoada-Next/synthetic")

	grava(func() { start = time.Now() })
	resp, err := p.client.Do(req)
	// Daqui em diante `r` é a cópia congelada; os ganchos não a tocam mais.
	r = congela()
	if err != nil {
		r.TotalMs = msSince(start)
		r.Diagnosis = classifyErr(err)
		mu.Lock()
		noHandshake := handshakeIniciado
		mu.Unlock()
		// A conexão caiu NO MEIO do handshake TLS: o endereço não fala TLS. No Linux
		// isso chega como RecordHeaderError (o cliente lê o lixo antes do fim); no
		// Windows o servidor que fecha com o ClientHello não lido manda RST e o erro é
		// só "conexão encerrada" — o mesmo fato, que tem de dar o mesmo diagnóstico.
		// (Inclui o "não classificado": a mensagem do Windows varia com a versão e o
		// momento do RST, mas a fase em que a conexão morreu não varia.)
		if noHandshake && !r.TLSOK && (r.Diagnosis == DiagConnError || r.Diagnosis == DiagNaoClassificado) {
			r.Diagnosis = DiagTLS
		}
		if conectouSemResponder(r, t.URL) {
			r.Diagnosis = DiagSemResposta
		}
		r.Blocked = DiagnosisIsOurFault(r.Diagnosis)
		r.Inconclusive = DiagnosisIsInconclusive(r.Diagnosis)
		return r
	}
	defer resp.Body.Close()

	// Varredura em streaming: procura a palavra-chave enquanto drena o corpo, sem
	// teto de POSIÇÃO (o antigo `io.LimitReader(1 MiB)` só enxergava o começo).
	achou, truncado := scanBody(resp.Body, t.Keyword, maxCorpo)
	r.Truncated = truncado
	// `client.Do` volta quando o CABEÇALHO chega. Parar o cronômetro ali fazia
	// `total_ms` ser o TTFB com outro nome — as duas colunas da tela mostravam o
	// mesmo número (medido: avg(total−ttfb) = 0,088 ms em 1.502 sondagens reais), e
	// uma página que responde 200 na hora mas leva 1,5s para transferir era publicada
	// como instantânea. Pior: `max_latency_ms`, o único controle de lentidão que o
	// usuário configura, comparava contra esse número — uma página de 925 ms com
	// limite de 1.000 ms era avaliada como 1,8 ms e nunca marcava "lento".
	// Mesma correção já aplicada em agent/internal/probe/probe.go.
	r.TotalMs = msSince(start)
	r.Status = resp.StatusCode

	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		r.CertDaysLeft = int(time.Until(resp.TLS.PeerCertificates[0].NotAfter).Hours() / 24)
	}

	expect := t.ExpectStatus
	if expect == 0 {
		expect = 200
	}
	switch {
	case resp.StatusCode != expect:
		if resp.StatusCode >= 500 {
			r.Diagnosis = DiagHTTP5xx
		} else {
			r.Diagnosis = DiagHTTPStatus
		}
		return r
	case t.Keyword != "" && !achou:
		// Corpo truncado → não dá para AFIRMAR que a palavra não existe. E, ao
		// contrário da versão anterior, "não sabemos" deixa de valer como falha: o
		// resultado sai marcado Inconclusive e o checker o trata como abstenção
		// (não vira DOWN, não notifica, fica fora do uptime). Ver
		// DiagnosisIsInconclusive.
		if truncado {
			r.Diagnosis = DiagKeywordIndet
			r.Inconclusive = true
		} else {
			r.Diagnosis = DiagKeywordAusent
		}
		return r
	}

	// Sucesso funcional. Lento demais? DEGRADADO. A comparação é contra o total
	// corrigido (cabeçalho + corpo), que é o número que o usuário sente.
	r.OK = true
	if t.MaxLatencyMS > 0 && r.TotalMs > float64(t.MaxLatencyMS) {
		r.Degraded = true
		r.Diagnosis = DiagSlow
	}
	return r
}

// scanBody drena o corpo até `limite` bytes procurando `needle`. Devolve
// (encontrou, truncou). A janela de sobreposição garante que uma ocorrência
// partida entre dois blocos de leitura ainda seja encontrada — sem isso, o
// streaming reintroduziria o falso `keyword_missing` que estamos corrigindo.
func scanBody(body io.Reader, needle string, limite int64) (found, truncated bool) {
	// Lê 1 byte a mais que o teto: se ele chegar, o corpo é MAIOR que o teto (e só
	// então o resultado é truncado). Terminar exatamente no teto não é truncagem.
	src := io.LimitReader(body, limite+1)
	overlap := 0
	if n := len(needle); n > 1 {
		overlap = n - 1
	}
	buf := make([]byte, 32<<10)
	cauda := make([]byte, 0, overlap)
	var lidos int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			lidos += int64(n)
			if needle != "" && !found {
				janela := make([]byte, 0, len(cauda)+n)
				janela = append(janela, cauda...)
				janela = append(janela, buf[:n]...)
				if bytes.Contains(janela, []byte(needle)) {
					found = true
				}
				if overlap > 0 {
					if len(janela) > overlap {
						janela = janela[len(janela)-overlap:]
					}
					cauda = append(cauda[:0], janela...)
				}
			}
			if lidos > limite {
				return found, true
			}
		}
		if err != nil {
			return found, false
		}
	}
}

func msSince(t time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	return float64(time.Since(t).Microseconds()) / 1000
}

// conectouSemResponder: o prazo estourou DEPOIS de a conexão abrir (e do TLS, quando
// https) e sem resposta dentro do prazo — o alvo aceitou e não respondeu. Não é a
// mesma coisa que "o pedido de conexão não foi respondido" (connect_timeout).
//
// "Sem resposta dentro do prazo" inclui o primeiro byte que chega no instante do
// estouro (TTFB >= prazo): o client.Do já voltou com erro de prazo, mas o gancho do
// trace gravou o TTFB antes do congelamento. Visto na CI: ttfb=501 ms com prazo de
// 500 saía connect_timeout, acusando a conexão de um site que conectou.
// aconteceu garante que uma fase que DE FATO ocorreu não fique com 0 ms: no
// Windows conectar no localhost cabe dentro da resolução do relógio e media 0,
// e "ConnectMs > 0" é como o resto do código sabe que a conexão abriu (sem isto,
// "conectou e ficou mudo" virava "não conectou").
func aconteceu(ms float64) float64 {
	return max(ms, 0.001)
}

func conectouSemResponder(r Result, url string) bool {
	if r.Diagnosis != DiagConnTimeout || r.ConnectMs <= 0 {
		return false
	}
	semByteNoPrazo := r.TTFBMs == 0 || (r.TimeoutMs > 0 && r.TTFBMs >= float64(r.TimeoutMs))
	tlsOK := r.TLSOK || !strings.HasPrefix(strings.ToLower(url), "https:")
	return semByteNoPrazo && tlsOK
}

// classifyErr traduz o erro de transporte no diagnóstico canônico.
//
// A versão anterior decidia por SUBSTRING da mensagem e tinha
// `default: return "connect_timeout"`, ou seja: todo erro imprevisto virava
// "timeout". Medido no dev, errava em quatro situações — porta fechada
// (`connection refused`) virava connect_timeout; TCP que aceita e nunca fala TLS
// virava tls só por acaso da palavra na mensagem; destino recusado pelo NOSSO
// guard SSRF virava connect_timeout (o alvo era acusado por uma recusa nossa); e o
// default. O check 6 do dev acumulou 1.172 registros `connect_timeout` com
// avg(total_ms) = 0,33 ms — um "timeout" que resolve em 0,3 ms é a assinatura do
// erro de classificação, não da rede.
//
// Agora a decisão é pelo TIPO do erro; o texto só entra como último recurso e, se
// nem ele resolver, o resultado é `unclassified` explícito.
func classifyErr(err error) string {
	if err == nil {
		return DiagOK
	}

	// 0) Laço de redirecionamento. Vem ANTES do guard porque, até aqui, o excesso
	//    de saltos era embrulhado em ErrBlockedAddress e virava
	//    `bloqueado_pelo_painel`: o checker retornava antes da máquina de estados e
	//    um site inutilizável (loop http↔https) ficava UP para sempre, com uptime
	//    intacto e sem alerta. Isto é falha do ALVO e tem de derrubar o check.
	if errors.Is(err, safehttp.ErrTooManyRedirects) {
		return DiagRedirectLoop
	}

	// 1) Recusa NOSSA (guard SSRF / esquema não permitido). Tem de vir primeiro: o
	//    alvo não pode ser acusado por uma decisão do painel.
	if errors.Is(err, safehttp.ErrBlockedAddress) {
		return DiagBloqueado
	}

	// 2) DNS antes de timeout: um *net.DNSError com IsTimeout também satisfaz
	//    net.Error.Timeout(), e "o nome não resolveu" é a informação útil.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return DiagDNSError
	}

	// 3) TLS: certificado inválido, autoridade desconhecida, hostname errado, alerta
	//    do servidor — e o caso "TCP aceita e nunca fala TLS", que antes caía no
	//    default e virava timeout. Ele chega de duas formas: tls.RecordHeaderError
	//    (lixo que nem é HTTP) e http.ErrSchemeMismatch (o servidor respondeu HTTP
	//    puro numa porta https — a stdlib troca o RecordHeaderError por este).
	if errors.Is(err, http.ErrSchemeMismatch) {
		return DiagTLS
	}
	var certErr *tls.CertificateVerificationError
	var recErr tls.RecordHeaderError
	var alertErr tls.AlertError
	var unknownCA x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var invalidErr x509.CertificateInvalidError
	switch {
	case errors.As(err, &certErr), errors.As(err, &recErr), errors.As(err, &alertErr),
		errors.As(err, &unknownCA), errors.As(err, &hostErr), errors.As(err, &invalidErr):
		return DiagTLS
	}

	// 4) Porta fechada: o alvo respondeu "fechado" na hora. Não é timeout.
	if errors.Is(err, syscall.ECONNREFUSED) {
		return DiagConnRefused
	}

	// 5) Timeout de verdade (deadline do contexto ou net.Error.Timeout()).
	if errors.Is(err, context.DeadlineExceeded) {
		return DiagConnTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return DiagConnTimeout
	}

	// 6) Outras falhas de rede identificáveis pelo errno.
	switch {
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE),
		errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH),
		errors.Is(err, syscall.ENETDOWN), errors.Is(err, syscall.ECONNABORTED):
		return DiagConnError
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return DiagConnError
	}

	// 7) Último recurso: texto. Só para erros que a stdlib não expõe por tipo.
	e := strings.ToLower(err.Error())
	switch {
	case strings.Contains(e, "no such host"), strings.Contains(e, "server misbehaving"):
		return DiagDNSError
	case strings.Contains(e, "x509"), strings.Contains(e, "certificate"), strings.Contains(e, "tls:"):
		return DiagTLS
	// No Windows o dial recusado é WSAECONNREFUSED (10061) e o reset é
	// WSAECONNRESET (10054), que não casam com os errno do syscall: o texto do
	// sistema é o sinal comum.
	case strings.Contains(e, "connection refused"), strings.Contains(e, "actively refused"):
		return DiagConnRefused
	case strings.Contains(e, "timeout"), strings.Contains(e, "deadline exceeded"):
		return DiagConnTimeout
	case strings.Contains(e, "no route to host"), strings.Contains(e, "connection reset"),
		strings.Contains(e, "network is unreachable"), strings.Contains(e, "forcibly closed"),
		strings.Contains(e, "unreachable network"), strings.Contains(e, "unreachable host"):
		return DiagConnError
	case strings.Contains(e, "stopped after"), strings.Contains(e, "redirect"):
		// Rede de segurança para laços de redirect que cheguem sem o erro tipado
		// (ex.: um http.Client montado sem safehttp.CheckRedirect, que cai no limite
		// interno da stdlib com "stopped after 10 redirects"). Antes isto era
		// INALCANÇÁVEL: o errors.Is(ErrBlockedAddress) vinha antes e capturava tudo.
		return DiagRedirectLoop
	}
	// Nunca mais devolver connect_timeout por não saber: `unclassified` é honesto e
	// aparece na tela como "falha não classificada".
	return DiagNaoClassificado
}

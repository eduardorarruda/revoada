// Package probe: modo sonda (P6.3). Sonda URLs a partir desta localização e
// reporta o resultado ao gateway, que agrega o consenso multi-região.
package probe

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

type result struct {
	URL           string  `json:"url"`
	ProbeLocation string  `json:"probe_location"`
	Up            bool    `json:"up"`
	TotalMs       float64 `json:"total_ms"`
	Diagnostic    string  `json:"diagnostic"`
	// Truncated: o corpo estourou maxCorpoSonda, então `total_ms` é um PISO (o
	// resto da transferência não entrou na conta) e a asserção de palavra-chave é
	// indeterminada. Antes nada marcava isso e o número era publicado como se
	// fosse o tempo completo.
	Truncated bool `json:"truncated"`
	// Status é o código HTTP observado, para o painel poder mostrar POR QUE a
	// asserção falhou (e não só "não está no ar").
	Status int `json:"status"`
}

// assignment é uma URL designada a esta sonda, COM as asserções do check.
//
// Antes o agente só recebia a URL e decidia "no ar" por `resp.StatusCode < 400`,
// ignorando expect_status e keyword — enquanto a sonda central usava
// expect_status. As duas alimentam o MESMO consenso, então um check com
// expect_status=401 (página de login legítima) era OK para a central e falho para
// TODA sonda remota: DEGRADADO permanente sem causa real.
type assignment struct {
	URL          string `json:"url"`
	ExpectStatus int    `json:"expect_status"`
	Keyword      string `json:"keyword"`
	// TimeoutMS é o limite QUE O PAINEL manda, por check. Existe porque a régua não
	// pode morar em duas constantes compiladas: em 13/08/2026 a central subiu de 15 s
	// para 20 s e a sonda do agente ficou para trás, e um alvo que responde em 17 s
	// virava "no ar" para a central e "connect_timeout" para toda sonda remota — o
	// consenso lia essa discordância de RELÓGIO como queda e mandava "🔴 CRÍTICO".
	// Zero = "use o seu default", que é o caso de agente antigo e de alvo estático.
	TimeoutMS int `json:"timeout_ms"`
}

// Timeout devolve o limite efetivo desta designação.
func (a assignment) Timeout() time.Duration {
	if a.TimeoutMS > 0 {
		return time.Duration(a.TimeoutMS) * time.Millisecond
	}
	return DefaultTimeout
}

// Reporter sonda URLs e envia resultados ao gateway.
type Reporter struct {
	gateway  string
	key      string
	location string
	urls     []string
	// client fala com o GATEWAY (destino conhecido, do nosso lado).
	client *http.Client
	// probeClient fala com os ALVOS, que são URLs digitadas pelo usuário. Só ele
	// tem o guard SSRF — misturar os dois faria a sonda recusar o próprio gateway
	// quando ele estiver num endereço interno, que é o caso normal.
	probeClient *http.Client
}

func NewReporter(gatewayURL, key, location string, urls []string) *Reporter {
	return &Reporter{
		gateway: strings.TrimRight(gatewayURL, "/"), key: key, location: location, urls: urls,
		client: &http.Client{Timeout: 15 * time.Second},
		// Sem Timeout no client: o prazo é por sondagem (ver probe), porque cada
		// designação pode trazer o seu. Um Timeout aqui seria um teto ESCONDIDO por
		// baixo do que o painel mandou.
		probeClient: &http.Client{
			Transport:     safeTransport(),
			CheckRedirect: checkRedirect,
		},
	}
}

// DefaultTimeout é o tempo máximo de uma sondagem QUANDO o painel não manda um.
// Mesmo valor da sonda central (server/internal/sitecheck.DefaultTimeout): duas
// sondas com limites diferentes discordariam sobre "no ar" só por causa do relógio.
//
// A duplicação da constante já cobrou o preço uma vez (a central foi para 20 s e esta
// ficou em 15 s), por isso o limite agora VIAJA na designação — ver assignment.TimeoutMS.
// Este valor só vale para alvo estático do config e para o intervalo em que um agente
// ainda não atualizado fala com um painel novo.
const DefaultTimeout = 20 * time.Second

// Run sonda a cada 30s até o ctx ser cancelado. Sempre roda (mesmo sem urls
// estáticas) para buscar as URLs designadas pelo servidor (site_checks).
func (r *Reporter) Run(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	r.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tick(ctx)
		}
	}
}

func (r *Reporter) tick(ctx context.Context) {
	// Alvos = estáticos do config + designados pelo servidor (checks com esta
	// location). Os estáticos não têm asserção configurada, então valem o padrão
	// (200, sem palavra-chave) — o mesmo default da sonda central.
	seen := map[string]bool{}
	var alvos []assignment
	for _, u := range r.urls {
		if !seen[u] {
			seen[u] = true
			alvos = append(alvos, assignment{URL: u, ExpectStatus: 200})
		}
	}
	for _, a := range r.assignments(ctx) {
		if !seen[a.URL] {
			seen[a.URL] = true
			alvos = append(alvos, a)
		}
	}
	for _, a := range alvos {
		_ = r.report(ctx, r.probe(ctx, a))
	}
}

// assignments busca no gateway as URLs designadas para esta sonda (best-effort),
// já com expect_status/keyword para a asserção ser a MESMA da central.
func (r *Reporter) assignments(ctx context.Context) []assignment {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		r.gateway+"/probe/assignments?location="+url.QueryEscape(r.location), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("X-Revoada-Key", r.key)
	resp, err := r.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var body struct {
		URLs []assignment `json:"urls"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return nil
	}
	return body.URLs
}

func (r *Reporter) probe(ctx context.Context, a assignment) result {
	res := result{URL: a.URL, ProbeLocation: r.location}
	start := time.Now()
	// O prazo vale para a sondagem INTEIRA (conexão, TLS, cabeçalho e leitura do
	// corpo), igual ao da central: `total_ms` mede o tempo até o último byte, então
	// um teto que parasse no cabeçalho mediria outra coisa.
	ctx, cancel := context.WithTimeout(ctx, a.Timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		res.Diagnostic = DiagBadURL
		return res
	}
	req.Header.Set("User-Agent", "Revoada-Next/sonda")
	resp, err := r.probeClient.Do(req)
	if err != nil {
		res.TotalMs = float64(time.Since(start).Microseconds()) / 1000
		res.Diagnostic = classifyErr(err)
		return res
	}
	defer resp.Body.Close()
	// `client.Do` volta assim que o CABEÇALHO chega. Parar o cronômetro aqui
	// mediria só o tempo até o primeiro byte e chamaria isso de "total": uma página
	// que responde 200 em 80ms e leva 4s para transferir era reportada como 80ms, e
	// o usuário reclamando de lentidão via um gráfico dizendo que estava tudo bem.
	// Ler o corpo até o fim é o que faz `total_ms` ser o tempo total de verdade.
	achou, truncado := scanBody(resp.Body, a.Keyword, maxCorpoSonda)
	res.TotalMs = float64(time.Since(start).Microseconds()) / 1000
	res.Truncated = truncado
	res.Status = resp.StatusCode

	// MESMA asserção da sonda central (server/internal/sitecheck/prober.go): status
	// esperado e, se houver, palavra-chave. `resp.StatusCode < 400` não era a mesma
	// pergunta e produzia DEGRADADO permanente em checks legítimos.
	esperado := a.ExpectStatus
	if esperado == 0 {
		esperado = 200
	}
	switch {
	case resp.StatusCode != esperado:
		if resp.StatusCode >= 500 {
			res.Diagnostic = DiagHTTP5xx
		} else {
			res.Diagnostic = DiagHTTPStatus
		}
	case a.Keyword != "" && !achou:
		// Corpo truncado → não dá para AFIRMAR que a palavra não existe.
		if truncado {
			res.Diagnostic = DiagKeywordIndet
		} else {
			res.Diagnostic = DiagKeywordAusent
		}
	default:
		res.Up = true
	}
	return res
}

// maxCorpoSonda limita o quanto a sonda baixa de cada resposta.
//
// O teto importa mais do que parece: a sonda roda a cada 30s, ou seja 2.880 vezes
// por dia POR URL, e o site sondado costuma ser do próprio parque — a banda sai
// dele e entra na sonda. Com 8 MiB o pior caso era 22 GiB/dia por URL (basta a URL
// apontar para um arquivo em vez de uma página). 512 KiB cobre HTML real com folga
// e limita o pior caso a ~1,4 GiB/dia.
//
// Passando do teto, `total_ms` vira um piso: o tempo medido é real, mas o restante
// da transferência não entrou na conta. Isso agora é REPORTADO (`truncated`), em
// vez de ficar invisível, e a asserção de palavra-chave vira indeterminada em vez
// de acusar ausência.
const maxCorpoSonda = 512 << 10 // 512 KiB

// scanBody drena o corpo até `limite` bytes procurando `needle`, com janela de
// sobreposição para achar ocorrências partidas entre blocos. Devolve
// (encontrou, truncou). Espelha sitecheck.scanBody da sonda central.
func scanBody(body io.Reader, needle string, limite int64) (found, truncated bool) {
	// Lê 1 byte a mais que o teto: se ele chegar, o corpo é MAIOR que o teto.
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

func (r *Reporter) report(ctx context.Context, res result) error {
	body, _ := json.Marshal(res)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.gateway+"/ingest/probe-result", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Revoada-Key", r.key)
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// classifyErr traduz o erro de transporte no diagnóstico canônico (diagnosis.go).
// Decide pelo TIPO do erro; o texto é último recurso e, se nem ele resolver, o
// resultado é `unclassified` — nunca um "connect_timeout" inventado. Espelha
// sitecheck.classifyErr da sonda central.
func classifyErr(err error) string {
	if err == nil {
		return DiagOK
	}
	// Recusa NOSSA (guard SSRF) primeiro: o alvo não pode ser acusado por isso.
	if errors.Is(err, ErrBlockedAddress) {
		return DiagBloqueado
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return DiagDNSError
	}
	// "TCP aceita e nunca fala TLS": lixo binário vira tls.RecordHeaderError;
	// resposta HTTP em claro numa porta https vira http.ErrSchemeMismatch.
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
	if errors.Is(err, syscall.ECONNREFUSED) {
		return DiagConnRefused
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return DiagConnTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return DiagConnTimeout
	}
	switch {
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE),
		errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH),
		errors.Is(err, syscall.ENETDOWN), errors.Is(err, syscall.ECONNABORTED):
		return DiagConnError
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return DiagConnError
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "no such host"), strings.Contains(s, "server misbehaving"):
		return DiagDNSError
	case strings.Contains(s, "x509"), strings.Contains(s, "certificate"), strings.Contains(s, "tls:"):
		return DiagTLS
	case strings.Contains(s, "connection refused"):
		return DiagConnRefused
	case strings.Contains(s, "timeout"), strings.Contains(s, "deadline exceeded"):
		return DiagConnTimeout
	case strings.Contains(s, "no route to host"), strings.Contains(s, "connection reset"),
		strings.Contains(s, "network is unreachable"):
		return DiagConnError
	}
	return DiagNaoClassif
}

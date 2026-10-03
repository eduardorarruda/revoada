package sitecheck

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProbeOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("bem-vindo à Revoada"))
	}))
	defer srv.Close()

	res := NewProber().Probe(context.Background(), Target{URL: srv.URL, ExpectStatus: 200, Keyword: "Revoada"})
	if !res.OK || res.Diagnosis != "" {
		t.Fatalf("esperava OK, obteve ok=%v diag=%q", res.OK, res.Diagnosis)
	}
	if res.TotalMs <= 0 {
		t.Error("total_ms deveria ser > 0")
	}
	if res.Truncated {
		t.Error("corpo minúsculo não pode ser marcado como truncado")
	}
}

func TestProbeKeywordMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("outra coisa"))
	}))
	defer srv.Close()
	res := NewProber().Probe(context.Background(), Target{URL: srv.URL, ExpectStatus: 200, Keyword: "Revoada"})
	if res.OK || res.Diagnosis != DiagKeywordAusent {
		t.Fatalf("esperava keyword_missing, obteve ok=%v diag=%q", res.OK, res.Diagnosis)
	}
}

func TestProbeHTTP5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	res := NewProber().Probe(context.Background(), Target{URL: srv.URL, ExpectStatus: 200})
	if res.OK || res.Diagnosis != DiagHTTP5xx {
		t.Fatalf("esperava http_5xx, obteve ok=%v diag=%q status=%d", res.OK, res.Diagnosis, res.Status)
	}
}

func TestProbeSlowDegraded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(60 * time.Millisecond)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	res := NewProber().Probe(context.Background(), Target{URL: srv.URL, ExpectStatus: 200, MaxLatencyMS: 20})
	if !res.OK || !res.Degraded || res.Diagnosis != DiagSlow {
		t.Fatalf("esperava OK+degraded+slow, obteve ok=%v deg=%v diag=%q", res.OK, res.Degraded, res.Diagnosis)
	}
}

func TestProbeTLSInvalid(t *testing.T) {
	// httptest TLS usa certificado auto-assinado → validação TLS deve falhar.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	res := NewProber().Probe(context.Background(), Target{URL: srv.URL, ExpectStatus: 200})
	if res.OK || res.Diagnosis != DiagTLS {
		t.Fatalf("esperava diagnóstico tls (cert auto-assinado), obteve ok=%v diag=%q", res.OK, res.Diagnosis)
	}
}

func TestProbeDNSError(t *testing.T) {
	res := NewProber().Probe(context.Background(), Target{URL: "http://nao-existe.invalido.revoada/", ExpectStatus: 200})
	if res.OK || res.Diagnosis != DiagDNSError {
		t.Fatalf("esperava dns_error, obteve ok=%v diag=%q", res.OK, res.Diagnosis)
	}
}

// =========================================================================
// CLASSIFICAÇÃO DE FALHA (defeito 2): o `default` devolvia connect_timeout,
// então TODO erro imprevisto virava "timeout". Medido no dev: 1.172 registros
// `connect_timeout` com avg(total_ms) = 0,33 ms — um "timeout" que resolve em
// 0,3 ms é a assinatura do erro de classificação, não da rede.
// Os testes abaixo usam erros de rede REAIS, não mensagens sintéticas.
// =========================================================================

// portaFechada abre e fecha um listener, devolvendo um endereço onde ninguém
// atende — a origem real de ECONNREFUSED.
func portaFechada(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listener: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func TestClassificaPortaFechadaComoRecusa(t *testing.T) {
	res := NewProber().Probe(context.Background(), Target{URL: "http://" + portaFechada(t) + "/", ExpectStatus: 200})
	if res.Diagnosis != DiagConnRefused {
		t.Fatalf("porta fechada deveria virar connect_refused, veio %q (total=%.2f ms)", res.Diagnosis, res.TotalMs)
	}
	// Prova do defeito original: se ainda fosse "connect_timeout", o tempo o
	// desmentiria — nenhum timeout de 15 s resolve em poucos milissegundos.
	if res.TotalMs > 1000 {
		t.Errorf("recusa imediata não deveria levar %.0f ms", res.TotalMs)
	}
}

func TestClassificaTCPQueNuncaFalaTLS(t *testing.T) {
	// Servidor TCP que aceita a conexão e nunca fala TLS. Antes caía no default →
	// connect_timeout. Duas variantes reais, com erros diferentes da stdlib:
	//  - lixo binário            → tls.RecordHeaderError
	//  - resposta HTTP em claro  → http.ErrSchemeMismatch
	casos := map[string]string{
		"lixo binário":             "LIXO QUE NAO E TLS NEM HTTP",
		"HTTP em claro na porta s": "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\noi",
	}
	for nome, resposta := range casos {
		t.Run(nome, func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listener: %v", err)
			}
			defer l.Close()
			go func() {
				for {
					conn, err := l.Accept()
					if err != nil {
						return
					}
					_, _ = conn.Write([]byte(resposta))
					_ = conn.Close()
				}
			}()

			res := NewProber().Probe(context.Background(), Target{URL: "https://" + l.Addr().String() + "/", ExpectStatus: 200})
			if res.Diagnosis != DiagTLS {
				t.Fatalf("TCP que aceita e não fala TLS deveria virar tls, veio %q", res.Diagnosis)
			}
		})
	}
}

func TestClassificaBloqueioDoGuardComoNossaRecusa(t *testing.T) {
	// 169.254.169.254 é o metadata da nuvem: SEMPRE bloqueado pelo safehttp. O
	// alvo não pode ser acusado por uma recusa nossa, então o diagnóstico é
	// `bloqueado_pelo_painel` e Blocked=true (o checker não deriva DOWN disso).
	res := NewProber().Probe(context.Background(), Target{URL: "http://169.254.169.254/latest/meta-data/", ExpectStatus: 200})
	if res.Diagnosis != DiagBloqueado {
		t.Fatalf("bloqueio do guard deveria virar %q, veio %q", DiagBloqueado, res.Diagnosis)
	}
	if !res.Blocked {
		t.Error("Blocked precisa estar marcado: recusa nossa não é falha do alvo")
	}
}

func TestClassificaTimeoutDeVerdade(t *testing.T) {
	// Servidor que aceita e nunca responde: aqui timeout é o diagnóstico CERTO.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listener: %v", err)
	}
	defer l.Close()
	parar := make(chan struct{})
	defer close(parar)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() { <-parar; _ = conn.Close() }()
		}
	}()

	res := NewProber().Probe(context.Background(), Target{
		URL: "http://" + l.Addr().String() + "/", ExpectStatus: 200, TimeoutMS: 300,
	})
	// Aceitou a conexão e nunca falou: desde a 0.9 isso é `sem_resposta` (o
	// alvo existe e está mudo), distinto de `connect_timeout` (o pedido de
	// conexão nem foi respondido). Os dois continuam sendo falha do alvo.
	if res.Diagnosis != DiagSemResposta {
		t.Fatalf("servidor mudo deveria virar sem_resposta, veio %q", res.Diagnosis)
	}
	if res.TimeoutMs != 300 {
		t.Errorf("o limite que valeu tem de sair no resultado: timeout_ms=%d", res.TimeoutMs)
	}
}

func TestClassifyErrNuncaInventaTimeout(t *testing.T) {
	// Erro que nenhuma regra reconhece: precisa virar `unclassified`, não o antigo
	// `default: connect_timeout`.
	if got := classifyErr(&net.OpError{Op: "dial", Err: errDesconhecido{}}); got != DiagNaoClassificado {
		t.Fatalf("erro imprevisto deveria virar %q, veio %q", DiagNaoClassificado, got)
	}
}

type errDesconhecido struct{}

func (errDesconhecido) Error() string { return "falha estranha e inédita" }

func TestURLInvalidaNaoViraDiagnosticoDeResposta(t *testing.T) {
	// Sem esquema não há requisição: `http_status` afirmava algo sobre uma
	// resposta que nunca existiu.
	res := NewProber().Probe(context.Background(), Target{URL: "://sem-esquema", ExpectStatus: 200})
	if res.Diagnosis != DiagBadURL {
		t.Fatalf("URL inválida deveria virar bad_url, veio %q", res.Diagnosis)
	}
}

// =========================================================================
// TRUNCAGEM (defeito 5): palavra-chave logo APÓS o teto de leitura devolvia
// ok=false diagnosis=keyword_missing status=200 — site no ar, HTTP 200, painel
// declarando falha (e, com 2 seguidas, DOWN + notificação).
// =========================================================================

// Palavra-chave ALÉM do teto de leitura: o corte não pode virar afirmação de ausência.
//
// O teto foi unificado em MaxBodyBytes (512 KiB) para casar com o do agente — sem
// isso as duas sondas discordavam por construção e o consenso derrubava site no ar.
// O que torna o teto menor seguro é justamente este contrato: além dele o resultado é
// "não sei" (DiagKeywordIndet) com Truncated marcado, e não "a palavra não existe".
func TestKeywordAlemDoTetoNaoAfirmaAusencia(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", MaxBodyBytes+4096)))
		_, _ = w.Write([]byte("PALAVRA-DEPOIS-DO-TETO"))
	}))
	defer srv.Close()

	res := NewProber().Probe(context.Background(), Target{
		URL: srv.URL, ExpectStatus: 200, Keyword: "PALAVRA-DEPOIS-DO-TETO",
	})
	if res.Diagnosis != DiagKeywordIndet {
		t.Fatalf("corpo truncado tem de virar %q (não sei), veio %q", DiagKeywordIndet, res.Diagnosis)
	}
	if !res.Truncated {
		t.Error("truncado precisa marcar que total_ms virou um piso")
	}
	if res.Diagnosis == DiagKeywordAusent {
		t.Error("afirmar ausência sobre corpo que não foi lido inteiro é a mentira que este teto criava")
	}
}

// Palavra-chave DENTRO do teto continua sendo encontrada bem além do antigo limite de
// 1 MiB de POSIÇÃO — o defeito original era enxergar só o começo do corpo.
func TestKeywordLongeDoInicioMasDentroDoTeto(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", MaxBodyBytes-8192)))
		_, _ = w.Write([]byte("PALAVRA-LA-NO-FIM"))
	}))
	defer srv.Close()

	res := NewProber().Probe(context.Background(), Target{
		URL: srv.URL, ExpectStatus: 200, Keyword: "PALAVRA-LA-NO-FIM",
	})
	if !res.OK {
		t.Fatalf("palavra dentro do teto deveria ser OK, veio ok=%v diag=%q", res.OK, res.Diagnosis)
	}
}

func TestKeywordPartidaEntreBlocos(t *testing.T) {
	// A palavra cai exatamente na fronteira de dois blocos de leitura (32 KiB): sem
	// a janela de sobreposição, o streaming reintroduziria o falso keyword_missing.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("y", (32<<10)-5)))
		_, _ = w.Write([]byte("FRONTEIRA"))
		_, _ = w.Write([]byte(strings.Repeat("y", 1024)))
	}))
	defer srv.Close()

	res := NewProber().Probe(context.Background(), Target{URL: srv.URL, ExpectStatus: 200, Keyword: "FRONTEIRA"})
	if !res.OK {
		t.Fatalf("palavra partida entre blocos deveria ser encontrada: ok=%v diag=%q", res.OK, res.Diagnosis)
	}
}

func TestCorpoTruncadoViraIndeterminadoNaoAusente(t *testing.T) {
	// Corpo maior que maxCorpo sem a palavra: não sabemos se ela existe adiante, e
	// afirmar "keyword_missing" seria declarar falha de um site no ar (HTTP 200).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		bloco := strings.Repeat("z", 1<<20)
		for i := 0; i < (maxCorpo>>20)+2; i++ {
			if _, err := w.Write([]byte(bloco)); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	res := NewProber().Probe(context.Background(), Target{URL: srv.URL, ExpectStatus: 200, Keyword: "NUNCA-APARECE"})
	if res.Diagnosis != DiagKeywordIndet {
		t.Fatalf("corpo truncado deveria virar %q, veio %q", DiagKeywordIndet, res.Diagnosis)
	}
	if !res.Truncated {
		t.Error("Truncated precisa marcar que total_ms virou um piso")
	}
}

// =========================================================================
// TOTAL vs TTFB e max_latency_ms (defeito 8, segunda parte)
// =========================================================================

func TestMaxLatencyComparaContraOTotalCorrigido(t *testing.T) {
	// Cabeçalho instantâneo, corpo lento: com o cronômetro parando no cabeçalho, a
	// página era avaliada em ~1 ms e o limite do usuário nunca disparava.
	const atraso = 200 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(atraso)
		_, _ = w.Write([]byte("corpo que demorou"))
	}))
	defer srv.Close()

	res := NewProber().Probe(context.Background(), Target{URL: srv.URL, ExpectStatus: 200, MaxLatencyMS: 100})
	if !res.Degraded || res.Diagnosis != DiagSlow {
		t.Fatalf("limite de 100 ms com corpo de %v deveria marcar slow: deg=%v diag=%q total=%.1f",
			atraso, res.Degraded, res.Diagnosis, res.TotalMs)
	}
	if res.TotalMs <= res.TTFBMs {
		t.Errorf("total (%.1f) tem de ser maior que o TTFB (%.1f) — senão é o TTFB com outro nome",
			res.TotalMs, res.TTFBMs)
	}
}

func TestTimeoutPadraoEExplicado(t *testing.T) {
	if (Target{}).Timeout() != DefaultTimeout {
		t.Fatalf("timeout padrão deveria ser %v", DefaultTimeout)
	}
	if got := TimeoutDiagnosisText(DefaultTimeout); got != "sem resposta em 20 s" {
		t.Fatalf("texto do timeout = %q", got)
	}
	if (Target{TimeoutMS: 3000}).Timeout() != 3*time.Second {
		t.Fatal("timeout por check deveria prevalecer sobre o padrão")
	}
}

// TLS válido só serve para garantir que o caminho feliz de HTTPS não regrediu na
// nova classificação por tipo (o certificado do httptest é auto-assinado, então
// aqui montamos um client que confia nele).
func TestTLSValidoNaoViraDiagnostico(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	p := NewProber()
	tr := p.client.Transport.(*http.Transport)
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
	res := p.Probe(context.Background(), Target{URL: srv.URL, ExpectStatus: 200})
	if !res.OK {
		t.Fatalf("HTTPS válido deveria passar: diag=%q", res.Diagnosis)
	}
	if res.CertDaysLeft <= 0 {
		t.Error("cert_days_left deveria ser medido em HTTPS")
	}
}

// TestClassificaConectouMasNaoRespondeu: TCP e TLS abriram e a página nunca veio.
// É a assinatura de servidor com a fila cheia (Apache no teto de workers, PHP-FPM
// sem filhos livres), vista no WHM em 25/09. Saía como "connect_timeout", o mesmo
// rótulo de quando o pedido de conexão nem é respondido — e a diferença é a que
// diz o que consertar.
func TestClassificaConectouMasNaoRespondeu(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()
	p := NewProber()
	tr := p.client.Transport.(*http.Transport)
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}

	res := p.Probe(context.Background(), Target{URL: srv.URL, TimeoutMS: 500})
	if res.OK || res.Diagnosis != DiagSemResposta {
		t.Fatalf("quero %q, obtive ok=%v diag=%q (conn=%v tls=%v ttfb=%v)", DiagSemResposta, res.OK, res.Diagnosis, res.ConnectMs, res.TLSMs, res.TTFBMs)
	}
	if res.ConnectMs <= 0 || res.TLSMs <= 0 || res.TTFBMs != 0 {
		t.Errorf("as fases precisam contar a história: conn=%v tls=%v ttfb=%v", res.ConnectMs, res.TLSMs, res.TTFBMs)
	}
	// Continua sendo falha do alvo (o usuário não consegue usar), não abstenção.
	if DiagnosisIsUnmeasured(res.Diagnosis) {
		t.Error("sem_resposta é falha medida, não abstenção")
	}
}

// Visto na CI: conexão e TLS completos, o primeiro byte chegou no MESMO instante
// em que o prazo estourou (ttfb=501 ms com limite de 500) e o client.Do voltou com
// erro de prazo. O TTFB já gravado impedia a troca e o diagnóstico saía
// "connect_timeout" — acusando a conexão de um site que conectou e não respondeu
// a tempo.
func TestSemRespostaNoLimiteDoPrazo(t *testing.T) {
	const https = "https://exemplo.com"
	base := Result{Diagnosis: DiagConnTimeout, ConnectMs: 0.1, TLSMs: 2, TLSOK: true, TimeoutMs: 500}
	casos := []struct {
		nome string
		r    Result
		url  string
		quer bool
	}{
		{"sem primeiro byte", base, https, true},
		{"primeiro byte no instante do prazo", comTTFB(base, 501), https, true},
		{"primeiro byte bem antes do prazo (outra falha)", comTTFB(base, 120), https, false},
		{"não conectou", Result{Diagnosis: DiagConnTimeout, TimeoutMs: 500}, https, false},
		{"https sem TLS completo", Result{Diagnosis: DiagConnTimeout, ConnectMs: 0.1, TimeoutMs: 500}, https, false},
		{"http sem TLS", Result{Diagnosis: DiagConnTimeout, ConnectMs: 0.1, TimeoutMs: 500}, "http://exemplo.com", true},
		{"outro diagnóstico", Result{Diagnosis: DiagSemResposta, ConnectMs: 0.1, TLSOK: true, TimeoutMs: 500}, https, false},
	}
	for _, c := range casos {
		if got := conectouSemResponder(c.r, c.url); got != c.quer {
			t.Errorf("%s: conectouSemResponder = %v; esperava %v", c.nome, got, c.quer)
		}
	}
}

func comTTFB(r Result, ms float64) Result { r.TTFBMs = ms; return r }

// As mensagens do Windows (WSAECONNREFUSED, WSAECONNRESET) não casam com os errno
// do syscall: sem o texto, porta fechada e conexão derrubada viravam "unclassified"
// num painel rodando no Windows.
func TestClassificaMensagensDoWindows(t *testing.T) {
	casos := map[string]string{
		"dial tcp 127.0.0.1:9: connectex: No connection could be made because the target machine actively refused it.":     DiagConnRefused,
		"read tcp 127.0.0.1:50000->127.0.0.1:443: wsarecv: An existing connection was forcibly closed by the remote host.": DiagConnError,
	}
	for msg, quer := range casos {
		if got := classifyErr(errors.New(msg)); got != quer {
			t.Errorf("%q: queria %q, veio %q", msg, quer, got)
		}
	}
}

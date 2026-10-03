package probe

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func alvo(u string) assignment { return assignment{URL: u, ExpectStatus: 200} }

// O caso que motivou a correção: um site que responde o CABEÇALHO na hora e leva
// segundos para entregar o corpo. Antes, o cronômetro parava no cabeçalho e o
// painel dizia "80ms" para quem estava reclamando de lentidão.
func TestTotalMsIncluiOTempoDeTransferirOCorpo(t *testing.T) {
	const atraso = 300 * time.Millisecond

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush() // cabeçalho já foi: aqui o client.Do retorna
		time.Sleep(atraso)
		_, _ = w.Write([]byte("<html>corpo que demorou</html>"))
	}))
	defer srv.Close()

	r := NewReporter("http://gateway.invalido", "chave", "teste", nil)
	res := r.probe(context.Background(), alvo(srv.URL))

	if !res.Up {
		t.Fatalf("200 deveria contar como no ar: %+v", res)
	}
	if min := float64(atraso.Milliseconds()); res.TotalMs < min {
		t.Errorf("total_ms = %.1f, esperava pelo menos %.0f — o corpo não entrou na conta", res.TotalMs, min)
	}
}

func TestProbeClassificaOResultado(t *testing.T) {
	fora := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer fora.Close()

	r := NewReporter("http://gateway.invalido", "chave", "teste", nil)

	res := r.probe(context.Background(), alvo(fora.URL))
	if res.Up {
		t.Error("502 não é estar no ar")
	}
	// Vocabulário unificado com a central: `http_5xx`, não `http_502`.
	if res.Diagnostic != DiagHTTP5xx {
		t.Errorf("diagnóstico = %q, esperava %q", res.Diagnostic, DiagHTTP5xx)
	}
	if res.Status != http.StatusBadGateway {
		t.Errorf("o status observado tem de ser reportado, veio %d", res.Status)
	}

	// URL impossível de montar: nem chega a sair da máquina, então não há tempo a medir.
	if res := r.probe(context.Background(), alvo("://sem-esquema")); res.Up || res.Diagnostic != DiagBadURL {
		t.Errorf("URL inválida deveria virar bad_url, veio %+v", res)
	}

	// Host que não resolve: o diagnóstico tem de dizer o motivo, não "unreachable" genérico.
	if res := r.probe(context.Background(), alvo("http://nao-existe.invalid.teste-revoada/")); res.Diagnostic != DiagDNSError {
		t.Errorf("host inexistente deveria virar dns_error, veio %q", res.Diagnostic)
	}
}

// O corpo é lido com teto: uma resposta gigante não pode fazer a sonda baixar o
// arquivo inteiro nem travar o ciclo — mas AGORA isso é marcado (`truncated`),
// porque `total_ms` vira um piso.
func TestCorpoGiganteNaoDerrubaASonda(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		bloco := strings.Repeat("x", 1<<20) // 1 MiB por escrita
		for i := 0; i < 16; i++ {           // 16 MiB, muito acima do teto
			if _, err := w.Write([]byte(bloco)); err != nil {
				return // a sonda parou de ler: é exatamente o que o teto faz
			}
		}
	}))
	defer srv.Close()

	r := NewReporter("http://gateway.invalido", "chave", "teste", nil)
	feito := make(chan result, 1)
	go func() { feito <- r.probe(context.Background(), alvo(srv.URL)) }()

	select {
	case res := <-feito:
		if !res.Up {
			t.Errorf("200 deveria contar como no ar mesmo com corpo grande: %+v", res)
		}
		if !res.Truncated {
			t.Error("corpo acima do teto tem de sair marcado como truncado — senão total_ms é publicado como se fosse completo")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a sonda travou lendo o corpo — o teto não está sendo aplicado")
	}
}

// =========================================================================
// PARIDADE COM A SONDA CENTRAL (defeito 3): o agente decidia por
// `resp.StatusCode < 400`, ignorando expect_status e keyword. Um check com
// expect_status=401 (página de login legítima) era OK para a central e falho
// para toda sonda remota → DEGRADADO permanente sem causa real.
// =========================================================================

func TestExpectStatus401NaoEhFalhaNaSonda(t *testing.T) {
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("faça login"))
	}))
	defer login.Close()

	r := NewReporter("http://gateway.invalido", "chave", "teste", nil)
	res := r.probe(context.Background(), assignment{URL: login.URL, ExpectStatus: 401})
	if !res.Up {
		t.Fatalf("401 esperado é sucesso, igual à central: %+v", res)
	}

	// E o contrário: 200 onde se esperava 401 também é falha, como na central.
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ok.Close()
	if res := r.probe(context.Background(), assignment{URL: ok.URL, ExpectStatus: 401}); res.Up {
		t.Error("200 onde se esperava 401 deveria falhar")
	}
}

func TestKeywordEhAplicadaNaSonda(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>bem-vindo à Revoada</html>"))
	}))
	defer srv.Close()

	r := NewReporter("http://gateway.invalido", "chave", "teste", nil)
	if res := r.probe(context.Background(), assignment{URL: srv.URL, ExpectStatus: 200, Keyword: "Revoada"}); !res.Up {
		t.Fatalf("palavra presente deveria dar no ar: %+v", res)
	}
	res := r.probe(context.Background(), assignment{URL: srv.URL, ExpectStatus: 200, Keyword: "NAO-EXISTE"})
	if res.Up || res.Diagnostic != DiagKeywordAusent {
		t.Fatalf("palavra ausente deveria virar %q, veio %+v", DiagKeywordAusent, res)
	}
}

func TestCorpoTruncadoNaoAcusaKeywordAusente(t *testing.T) {
	// Palavra depois do teto de 512 KiB: afirmar keyword_missing seria declarar
	// falha de um site no ar (HTTP 200).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxCorpoSonda+4096)))
		_, _ = w.Write([]byte("PALAVRA-LA-NO-FIM"))
	}))
	defer srv.Close()

	r := NewReporter("http://gateway.invalido", "chave", "teste", nil)
	res := r.probe(context.Background(), assignment{URL: srv.URL, ExpectStatus: 200, Keyword: "PALAVRA-LA-NO-FIM"})
	if res.Diagnostic != DiagKeywordIndet {
		t.Fatalf("corpo truncado deveria virar %q, veio %q", DiagKeywordIndet, res.Diagnostic)
	}
	if !res.Truncated {
		t.Error("truncated precisa marcar que total_ms é um piso")
	}
}

func TestKeywordPartidaEntreBlocos(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("y", (32<<10)-4)))
		_, _ = w.Write([]byte("FRONTEIRA"))
		_, _ = w.Write([]byte(strings.Repeat("y", 512)))
	}))
	defer srv.Close()

	r := NewReporter("http://gateway.invalido", "chave", "teste", nil)
	if res := r.probe(context.Background(), assignment{URL: srv.URL, ExpectStatus: 200, Keyword: "FRONTEIRA"}); !res.Up {
		t.Fatalf("palavra partida entre blocos deveria ser encontrada: %+v", res)
	}
}

// =========================================================================
// SSRF (defeito 4): a sonda usava `&http.Client{Timeout: 15s}` puro. Medido:
// http://169.254.169.254/latest/meta-data/ ficou pendurado 15 s tentando de
// verdade; pela sonda central é recusado em 0 ms. A URL é digitada pelo usuário
// e a sonda roda DENTRO da rede do cliente.
// =========================================================================

func TestSondaRecusaMetadataDaNuvemNaHora(t *testing.T) {
	r := NewReporter("http://gateway.invalido", "chave", "teste", nil)
	inicio := time.Now()
	res := r.probe(context.Background(), alvo("http://169.254.169.254/latest/meta-data/"))
	gasto := time.Since(inicio)

	if res.Diagnostic != DiagBloqueado {
		t.Fatalf("metadata da nuvem deveria virar %q, veio %q", DiagBloqueado, res.Diagnostic)
	}
	if res.Up {
		t.Error("destino bloqueado nunca é 'no ar'")
	}
	// Não é só o diagnóstico: o ponto é NÃO tentar. 1 s é folga generosa contra os
	// 15 s medidos antes da correção.
	if gasto > time.Second {
		t.Errorf("a sonda levou %v — deveria recusar antes de conectar", gasto)
	}
}

func TestSondaRecusaEsquemaNaoHTTP(t *testing.T) {
	r := NewReporter("http://gateway.invalido", "chave", "teste", nil)
	if res := r.probe(context.Background(), alvo("file:///etc/passwd")); res.Up {
		t.Fatalf("file:// nunca pode ser sondado: %+v", res)
	}
}

func TestClassifyErrDaSondaNaoInventaTimeout(t *testing.T) {
	// Porta fechada: erro real de ECONNREFUSED. Antes o agente até acertava por
	// substring ("refused"), mas o default ("unreachable") engolia o resto.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listener: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	r := NewReporter("http://gateway.invalido", "chave", "teste", nil)
	if res := r.probe(context.Background(), alvo("http://"+addr+"/")); res.Diagnostic != DiagConnRefused {
		t.Fatalf("porta fechada deveria virar %q, veio %q", DiagConnRefused, res.Diagnostic)
	}

	if got := classifyErr(&net.OpError{Op: "dial", Err: errEstranho{}}); got != DiagNaoClassif {
		t.Fatalf("erro imprevisto deveria virar %q, veio %q", DiagNaoClassif, got)
	}
}

type errEstranho struct{}

func (errEstranho) Error() string { return "falha estranha e inédita" }

// A tabela de diagnósticos é copiada de server/internal/sitecheck/diagnosis.go.
// Este teste trava a lista: mudar um valor aqui sem mudar lá (ou vice-versa)
// quebra, e o painel volta a mostrar dois vocabulários lado a lado.
func TestVocabularioCanonicoTravado(t *testing.T) {
	esperado := []string{
		"dns_error", "connect_refused", "connect_timeout", "connect_error", "tls",
		"http_5xx", "http_status", "keyword_missing", "keyword_indeterminado",
		"slow", "bad_url", "bloqueado_pelo_painel", "unclassified",
	}
	if len(diagnosticosCanonicos) != len(esperado) {
		t.Fatalf("a tabela tem %d itens, esperava %d", len(diagnosticosCanonicos), len(esperado))
	}
	for i, d := range esperado {
		if diagnosticosCanonicos[i] != d {
			t.Errorf("diagnóstico %d = %q, esperava %q", i, diagnosticosCanonicos[i], d)
		}
	}
}

// O assignment precisa carregar as asserções do check; se o gateway voltar a
// mandar só a URL, o agente cai no default (200, sem palavra) — que é o mesmo
// default da central, não uma regra própria.
func TestAssignmentsTrazAsAssercoes(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/probe/assignments" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"urls":[{"url":"https://ex.test/","expect_status":401,"keyword":"login"}]}`))
	}))
	defer gw.Close()

	r := NewReporter(gw.URL, "chave", "sp-saopaulo", nil)
	got := r.assignments(context.Background())
	if len(got) != 1 {
		t.Fatalf("esperava 1 designação, veio %d", len(got))
	}
	if got[0].ExpectStatus != 401 || got[0].Keyword != "login" {
		t.Fatalf("asserções não chegaram: %+v", got[0])
	}
}

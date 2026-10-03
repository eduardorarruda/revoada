package hostadmin

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eduardorarruda/revoada/server/internal/chquery"
	"github.com/eduardorarruda/revoada/server/internal/provision"
)

func TestQuoteEscaping(t *testing.T) {
	if got := quote("srv-01"); got != "'srv-01'" {
		t.Errorf("quote simples: %s", got)
	}
	if got := quote("a'b"); got != `'a\'b'` {
		t.Errorf("quote com aspa: %s", got)
	}
}

func TestDedupNonEmpty(t *testing.T) {
	got := dedupNonEmpty("srv-01", "", "  Apelido  ", "srv-01")
	want := []string{"srv-01", "Apelido"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// fakeCH captura os statements enviados ao ClickHouse e responde conforme a rota
// (nil = respostas vazias, como antes).
func fakeCH(t *testing.T, capture *[]string, route ...func(string) string) *chquery.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*capture = append(*capture, string(body))
		w.Header().Set("Content-Type", "application/json")
		if len(route) > 0 && route[0] != nil {
			_, _ = io.WriteString(w, route[0](string(body)))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return chquery.New(srv.URL, "", "", "default")
}

func TestPurgeClickHouseAllTables(t *testing.T) {
	var stmts []string
	ch := fakeCH(t, &stmts, func(q string) string {
		if strings.Contains(q, "system.mutations") {
			return `{"mutation_id":"m1","is_done":"1","parts_to_do":"0","latest_fail_reason":""}`
		}
		if strings.Contains(q, "count()") {
			return `{"c":"7"}`
		}
		return ""
	})
	h := &Handler{ch: ch, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	states, rows, failed := h.purgeClickHouse(context.Background(), "srv-01")
	if len(failed) != 0 {
		t.Fatalf("nenhuma tabela deveria falhar, falharam: %v", failed)
	}
	// Uma statement de DELETE por tabela base, todas presas ao host E ao tenant. O
	// tenant no predicado é o que impede que apagar `web01` de um cliente leve junto o
	// `web01` de outro no dia em que houver um segundo.
	joined := strings.Join(stmts, "\n")
	for _, tbl := range chTables {
		esperado := "ALTER TABLE " + tbl + " DELETE WHERE tenant_id='default' AND labels['host']='srv-01'"
		if !strings.Contains(joined, esperado) {
			t.Errorf("faltou DELETE por host na tabela %s; got:\n%s", tbl, joined)
		}
	}
	// A mutation conferida é a NOSSA: a consulta a system.mutations casa o hostname
	// dentro do comando, e não "a mais recente da tabela".
	if !strings.Contains(joined, "command LIKE '%srv-01%'") {
		t.Errorf("a consulta de mutation precisa casar o host, senão lê a exclusão do vizinho:\n%s", joined)
	}
	// Antes, o handler NUNCA consultava system.mutations e já dizia "apagado".
	if !strings.Contains(joined, "system.mutations") {
		t.Error("o resultado da mutation precisa ser CONFERIDO, não presumido")
	}
	if len(states) != len(chTables) {
		t.Fatalf("esperado estado das %d tabelas, obtido %d", len(chTables), len(states))
	}
	for _, st := range states {
		if !st.Done || st.MutationID != "m1" {
			t.Errorf("estado inesperado de %s: %+v", st.Table, st)
		}
	}
	// Contagem antes do DELETE: 7 linhas por tabela (é o deleted_rows da trilha).
	if want := int64(7 * len(chTables)); rows != want {
		t.Errorf("linhas contadas = %d; quer %d", rows, want)
	}
}

// TestPurgeClickHouseMutationFalhou: mutation aceita e depois travada (disco cheio,
// part corrompida) NÃO pode virar "apagado". Este é o caso em que o painel dizia ao
// operador que o servidor do cliente tinha sido limpo.
func TestPurgeClickHouseMutationFalhou(t *testing.T) {
	var stmts []string
	ch := fakeCH(t, &stmts, func(q string) string {
		if strings.Contains(q, "system.mutations") {
			return `{"mutation_id":"m9","is_done":"0","parts_to_do":"3","latest_fail_reason":"disk full"}`
		}
		if strings.Contains(q, "count()") {
			return `{"c":"1"}`
		}
		return ""
	})
	h := &Handler{ch: ch, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	states, _, _ := h.purgeClickHouse(context.Background(), "srv-01")
	for _, st := range states {
		if st.Done {
			t.Errorf("%s: mutation travada não está concluída", st.Table)
		}
		if st.FailReason != "disk full" {
			t.Errorf("%s: motivo da falha perdido: %+v", st.Table, st)
		}
	}
}

// TestPurgeClickHouseComandoRecusado: tabela cujo ALTER nem foi aceito não pode
// herdar o estado de uma mutation ANTERIOR (de outra exclusão) e aparecer como
// concluída. Com o ClickHouse inalcançável, TODAS as tabelas caem nesse caso.
func TestPurgeClickHouseComandoRecusado(t *testing.T) {
	ch := chquery.New("http://127.0.0.1:1", "", "", "default") // porta fechada
	h := &Handler{ch: ch, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	states, rows, failed := h.purgeClickHouse(context.Background(), "srv-01")
	if len(failed) != len(chTables) {
		t.Fatalf("todas as tabelas deveriam falhar, falharam: %v", failed)
	}
	if rows != 0 {
		t.Errorf("sem banco não há contagem: %d", rows)
	}
	if len(states) != len(chTables) {
		t.Fatalf("esperado estado das %d tabelas, obtido %d", len(chTables), len(states))
	}
	for _, st := range states {
		if st.Done || st.FailReason == "" {
			t.Errorf("%s: comando recusado não pode aparecer como concluído: %+v", st.Table, st)
		}
	}
}

// fakeRunner / fakeSession implementam as interfaces de provision para testar a
// desinstalação sem SSH real.
type fakeSession struct {
	out    string
	runErr error
}

func (s *fakeSession) Run(ctx context.Context, cmd string) (string, error) { return s.out, s.runErr }
func (s *fakeSession) HostKey() string                                     { return "SHA256:fake" }
func (s *fakeSession) Close() error                                        { return nil }

type fakeRunner struct {
	sess       *fakeSession
	connectErr error
}

func (r *fakeRunner) Connect(ctx context.Context, t provision.Target) (provision.Session, error) {
	if r.connectErr != nil {
		return nil, r.connectErr
	}
	return r.sess, nil
}

func newHandler(runner provision.Runner) *Handler {
	return &Handler{runner: runner, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestUninstallNoCredential(t *testing.T) {
	h := newHandler(&fakeRunner{})
	if got := h.uninstallAgent(context.Background(), nil, nil); got != "pulado (sem credencial)" {
		t.Errorf("sem credencial: %q", got)
	}
}

func TestUninstallOK(t *testing.T) {
	h := newHandler(&fakeRunner{sess: &fakeSession{out: "...\nUNINSTALL_OK\n"}})
	creds := &sshCreds{Host: "1.2.3.4", User: "root", AuthType: "password", Secret: "s3cr3t"}
	if got := h.uninstallAgent(context.Background(), nil, creds); got != "ok" {
		t.Errorf("uninstall ok esperado, obtido %q", got)
	}
}

func TestUninstallConnectFails(t *testing.T) {
	h := newHandler(&fakeRunner{connectErr: errors.New("timeout s3cr3t")})
	creds := &sshCreds{Host: "1.2.3.4", User: "root", AuthType: "password", Secret: "s3cr3t"}
	got := h.uninstallAgent(context.Background(), nil, creds)
	if !strings.HasPrefix(got, "falhou: conexão SSH") {
		t.Errorf("esperado falha de conexão, obtido %q", got)
	}
	if strings.Contains(got, "s3cr3t") {
		t.Errorf("o segredo vazou na mensagem: %q", got)
	}
}

func TestUninstallNoConfirmation(t *testing.T) {
	h := newHandler(&fakeRunner{sess: &fakeSession{out: "algo deu errado, sem marcador"}})
	creds := &sshCreds{Host: "1.2.3.4", User: "root", AuthType: "password", Secret: "x"}
	got := h.uninstallAgent(context.Background(), nil, creds)
	if !strings.Contains(got, "não confirmou") {
		t.Errorf("esperado 'não confirmou', obtido %q", got)
	}
}

// TestContarSobrasSomaAsSeisTabelas cobre a metade ClickHouse da varredura de
// rescaldo: ela só faz trabalho quando SOBROU coisa, e "sobrou" é a soma das seis
// tabelas. Se a contagem olhasse só `metrics`, o resíduo que a janela do cache
// deixou em `logs` ficaria lá para sempre — que é exatamente o furo original.
func TestContarSobrasSomaAsSeisTabelas(t *testing.T) {
	var stmts []string
	ch := fakeCH(t, &stmts, func(q string) string {
		if strings.Contains(q, "FROM logs") {
			return `{"c":"7"}` + "\n"
		}
		if strings.Contains(q, "SELECT count()") {
			return `{"c":"0"}` + "\n"
		}
		return ""
	})
	h := NewHandler(nil, ch, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	got, mediuTudo := h.contarSobrasCH(context.Background(), "srv-01")
	if got != 7 {
		t.Errorf("sobras: got %d, want 7", got)
	}
	if !mediuTudo {
		t.Error("todas as tabelas responderam; a varredura tinha de se dar por completa")
	}
	if len(stmts) != len(chTables) {
		t.Errorf("deveria contar uma vez por tabela: %d consultas para %d tabelas", len(stmts), len(chTables))
	}
	for _, s := range stmts {
		if !strings.Contains(s, "labels['host']='srv-01'") {
			t.Errorf("contagem sem filtro de host: %q", s)
		}
	}
}

// TestContarSobrasZeroNaoPedeExpurgo: sem resíduo, a varredura não dispara mutation
// nenhuma — apagar de novo o que já sumiu só custaria I/O no ClickHouse.
func TestContarSobrasZeroNaoPedeExpurgo(t *testing.T) {
	var stmts []string
	ch := fakeCH(t, &stmts, func(string) string { return `{"c":"0"}` + "\n" })
	h := NewHandler(nil, ch, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if got, _ := h.contarSobrasCH(context.Background(), "srv-01"); got != 0 {
		t.Errorf("sobras: got %d, want 0", got)
	}
	for _, s := range stmts {
		if strings.Contains(s, "ALTER TABLE") {
			t.Errorf("contagem não pode disparar expurgo: %q", s)
		}
	}
}

// TestContarSobrasAcusaFalhaDeLeitura é o teste do caso que apagava a varredura
// pendente para sempre.
//
// Com o ClickHouse fora do ar a contagem devolve zero — e zero por "não achei nada"
// era indistinguível de zero por "não consegui perguntar". Provado ao vivo: o
// servidor subiu antes do banco, a varredura de rescaldo registrou "nada voltou",
// apagou a pendência, e a linha do host apagado continuou no ClickHouse.
func TestContarSobrasAcusaFalhaDeLeitura(t *testing.T) {
	ch := chquery.New("http://127.0.0.1:1", "", "", "default") // porta fechada
	h := NewHandler(nil, ch, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	got, mediuTudo := h.contarSobrasCH(context.Background(), "srv-01")
	if got != 0 {
		t.Errorf("sem resposta não há sobra contada: %d", got)
	}
	if mediuTudo {
		t.Error("a varredura não pode se declarar completa sem o banco ter respondido")
	}
}

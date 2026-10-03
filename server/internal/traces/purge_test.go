package traces

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/chquery"
)

// fakeCH captura os statements enviados e responde conforme o texto da consulta.
func fakeCH(t *testing.T, capture *[]string, route func(string) string) *chquery.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if capture != nil {
			*capture = append(*capture, string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, route(string(body)))
	}))
	t.Cleanup(srv.Close)
	return chquery.New(srv.URL, "", "", "default")
}

func rota(q string) string {
	switch {
	case strings.Contains(q, "system.mutations"):
		return `{"mutation_id":"m1","is_done":"1","parts_to_do":"0","latest_fail_reason":""}`
	case strings.Contains(q, "count()"):
		return `{"c":"5"}`
	}
	return ""
}

func post(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Purge(rec, httptest.NewRequest("POST", "/api/traces/purge", strings.NewReader(body)))
	return rec
}

// TestTruncateExigeConfirmacaoNoCorpo: a confirmação vivia só na tela, e tela não
// protege rota. Sem a frase no CORPO, o TRUNCATE não sai.
func TestTruncateExigeConfirmacaoNoCorpo(t *testing.T) {
	var stmts []string
	h := NewHandler(fakeCH(t, &stmts, rota))
	for _, body := range []string{``, `{}`, `{"confirm":"sim"}`} {
		rec := post(t, h, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("corpo %q: esperado 400, obtido %d (%s)", body, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), confirmTruncate) {
			t.Errorf("a mensagem precisa dizer a frase exigida: %s", rec.Body.String())
		}
	}
	for _, s := range stmts {
		if strings.Contains(s, "TRUNCATE") {
			t.Fatalf("TRUNCATE disparado sem confirmação: %s", s)
		}
	}
}

// TestTruncateComConfirmacao: com a frase exata, zera a tabela e reporta o número.
func TestTruncateComConfirmacao(t *testing.T) {
	var stmts []string
	h := NewHandler(fakeCH(t, &stmts, rota))
	rec := post(t, h, `{"confirm":"`+confirmTruncate+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("esperado 200, obtido %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(strings.Join(stmts, "\n"), "TRUNCATE TABLE spans") {
		t.Errorf("TRUNCATE não foi disparado: %v", stmts)
	}
	var got struct {
		Deleted   int64 `json:"deleted_rows"`
		NotPurged []any `json:"not_purged"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Deleted != 5 {
		t.Errorf("deleted_rows = %d; quer 5", got.Deleted)
	}
	if len(got.NotPurged) == 0 {
		t.Error("a resposta precisa declarar o que NÃO foi alcançado")
	}
}

// TestRecorteViraMutation: com recorte, sai ALTER … DELETE preso ao recorte — não
// TRUNCATE. É o que permite tirar um token de dentro de um span sem levar 15 dias de
// traces de todos os hosts.
func TestRecorteViraMutation(t *testing.T) {
	var stmts []string
	h := NewHandler(fakeCH(t, &stmts, rota))
	rec := post(t, h, `{"host":"srv-01","from":"2026-01-01T00:00:00Z","to":"2026-01-02T00:00:00Z","body_like":"Bearer abc"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("esperado 200, obtido %d (%s)", rec.Code, rec.Body.String())
	}
	joined := strings.Join(stmts, "\n")
	if strings.Contains(joined, "TRUNCATE") {
		t.Fatal("recorte não pode virar TRUNCATE")
	}
	var alter string
	for _, s := range stmts {
		if strings.HasPrefix(s, "ALTER TABLE spans DELETE WHERE ") {
			alter = s
		}
	}
	if alter == "" {
		t.Fatalf("faltou o ALTER: %v", stmts)
	}
	for _, want := range []string{
		"labels['host']='srv-01'",
		"ts >= toDateTime(",
		"ts < toDateTime(",
		"name ILIKE '%Bearer abc%'",
		"mapValues(labels)",
		"mapKeys(labels)",
		noLogSuffix,
	} {
		if !strings.Contains(alter, want) {
			t.Errorf("ALTER sem %q:\n%s", want, alter)
		}
	}
	// A contagem (o número mostrado) usa a MESMA cláusula do DELETE.
	var count string
	for _, s := range stmts {
		if strings.HasPrefix(s, "SELECT count() AS c FROM spans WHERE ") {
			count = s
		}
	}
	wCount := strings.TrimPrefix(count, "SELECT count() AS c FROM spans WHERE ")
	wAlter := strings.TrimPrefix(alter, "ALTER TABLE spans DELETE WHERE ")
	if wCount == "" || wCount != wAlter {
		t.Errorf("contagem e DELETE divergiram:\ncount=%s\nalter=%s", wCount, wAlter)
	}
}

// TestSpansSemHost: os spans sem rótulo de host também têm rota.
func TestSpansSemHost(t *testing.T) {
	var stmts []string
	h := NewHandler(fakeCH(t, &stmts, rota))
	rec := post(t, h, `{"no_host":true,"to":"2026-01-02T00:00:00Z"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("esperado 200, obtido %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(strings.Join(stmts, "\n"), "labels['host']=''") {
		t.Errorf("no_host não virou predicado: %v", stmts)
	}
}

// TestDryRunNaoApaga: o preview conta e não toca em nada.
func TestDryRunNaoApaga(t *testing.T) {
	var stmts []string
	h := NewHandler(fakeCH(t, &stmts, rota))
	rec := post(t, h, `{"host":"srv-01","dry_run":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("esperado 200, obtido %d (%s)", rec.Code, rec.Body.String())
	}
	joined := strings.Join(stmts, "\n")
	if strings.Contains(joined, "ALTER") || strings.Contains(joined, "TRUNCATE") {
		t.Errorf("dry_run apagou: %s", joined)
	}
	var got struct {
		Rows int64 `json:"rows"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Rows != 5 {
		t.Errorf("rows = %d; quer 5", got.Rows)
	}
}

// TestCampoDesconhecido400: filtro digitado errado não pode ser ignorado em silêncio
// — quem acha que restringiu está prestes a apagar muito mais do que pediu.
func TestCampoDesconhecido400(t *testing.T) {
	var stmts []string
	h := NewHandler(fakeCH(t, &stmts, rota))
	rec := post(t, h, `{"host":"srv-01","body":"segredo"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400, obtido %d (%s)", rec.Code, rec.Body.String())
	}
	if len(stmts) != 0 {
		t.Errorf("não deveria tocar o banco: %v", stmts)
	}
}

// TestJanelaInvalida: corte no futuro e início depois do fim são recusados.
func TestJanelaInvalida(t *testing.T) {
	h := NewHandler(fakeCH(t, nil, rota))
	futuro := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	if rec := post(t, h, `{"host":"s","to":"`+futuro+`"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("corte no futuro: esperado 400, obtido %d", rec.Code)
	}
	if rec := post(t, h, `{"host":"s","from":"2026-01-02T00:00:00Z","to":"2026-01-01T00:00:00Z"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("janela invertida: esperado 400, obtido %d", rec.Code)
	}
}

// TestEscapeLikeTraces: curinga do trecho procurado é neutralizado (ver o teste
// equivalente em internal/logs para a prova no banco).
func TestEscapeLikeTraces(t *testing.T) {
	w := purgeReq{Host: "h", BodyLike: "a%b_c"}.purgeWhere(time.Unix(10, 0), time.Time{})
	if !strings.Contains(w, `'%a\\%b\\_c%'`) {
		t.Errorf("curingas não neutralizados: %s", w)
	}
}

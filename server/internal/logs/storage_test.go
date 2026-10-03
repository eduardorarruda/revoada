package logs

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eduardorarruda/revoada/server/internal/authz"
	"github.com/eduardorarruda/revoada/server/internal/chquery"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// TestAsInt64 cobre os três formatos que o ClickHouse devolve em JSONEachRow.
func TestAsInt64(t *testing.T) {
	if got := asInt64(float64(42)); got != 42 {
		t.Errorf("float64: %d", got)
	}
	if got := asInt64("123"); got != 123 { // UInt64/Int64 vêm como string
		t.Errorf("string: %d", got)
	}
	if got := asInt64(json.Number("7")); got != 7 {
		t.Errorf("json.Number: %d", got)
	}
	if got := asInt64(nil); got != 0 {
		t.Errorf("nil deveria ser 0, got %d", got)
	}
}

// fakeCH devolve respostas JSONEachRow conforme o texto da query recebida.
func fakeCH(t *testing.T, route func(query string) string) *chquery.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, route(string(body)))
	}))
	t.Cleanup(srv.Close)
	return chquery.New(srv.URL, "", "", "default")
}

// TestStorageScopeForbiddenHost: usuário com escopo restrito pedindo o detalhe de um
// host FORA do escopo recebe 403 antes de qualquer consulta (anti-vazamento de
// volumetria/período de servidores proibidos — plano §5, storage é [F]).
func TestStorageScopeForbiddenHost(t *testing.T) {
	h := NewHandler(nil, nil, "", nil) // o 403 sai antes de tocar CH/PG
	scope := authz.NewScope(7, []store.ServerPerm{{Hostname: "host-a", CanView: true}})
	req := httptest.NewRequest("GET", "/api/logs/storage?host=host-b", nil)
	req = req.WithContext(authz.WithScope(req.Context(), scope))
	rec := httptest.NewRecorder()
	h.Storage(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("host fora do escopo: esperado 403, obtido %d", rec.Code)
	}
}

// TestStorageScopeZeroHosts: usuário sem NENHUM host também é barrado ao pedir detalhe.
func TestStorageScopeZeroHosts(t *testing.T) {
	h := NewHandler(nil, nil, "", nil)
	scope := authz.NewScope(8, nil)
	req := httptest.NewRequest("GET", "/api/logs/storage?host=host-a", nil)
	req = req.WithContext(authz.WithScope(req.Context(), scope))
	rec := httptest.NewRecorder()
	h.Storage(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("sem hosts no escopo: esperado 403, obtido %d", rec.Code)
	}
}

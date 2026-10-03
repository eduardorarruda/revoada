package logs

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestParseFiltersSource garante que o param `source` é lido e vira a cláusula
// segura labels['source'] = '...', combinável com host/service/severity/q.
func TestParseFiltersSource(t *testing.T) {
	r := httptest.NewRequest("GET",
		"/api/logs/search?source=journald&host=web-01&service=nginx&severity=ERROR&q=timeout", nil)
	f := parseFilters(r)
	if f.source != "journald" {
		t.Fatalf("source esperado 'journald', obtido %q", f.source)
	}
	w := f.where()
	for _, want := range []string{
		"labels['source'] = 'journald'",
		"labels['host'] = 'web-01'",
		"service = 'nginx'",
		"severity_num >= 17",
		"body ILIKE '%timeout%'",
	} {
		if !strings.Contains(w, want) {
			t.Errorf("WHERE não contém %q; WHERE=%s", want, w)
		}
	}
}

// TestParseFiltersSourceEmpty garante que sem `source` não há filtro por fonte.
func TestParseFiltersSourceEmpty(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/logs/search", nil)
	f := parseFilters(r)
	if f.source != "" {
		t.Fatalf("source deveria ser vazio, obtido %q", f.source)
	}
	if strings.Contains(f.where(), "labels['source']") {
		t.Errorf("WHERE não deveria filtrar por source: %s", f.where())
	}
}

// TestWhereSourceEscaping confirma o mesmo escaping dos demais filtros de label.
func TestWhereSourceEscaping(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/logs/search?source=a'b", nil)
	f := parseFilters(r)
	if got, want := f.where(), "labels['source'] = 'a\\'b'"; !strings.Contains(got, want) {
		t.Errorf("escaping esperado %q em %q", want, got)
	}
}

// TestParseFiltersContainer garante que o param `container` (filtro encadeado
// servidor→container da UI) vira a cláusula segura labels['container'] = '...'.
func TestParseFiltersContainer(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/logs/search?host=web-01&container=api", nil)
	f := parseFilters(r)
	if f.container != "api" {
		t.Fatalf("container esperado 'api', obtido %q", f.container)
	}
	if got := f.where(); !strings.Contains(got, "labels['container'] = 'api'") {
		t.Errorf("WHERE não contém filtro de container; WHERE=%s", got)
	}
}

// TestParseFiltersContainerEmpty garante que sem `container` não há filtro.
func TestParseFiltersContainerEmpty(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/logs/search", nil)
	f := parseFilters(r)
	if strings.Contains(f.where(), "labels['container']") {
		t.Errorf("WHERE não deveria filtrar por container: %s", f.where())
	}
}

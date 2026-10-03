package traces

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestScopeFilter: o escopo da correlação por serviço e/ou host. Sem host, o
// bug original: nenhum filtro por labels['host'] → mistura todos os servidores.
func TestScopeFilter(t *testing.T) {
	cases := []struct {
		name, service, host string
		wantContains        []string
		wantEmpty           bool
	}{
		{name: "nenhum", wantEmpty: true},
		{name: "só host", host: "srv1", wantContains: []string{"AND labels['host'] = 'srv1' "}},
		{name: "só serviço", service: "api", wantContains: []string{"AND service = 'api' "}},
		{name: "ambos", service: "api", host: "srv1", wantContains: []string{"AND service = 'api' ", "AND labels['host'] = 'srv1' "}},
		{name: "escapa aspas", host: "o'brien", wantContains: []string{"AND labels['host'] = 'o\\'brien' "}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := scopeFilter(c.service, c.host)
			if c.wantEmpty && got != "" {
				t.Fatalf("scopeFilter(%q,%q) = %q; quer vazio", c.service, c.host, got)
			}
			for _, sub := range c.wantContains {
				if !strings.Contains(got, sub) {
					t.Errorf("scopeFilter(%q,%q) = %q; quer conter %q", c.service, c.host, got, sub)
				}
			}
		})
	}
}

// TestAsCount: a contagem do ClickHouse pode chegar como string (UInt64), número ou
// json.Number — asCount normaliza todas para int64.
func TestAsCount(t *testing.T) {
	cases := []struct {
		in   any
		want int64
	}{
		{"70123", 70123},
		{" 42 ", 42},
		{float64(7), 7},
		{json.Number("999"), 999},
		{nil, 0},
		{"abc", 0},
	}
	for _, c := range cases {
		if got := asCount(c.in); got != c.want {
			t.Errorf("asCount(%v) = %d; quer %d", c.in, got, c.want)
		}
	}
}

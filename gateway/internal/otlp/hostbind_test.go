package otlp

import "testing"

func TestParseHostBindMode(t *testing.T) {
	cases := map[string]HostBindMode{
		"":          HostBindOff,
		"off":       HostBindOff,
		"lixo":      HostBindOff,
		"normalize": HostBindNormalize,
		"ON":        HostBindNormalize,
		"strict":    HostBindStrict,
	}
	for in, want := range cases {
		if got := ParseHostBindMode(in); got != want {
			t.Errorf("ParseHostBindMode(%q)=%v, quero %v", in, got, want)
		}
	}
}

func TestBindHost(t *testing.T) {
	cases := []struct {
		name        string
		mode        HostBindMode
		bound, payl string
		wantHost    string
		wantDrop    bool
	}{
		{"off passa direto", HostBindOff, "srvA", "srvB", "srvB", false},
		{"sem vínculo passa direto", HostBindNormalize, "", "srvB", "srvB", false},
		{"normalize casando", HostBindNormalize, "srvA", "srvA", "srvA", false},
		{"normalize forçando (spoof)", HostBindNormalize, "srvA", "srvB", "srvA", false},
		{"strict casando", HostBindStrict, "srvA", "srvA", "srvA", false},
		{"strict divergindo descarta", HostBindStrict, "srvA", "srvB", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			host, drop := bindHost(c.mode, c.bound, c.payl)
			if host != c.wantHost || drop != c.wantDrop {
				t.Fatalf("bindHost(%v,%q,%q)=(%q,%v), quero (%q,%v)",
					c.mode, c.bound, c.payl, host, drop, c.wantHost, c.wantDrop)
			}
		})
	}
}

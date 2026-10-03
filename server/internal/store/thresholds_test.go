package store

import "testing"

func TestBuildThresholdResolver(t *testing.T) {
	rows := []HostThreshold{
		{Hostname: "", Metric: "cpu", Warn: 60, Crit: 80},      // override global de cpu
		{Hostname: "web01", Metric: "cpu", Warn: 50, Crit: 75}, // override por host de cpu
		{Hostname: "web01", Metric: "disk", Warn: 65, Crit: 85},
	}
	resolve := buildThresholdResolver(rows)

	cases := []struct {
		host, metric     string
		wantWarn, wantCr float64
		why              string
	}{
		{"web01", "cpu", 50, 75, "override por host vence global e default"},
		{"web01", "disk", 65, 85, "override por host de disco"},
		{"web01", "mem", 70, 90, "sem linha => default embutido de mem"},
		{"other", "cpu", 60, 80, "sem host-específico => global de cpu"},
		{"other", "disk", 75, 90, "sem global de disco => default embutido"},
		{"other", "mem", 70, 90, "default embutido de mem"},
	}
	for _, c := range cases {
		w, cr := resolve(c.host, c.metric)
		if w != c.wantWarn || cr != c.wantCr {
			t.Errorf("resolve(%q,%q) = (%v,%v); want (%v,%v) — %s",
				c.host, c.metric, w, cr, c.wantWarn, c.wantCr, c.why)
		}
	}
}

func TestBuildThresholdResolverEmpty(t *testing.T) {
	// Sem nenhuma linha, tudo cai no default embutido.
	resolve := buildThresholdResolver(nil)
	if w, cr := resolve("qualquer", "cpu"); w != 70 || cr != 90 {
		t.Errorf("cpu default = (%v,%v); want (70,90)", w, cr)
	}
	if w, cr := resolve("qualquer", "disk"); w != 75 || cr != 90 {
		t.Errorf("disk default = (%v,%v); want (75,90)", w, cr)
	}
}

package inventory

import "testing"

func TestCardState(t *testing.T) {
	cases := []struct {
		name                     string
		up                       bool
		cpu, mem, disk, alertSev string
		want                     string
	}{
		{"sem dados => nosignal", false, "ok", "ok", "ok", "", "nosignal"},
		{"tudo ok sem alerta", true, "ok", "ok", "ok", "", "ok"},
		// Núcleo do requisito: 95% de CPU (crit por recurso) sem alerta => crit, não verde.
		{"cpu crit sem alerta", true, "crit", "ok", "ok", "", "crit"},
		{"disco warn sem alerta", true, "ok", "ok", "warn", "", "warn"},
		// Alerta ativo eleva o card mesmo com recursos ok.
		{"alerta critical com recursos ok", true, "ok", "ok", "ok", "critical", "crit"},
		{"alerta warning com recursos ok", true, "ok", "ok", "ok", "warning", "warn"},
		// Combina (pior vence): recurso warn + alerta critical => crit.
		{"pior entre recurso e alerta", true, "warn", "ok", "ok", "critical", "crit"},
		{"info não eleva acima de ok", true, "ok", "ok", "ok", "info", "ok"},
	}
	for _, c := range cases {
		if got := cardState(c.up, c.cpu, c.mem, c.disk, c.alertSev); got != c.want {
			t.Errorf("%s: cardState = %q; want %q", c.name, got, c.want)
		}
	}
}

func TestAlertState(t *testing.T) {
	cases := map[string]string{
		"critical": "crit",
		"warning":  "warn",
		"info":     "ok",
		"":         "ok",
		"outro":    "ok",
	}
	for sev, want := range cases {
		if got := alertState(sev); got != want {
			t.Errorf("alertState(%q) = %q; want %q", sev, got, want)
		}
	}
}

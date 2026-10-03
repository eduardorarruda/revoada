package inventory

import "testing"

func TestContainerStatus(t *testing.T) {
	cases := []struct {
		name     string
		running  bool
		state    string
		health   string
		exitCode string
		want     string
	}{
		{"running healthy => ok", true, "running", "healthy", "0", statusOK},
		{"running sem healthcheck (none) => ok", true, "running", "none", "0", statusOK},
		{"running unhealthy => warn", true, "running", "unhealthy", "0", statusWarn},
		{"restarting => warn", false, "restarting", "none", "0", statusWarn},
		{"dead => crit", false, "dead", "none", "0", statusCrit},
		{"exited com exit!=0 => crit", false, "exited", "none", "137", statusCrit},
		{"exited exit=0 => neutral (parada intencional)", false, "exited", "none", "0", statusNeutral},
		{"paused => neutral", false, "paused", "none", "0", statusNeutral},
		{"created => neutral", false, "created", "none", "0", statusNeutral},
		{"estado desconhecido => neutral", false, "removing", "none", "0", statusNeutral},
		{"running com health starting (não coberto) => neutral", true, "running", "starting", "0", statusNeutral},
		{"exited exit vazio tratado como !=0 => crit", false, "exited", "none", "", statusCrit},
	}
	for _, c := range cases {
		got := containerStatus(c.running, c.state, c.health, c.exitCode)
		if got != c.want {
			t.Errorf("%s: containerStatus(%v,%q,%q,%q) = %q; want %q",
				c.name, c.running, c.state, c.health, c.exitCode, got, c.want)
		}
	}
}

func TestSortContainers(t *testing.T) {
	// Falhas (crit/warn) no topo, depois neutral, depois ok; empate por nome.
	cs := []ContainerStatus{
		{Name: "zeta-ok", Status: statusOK},
		{Name: "beta-neutral", Status: statusNeutral},
		{Name: "gamma-crit", Status: statusCrit},
		{Name: "alpha-warn", Status: statusWarn},
		{Name: "delta-ok", Status: statusOK},
		{Name: "epsilon-crit", Status: statusCrit},
	}
	sortContainers(cs)

	wantOrder := []string{
		"alpha-warn",   // rank 0 (warn), nome "a..."
		"epsilon-crit", // rank 0 (crit), nome "e..."
		"gamma-crit",   // rank 0 (crit), nome "g..."
		"beta-neutral", // rank 1 (neutral)
		"delta-ok",     // rank 2 (ok), nome "d..."
		"zeta-ok",      // rank 2 (ok), nome "z..."
	}
	if len(cs) != len(wantOrder) {
		t.Fatalf("esperado %d containers, veio %d", len(wantOrder), len(cs))
	}
	for i, want := range wantOrder {
		if cs[i].Name != want {
			t.Errorf("cs[%d] = %q; want %q (ordem completa: %v)", i, cs[i].Name, want, names(cs))
		}
	}
}

func TestStatusRank(t *testing.T) {
	// crit e warn compartilham a faixa de topo; neutral no meio; ok no fim.
	if statusRank(statusCrit) != statusRank(statusWarn) {
		t.Errorf("crit e warn devem ter o mesmo rank (topo); crit=%d warn=%d",
			statusRank(statusCrit), statusRank(statusWarn))
	}
	if statusRank(statusWarn) >= statusRank(statusNeutral) || statusRank(statusNeutral) >= statusRank(statusOK) {
		t.Errorf("ordem esperada topo<neutral<ok; ranks warn=%d neutral=%d ok=%d",
			statusRank(statusWarn), statusRank(statusNeutral), statusRank(statusOK))
	}
}

func names(cs []ContainerStatus) []string {
	out := make([]string, len(cs))
	for i := range cs {
		out[i] = cs[i].Name
	}
	return out
}

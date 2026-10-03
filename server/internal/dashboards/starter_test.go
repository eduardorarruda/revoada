package dashboards

import "testing"

// TestStarterHostConcrete: o dashboard por-servidor fixa o host em cada painel.
func TestStarterHostConcrete(t *testing.T) {
	m := StarterHost("web01", "")
	if m.UID != "host-web01" {
		t.Fatalf("uid esperado host-web01, veio %s", m.UID)
	}
	if len(m.Panels) == 0 {
		t.Fatal("sem painéis")
	}
	for _, p := range m.Panels {
		if p.Query.Filters["host"] != "web01" {
			t.Fatalf("painel %d deveria filtrar host=web01, veio %q", p.ID, p.Query.Filters["host"])
		}
	}
}

// TestStarterHostTitleUsesFriendlyName: o TÍTULO usa o nome amigável quando informado,
// mas o UID, a variável e os filtros continuam com o hostname técnico (chave das métricas).
func TestStarterHostTitleUsesFriendlyName(t *testing.T) {
	m := StarterHost("srv-02", "Servidor Secundário")
	if m.Title != "Visão do Host, Servidor Secundário" {
		t.Fatalf("título deveria usar o nome amigável, veio %q", m.Title)
	}
	if m.UID != "host-srv-02" {
		t.Fatalf("uid deveria ser técnico, veio %q", m.UID)
	}
	for _, p := range m.Panels {
		if p.Query.Filters["host"] != "srv-02" {
			t.Fatalf("filtro deveria ser o hostname técnico, veio %q", p.Query.Filters["host"])
		}
	}
	// Sem alias: título cai no hostname técnico.
	if got := StarterHost("srv-02", "").Title; got != "Visão do Host, srv-02" {
		t.Fatalf("sem alias, título deveria usar o técnico, veio %q", got)
	}
}

// TestStarterHostGenericPlaceholder: o dashboard genérico usa o sentinela $host em
// todos os painéis (substituído no cliente pelo servidor escolhido) e tem os MESMOS
// painéis do por-servidor (mesma fonte hostPanels) — nunca podem divergir.
func TestStarterHostGenericPlaceholder(t *testing.T) {
	g := StarterHostGeneric()
	if g.UID != GenericHostUID {
		t.Fatalf("uid esperado %s, veio %s", GenericHostUID, g.UID)
	}
	for _, p := range g.Panels {
		if p.Query.Filters["host"] != HostVarPlaceholder {
			t.Fatalf("painel %d do genérico deveria usar %q, veio %q", p.ID, HostVarPlaceholder, p.Query.Filters["host"])
		}
	}
	// Paridade de painéis com o por-servidor (mesmos títulos/métricas, na mesma ordem).
	h := StarterHost("x", "")
	if len(h.Panels) != len(g.Panels) {
		t.Fatalf("genérico e por-servidor devem ter o mesmo nº de painéis: %d vs %d", len(g.Panels), len(h.Panels))
	}
	for i := range h.Panels {
		if h.Panels[i].Query.Metric != g.Panels[i].Query.Metric || h.Panels[i].Title != g.Panels[i].Title {
			t.Fatalf("painel %d divergiu entre genérico e por-servidor", i)
		}
	}
}

// TestStarterHostHasSwap: o feedback do gestor pediu swap explicitamente.
func TestStarterHostHasSwap(t *testing.T) {
	var hasSwap bool
	for _, p := range StarterHost("h", "").Panels {
		if p.Query.Metric == "system.paging.utilization" {
			hasSwap = true
		}
	}
	if !hasSwap {
		t.Fatal("faltou o painel de swap (system.paging.utilization)")
	}
}

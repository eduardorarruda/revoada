package alerting

import (
	"context"
	"testing"

	"github.com/eduardorarruda/revoada/server/internal/authz"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

func userScope(hosts ...string) *authz.Scope {
	perms := make([]store.ServerPerm, 0, len(hosts))
	for _, h := range hosts {
		perms = append(perms, store.ServerPerm{Hostname: h, CanView: true})
	}
	return authz.NewScope(1, perms)
}

func TestFilterAlerts(t *testing.T) {
	alerts := []store.AlertEvent{
		{ID: 1, Labels: map[string]string{"host": "host-a"}},
		{ID: 2, Labels: map[string]string{"host": "host-b"}},
		{ID: 3, Labels: map[string]string{}}, // sem host (ex.: métrica de app)
	}

	// Admin vê tudo.
	adminCtx := authz.WithScope(context.Background(), authz.NewAdminScope(1))
	if got := filterAlerts(adminCtx, alerts); len(got) != 3 {
		t.Fatalf("admin deveria ver 3, veio %d", len(got))
	}

	// Bob (host-a): só o alerta 1; NUNCA host-b nem o sem-host.
	bobCtx := authz.WithScope(context.Background(), userScope("host-a"))
	got := filterAlerts(bobCtx, alerts)
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("bob deveria ver só o alerta de host-a, veio %+v", got)
	}
	for _, a := range got {
		if a.Labels["host"] == "host-b" || a.Labels["host"] == "" {
			t.Fatal("VAZAMENTO: bob recebeu alerta indevido")
		}
	}
}

func TestFilterRules(t *testing.T) {
	rules := []store.AlertRule{
		{ID: 1, Hosts: nil},                          // global (informativa)
		{ID: 2, Hosts: []string{"host-a"}},           // toca host-a
		{ID: 3, Hosts: []string{"host-b"}},           // só host-b
		{ID: 4, Hosts: []string{"host-b", "host-a"}}, // toca ambos
	}

	adminCtx := authz.WithScope(context.Background(), authz.NewAdminScope(1))
	if got := filterRules(adminCtx, rules); len(got) != 4 {
		t.Fatalf("admin deveria ver 4, veio %d", len(got))
	}

	// Bob (host-a): vê a global (1), a de host-a (2) e a mista (4); NÃO a só-host-b (3).
	bobCtx := authz.WithScope(context.Background(), userScope("host-a"))
	got := filterRules(bobCtx, rules)
	ids := map[int64]bool{}
	for _, ru := range got {
		ids[ru.ID] = true
	}
	if !ids[1] || !ids[2] || !ids[4] {
		t.Fatalf("bob deveria ver as regras 1,2,4, veio %v", ids)
	}
	if ids[3] {
		t.Fatal("VAZAMENTO: bob viu regra restrita a host-b")
	}
}

// TestFilterRulesNaoVazaPorFiltroDeHost é o canário do item 4.
//
// Há DOIS jeitos de amarrar uma regra a um servidor: a lista `hosts` e o filtro de label
// `filters['host']` — e para o avaliador (ruleFilterSets) eles são a mesma coisa. O
// filtro olhava só `hosts`, então uma regra escrita do segundo jeito tinha `Hosts` vazio,
// era classificada como "global: informativa" e ia para TODO usuário autenticado.
//
// Provado ao vivo: um usuário limitado a um servidor recebeu, na listagem de regras, uma
// com filters={'host':'servidor-secreto-do-cliente-X'} — hostname, métrica e limiar de
// outro cliente, num painel multi-cliente.
func TestFilterRulesNaoVazaPorFiltroDeHost(t *testing.T) {
	rules := []store.AlertRule{
		{ID: 1, Filters: map[string]string{}},                                         // global de verdade
		{ID: 2, Filters: map[string]string{"host": "host-a"}},                         // recorta host-a por filtro
		{ID: 3, Filters: map[string]string{"host": "servidor-secreto-do-cliente-X"}},  // outro cliente
		{ID: 4, Hosts: []string{"host-a"}, Filters: map[string]string{"host": "zzz"}}, // os dois jeitos
		{ID: 5, Filters: map[string]string{"container": "api", "env": "prod"}},        // filtro sem host: global
		{ID: 6, Filters: map[string]string{"host": ""}},                               // host vazio não recorta nada
	}

	bobCtx := authz.WithScope(context.Background(), userScope("host-a"))
	ids := map[int64]bool{}
	for _, ru := range filterRules(bobCtx, rules) {
		ids[ru.ID] = true
	}
	if ids[3] {
		t.Fatal("VAZAMENTO: usuário recebeu regra de servidor de outro cliente, recortada por filters['host']")
	}
	if !ids[1] || !ids[2] || !ids[4] || !ids[5] || !ids[6] {
		t.Fatalf("regras globais e as do próprio servidor têm de continuar visíveis, veio %v", ids)
	}

	// Admin continua vendo tudo — o recorte é de usuário comum, não de conteúdo.
	adminCtx := authz.WithScope(context.Background(), authz.NewAdminScope(1))
	if got := filterRules(adminCtx, rules); len(got) != len(rules) {
		t.Fatalf("admin deveria ver %d regras, veio %d", len(rules), len(got))
	}
}

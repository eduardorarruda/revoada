package inventory

import (
	"context"
	"testing"

	"github.com/eduardorarruda/revoada/server/internal/authz"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// userScope monta um escopo de usuário comum com permissão de VER os hosts dados.
func userScope(hosts ...string) *authz.Scope {
	perms := make([]store.ServerPerm, 0, len(hosts))
	for _, h := range hosts {
		perms = append(perms, store.ServerPerm{Hostname: h, CanView: true})
	}
	return authz.NewScope(1, perms)
}

func TestScopeFilterHosts(t *testing.T) {
	items := []store.HostDetail{{Hostname: "host-a"}, {Hostname: "host-b"}}
	hostOf := func(h store.HostDetail) string { return h.Hostname }

	// Sem escopo (chamada de sistema): passa tudo.
	got := scopeFilter(context.Background(), items, hostOf)
	if len(got) != 2 {
		t.Fatalf("sem escopo deveria passar tudo, veio %d", len(got))
	}

	// Admin: passa tudo.
	adminCtx := authz.WithScope(context.Background(), authz.NewAdminScope(1))
	if got := scopeFilter(adminCtx, items, hostOf); len(got) != 2 {
		t.Fatalf("admin deveria ver os 2, veio %d", len(got))
	}

	// Bob (só host-a): NUNCA recebe host-b.
	bobCtx := authz.WithScope(context.Background(), userScope("host-a"))
	got = scopeFilter(bobCtx, items, hostOf)
	if len(got) != 1 || got[0].Hostname != "host-a" {
		t.Fatalf("bob deveria ver só host-a, veio %+v", got)
	}
	for _, h := range got {
		if h.Hostname == "host-b" {
			t.Fatal("VAZAMENTO: bob recebeu host-b")
		}
	}

	// Usuário sem nenhum host: lista vazia (não-nil).
	emptyCtx := authz.WithScope(context.Background(), userScope())
	if got := scopeFilter(emptyCtx, items, hostOf); got == nil || len(got) != 0 {
		t.Fatalf("usuário sem host deveria receber slice vazio não-nil, veio %+v", got)
	}
}

func TestScopeCanView(t *testing.T) {
	bobCtx := authz.WithScope(context.Background(), userScope("host-a"))
	if !scopeCanView(bobCtx, "host-a") {
		t.Fatal("bob deveria ver host-a")
	}
	if scopeCanView(bobCtx, "host-b") {
		t.Fatal("VAZAMENTO: bob não deveria ver host-b")
	}
	// Sem escopo (sistema) e admin: sempre true.
	if !scopeCanView(context.Background(), "qualquer") {
		t.Fatal("sem escopo deveria permitir")
	}
}

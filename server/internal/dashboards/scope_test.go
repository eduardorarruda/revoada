package dashboards

import (
	"context"
	"testing"

	"github.com/eduardorarruda/revoada/server/internal/authz"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// TestHostOfDashboard: só dashboards por-servidor ("host-<host>") mapeiam um hostname;
// o genérico e os dashboards comuns não.
func TestHostOfDashboard(t *testing.T) {
	cases := map[string]string{
		"host-srv-03":   "srv-03",
		"host-web01":    "web01",
		GenericHostUID:  "", // genérico não é de um servidor
		"vendas-gerais": "", // dashboard comum
		"host-":         "", // prefixo sem host → genérico-vazio, não filtra
	}
	for uid, want := range cases {
		if got := hostOfDashboard(uid); got != want {
			t.Errorf("hostOfDashboard(%q) = %q; quer %q", uid, got, want)
		}
	}
}

// TestScopedDashboardsLeak: um usuário comum com permissão só em host-a NÃO recebe o
// dashboard por-servidor de host-b; mantém o dele, o genérico e os dashboards comuns.
// Admin recebe tudo.
func TestScopedDashboardsLeak(t *testing.T) {
	// UID de dashboard por-servidor = "host-" + hostname; "host-a" é o dashboard do host "a".
	items := []store.DashboardMeta{
		{UID: "host-a", Title: "Visão do Host, a"},
		{UID: "host-b", Title: "Visão do Host, b"},
		{UID: GenericHostUID, Title: "Visão do Host (genérico)"},
		{UID: "vendas", Title: "Vendas"},
	}

	bob := authz.NewScope(2, []store.ServerPerm{{Hostname: "a", CanView: true}})
	got := scopedDashboards(authz.WithScope(context.Background(), bob), items)
	seen := map[string]bool{}
	for _, m := range got {
		seen[m.UID] = true
	}
	if seen["host-b"] {
		t.Error("bob NÃO deveria ver o dashboard de host-b")
	}
	for _, want := range []string{"host-a", GenericHostUID, "vendas"} {
		if !seen[want] {
			t.Errorf("bob deveria ver %q", want)
		}
	}

	admin := authz.NewAdminScope(1)
	if n := len(scopedDashboards(authz.WithScope(context.Background(), admin), items)); n != len(items) {
		t.Errorf("admin deveria ver todos os %d dashboards, viu %d", len(items), n)
	}

	// Sem escopo no contexto (chamada de sistema) = não filtra.
	if n := len(scopedDashboards(context.Background(), items)); n != len(items) {
		t.Errorf("sem escopo deveria devolver todos, veio %d", n)
	}
}

// TestScopedDashboardsVazamentoSvc é o vazamento medido em dev: um usuário com
// permissão APENAS em `notebook-dev` recebia na lista `svc-mysql-agent-test-host` e
// `svc-postgres-agent-test-host` — e o GET direto devolvia 200 —, porque o filtro só
// reconhecia o prefixo "host-". O UID e o título desses painéis carregam o hostname,
// então vazava a existência e o nome de servidores que ele não pode ver.
func TestScopedDashboardsVazamentoSvc(t *testing.T) {
	items := []store.DashboardMeta{
		{UID: "host-notebook-dev", Title: "Visão do Host, notebook-dev"},
		{UID: "svc-mysql-agent-test-host", Title: "MySQL — agent-test-host"},
		{UID: "svc-postgres-agent-test-host", Title: "PostgreSQL — agent-test-host"},
		{UID: "svc-redis-notebook-dev", Title: "Redis — notebook-dev"},
		{UID: GenericHostUID, Title: "Visão do Host (genérico)"},
		{UID: "vendas", Title: "Vendas"},
	}
	bob := authz.NewScope(2, []store.ServerPerm{{Hostname: "notebook-dev", CanView: true}})
	seen := map[string]bool{}
	for _, m := range scopedDashboards(authz.WithScope(context.Background(), bob), items) {
		seen[m.UID] = true
	}
	for _, proibido := range []string{"svc-mysql-agent-test-host", "svc-postgres-agent-test-host"} {
		if seen[proibido] {
			t.Errorf("bob NÃO deveria ver %q (host agent-test-host fora do escopo)", proibido)
		}
	}
	for _, permitido := range []string{"host-notebook-dev", "svc-redis-notebook-dev", GenericHostUID, "vendas"} {
		if !seen[permitido] {
			t.Errorf("bob deveria continuar vendo %q", permitido)
		}
	}
}

// TestHostOfServiceDashboard: hostname com hífen (o caso comum: "agent-test-host")
// tem de sair inteiro, e um "svc-" de serviço desconhecido erra para o lado de
// ESCONDER — host inventado nunca está no escopo de usuário comum.
func TestHostOfServiceDashboard(t *testing.T) {
	cases := map[string]string{
		"svc-mysql-agent-test-host": "agent-test-host",
		"svc-postgres-web01":        "web01",
		"svc-redis-notebook-dev":    "notebook-dev",
		"svc-desconhecido-web01":    "web01", // serviço fora do catálogo: split genérico
		"svc-":                      "",
		"svcmysql-web01":            "", // sem o hífen do prefixo não é starter de serviço
		"vendas":                    "",
	}
	for uid, want := range cases {
		if got := hostOfDashboard(uid); got != want {
			t.Errorf("hostOfDashboard(%q) = %q; quer %q", uid, got, want)
		}
	}
}

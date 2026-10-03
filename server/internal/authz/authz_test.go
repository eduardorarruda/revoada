package authz

import (
	"testing"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

func TestHostPredicate(t *testing.T) {
	admin := NewAdminScope(1)
	if got := admin.HostPredicate("labels['host']"); got != "" {
		t.Fatalf("admin deve ter predicado vazio (sem filtro), veio %q", got)
	}

	// Usuário sem nenhum host: predicado bloqueia tudo, NUNCA vazio.
	empty := NewScope(2, nil)
	if got := empty.HostPredicate("labels['host']"); got != "1=0" {
		t.Fatalf("escopo vazio deve ser 1=0, veio %q", got)
	}

	// Usuário com hosts: IN ordenado e escapado.
	u := NewScope(3, []store.ServerPerm{
		{Hostname: "host-b", CanView: true},
		{Hostname: "host-a", CanEdit: true}, // edit implica view
		{Hostname: "o'brien", Notify: true}, // notify implica view; aspa escapada
	})
	got := u.HostPredicate("labels['host']")
	want := `labels['host'] IN ('host-a', 'host-b', 'o\'brien')`
	if got != want {
		t.Fatalf("predicado inesperado:\n got=%q\nwant=%q", got, want)
	}
}

func TestCanViewEdit(t *testing.T) {
	u := NewScope(1, []store.ServerPerm{
		{Hostname: "a", CanView: true},
		{Hostname: "b", CanEdit: true},
		{Hostname: "c", Notify: true},
	})
	// view: a (view), b (edit⇒view), c (notify⇒view); não d
	for _, h := range []string{"a", "b", "c"} {
		if !u.CanView(h) {
			t.Fatalf("deveria poder ver %q", h)
		}
	}
	if u.CanView("d") {
		t.Fatal("não deveria ver d")
	}
	// edit: só b
	if !u.CanEdit("b") {
		t.Fatal("deveria poder editar b")
	}
	if u.CanEdit("a") || u.CanEdit("c") {
		t.Fatal("só b é editável")
	}

	admin := NewAdminScope(9)
	if !admin.CanView("qualquer") || !admin.CanEdit("qualquer") {
		t.Fatal("admin vê e edita tudo")
	}
}

func TestViewHostsSorted(t *testing.T) {
	u := NewScope(1, []store.ServerPerm{
		{Hostname: "z", CanView: true},
		{Hostname: "a", CanView: true},
	})
	got := u.ViewHosts()
	if len(got) != 2 || got[0] != "a" || got[1] != "z" {
		t.Fatalf("ViewHosts deve vir ordenado, veio %v", got)
	}
	if NewAdminScope(1).ViewHosts() != nil {
		t.Fatal("admin.ViewHosts deve ser nil (não precisa de lista)")
	}
}

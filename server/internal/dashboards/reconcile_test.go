package dashboards

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

// fakeReconcileStore é um store em memória para testar ReconcileHostDashboards sem banco.
type fakeReconcileStore struct {
	hosts    []string
	labels   map[string]string
	existing map[string]store.Dashboard // uid -> dashboard
	creates  []string                   // uids criados, na ordem
	failList bool
}

func newFakeStore() *fakeReconcileStore {
	return &fakeReconcileStore{labels: map[string]string{}, existing: map[string]store.Dashboard{}}
}

func (f *fakeReconcileStore) ActiveHosts(context.Context) ([]string, error) {
	return f.hosts, nil
}

func (f *fakeReconcileStore) ListDashboards(context.Context, string) ([]store.DashboardMeta, error) {
	if f.failList {
		return nil, errors.New("boom")
	}
	out := make([]store.DashboardMeta, 0, len(f.existing))
	for uid := range f.existing {
		out = append(out, store.DashboardMeta{UID: uid})
	}
	return out, nil
}

func (f *fakeReconcileStore) HostDisplayName(_ context.Context, hostname string) (string, error) {
	return f.labels[hostname], nil
}

func (f *fakeReconcileStore) CreateDashboard(_ context.Context, uid, title, folder string, model json.RawMessage, _ string) error {
	if _, ok := f.existing[uid]; ok {
		return errors.New("uid duplicado")
	}
	f.existing[uid] = store.Dashboard{UID: uid, Title: title, Folder: folder, Model: model}
	f.creates = append(f.creates, uid)
	return nil
}

func (f *fakeReconcileStore) GetDashboard(_ context.Context, uid string) (store.Dashboard, error) {
	d, ok := f.existing[uid]
	if !ok {
		return store.Dashboard{}, store.ErrNotFound
	}
	return d, nil
}

// TestReconcileCreatesMissing: cria só os hosts que ainda não têm dashboard.
func TestReconcileCreatesMissing(t *testing.T) {
	f := newFakeStore()
	f.hosts = []string{"web01", "db01"}
	f.labels["db01"] = "Banco Principal"
	// web01 já tem dashboard; db01 não.
	f.existing["host-web01"] = store.Dashboard{UID: "host-web01"}

	n, err := ReconcileHostDashboards(context.Background(), f)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if n != 1 {
		t.Fatalf("esperava 1 criação, veio %d", n)
	}
	if len(f.creates) != 1 || f.creates[0] != "host-db01" {
		t.Fatalf("deveria ter criado host-db01, veio %v", f.creates)
	}
	// O título do novo usa o nome amigável; folder = Hosts.
	d := f.existing["host-db01"]
	if d.Title != "Visão do Host, Banco Principal" {
		t.Fatalf("título deveria usar o nome amigável, veio %q", d.Title)
	}
	if d.Folder != "Hosts" {
		t.Fatalf("folder esperado Hosts, veio %q", d.Folder)
	}
	// O modelo deve conter os painéis do starter (mesma fonte hostPanels).
	var m Model
	if err := json.Unmarshal(d.Model, &m); err != nil {
		t.Fatalf("modelo inválido: %v", err)
	}
	if len(m.Panels) != len(StarterHost("db01", "").Panels) {
		t.Fatalf("modelo deveria reusar o template do starter")
	}
}

// TestReconcileIdempotent: rodar duas vezes não duplica nada.
func TestReconcileIdempotent(t *testing.T) {
	f := newFakeStore()
	f.hosts = []string{"web01"}
	if _, err := ReconcileHostDashboards(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	before := len(f.creates)
	if _, err := ReconcileHostDashboards(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if len(f.creates) != before {
		t.Fatalf("reconcile não deveria recriar; criações: %v", f.creates)
	}
}

// TestReconcileNoHosts: sem hosts, não faz nada (nem lê a lista de dashboards).
func TestReconcileNoHosts(t *testing.T) {
	f := newFakeStore()
	n, err := ReconcileHostDashboards(context.Background(), f)
	if err != nil || n != 0 {
		t.Fatalf("esperava 0 criações e sem erro, veio n=%d err=%v", n, err)
	}
}

// TestIsHostDashboard: reconhece por-servidor e genérico; ignora o resto.
func TestIsHostDashboard(t *testing.T) {
	for _, uid := range []string{"host-web01", GenericHostUID} {
		if !IsHostDashboard(uid) {
			t.Fatalf("%q deveria ser reconhecido como dashboard de host", uid)
		}
	}
	if IsHostDashboard("producao") {
		t.Fatal("dashboard comum não deveria ser tratado como de host")
	}
}

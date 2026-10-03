package dashboards

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/authz"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

type Handler struct{ st *store.Store }

func NewHandler(st *store.Store) *Handler { return &Handler{st: st} }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func actor(r *http.Request) string {
	if c, ok := auth.ClaimsFrom(r.Context()); ok {
		return c.Name
	}
	return "?"
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	// Reconcilia (best-effort): garante que todo servidor do inventário tenha o seu
	// dashboard "Visão do Host — <host>". Idempotente; roda no caminho de listagem
	// (não no de ingestão) para que abrir a tela já traga um painel pronto por host.
	// Nunca bloqueia a listagem: um erro aqui é ignorado e a lista atual é servida.
	_, _ = ReconcileHostDashboards(r.Context(), h.st)

	items, err := h.st.ListDashboards(r.Context(), r.URL.Query().Get("search"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	items = scopedDashboards(r.Context(), items)
	if items == nil {
		items = []store.DashboardMeta{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"dashboards": items})
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	// Dashboard por-servidor de um host que o usuário não pode ver: 404 (não vaza a
	// existência, coerente com a lista, que também o esconde).
	if host := hostOfDashboard(uid); host != "" {
		if scope, ok := authz.ScopeFrom(r.Context()); ok && !scope.CanView(host) {
			http.Error(w, "não encontrado", http.StatusNotFound)
			return
		}
	}
	d, err := h.st.GetDashboard(r.Context(), uid)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "não encontrado", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// scopedDashboards remove da lista os dashboards por-servidor ("host-<hostname>") cujo
// host o usuário não pode ver. O dashboard genérico ("host-visao-geral") e os dashboards
// comuns permanecem — seus DADOS já são filtrados no /api/query, então gráficos de hosts
// proibidos vêm vazios. Admin e chamadas de sistema (sem escopo) veem tudo.
func scopedDashboards(ctx context.Context, items []store.DashboardMeta) []store.DashboardMeta {
	scope, ok := authz.ScopeFrom(ctx)
	if !ok || scope.Admin {
		return items
	}
	out := make([]store.DashboardMeta, 0, len(items))
	for _, m := range items {
		if host := hostOfDashboard(m.UID); host != "" && !scope.CanView(host) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// hostOfDashboard devolve o hostname técnico de um dashboard por-servidor, ou ""
// quando não é um (dashboard comum ou o genérico "host-visao-geral", que serve
// qualquer host via variável var-host).
//
// Reconhece DUAS convenções de UID, e a segunda é a correção:
//
//	host-<hostname>          — "Visão do Host" (ReconcileHostDashboards)
//	svc-<serviço>-<hostname> — starter pack de serviço (ServicePack)
//
// Antes só a primeira era reconhecida. Medido em dev: um usuário com permissão
// APENAS em `notebook-dev` recebia na lista `svc-mysql-agent-test-host` e
// `svc-postgres-agent-test-host` — e o GET direto devolvia 200 — enquanto
// `host-agente-medicao` era corretamente escondido (404). O UID e o título desses
// painéis carregam o hostname, então o vazamento não é só de gráfico: é a
// existência e o nome de servidores que o usuário não pode ver.
func hostOfDashboard(uid string) string { return HostOfUID(uid) }

// HostOfUID é hostOfDashboard exportado, para o modo TV derivar do UID o servidor a
// que a tela pertence (ver tv.hostsDoAlvo). Mesma regra, uma implementação só: se um
// dia um novo tipo de dashboard por-servidor entrar aqui, a TV herda a restrição.
func HostOfUID(uid string) string {
	if IsHostDashboard(uid) {
		if uid == GenericHostUID {
			return ""
		}
		return strings.TrimPrefix(uid, HostDashboardPrefix)
	}
	return hostOfServiceDashboard(uid)
}

// ServiceDashboardPrefix é o prefixo do UID dos starter packs de serviço.
const ServiceDashboardPrefix = "svc-"

// hostOfServiceDashboard extrai o hostname de "svc-<serviço>-<hostname>".
//
// Tenta primeiro os serviços que o painel realmente gera (servicePanels), porque o
// hostname pode conter hífen e um split cego partiria no lugar errado. Se nenhum
// casar (UID feito à mão, serviço novo ainda não catalogado), cai no split após o
// SEGUNDO hífen: o resultado pode ser um "host" que não existe, e isso é de
// propósito — um host desconhecido nunca está no escopo de um usuário comum, então
// o erro é para o lado de esconder, nunca para o lado de vazar.
func hostOfServiceDashboard(uid string) string {
	if !strings.HasPrefix(uid, ServiceDashboardPrefix) {
		return ""
	}
	resto := strings.TrimPrefix(uid, ServiceDashboardPrefix)
	for kind := range servicePanels {
		if h, ok := strings.CutPrefix(resto, kind+"-"); ok && h != "" {
			return h
		}
	}
	if _, h, ok := strings.Cut(resto, "-"); ok {
		return h
	}
	return ""
}

type upsertReq struct {
	UID    string          `json:"uid"`
	Title  string          `json:"title"`
	Folder string          `json:"folder"`
	Model  json.RawMessage `json:"model"`
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var req upsertReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UID == "" || req.Title == "" || len(req.Model) == 0 {
		http.Error(w, "uid/title/model obrigatórios", http.StatusBadRequest)
		return
	}
	if req.Folder == "" {
		req.Folder = "Geral"
	}
	if err := h.st.CreateDashboard(r.Context(), req.UID, req.Title, req.Folder, req.Model, actor(r)); err != nil {
		http.Error(w, "erro ao criar (uid duplicado?): "+err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"uid": req.UID, "version": 1})
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	var req upsertReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Title == "" || len(req.Model) == 0 {
		http.Error(w, "title/model obrigatórios", http.StatusBadRequest)
		return
	}
	if req.Folder == "" {
		req.Folder = "Geral"
	}
	ver, err := h.st.UpdateDashboard(r.Context(), uid, req.Title, req.Folder, req.Model, actor(r))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "não encontrado", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uid": uid, "version": ver})
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	err := h.st.DeleteDashboard(r.Context(), r.PathValue("uid"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "não encontrado", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Versions(w http.ResponseWriter, r *http.Request) {
	vs, err := h.st.ListVersions(r.Context(), r.PathValue("uid"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if vs == nil {
		vs = []store.DashboardVersion{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": vs})
}

func (h *Handler) Rollback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version int `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Version <= 0 {
		http.Error(w, "version obrigatória", http.StatusBadRequest)
		return
	}
	ver, err := h.st.Rollback(r.Context(), r.PathValue("uid"), body.Version, actor(r))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "não encontrado", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"restored_from": body.Version, "new_version": ver})
}

// Starter cria (ou recria) o dashboard de fábrica "Visão do Host".
func (h *Handler) Starter(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Query().Get("host")
	if host == "" {
		http.Error(w, "host obrigatório", http.StatusBadRequest)
		return
	}
	// Nome amigável para o TÍTULO (o UID e os filtros continuam técnicos). Best-effort.
	label, _ := h.st.HostDisplayName(r.Context(), host)
	model, err := StarterHostJSON(host, label)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	uid := "host-" + host
	displayName := host
	if label != "" {
		displayName = label
	}
	title := "Visão do Host, " + displayName
	// cria; se já existir, atualiza (nova versão).
	if err := h.st.CreateDashboard(r.Context(), uid, title, "Hosts", model, actor(r)); err != nil {
		if _, uerr := h.st.UpdateDashboard(r.Context(), uid, title, "Hosts", model, actor(r)); uerr != nil {
			http.Error(w, uerr.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"uid": uid})
}

// Hosts lista os hosts ativos (para inventário e criação de starter packs).
func (h *Handler) Hosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := h.st.ActiveHosts(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if hosts == nil {
		hosts = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": hosts})
}

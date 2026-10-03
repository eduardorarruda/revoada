// Package discovery expõe a auto-descoberta (P6.2): lista serviços detectados,
// sugere starter packs e cria os dashboards sob confirmação.
package discovery

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/authz"
	"github.com/eduardorarruda/revoada/server/internal/dashboards"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

type Handler struct{ st *store.Store }

func NewHandler(st *store.Store) *Handler { return &Handler{st: st} }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// scopedServices filtra os serviços descobertos aos hosts que o usuário pode ver.
func scopedServices(ctx context.Context, svc []store.HostService) []store.HostService {
	scope, ok := authz.ScopeFrom(ctx)
	if !ok || scope.Admin {
		return svc
	}
	out := make([]store.HostService, 0, len(svc))
	for _, s := range svc {
		if scope.CanView(s.Hostname) {
			out = append(out, s)
		}
	}
	return out
}

// List devolve os serviços descobertos por host.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	svc, err := h.st.ListHostServices(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	svc = scopedServices(r.Context(), svc)
	if svc == nil {
		svc = []store.HostService{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": svc})
}

// Suggestion é um starter pack sugerido a partir da descoberta.
type Suggestion struct {
	Host   string `json:"host"`
	Kind   string `json:"kind"`
	UID    string `json:"uid"`
	Title  string `json:"title"`
	Exists bool   `json:"exists"`
}

// Suggestions mapeia (host, serviço) → starter pack ainda não criado.
func (h *Handler) Suggestions(w http.ResponseWriter, r *http.Request) {
	svc, err := h.st.ListHostServices(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	svc = scopedServices(r.Context(), svc)
	out := []Suggestion{}
	for _, s := range svc {
		if !dashboards.SupportedServicePack(s.Kind) {
			continue
		}
		_, uid, title, ok := dashboards.ServicePackJSON(s.Hostname, s.Kind)
		if !ok {
			continue
		}
		out = append(out, Suggestion{
			Host: s.Hostname, Kind: s.Kind, UID: uid, Title: title,
			Exists: h.st.DashboardExists(r.Context(), uid),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestions": out})
}

// Apply cria o dashboard starter de um serviço num host (sob confirmação do usuário).
func (h *Handler) Apply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host string `json:"host"`
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Host == "" || req.Kind == "" {
		http.Error(w, "host e kind obrigatórios", http.StatusBadRequest)
		return
	}
	model, uid, title, ok := dashboards.ServicePackJSON(req.Host, req.Kind)
	if !ok {
		http.Error(w, "sem starter pack para "+req.Kind, http.StatusBadRequest)
		return
	}
	actor := "desconhecido"
	if c, ok := auth.ClaimsFrom(r.Context()); ok {
		actor = c.Name
	}
	if err := h.st.CreateDashboard(r.Context(), uid, title, "Serviços", model, actor); err != nil {
		// já existe → devolve o uid mesmo assim (idempotente para o botão "criar").
		if !h.st.DashboardExists(r.Context(), uid) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"uid": uid})
}

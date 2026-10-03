package ingest

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/eduardorarruda/revoada/gateway/internal/pg"
)

// DiscoveryHandler recebe o inventário de auto-discovery do agente (P6.2).
type DiscoveryHandler struct {
	log   *slog.Logger
	store *pg.Store
}

func NewDiscovery(log *slog.Logger, store *pg.Store) *DiscoveryHandler {
	return &DiscoveryHandler{log: log, store: store}
}

type discoveryReq struct {
	Host     string `json:"host"`
	Services []struct {
		Kind   string `json:"kind"`
		Detail string `json:"detail"`
		Source string `json:"source"`
	} `json:"services"`
	DockerPresent    bool     `json:"docker_present"`
	DockerContainers []string `json:"docker_containers"`
}

func (h *DiscoveryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("X-Revoada-Key")
	agent, err := h.store.Authenticate(r.Context(), key)
	switch {
	case errors.Is(err, pg.ErrUnknownAgent):
		http.Error(w, "chave desconhecida", http.StatusUnauthorized)
		return
	case err != nil:
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	case agent.Revoked:
		http.Error(w, "chave revogada", http.StatusForbidden)
		return
	}

	var req discoveryReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.Host == "" {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}

	services := make([]pg.HostService, 0, len(req.Services)+1)
	for _, s := range req.Services {
		if s.Kind == "" {
			continue
		}
		services = append(services, pg.HostService{Kind: s.Kind, Detail: s.Detail, Source: s.Source})
	}
	if req.DockerPresent {
		services = append(services, pg.HostService{
			Kind: "docker", Source: "docker",
			Detail: strings.Join(req.DockerContainers, ","),
		})
	}

	if err := h.store.SaveDiscovery(r.Context(), agent.TenantID, req.Host, services); err != nil {
		h.log.Warn("discovery: salvando", "err", err)
		http.Error(w, "erro salvando", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"saved":` + itoa(len(services)) + `}`))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

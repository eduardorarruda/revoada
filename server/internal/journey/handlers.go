package journey

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

type Handler struct{ st *store.Store }

func NewHandler(st *store.Store) *Handler { return &Handler{st: st} }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	js, err := h.st.ListJourneys(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if js == nil {
		js = []store.Journey{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"journeys": js})
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var req store.Journey
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || len(req.Steps) == 0 {
		http.Error(w, "name e ao menos 1 passo obrigatórios", http.StatusBadRequest)
		return
	}
	id, err := h.st.CreateJourney(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	if err := h.st.DeleteJourney(r.Context(), id); err != nil {
		http.Error(w, "não encontrado", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

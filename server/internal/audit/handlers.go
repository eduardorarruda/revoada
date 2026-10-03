package audit

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

// Handler serve a consulta da trilha (tela Auditoria). Leitura admin-only — quem
// pode ver o que os outros fizeram é só o administrador.
type Handler struct{ st *store.Store }

func NewHandler(st *store.Store) *Handler { return &Handler{st: st} }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// List devolve as entradas filtradas (mais recentes primeiro), o total para a
// paginação e a lista de recursos existentes (para o filtro da tela).
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.AuditFilter{
		Actor:    q.Get("actor"),
		Resource: q.Get("resource"),
		Method:   q.Get("method"),
		Limit:    atoiOr(q.Get("limit"), 100),
		Offset:   atoiOr(q.Get("offset"), 0),
	}
	// Janela opcional: o front manda os limites do dia no fuso do navegador, então
	// a fronteira bate com a data que o usuário escolheu na tela.
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			http.Error(w, "from inválido (use RFC3339)", http.StatusBadRequest)
			return
		}
		f.From = t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			http.Error(w, "to inválido (use RFC3339)", http.StatusBadRequest)
			return
		}
		f.To = t
	}

	entries, total, err := h.st.ListAudit(r.Context(), f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resources, _ := h.st.AuditResources(r.Context()) // best-effort: só alimenta o filtro
	if resources == nil {
		resources = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries":   entries,
		"total":     total,
		"resources": resources,
	})
}

func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// Integridade recalcula a corrente de hashes da trilha e diz se alguma linha foi
// alterada ou apagada no meio (ARQUITETURA §15).
func (h *Handler) Integridade(w http.ResponseWriter, r *http.Request) {
	res, err := h.st.VerificarIntegridadeAuditoria(r.Context())
	if err != nil {
		http.Error(w, "erro ao verificar a trilha", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

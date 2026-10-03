package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/eduardorarruda/revoada/gateway/internal/pg"
)

// ProbeStore é o que o endpoint de sondas precisa do PostgreSQL. É uma interface (e
// não *pg.Store) para que o comportamento do endpoint — em particular a RECUSA de um
// voto forjado — tenha teste sem depender de um banco de pé.
type ProbeStore interface {
	Authenticate(ctx context.Context, serverkey string) (pg.Agent, error)
	AssignedChecks(ctx context.Context, tenant, location string) ([]pg.AssignedURL, error)
	BindProbeLocation(ctx context.Context, tenant, serverkey, location string) error
	UpsertProbeResult(ctx context.Context, tenant string, r pg.ProbeResult) error
}

// ProbeResultHandler recebe resultados de sondas multi-região (P6.3).
type ProbeResultHandler struct {
	log   *slog.Logger
	store ProbeStore
}

func NewProbeResult(log *slog.Logger, store ProbeStore) *ProbeResultHandler {
	return &ProbeResultHandler{log: log, store: store}
}

type probeReq struct {
	URL           string  `json:"url"`
	ProbeLocation string  `json:"probe_location"`
	Up            bool    `json:"up"`
	TotalMs       float64 `json:"total_ms"`
	Diagnostic    string  `json:"diagnostic"`

	// Truncated e Status EXISTIAM no que o agente-sonda manda (ver
	// agent/internal/probe/probe.go: os campos `truncated` e `status` do result) e não
	// existiam aqui. json.Decode ignora campo desconhecido em SILÊNCIO: os dois vinham
	// no corpo de toda requisição e eram jogados fora sem uma linha de log.
	//
	// Eles existem para o painel não mentir:
	//
	//   - Truncated: a sonda cortou o corpo no teto de leitura, então `total_ms` é um
	//     PISO (o resto da transferência não entrou na conta) e a asserção de
	//     palavra-chave é INDETERMINADA — não dá para afirmar que a palavra não está
	//     lá. O consenso do servidor já sabe não contar um reporte truncado como voto
	//     de falha (sitecheck.votoDeFalha), mas só se o campo chegar até ele.
	//   - Status: o código HTTP que a sonda viu. É a diferença entre "não está no ar" e
	//     "respondeu 401 quando o check esperava 200" na tela.
	Truncated bool `json:"truncated"`
	Status    int  `json:"status"`
}

// Assignments (GET /probe/assignments?location=X) devolve as URLs que esta sonda
// deve monitorar (site_checks com a location em probe_locations). Auth por chave.
func (h *ProbeResultHandler) Assignments(w http.ResponseWriter, r *http.Request) {
	agent, err := h.store.Authenticate(r.Context(), r.Header.Get("X-Revoada-Key"))
	if err != nil || agent.Revoked {
		http.Error(w, "não autorizado", http.StatusUnauthorized)
		return
	}
	loc := r.URL.Query().Get("location")
	if loc == "" {
		http.Error(w, "location obrigatória", http.StatusBadRequest)
		return
	}
	urls, err := h.store.AssignedChecks(r.Context(), agent.TenantID, loc)
	if err != nil {
		http.Error(w, "erro", http.StatusInternalServerError)
		return
	}
	if urls == nil {
		urls = []pg.AssignedURL{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"urls": urls})
}

func (h *ProbeResultHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	agent, err := h.store.Authenticate(r.Context(), r.Header.Get("X-Revoada-Key"))
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
	var req probeReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.URL == "" || req.ProbeLocation == "" {
		http.Error(w, "url e probe_location obrigatórios", http.StatusBadRequest)
		return
	}
	// Voto de sonda é VOTO: uma chave, uma localização. Ver BindProbeLocation — sem
	// isto, uma única serverkey forjava `caos-fake-tokyo` e `caos-fake-berlim` e
	// derrubava, por consenso, um site que estava no ar.
	key := r.Header.Get("X-Revoada-Key")
	switch err := h.store.BindProbeLocation(r.Context(), agent.TenantID, key, req.ProbeLocation); {
	case errors.Is(err, pg.ErrProbeLocationDenied):
		h.log.Warn("probe-result: localização não registrada para a chave",
			"tenant", agent.TenantID, "location", req.ProbeLocation)
		http.Error(w, "localização de sonda não registrada para esta chave", http.StatusForbidden)
		return
	case err != nil:
		h.log.Warn("probe-result: vinculando localização", "err", err)
		http.Error(w, "erro salvando", http.StatusServiceUnavailable)
		return
	}
	if err := h.store.UpsertProbeResult(r.Context(), agent.TenantID, pg.ProbeResult{
		URL: req.URL, Location: req.ProbeLocation, Up: req.Up, TotalMs: req.TotalMs,
		Diagnostic: req.Diagnostic, Truncated: req.Truncated, Status: req.Status,
	}); err != nil {
		h.log.Warn("probe-result: salvando", "err", err)
		http.Error(w, "erro salvando", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

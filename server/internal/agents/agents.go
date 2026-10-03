// Package agents expõe a gestão das chaves de ingestão (serverkeys) dos agentes:
// listar, gerar e revogar/apagar. Todas as rotas são restritas a admin (httpapi).
package agents

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

type Handler struct {
	st  *store.Store
	log *slog.Logger
}

func NewHandler(st *store.Store, log *slog.Logger) *Handler {
	return &Handler{st: st, log: log}
}

// agentID é o identificador PÚBLICO de uma chave (ver store.AgentID). Mora no store
// porque o instalador também precisa dele e não pode importar este pacote (agents já
// importa installer) — uma definição só evita dois ids divergentes para a mesma chave.
func agentID(serverkey string) string { return store.AgentID(serverkey) }

// maskKey mostra o suficiente para o operador reconhecer a chave numa lista e não o
// bastante para usá-la.
func maskKey(k string) string {
	if len(k) <= 6 {
		if k == "" {
			return ""
		}
		return "***"
	}
	return k[:4] + "…" + k[len(k)-2:]
}

// agentDTO é a linha da listagem: MASCARADA.
type agentDTO struct {
	ID        string     `json:"id"`
	Serverkey string     `json:"serverkey"` // mascarada — nunca a chave real
	TenantID  string     `json:"tenant_id"`
	Hostname  string     `json:"hostname"`
	Revoked   bool       `json:"revoked"`
	LastSeen  *time.Time `json:"last_seen"`
	CreatedAt time.Time  `json:"created_at"`
	// Hold/HoldReason: auto-atualização segurada NESTE host (ver update_policy.go).
	Hold       bool   `json:"update_hold"`
	HoldReason string `json:"update_hold_reason,omitempty"`
	// Report é o último relato de atualização que o agente mandou.
	Report *store.AgentUpdateReport `json:"update_report,omitempty"`
}

// List devolve as chaves com a serverkey MASCARADA (admin).
//
// Antes ia a frota inteira em claro: uma única sessão de admin comprometida (XSS,
// token roubado, laptop aberto) colhia num GET a credencial de ingestão de TODOS os
// servidores — e com serverkey se escreve métrica, log e heartbeat em nome do host.
// Medido em dev: `GET /api/agents` devolvia 6 chaves completas, entre elas a de
// produção-simulada. O painel já sabia fazer diferente (provision/handlers.go usa
// maskKey desde a Fase G); a tela de chaves é que não usava.
//
// A chave completa passa a existir em dois momentos só: na CRIAÇÃO (uma vez, para
// copiar no instalador) e num POST /api/agents/reveal explícito, uma chave por vez,
// que fica na trilha de auditoria. Roubar a frota deixa de ser um GET e passa a ser
// N requisições registradas.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	list, err := h.st.ListAgents(r.Context())
	if err != nil {
		http.Error(w, "erro ao listar chaves", http.StatusInternalServerError)
		return
	}
	pol, err := h.st.ListAgentUpdatePolicies(r.Context())
	if err != nil {
		// A política é informação acessória: sem ela a lista ainda é útil.
		h.log.Warn("chaves de agente: não consegui ler as políticas de atualização", "err", err)
		pol = map[string]store.AgentUpdatePolicy{}
	}
	out := make([]agentDTO, 0, len(list))
	for _, a := range list {
		d := agentDTO{
			ID: agentID(a.Serverkey), Serverkey: maskKey(a.Serverkey), TenantID: a.TenantID,
			Hostname: a.Hostname, Revoked: a.Revoked, LastSeen: a.LastSeen, CreatedAt: a.CreatedAt,
		}
		if p, ok := pol[a.Serverkey]; ok {
			d.Hold, d.HoldReason, d.Report = p.Hold, p.HoldReason, p.Report
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": out})
}

// errChaveNaoEncontrada distingue "não achei a chave" de erro de banco.
var errChaveNaoEncontrada = errors.New("chave não encontrada")

// resolverChave traduz o identificador vindo do cliente na serverkey real. Aceita o
// `id` público (caminho novo) e, por compatibilidade, a serverkey em claro — que
// clientes antigos ainda mandam. Resolver por id exige varrer a lista, mas ela tem
// tamanho de frota (dezenas), não de telemetria.
func (h *Handler) resolverChave(ctx context.Context, id, serverkey string) (string, error) {
	if s := strings.TrimSpace(serverkey); s != "" {
		return s, nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return "", errChaveNaoEncontrada
	}
	list, err := h.st.ListAgents(ctx)
	if err != nil {
		return "", err
	}
	for _, a := range list {
		if agentID(a.Serverkey) == id {
			return a.Serverkey, nil
		}
	}
	return "", errChaveNaoEncontrada
}

// Reveal devolve UMA serverkey em claro, a partir do id público. Admin-only e POST de
// propósito: POST passa pelo middleware de auditoria, então "quem revelou a chave de
// qual servidor e quando" fica registrado — que é a diferença entre um vazamento
// silencioso e um incidente investigável.
func (h *Handler) Reveal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.ID) == "" {
		http.Error(w, "id obrigatório", http.StatusBadRequest)
		return
	}
	key, err := h.resolverChave(r.Context(), req.ID, "")
	if errors.Is(err, errChaveNaoEncontrada) {
		http.Error(w, "chave não encontrada", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "erro ao consultar chaves", http.StatusInternalServerError)
		return
	}
	// Cache-Control: a chave não pode ficar em cache de proxy/navegador.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"id": req.ID, "serverkey": key})
}

// Create gera uma serverkey aleatória para um host e a devolve uma vez.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hostname string `json:"hostname"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	req.Hostname = strings.TrimSpace(req.Hostname)
	if req.Hostname == "" {
		http.Error(w, "hostname obrigatório", http.StatusBadRequest)
		return
	}
	key, err := auth.RandomToken()
	if err != nil {
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	if err := h.st.CreateAgent(r.Context(), key, "default", req.Hostname); err != nil {
		http.Error(w, "erro ao criar chave", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"serverkey": key, "hostname": req.Hostname})
}

// Revoke liga/desliga a revogação de uma chave. Identifica por `id` (o da listagem
// mascarada) ou, para clientes antigos, pela `serverkey` em claro.
func (h *Handler) Revoke(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID        string `json:"id"`
		Serverkey string `json:"serverkey"`
		Revoked   bool   `json:"revoked"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	key, err := h.resolverChave(r.Context(), req.ID, req.Serverkey)
	if err != nil {
		http.Error(w, "id ou serverkey obrigatório (chave não encontrada)", http.StatusNotFound)
		return
	}
	if err := h.st.SetAgentRevoked(r.Context(), key, req.Revoked); err != nil {
		http.Error(w, "chave não encontrada", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Delete apaga a chave de vez. Mesma identificação do Revoke.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID        string `json:"id"`
		Serverkey string `json:"serverkey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	key, err := h.resolverChave(r.Context(), req.ID, req.Serverkey)
	if err != nil {
		http.Error(w, "id ou serverkey obrigatório (chave não encontrada)", http.StatusNotFound)
		return
	}
	if err := h.st.DeleteAgent(r.Context(), key); err != nil {
		http.Error(w, "chave não encontrada", http.StatusNotFound)
		return
	}
	// A política de atualização daquela chave morre junto: sem isto, uma linha órfã
	// ficaria segurando a atualização de uma serverkey futura com o mesmo valor.
	if err := h.st.DeleteAgentUpdatePolicy(r.Context(), key); err != nil {
		h.log.Warn("chaves de agente: não consegui limpar a política da chave apagada", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

package notify

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/eduardorarruda/revoada/server/internal/safehttp"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// validateChannel confere o que dá para conferir na hora de criar/editar, para o
// admin descobrir o erro no formulário e não no alerta perdido.
//
// URL: rejeita esquema inválido nos canais que fazem requisição de saída. O bloqueio
// de IPs internos/metadata é reforçado no dial (safehttp.Transport), então aqui basta
// garantir http/https e presença da URL onde é obrigatória.
// WhatsApp: confere também o formato do LID (ver validarLIDs).
func validateChannel(req store.NotificationChannel) error {
	switch req.Type {
	case "webhook":
		if u, _ := req.Config["url"].(string); u != "" {
			return safehttp.ValidateURL(u)
		}
	case "whatsapp":
		if u, _ := req.Config["base_url"].(string); u != "" {
			if err := safehttp.ValidateURL(u); err != nil {
				return err
			}
		}
		return validarLIDs(req.Config)
	}
	return nil
}

// validarLIDs recusa LID fora de formato na hora de salvar o canal.
//
// É o campo onde um dígito errado sai caro: um canal foi salvo com "1" e passou
// dias sem entregar, porque o WhatsApp recusa o endereço inexistente com erro 463 —
// e, até 05/08/2026, o painel encobria a recusa e registrava "enviado". LID de
// verdade tem 14 a 16 dígitos (ex.: 123456789012345). Vazio continua valendo: é
// legítimo não saber o LID de alguém.
func validarLIDs(cfg map[string]any) error {
	lids, _ := cfg["lid"].(string)
	for _, lid := range strings.Split(lids, ",") {
		v := strings.TrimSuffix(strings.TrimSpace(lid), "@lid")
		if v == "" {
			continue
		}
		if strings.TrimLeft(v, "0123456789") != "" || len(v) < 14 || len(v) > 16 {
			return fmt.Errorf(
				"LID inválido: %q. O LID do WhatsApp tem de 14 a 16 dígitos (ex.: 123456789012345). "+
					"Para descobrir o de alguém, peça que a pessoa envie uma mensagem para o número do painel; "+
					"se não souber, deixe em branco", strings.TrimSpace(lid))
		}
	}
	return nil
}

// Handler expõe a API de notificações (canais, rotas, auditoria, teste).
type Handler struct {
	st     *store.Store
	router *Router
}

func NewHandler(st *store.Store, router *Router) *Handler { return &Handler{st: st, router: router} }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// --- Canais ---

func (h *Handler) ListChannels(w http.ResponseWriter, r *http.Request) {
	chs, err := h.st.ListChannels(r.Context(), true) // redação de segredos
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if chs == nil {
		chs = []store.NotificationChannel{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": chs})
}

func (h *Handler) CreateChannel(w http.ResponseWriter, r *http.Request) {
	var req store.NotificationChannel
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.Type == "" {
		http.Error(w, "name e type obrigatórios", http.StatusBadRequest)
		return
	}
	if _, err := senderFor(req.Type); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateChannel(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id, err := h.st.CreateChannel(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (h *Handler) UpdateChannel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	var req store.NotificationChannel
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.Type == "" {
		http.Error(w, "name e type obrigatórios", http.StatusBadRequest)
		return
	}
	if _, err := senderFor(req.Type); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateChannel(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.st.UpdateChannel(r.Context(), id, req); err != nil {
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "não encontrado", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) DeleteChannel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	if err := h.st.DeleteChannel(r.Context(), id); err != nil {
		http.Error(w, "não encontrado", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) TestChannel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	// `ok:false` com status `unverified` é o caso em que o provedor aceitou mas
	// ninguém confirma a entrega: não é um ✓ verde, e também não é uma falha — a
	// tela mostra o aviso em amarelo com o que fazer para resolver.
	if err := h.router.SendTest(r.Context(), id); err != nil {
		status := "error"
		var semConf *SemConfirmacao
		if errors.As(err, &semConf) {
			status = statusSemConfirmacao
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "status": status, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "sent"})
}

// --- Integração WhatsApp (wuzapi): config única reusada pelos canais ---

// GetWhatsAppIntegration devolve a config global com o token mascarado (para a UI).
func (h *Handler) GetWhatsAppIntegration(w http.ResponseWriter, r *http.Request) {
	wa, err := h.st.GetWhatsAppIntegration(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	hasToken := wa.Token != ""
	token := ""
	if hasToken {
		token = "••••••"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"base_url":   wa.BaseURL,
		"token":      token,
		"configured": wa.BaseURL != "" && hasToken,
	})
}

// UpdateWhatsAppIntegration grava a config global. token em branco preserva o atual.
func (h *Handler) UpdateWhatsAppIntegration(w http.ResponseWriter, r *http.Request) {
	var req store.WhatsAppIntegration
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "corpo inválido", http.StatusBadRequest)
		return
	}
	if err := h.st.SetWhatsAppIntegration(r.Context(), req); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- Auditoria (histórico de envios) ---

// Log devolve o histórico de notificações enviadas (canal, assunto, status). O
// histórico segue vivo — apenas mudou de aba para um modal na tela de Canais.
func (h *Handler) Log(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := h.st.ListNotificationLog(r.Context(), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []store.NotificationLogEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"log": entries})
}

// Nota: rotas de notificação, políticas de escalonamento e plantão (on-call) foram
// DESCONTINUADOS. Os handlers e o roteamento por rotas foram removidos; os alertas
// agora caem direto em TODOS os canais habilitados (ver Router.Notify). As tabelas e
// os métodos de store foram preservados (sem uso), para não perder dados nem mexer no
// pacote store compartilhado. O histórico/auditoria de envios PERMANECE (ver Log).

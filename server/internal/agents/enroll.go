package agents

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// Inscrição de servidor (instalador universal).
//
// O instalador por servidor carrega a chave de ingestão pronta — por isso é um
// arquivo por máquina. O instalador UNIVERSAL carrega, no lugar dela, um token de
// inscrição: ao rodar, o agente chama a rota pública abaixo dizendo só o seu
// hostname, e recebe uma chave própria, criada na hora. Um arquivo, quantos
// servidores forem precisos.
//
// Três decisões que valem estar escritas:
//
//  1. A rota é PÚBLICA (o servidor que está entrando ainda não tem credencial
//     nenhuma), então o httpapi a protege com limite por IP e ela entra na trilha
//     de auditoria — cada servidor que entra fica registrado.
//
//  2. Cada inscrição cria uma chave NOVA. Reaproveitar a chave existente de um
//     hostname seria pior que inútil: quem tivesse o token pediria a inscrição
//     com o hostname do seu servidor de produção e receberia a chave dele de
//     bandeja. Reinstalar a mesma máquina, portanto, deixa a chave anterior para
//     trás — dá para revogá-la na lista.
//
//  3. Token inválido e token revogado devolvem a MESMA resposta. Quem chama aqui
//     não está autenticado; separar os dois casos só ajudaria quem adivinha token.

type enrollReq struct {
	Token    string `json:"token"`
	Hostname string `json:"hostname"`
}

// Enroll troca um token de inscrição por uma chave de ingestão nova.
func (h *Handler) Enroll(w http.ResponseWriter, r *http.Request) {
	var req enrollReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	req.Hostname = strings.TrimSpace(req.Hostname)
	if req.Token == "" || req.Hostname == "" {
		http.Error(w, "token e hostname são obrigatórios", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	tenant, err := h.st.ConsumeEnrollToken(ctx, req.Token)
	if errors.Is(err, store.ErrEnrollTokenInvalido) {
		// Sem detalhe: o instalador mostra esta frase ao administrador da máquina,
		// e ela precisa dizer o que fazer sem contar o que existe do outro lado.
		http.Error(w, "token de inscrição inválido ou revogado, gere um instalador novo no painel", http.StatusUnauthorized)
		return
	}
	if err != nil {
		h.log.Error("inscrição: falha ao validar token", "err", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}

	key, err := auth.RandomToken()
	if err != nil {
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	if err := h.st.CreateAgent(ctx, key, tenant, req.Hostname); err != nil {
		h.log.Error("inscrição: falha ao criar a chave", "host", req.Hostname, "err", err)
		http.Error(w, "erro ao criar a chave", http.StatusInternalServerError)
		return
	}
	h.log.Info("servidor inscrito pelo instalador universal", "host", req.Hostname)
	writeJSON(w, http.StatusCreated, map[string]any{"key": key, "hostname": req.Hostname})
}

// ─── Gestão dos tokens (admin) ───────────────────────────────────────────────

// ListEnrollTokens devolve os tokens com o contador de uso.
func (h *Handler) ListEnrollTokens(w http.ResponseWriter, r *http.Request) {
	list, err := h.st.ListEnrollTokens(r.Context())
	if err != nil {
		http.Error(w, "erro ao listar tokens", http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []store.EnrollToken{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": list})
}

// CreateEnrollToken gera um token de inscrição.
func (h *Handler) CreateEnrollToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Label string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	label := strings.TrimSpace(req.Label)
	if label == "" {
		http.Error(w, "dê um nome ao instalador (ex.: \"Mutirão agosto\") para saber depois quem entrou por ele", http.StatusBadRequest)
		return
	}
	token, err := auth.RandomToken()
	if err != nil {
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	var por string
	if claims, ok := auth.ClaimsFrom(r.Context()); ok {
		por = claims.Name
	}
	if err := h.st.CreateEnrollToken(r.Context(), token, label, por); err != nil {
		http.Error(w, "erro ao criar o token", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "label": label})
}

// RevokeEnrollToken liga/desliga a revogação (revoked no corpo).
func (h *Handler) RevokeEnrollToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token   string `json:"token"`
		Revoked bool   `json:"revoked"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" {
		http.Error(w, "token obrigatório", http.StatusBadRequest)
		return
	}
	if err := h.st.SetEnrollTokenRevoked(r.Context(), req.Token, req.Revoked); err != nil {
		http.Error(w, "token não encontrado", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteEnrollToken apaga o token.
func (h *Handler) DeleteEnrollToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" {
		http.Error(w, "token obrigatório", http.StatusBadRequest)
		return
	}
	if err := h.st.DeleteEnrollToken(r.Context(), req.Token); err != nil {
		http.Error(w, "token não encontrado", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

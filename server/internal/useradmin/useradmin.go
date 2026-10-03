// Package useradmin expõe a administração de usuários, grupos de servidores e
// permissões por servidor (tela "Usuários & Acessos"). TODAS as rotas são admin-only
// (aplicado no httpapi). A permissão efetiva que estes dados produzem é consumida pelo
// enforcement (pacote authz, Fase 2) e pelo dispatch de notificações (Fase 3).
package useradmin

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	"github.com/eduardorarruda/revoada/core/seguranca/senha"
	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/notify"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// Invalidator permite ao handler avisar o cache de escopo (authz.Resolver, Fase 2) que
// as permissões de um usuário mudaram. Nil-safe: na Fase 1 ainda não há cache.
type Invalidator func(userID int64)

type Handler struct {
	st         *store.Store
	log        *slog.Logger
	invalidate Invalidator
}

func NewHandler(st *store.Store, log *slog.Logger, inv Invalidator) *Handler {
	if inv == nil {
		inv = func(int64) {}
	}
	return &Handler{st: st, log: log, invalidate: inv}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// --- usuários ---

// ListUsers devolve os usuários + os hostnames do inventário ainda sem permissão
// atribuída (para o banner de aviso na tela).
func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.st.ListUsers(r.Context())
	if err != nil {
		http.Error(w, "erro ao listar usuários", http.StatusInternalServerError)
		return
	}
	if users == nil {
		users = []store.UserAdmin{}
	}
	unassigned, err := h.st.HostsWithoutPermission(r.Context())
	if err != nil {
		http.Error(w, "erro ao listar servidores", http.StatusInternalServerError)
		return
	}
	if unassigned == nil {
		unassigned = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"users":                    users,
		"hosts_without_permission": unassigned,
	})
}

// CreateUser cria um usuário com perfil (Nome, email, celular) e senha provisória
// (must_reset_password=true). O login é por EMAIL: guardamos username = email, então o
// fluxo de autenticação (UserByUsername) não muda. Se houver celular, já cria o canal
// pessoal de WhatsApp vinculado ao usuário (nosso canal principal). Papel admin|user;
// reusa o hash Argon2id do pacote auth.
func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FullName string `json:"full_name"`
		Email    string `json:"email"`
		Phone    string `json:"phone"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	req.FullName = strings.TrimSpace(req.FullName)
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.Phone = strings.TrimSpace(req.Phone)
	if req.FullName == "" {
		http.Error(w, "informe o nome do usuário", http.StatusBadRequest)
		return
	}
	if !validEmail(req.Email) {
		http.Error(w, "informe um email válido", http.StatusBadRequest)
		return
	}
	if len(onlyDigits(req.Phone)) < 10 { // DDD (2) + número (8/9) no mínimo
		http.Error(w, "informe um celular válido com DDD", http.StatusBadRequest)
		return
	}
	// Normaliza para o formato que a Evolution API aceita: DDI 55 + DDD + número. Sem o
	// 55 o envio falha com {"exists":false}. Guardamos o número já normalizado.
	phoneNumber := notify.NormalizeWhatsAppNumber(req.Phone)
	if err := senha.Validar(req.Password, req.Email); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	role := normalizeRole(req.Role)
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	id, err := h.st.CreateUserFull(r.Context(), store.NewUser{
		Username:     req.Email, // login por email
		PasswordHash: hash,
		Role:         role,
		MustReset:    true,
		FullName:     req.FullName,
		Email:        req.Email,
		Phone:        phoneNumber,
	})
	if err != nil {
		if errors.Is(err, store.ErrEmailTaken) || errors.Is(err, store.ErrUsernameTaken) {
			http.Error(w, "já existe um usuário com esse email", http.StatusConflict)
			return
		}
		http.Error(w, "erro ao criar usuário", http.StatusInternalServerError)
		return
	}

	// Canal pessoal de WhatsApp: recebe SÓ os alertas dos servidores em que o usuário
	// tiver "Notificar" ligado (não entra no fan-out das regras). base_url/instance/apikey
	// vêm da Integração WhatsApp global no envio (effectiveConfig) — o canal guarda só o
	// destino. Best-effort: se a criação do canal falhar, o usuário já existe; o admin
	// pode criar/vincular o canal manualmente em Canais.
	channelID, cerr := h.st.CreateChannel(r.Context(), store.NotificationChannel{
		Name:   req.FullName,
		Type:   "whatsapp",
		Config: map[string]any{"to": phoneNumber},
		UserID: &id,
	})
	if cerr != nil {
		h.log.Warn("useradmin: falha ao criar canal pessoal de whatsapp", "user", id, "err", cerr)
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         id,
		"email":      req.Email,
		"role":       role,
		"channel_id": channelID,
	})
}

// validEmail faz a validação básica de sintaxe de email (net/mail) e exige uma arroba
// com domínio contendo ponto — suficiente para pegar erros de digitação sem barrar
// endereços legítimos.
func validEmail(s string) bool {
	if s == "" {
		return false
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s {
		return false
	}
	at := strings.LastIndex(s, "@")
	return at > 0 && strings.Contains(s[at+1:], ".")
}

// onlyDigits mantém apenas os dígitos de uma string (para normalizar o celular no
// formato que a Evolution API espera: só números, com DDI+DDD).
func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// PatchUser altera papel, ativa/desativa ou redefine a senha de um usuário. Protege o
// último admin ativo (não deixa demover/desativar quem sobrou).
func (h *Handler) PatchUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	var req struct {
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
		Password *string `json:"password"`
		ResetMFA bool    `json:"reset_mfa"` // apaga o 2FA (aparelho perdido)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	target, err := h.st.UserByID(r.Context(), id)
	if err != nil {
		http.Error(w, "usuário não encontrado", http.StatusNotFound)
		return
	}

	// Guarda do último admin: bloqueia demover ou desativar o único admin ativo.
	demoting := req.Role != nil && normalizeRole(*req.Role) != "admin" && target.Role == "admin"
	disabling := req.Disabled != nil && *req.Disabled && !target.Disabled && target.Role == "admin"
	if demoting || disabling {
		n, err := h.st.CountAdmins(r.Context())
		if err != nil {
			http.Error(w, "erro interno", http.StatusInternalServerError)
			return
		}
		if n <= 1 {
			http.Error(w, "não é possível remover o último administrador ativo", http.StatusConflict)
			return
		}
	}

	if req.Role != nil {
		if err := h.st.UpdateUserRole(r.Context(), id, normalizeRole(*req.Role)); err != nil {
			http.Error(w, "erro ao atualizar papel", http.StatusInternalServerError)
			return
		}
	}
	if req.Disabled != nil {
		if err := h.st.SetUserDisabled(r.Context(), id, *req.Disabled); err != nil {
			http.Error(w, "erro ao atualizar status", http.StatusInternalServerError)
			return
		}
		if *req.Disabled {
			_ = h.st.RevokeUserSessions(r.Context(), id) // derruba tokens em circulação
		}
	}
	if req.ResetMFA {
		// Usuário perdeu o aparelho: o admin apaga o 2FA e derruba as sessões; no
		// próximo login a pessoa cadastra o celular de novo.
		if err := h.st.DesativarMFA(r.Context(), id); err != nil {
			http.Error(w, "erro ao redefinir o 2FA", http.StatusInternalServerError)
			return
		}
		_ = h.st.RevokeUserSessions(r.Context(), id)
	}
	if req.Password != nil {
		if err := senha.Validar(*req.Password, target.Username); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		hash, err := auth.HashPassword(*req.Password)
		if err != nil {
			http.Error(w, "erro interno", http.StatusInternalServerError)
			return
		}
		if err := h.st.ResetUserPassword(r.Context(), id, hash); err != nil {
			http.Error(w, "erro ao redefinir senha", http.StatusInternalServerError)
			return
		}
		_ = h.st.RevokeUserSessions(r.Context(), id)
	}
	h.invalidate(id)
	w.WriteHeader(http.StatusNoContent)
}

// DeleteUser exclui um usuário de vez (e, por cascata no banco, suas permissões, vínculos
// de grupo, sessões e o canal pessoal de WhatsApp). Protege o último administrador ATIVO:
// não deixa excluir quem é o único admin que ainda pode logar.
func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	target, err := h.st.UserByID(r.Context(), id)
	if err != nil {
		http.Error(w, "usuário não encontrado", http.StatusNotFound)
		return
	}
	if target.Role == "admin" && !target.Disabled {
		n, err := h.st.CountAdmins(r.Context())
		if err != nil {
			http.Error(w, "erro interno", http.StatusInternalServerError)
			return
		}
		if n <= 1 {
			http.Error(w, "não é possível excluir o último administrador ativo", http.StatusConflict)
			return
		}
	}
	if err := h.st.DeleteUser(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "usuário não encontrado", http.StatusNotFound)
			return
		}
		http.Error(w, "erro ao excluir usuário", http.StatusInternalServerError)
		return
	}
	h.invalidate(id)
	w.WriteHeader(http.StatusNoContent)
}

// --- permissões de um usuário ---

// GetUserPerms devolve as permissões diretas, os grupos do usuário e a permissão efetiva
// (para a tela pré-marcar a matriz e mostrar o que é herdado).
func (h *Handler) GetUserPerms(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	direct, err := h.st.DirectServerPerms(r.Context(), id)
	if err != nil {
		http.Error(w, "erro ao ler permissões", http.StatusInternalServerError)
		return
	}
	groups, err := h.st.UserGroups(r.Context(), id)
	if err != nil {
		http.Error(w, "erro ao ler grupos", http.StatusInternalServerError)
		return
	}
	effective, err := h.st.EffectiveServerPerms(r.Context(), id)
	if err != nil {
		http.Error(w, "erro ao calcular permissão efetiva", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"direct":    orEmptyPerms(direct),
		"groups":    orEmptyLinks(groups),
		"effective": orEmptyPerms(effective),
	})
}

// SetUserPerms substitui as permissões diretas E os grupos do usuário (a tela envia o
// estado completo). Invalida o cache de escopo ao final.
func (h *Handler) SetUserPerms(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	var req struct {
		Direct []store.ServerPerm    `json:"direct"`
		Groups []store.UserGroupLink `json:"groups"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	if _, err := h.st.UserByID(r.Context(), id); err != nil {
		http.Error(w, "usuário não encontrado", http.StatusNotFound)
		return
	}
	if err := h.st.SetUserServerPerms(r.Context(), id, req.Direct); err != nil {
		http.Error(w, "erro ao salvar permissões", http.StatusInternalServerError)
		return
	}
	if err := h.st.SetUserGroups(r.Context(), id, req.Groups); err != nil {
		http.Error(w, "erro ao salvar grupos", http.StatusInternalServerError)
		return
	}
	h.invalidate(id)
	w.WriteHeader(http.StatusNoContent)
}

// --- grupos de servidores ---

func (h *Handler) ListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := h.st.ListServerGroups(r.Context())
	if err != nil {
		http.Error(w, "erro ao listar grupos", http.StatusInternalServerError)
		return
	}
	if groups == nil {
		groups = []store.ServerGroup{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

func (h *Handler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string   `json:"name"`
		Hosts []string `json:"hosts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		http.Error(w, "nome do grupo obrigatório", http.StatusBadRequest)
		return
	}
	gid, err := h.st.CreateServerGroup(r.Context(), req.Name)
	if err != nil {
		if errors.Is(err, store.ErrUsernameTaken) {
			http.Error(w, "já existe um grupo com esse nome", http.StatusConflict)
			return
		}
		http.Error(w, "erro ao criar grupo", http.StatusInternalServerError)
		return
	}
	if len(req.Hosts) > 0 {
		if err := h.st.SetServerGroupHosts(r.Context(), gid, req.Hosts); err != nil {
			http.Error(w, "erro ao definir servidores do grupo", http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": gid, "name": req.Name})
}

// UpdateGroup renomeia e/ou substitui os servidores do grupo. Mudar os servidores de um
// grupo afeta todos os usuários vinculados → invalida o cache global de escopo.
func (h *Handler) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	var req struct {
		Name  *string   `json:"name"`
		Hosts *[]string `json:"hosts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			http.Error(w, "nome do grupo obrigatório", http.StatusBadRequest)
			return
		}
		if err := h.st.RenameServerGroup(r.Context(), id, name); err != nil {
			if errors.Is(err, store.ErrUsernameTaken) {
				http.Error(w, "já existe um grupo com esse nome", http.StatusConflict)
				return
			}
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "grupo não encontrado", http.StatusNotFound)
				return
			}
			http.Error(w, "erro ao renomear grupo", http.StatusInternalServerError)
			return
		}
	}
	if req.Hosts != nil {
		if err := h.st.SetServerGroupHosts(r.Context(), id, *req.Hosts); err != nil {
			http.Error(w, "erro ao definir servidores do grupo", http.StatusInternalServerError)
			return
		}
	}
	h.invalidate(0) // 0 = invalida todos (mudança de grupo afeta vários usuários)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	if err := h.st.DeleteServerGroup(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "grupo não encontrado", http.StatusNotFound)
			return
		}
		http.Error(w, "erro ao apagar grupo", http.StatusInternalServerError)
		return
	}
	h.invalidate(0)
	w.WriteHeader(http.StatusNoContent)
}

// normalizeRole reduz qualquer entrada aos papéis válidos: admin, operador ou leitor
// (desconhecido vira leitor — menor privilégio).
func normalizeRole(role string) string { return auth.NormalizarPapel(role) }

func orEmptyPerms(p []store.ServerPerm) []store.ServerPerm {
	if p == nil {
		return []store.ServerPerm{}
	}
	return p
}

func orEmptyLinks(l []store.UserGroupLink) []store.UserGroupLink {
	if l == nil {
		return []store.UserGroupLink{}
	}
	return l
}

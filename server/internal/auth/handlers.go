package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/eduardorarruda/revoada/core/seguranca/cofre"
	"github.com/eduardorarruda/revoada/core/seguranca/senha"
	"github.com/eduardorarruda/revoada/server/internal/config"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// dummyHash é um hash Argon2id válido usado para equalizar o tempo de resposta do
// login quando o usuário não existe (evita enumeração de usuários por timing).
// Calculado uma vez no boot.
var dummyHash = func() string { h, _ := HashPassword("revoada-timing-equalizer"); return h }()

const (
	accessTTL  = 15 * time.Minute
	cookieName = "revoada_refresh"
)

// Expiração de sessão (item 11): inatividade = validade do refresh (renovada a cada
// uso); absoluta = limite contado do login, mesmo com uso contínuo.
var (
	refreshTTL        = config.SessaoInatividade()
	sessaoAbsolutaTTL = config.SessaoMaxima()
)

type ctxKey int

const claimsKey ctxKey = 0

// Handler expõe os endpoints de autenticação.
type Handler struct {
	store  *store.Store
	secret string
	cofre  *cofre.Cofre // guarda o segredo do 2FA cifrado (UsarCofre)
}

func NewHandler(s *store.Store, secret string) *Handler { return &Handler{store: s, secret: secret} }

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Register cria um usuário. O PRIMEIRO usuário do sistema vira admin (bootstrap, sem
// autenticação); depois disso, criar usuário é invite-only — exige um admin autenticado
// (fecha o auto-registro público, que dava acesso de leitura a toda a telemetria).
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil || c.Username == "" {
		http.Error(w, "usuário e senha são obrigatórios", http.StatusBadRequest)
		return
	}
	if err := senha.Validar(c.Password, c.Username); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	n, err := h.store.CountUsers(r.Context())
	if err != nil {
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	if n > 0 {
		// já há usuários: só um admin autenticado pode criar novos.
		if cl, ok := h.claimsFromRequest(r); !ok || NormalizarPapel(cl.Role) != PapelAdmin {
			http.Error(w, "somente um admin pode criar usuários", http.StatusForbidden)
			return
		}
	}
	role := PapelLeitor // menor privilégio; o admin promove depois
	if n == 0 {
		role = PapelAdmin // bootstrap: o primeiro usuário do sistema
	}
	hash, err := HashPassword(c.Password)
	if err != nil {
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	id, err := h.store.CreateUser(r.Context(), c.Username, hash, role)
	if err != nil {
		http.Error(w, "usuário já existe", http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "username": c.Username, "role": role})
}

// Login valida credenciais e emite access token + refresh cookie httpOnly.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	u, err := h.store.UserByUsername(r.Context(), c.Username)
	if err != nil {
		// Usuário inexistente: roda um Argon2 dummy para igualar o tempo de resposta
		// ao do caminho "senha errada" (senão o timing revela se o usuário existe).
		_ = VerifyPassword(c.Password, dummyHash)
		http.Error(w, "credenciais inválidas", http.StatusUnauthorized)
		return
	}
	if msg := mensagemBloqueio(u, time.Now()); msg != "" {
		http.Error(w, msg, http.StatusTooManyRequests)
		return
	}
	authed := u.PasswordHash != "" && VerifyPassword(c.Password, u.PasswordHash)
	if !authed {
		h.contarFalha(r.Context(), u.ID)
		http.Error(w, "credenciais inválidas", http.StatusUnauthorized)
		return
	}
	if u.Disabled {
		http.Error(w, "conta desativada, fale com um administrador", http.StatusForbidden)
		return
	}
	_ = h.store.ZerarFalhasLogin(r.Context(), u.ID)
	// Com 2FA ativo, a senha certa só abre a 2ª etapa: nenhuma sessão é emitida aqui.
	if u.MFAAtivo {
		desafio, err := h.emitirDesafio(u.ID, u.Username)
		if err != nil {
			http.Error(w, "erro interno", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"mfa_necessario": true, "desafio": desafio})
		return
	}
	if err := h.issueSession(w, r.Context(), u.ID, u.Username, u.Role, false); err != nil {
		http.Error(w, "erro emitindo sessão", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"role":                NormalizarPapel(u.Role),
		"must_reset_password": u.MustResetPassword,
		"mfa_obrigatorio":     MFAObrigatorio(u.Role),
	})
}

// Refresh rotaciona o refresh token e emite novo access token.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	ck, err := r.Cookie(cookieName)
	if err != nil {
		http.Error(w, "sem refresh", http.StatusUnauthorized)
		return
	}
	hash := HashToken(ck.Value)
	userID, mfa, inicio, err := h.store.SessionByHash(r.Context(), hash)
	if err != nil {
		http.Error(w, "refresh inválido", http.StatusUnauthorized)
		return
	}
	if time.Since(inicio) > sessaoAbsolutaTTL {
		_ = h.store.RevokeSession(r.Context(), hash)
		h.clearCookie(w)
		http.Error(w, "sessão expirada; entre de novo", http.StatusUnauthorized)
		return
	}
	_ = h.store.RevokeSession(r.Context(), hash) // rotação: invalida o antigo
	u, err := h.store.UserByID(r.Context(), userID)
	if err != nil {
		http.Error(w, "usuário sumiu", http.StatusUnauthorized)
		return
	}
	// Conta desativada não renova sessão (defesa em profundidade: desativar já revoga as
	// sessões, mas se a revogação falhar o refresh seguiria emitindo tokens por 7 dias).
	if u.Disabled {
		h.clearCookie(w)
		http.Error(w, "conta desativada, fale com um administrador", http.StatusForbidden)
		return
	}
	// Se o 2FA foi desligado depois, a sessão perde a marca de 2FA no refresh.
	if err := h.emitirSessao(w, r.Context(), u.ID, u.Username, u.Role, mfa && u.MFAAtivo, inicio); err != nil {
		http.Error(w, "erro emitindo sessão", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"role": NormalizarPapel(u.Role)})
}

// Logout revoga a sessão e limpa o cookie.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie(cookieName); err == nil {
		_ = h.store.RevokeSession(r.Context(), HashToken(ck.Value))
	}
	h.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// Me devolve o usuário autenticado + o resumo do seu escopo de servidores, para o front
// esconder botões de edição. Admin recebe `all: true` (vê e edita tudo); usuário comum
// recebe as listas view_hosts/edit_hosts derivadas da permissão efetiva.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	c, ok := ClaimsFrom(r.Context())
	if !ok {
		http.Error(w, "não autenticado", http.StatusUnauthorized)
		return
	}
	u, err := h.store.UserByID(r.Context(), c.Sub)
	if err != nil {
		http.Error(w, "não encontrado", http.StatusNotFound)
		return
	}
	papel := NormalizarPapel(u.Role)
	resp := map[string]any{
		"id":                  u.ID,
		"username":            u.Username,
		"role":                papel,
		"papel_nome":          NomePapel(papel),
		"must_reset_password": u.MustResetPassword,
		"mfa_ativo":           u.MFAAtivo,
		"mfa_obrigatorio":     MFAObrigatorio(papel),
		"sessao_com_mfa":      c.MFA,
	}
	if papel == PapelAdmin {
		resp["all"] = true
	} else {
		perms, err := h.store.EffectiveServerPerms(r.Context(), u.ID)
		if err != nil {
			http.Error(w, "erro ao ler permissões", http.StatusInternalServerError)
			return
		}
		viewHosts := []string{}
		editHosts := []string{}
		for _, p := range perms {
			if p.CanView {
				viewHosts = append(viewHosts, p.Hostname)
			}
			if p.CanEdit {
				editHosts = append(editHosts, p.Hostname)
			}
		}
		resp["all"] = false
		resp["view_hosts"] = viewHosts
		resp["edit_hosts"] = editHosts
	}
	writeJSON(w, http.StatusOK, resp)
}

// ChangePassword deixa o usuário autenticado trocar a própria senha (fluxo de senha
// provisória: must_reset_password some ao trocar). Exige a senha atual, salvo quando o
// usuário está marcado para trocar (primeiro acesso com senha provisória do admin).
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	c, ok := ClaimsFrom(r.Context())
	if !ok {
		http.Error(w, "não autenticado", http.StatusUnauthorized)
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	u, err := h.store.UserByID(r.Context(), c.Sub)
	if err != nil {
		http.Error(w, "não encontrado", http.StatusNotFound)
		return
	}
	if err := senha.Validar(req.NewPassword, u.Username); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Se não é primeiro acesso, valida a senha atual (evita troca por sessão sequestrada).
	if !u.MustResetPassword {
		if u.PasswordHash == "" || !VerifyPassword(req.CurrentPassword, u.PasswordHash) {
			http.Error(w, "senha atual incorreta", http.StatusUnauthorized)
			return
		}
	}
	hash, err := HashPassword(req.NewPassword)
	if err != nil {
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	if err := h.store.SetPassword(r.Context(), u.ID, hash); err != nil {
		http.Error(w, "erro ao trocar senha", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) issueSession(w http.ResponseWriter, ctx context.Context, userID int64, username, role string, mfa bool) error {
	return h.emitirSessao(w, ctx, userID, username, role, mfa, time.Now())
}

// emitirSessao emite access + refresh; `inicio` é o login original (limite absoluto).
func (h *Handler) emitirSessao(w http.ResponseWriter, ctx context.Context, userID int64, username, role string, mfa bool, inicio time.Time) error {
	access, err := SignClaims(h.secret, Claims{Sub: userID, Name: username, Role: NormalizarPapel(role), MFA: mfa}, accessTTL)
	if err != nil {
		return err
	}
	refresh, err := RandomToken()
	if err != nil {
		return err
	}
	if err := h.store.CreateSession(ctx, userID, HashToken(refresh), time.Now().Add(refreshTTL), mfa, inicio); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: refresh, Path: "/api/auth", HttpOnly: true,
		Secure: config.SecureCookies(), SameSite: http.SameSiteStrictMode, Expires: time.Now().Add(refreshTTL),
	})
	w.Header().Set("X-Access-Token", access)
	return nil
}

func (h *Handler) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/api/auth", HttpOnly: true, Secure: config.SecureCookies(), MaxAge: -1})
}

// claimsFromRequest tenta ler as claims do Bearer token (para rotas públicas que
// precisam checar o papel opcionalmente, como o Register pós-bootstrap).
func (h *Handler) claimsFromRequest(r *http.Request) (Claims, bool) {
	authz := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(authz) <= len(p) || authz[:len(p)] != p {
		return Claims{}, false
	}
	c, err := ParseAccessToken(h.secret, authz[len(p):])
	if err != nil {
		return Claims{}, false
	}
	return c, true
}

// --- middleware RBAC ---

// RequireAuth valida o Bearer access token e injeta as claims no contexto.
func (h *Handler) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authz := r.Header.Get("Authorization")
		const p = "Bearer "
		if len(authz) <= len(p) || authz[:len(p)] != p {
			http.Error(w, "sem token", http.StatusUnauthorized)
			return
		}
		claims, err := ParseAccessToken(h.secret, authz[len(p):])
		if err != nil {
			http.Error(w, "token inválido", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), claimsKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// OptionalAuth injeta as claims no contexto QUANDO há um Bearer válido, mas nunca
// barra a requisição. Serve para rotas públicas que um admin também usa (ex.: o
// cadastro de usuário): sem isto a trilha de auditoria registraria a ação de um
// admin autenticado como se fosse anônima.
func (h *Handler) OptionalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if claims, ok := h.claimsFromRequest(r); ok {
			r = r.WithContext(context.WithValue(r.Context(), claimsKey, claims))
		}
		next.ServeHTTP(w, r)
	})
}

// RequireRole garante um papel mínimo (leitor < operador < admin). Papéis legados
// ('user', 'editor', 'viewer') e desconhecidos são normalizados — nunca concedem admin
// por engano.
func RequireRole(min string, next http.Handler) http.Handler {
	rank := map[string]int{PapelLeitor: 1, PapelOperador: 2, PapelAdmin: 3}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := ClaimsFrom(r.Context())
		if !ok {
			http.Error(w, "sem permissão", http.StatusForbidden)
			return
		}
		// Papéis legados e desconhecidos passam por NormalizarPapel (viram leitor).
		if rank[NormalizarPapel(c.Role)] < rank[NormalizarPapel(min)] {
			http.Error(w, "sem permissão", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ClaimsFrom recupera as claims do contexto.
func ClaimsFrom(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(claimsKey).(Claims)
	return c, ok
}

// Estado diz à tela de login se o sistema ainda não tem nenhum usuário — aí ela
// oferece criar o administrador (bootstrap) em vez de pedir uma senha que não existe.
// Não revela nada além disso: nem quantos usuários há, nem quais.
func (h *Handler) Estado(w http.ResponseWriter, r *http.Request) {
	n, err := h.store.CountUsers(r.Context())
	if err != nil {
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"precisa_configurar": n == 0})
}

// SairDeTudo revoga todas as sessões do usuário (todos os aparelhos).
func (h *Handler) SairDeTudo(w http.ResponseWriter, r *http.Request) {
	c, ok := ClaimsFrom(r.Context())
	if !ok {
		http.Error(w, "não autenticado", http.StatusUnauthorized)
		return
	}
	if err := h.store.RevokeUserSessions(r.Context(), c.Sub); err != nil {
		http.Error(w, "erro ao encerrar as sessões", http.StatusInternalServerError)
		return
	}
	h.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/core/seguranca/cofre"
	"github.com/eduardorarruda/revoada/core/seguranca/totp"
	"github.com/eduardorarruda/revoada/server/internal/store"
	"rsc.io/qr"
)

const (
	emissorTOTP        = "Revoada"
	validadeDesafio    = 5 * time.Minute // entre a senha certa e o código do app
	validadeReauth     = 5 * time.Minute // janela das ações críticas depois de confirmar identidade
	qtdCodigosRecuper  = 10
	papelDesafioMFA    = "mfa-desafio"
	sufixoChaveDesafio = "|revoada/mfa-desafio"
)

// UsarCofre liga o cofre de segredos ao handler (o segredo do 2FA é guardado cifrado).
func (h *Handler) UsarCofre(c *cofre.Cofre) { h.cofre = c }

// --- desafio do login em duas etapas ---

// O desafio prova "a senha deste usuário acabou de ser conferida". É assinado com
// uma chave DERIVADA (outra que a dos tokens de acesso) para nunca servir de token
// de acesso, mesmo se alguém o mandar no Authorization.
func (h *Handler) emitirDesafio(userID int64, username string) (string, error) {
	return SignClaims(h.secret+sufixoChaveDesafio, Claims{Sub: userID, Name: username, Role: papelDesafioMFA}, validadeDesafio)
}

func (h *Handler) lerDesafio(tok string) (Claims, error) {
	c, err := ParseAccessToken(h.secret+sufixoChaveDesafio, tok)
	if err != nil || c.Role != papelDesafioMFA {
		return Claims{}, ErrInvalidToken
	}
	return c, nil
}

// VerificarMFA é a 2ª etapa do login: troca o desafio + código por uma sessão.
// Aceita o código de 6 dígitos do app ou um código de recuperação.
func (h *Handler) VerificarMFA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Desafio string `json:"desafio"`
		Codigo  string `json:"codigo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	c, err := h.lerDesafio(req.Desafio)
	if err != nil {
		http.Error(w, "a etapa de verificação expirou; entre de novo com a senha", http.StatusUnauthorized)
		return
	}
	u, err := h.store.UserByID(r.Context(), c.Sub)
	if err != nil || u.Disabled {
		http.Error(w, "credenciais inválidas", http.StatusUnauthorized)
		return
	}
	if msg := mensagemBloqueio(u, time.Now()); msg != "" {
		http.Error(w, msg, http.StatusTooManyRequests)
		return
	}
	ok, err := h.conferirSegundoFator(r.Context(), u.ID, req.Codigo, true)
	if err != nil {
		http.Error(w, "erro ao verificar o código", http.StatusInternalServerError)
		return
	}
	if !ok {
		h.contarFalha(r.Context(), u.ID)
		http.Error(w, "código inválido", http.StatusUnauthorized)
		return
	}
	_ = h.store.ZerarFalhasLogin(r.Context(), u.ID)
	if err := h.issueSession(w, r.Context(), u.ID, u.Username, u.Role, true); err != nil {
		http.Error(w, "erro emitindo sessão", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"role": NormalizarPapel(u.Role), "must_reset_password": u.MustResetPassword})
}

// conferirSegundoFator valida um código do app (6 dígitos) ou, se `aceitaRecuperacao`,
// um código de recuperação (que é queimado ao ser usado).
func (h *Handler) conferirSegundoFator(ctx context.Context, userID int64, codigo string, aceitaRecuperacao bool) (bool, error) {
	codigo = strings.TrimSpace(codigo)
	est, err := h.store.MFA(ctx, userID)
	if err != nil {
		return false, err
	}
	if !est.Ativo {
		return false, nil
	}
	if soDigitos(codigo) {
		segredo, err := h.abrirSegredoMFA(ctx, userID, est.SegredoCifrado)
		if err != nil {
			return false, err
		}
		passo, ok := totp.Verificar(segredo, codigo, time.Now(), est.UltimoPasso)
		if !ok {
			return false, nil
		}
		return h.store.AvancarPassoMFA(ctx, userID, passo)
	}
	if !aceitaRecuperacao {
		return false, nil
	}
	return h.store.ConsumirCodigoRecuperacao(ctx, userID, hashRecuperacao(codigo))
}

// --- ativação e desativação ---

// IniciarMFA gera um segredo novo (ainda inativo) e devolve o QR code para o app.
func (h *Handler) IniciarMFA(w http.ResponseWriter, r *http.Request) {
	c, ok := ClaimsFrom(r.Context())
	if !ok {
		http.Error(w, "não autenticado", http.StatusUnauthorized)
		return
	}
	if h.cofre == nil {
		http.Error(w, "cofre de segredos indisponível", http.StatusServiceUnavailable)
		return
	}
	segredo, err := totp.NovoSegredo()
	if err != nil {
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	cif, err := h.cofre.Cifrar(r.Context(), []byte(segredo), contextoMFA(c.Sub))
	if err != nil {
		http.Error(w, "erro ao proteger o segredo", http.StatusInternalServerError)
		return
	}
	b, _ := json.Marshal(cif)
	if err := h.store.GuardarSegredoMFAPendente(r.Context(), c.Sub, b); err != nil {
		if errors.Is(err, store.ErrConflito) {
			http.Error(w, "o 2FA já está ativo; desative antes de trocar de aparelho", http.StatusConflict)
			return
		}
		http.Error(w, "erro ao guardar o segredo", http.StatusInternalServerError)
		return
	}
	uri := totp.URI(emissorTOTP, c.Name, segredo)
	png, err := qrPNG(uri)
	if err != nil {
		http.Error(w, "erro ao gerar o QR code", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"segredo": segredo, // para digitar à mão se a câmera não ler o QR
		"uri":     uri,
		"qr":      "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
	})
}

// ConfirmarMFA ativa o 2FA depois que o usuário digita um código certo do app, e
// devolve os códigos de recuperação (mostrados UMA vez).
func (h *Handler) ConfirmarMFA(w http.ResponseWriter, r *http.Request) {
	c, ok := ClaimsFrom(r.Context())
	if !ok {
		http.Error(w, "não autenticado", http.StatusUnauthorized)
		return
	}
	var req struct {
		Codigo string `json:"codigo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	est, err := h.store.MFA(r.Context(), c.Sub)
	if err != nil {
		http.Error(w, "erro ao ler o 2FA", http.StatusInternalServerError)
		return
	}
	if est.Ativo {
		http.Error(w, "o 2FA já está ativo", http.StatusConflict)
		return
	}
	if len(est.SegredoCifrado) == 0 {
		http.Error(w, "comece pela leitura do QR code", http.StatusConflict)
		return
	}
	segredo, err := h.abrirSegredoMFA(r.Context(), c.Sub, est.SegredoCifrado)
	if err != nil {
		http.Error(w, "erro ao ler o segredo", http.StatusInternalServerError)
		return
	}
	passo, ok := totp.Verificar(segredo, req.Codigo, time.Now(), 0)
	if !ok {
		http.Error(w, "código inválido; confira o horário do celular e tente o código atual", http.StatusUnauthorized)
		return
	}
	codigos, hashes, err := novosCodigosRecuperacao()
	if err != nil {
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	if err := h.store.AtivarMFA(r.Context(), c.Sub, passo, hashes); err != nil {
		http.Error(w, "não foi possível ativar o 2FA", http.StatusConflict)
		return
	}
	// A sessão atual passa a contar como "com 2FA": troca o refresh por um novo marcado.
	h.revogarRefreshAtual(r)
	if err := h.issueSession(w, r.Context(), c.Sub, c.Name, c.Role, true); err != nil {
		http.Error(w, "2FA ativado, mas houve erro ao renovar a sessão; entre de novo", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"codigos_recuperacao": codigos})
}

// DesativarMFA desliga o 2FA. Exige reautenticação recente e não vale para quem é
// obrigado a usar 2FA (administrador e operador).
func (h *Handler) DesativarMFA(w http.ResponseWriter, r *http.Request) {
	c, ok := ClaimsFrom(r.Context())
	if !ok {
		http.Error(w, "não autenticado", http.StatusUnauthorized)
		return
	}
	if MFAObrigatorio(c.Role) {
		negar(w, CodigoSemPermissao, "o 2FA é obrigatório para o seu papel ("+NomePapel(c.Role)+"); um administrador pode redefini-lo se você perdeu o aparelho")
		return
	}
	if c.Reauth < time.Now().Unix() {
		negar(w, CodigoReautenticacao, "confirme sua identidade para desativar o 2FA")
		return
	}
	if err := h.store.DesativarMFA(r.Context(), c.Sub); err != nil {
		http.Error(w, "erro ao desativar", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Reautenticar confirma a identidade para liberar ações críticas por alguns minutos.
// Com 2FA ativo, pede o código do app; sem 2FA, pede a senha.
func (h *Handler) Reautenticar(w http.ResponseWriter, r *http.Request) {
	c, ok := ClaimsFrom(r.Context())
	if !ok {
		http.Error(w, "não autenticado", http.StatusUnauthorized)
		return
	}
	var req struct {
		Senha  string `json:"senha"`
		Codigo string `json:"codigo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	u, err := h.store.UserByID(r.Context(), c.Sub)
	if err != nil || u.Disabled {
		http.Error(w, "não autenticado", http.StatusUnauthorized)
		return
	}
	if msg := mensagemBloqueio(u, time.Now()); msg != "" {
		http.Error(w, msg, http.StatusTooManyRequests)
		return
	}
	var confere bool
	if u.MFAAtivo {
		confere, err = h.conferirSegundoFator(r.Context(), u.ID, req.Codigo, false)
		if err != nil {
			http.Error(w, "erro ao verificar o código", http.StatusInternalServerError)
			return
		}
	} else {
		confere = u.PasswordHash != "" && VerifyPassword(req.Senha, u.PasswordHash)
	}
	if !confere {
		h.contarFalha(r.Context(), u.ID)
		http.Error(w, "não confere", http.StatusUnauthorized)
		return
	}
	_ = h.store.ZerarFalhasLogin(r.Context(), u.ID)
	tok, err := SignClaims(h.secret, Claims{
		Sub: u.ID, Name: u.Username, Role: NormalizarPapel(u.Role),
		MFA: c.MFA, Reauth: time.Now().Add(validadeReauth).Unix(),
	}, accessTTL)
	if err != nil {
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	w.Header().Set("X-Access-Token", tok)
	writeJSON(w, http.StatusOK, map[string]any{"valido_ate": time.Now().Add(validadeReauth).Unix()})
}

// --- utilitários ---

func (h *Handler) abrirSegredoMFA(ctx context.Context, userID int64, cifrado []byte) (string, error) {
	if h.cofre == nil {
		return "", errors.New("cofre de segredos indisponível")
	}
	var s cofre.Segredo
	if err := json.Unmarshal(cifrado, &s); err != nil {
		return "", fmt.Errorf("segredo de 2FA corrompido: %w", err)
	}
	b, err := h.cofre.Decifrar(ctx, s, contextoMFA(userID))
	if err != nil {
		return "", err
	}
	defer cofre.Limpar(b)
	return string(b), nil
}

func (h *Handler) contarFalha(ctx context.Context, userID int64) {
	_, _ = h.store.RegistrarFalhaLogin(ctx, userID)
}

func (h *Handler) revogarRefreshAtual(r *http.Request) {
	if ck, err := r.Cookie(cookieName); err == nil {
		_ = h.store.RevokeSession(r.Context(), HashToken(ck.Value))
	}
}

// mensagemBloqueio devolve o aviso se a conta está bloqueada por excesso de falhas.
func mensagemBloqueio(u store.User, agora time.Time) string {
	if u.BloqueadoAte == nil || !u.BloqueadoAte.After(agora) {
		return ""
	}
	min := int(u.BloqueadoAte.Sub(agora).Minutes()) + 1
	return "muitas tentativas erradas; por segurança, tente de novo em " + strconv.Itoa(min) + " min"
}

func contextoMFA(userID int64) []byte { return []byte("mfa:" + strconv.FormatInt(userID, 10)) }

func soDigitos(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// alfabetoRecuperacao evita caracteres que se confundem ao ler (0/O, 1/I/L).
const alfabetoRecuperacao = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// novosCodigosRecuperacao gera 10 códigos no formato XXXXX-XXXXX e seus hashes.
func novosCodigosRecuperacao() (codigos, hashes []string, err error) {
	for i := 0; i < qtdCodigosRecuper; i++ {
		b := make([]byte, 10)
		if _, err := rand.Read(b); err != nil {
			return nil, nil, err
		}
		var sb strings.Builder
		for j, x := range b {
			if j == 5 {
				sb.WriteByte('-')
			}
			sb.WriteByte(alfabetoRecuperacao[int(x)%len(alfabetoRecuperacao)])
		}
		codigos = append(codigos, sb.String())
		hashes = append(hashes, hashRecuperacao(sb.String()))
	}
	return codigos, hashes, nil
}

// hashRecuperacao normaliza (maiúsculas, sem traço/espaço) e faz sha256. O código tem
// ~50 bits aleatórios, então hash rápido basta (não é senha escolhida por gente).
func hashRecuperacao(codigo string) string {
	n := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(codigo)))
	sum := sha256.Sum256([]byte("revoada/recuperacao|" + n))
	return hex.EncodeToString(sum[:])
}

func qrPNG(texto string) ([]byte, error) {
	code, err := qr.Encode(texto, qr.M)
	if err != nil {
		return nil, err
	}
	code.Scale = 6
	return code.PNG(), nil
}

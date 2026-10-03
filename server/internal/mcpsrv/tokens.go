// Package mcpsrv é o servidor MCP do painel (ARQUITETURA §12): ferramentas para um
// agente de IA ler o estado dos servidores e ajudar no mapeamento de migração.
//
// Limites que o código garante (não são só documentação):
//   - token próprio, com escopo (leitura | mapeamento | simulacao), guardado só como hash;
//   - nenhuma ferramenta executa migração, reverte, aprova ou vê credencial;
//   - nada de linha de dado: só estrutura, contagens e estados (a chave de amostra
//     dos relatórios e de mensagens de erro é ocultada);
//   - cada chamada vira uma linha da trilha de auditoria com origem = mcp.
package mcpsrv

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/core/validacao"
	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/entrada"
	"github.com/eduardorarruda/revoada/server/internal/store"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
)

// Escopos de um token MCP.
const (
	EscopoLeitura    = "leitura"
	EscopoMapeamento = "mapeamento"
	EscopoSimulacao  = "simulacao"
)

var escopos = []string{EscopoLeitura, EscopoMapeamento, EscopoSimulacao}

const prefixoToken = "rvm_"

// validadeMaxima de um token (dias).
const validadeMaxima = 365

func hashToken(t string) string {
	s := sha256.Sum256([]byte(t))
	return hex.EncodeToString(s[:])
}

func novoToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefixoToken + base64.RawURLEncoding.EncodeToString(b), nil
}

// verificador confere o Bearer do /mcp contra os tokens ativos.
func (s *Servidor) verificador(ctx context.Context, token string, _ *http.Request) (*mcpauth.TokenInfo, error) {
	if !strings.HasPrefix(token, prefixoToken) {
		return nil, mcpauth.ErrInvalidToken
	}
	t, err := s.st.TokenMCPPorHash(ctx, hashToken(token))
	if err != nil {
		return nil, mcpauth.ErrInvalidToken
	}
	_ = s.st.MarcarUsoTokenMCP(ctx, t.ID)
	return &mcpauth.TokenInfo{Scopes: t.Escopos, Expiration: t.ExpiraEm, UserID: t.ID, Extra: map[string]any{"nome": t.Nome}}, nil
}

// ---------------------------------------------------------------- API de tokens (admin)

// CriarToken: POST /api/mcp/tokens {nome, escopos, validade_dias} — o token aparece
// UMA vez, na resposta.
func (s *Servidor) CriarToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Nome         string   `json:"nome"`
		Escopos      []string `json:"escopos"`
		ValidadeDias int      `json:"validade_dias"`
	}
	if !entrada.LerJSON(w, r, &req) {
		return
	}
	nome, err := validacao.Texto("nome", req.Nome, true, 80)
	if err != nil {
		entrada.ErroValidacao(w, err)
		return
	}
	if len(req.Escopos) == 0 {
		req.Escopos = []string{EscopoLeitura}
	}
	for _, e := range req.Escopos {
		if !slices.Contains(escopos, e) {
			entrada.ResponderJSON(w, http.StatusUnprocessableEntity, validacao.Erro{Campo: "escopos", Mensagem: "escopo desconhecido: " + e})
			return
		}
	}
	if !slices.Contains(req.Escopos, EscopoLeitura) {
		req.Escopos = append(req.Escopos, EscopoLeitura) // mapear e simular pressupõem ler
	}
	if req.ValidadeDias <= 0 {
		req.ValidadeDias = 90
	}
	if req.ValidadeDias > validadeMaxima {
		entrada.ResponderJSON(w, http.StatusUnprocessableEntity, validacao.Erro{Campo: "validade_dias", Mensagem: "no máximo 365 dias"})
		return
	}
	valor, err := novoToken()
	if err != nil {
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	autor := ""
	if c, ok := auth.ClaimsFrom(r.Context()); ok {
		autor = c.Name
	}
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	t := store.TokenMCP{ID: "mcp_" + hex.EncodeToString(id), Nome: nome, Hash: hashToken(valor), Escopos: req.Escopos,
		CriadoPor: autor, ExpiraEm: time.Now().Add(time.Duration(req.ValidadeDias) * 24 * time.Hour)}
	if err := s.st.CriarTokenMCP(r.Context(), t); err != nil {
		http.Error(w, "erro ao criar o token", http.StatusInternalServerError)
		return
	}
	entrada.ResponderJSON(w, http.StatusCreated, map[string]any{"token": valor, "id": t.ID, "nome": t.Nome,
		"escopos": t.Escopos, "expira_em": t.ExpiraEm})
}

// ListarTokens: GET /api/mcp/tokens (sem o valor, que não é guardado).
func (s *Servidor) ListarTokens(w http.ResponseWriter, r *http.Request) {
	ts, err := s.st.ListarTokensMCP(r.Context())
	if err != nil {
		http.Error(w, "erro ao listar", http.StatusInternalServerError)
		return
	}
	entrada.ResponderJSON(w, http.StatusOK, ts)
}

// RevogarToken: POST /api/mcp/tokens/{id}/revogar — vale na próxima chamada.
func (s *Servidor) RevogarToken(w http.ResponseWriter, r *http.Request) {
	if err := s.st.RevogarTokenMCP(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "não encontrado", http.StatusNotFound)
			return
		}
		http.Error(w, "erro ao revogar", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

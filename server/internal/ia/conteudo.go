package ia

import (
	"context"
	"fmt"
	"net/http"

	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/authz"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// Mensagem é um trecho gravado de prompt ou de resposta.
type Mensagem struct {
	SpanID   string `json:"span_id"`
	Lado     string `json:"lado"`
	Papel    string `json:"papel"`
	Ordem    int64  `json:"ordem"`
	Texto    string `json:"texto"`
	Truncado bool   `json:"truncado"`
	Redigido bool   `json:"redigido"`
}

// ConteudoHTTP atende GET /api/ia/execucoes/{trace_id}/conteudo. A rota exige a
// permissão PermVerConteudoIA; aqui a leitura é auditada (GET não passa pelo
// middleware da trilha, que só registra escrita) — ler prompt de cliente é tão
// sensível quanto alterar alguma coisa.
func (h *Handler) ConteudoHTTP(w http.ResponseWriter, r *http.Request) {
	tid := r.PathValue("trace_id")
	if !ehTraceID(tid) {
		http.Error(w, "trace_id inválido", http.StatusBadRequest)
		return
	}
	escopo, _ := authz.ScopeFrom(r.Context())
	ms, err := h.Conteudo(r.Context(), tid, escopo)
	if err != nil {
		erroInterno(w, err)
		return
	}
	h.auditarLeitura(r, tid, len(ms))
	if len(ms) == 0 {
		http.Error(w, "nenhum conteúdo gravado para esta execução", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mensagens": ms})
}

// Conteudo devolve as mensagens gravadas do trace, só dos spans que o usuário vê.
func (h *Handler) Conteudo(ctx context.Context, traceID string, escopo *authz.Scope) ([]Mensagem, error) {
	if !ehTraceID(traceID) { // a borda HTTP já valida; o MCP chega aqui direto
		return nil, nil
	}
	pred := eTambem(predicadoHost(escopo, "host"))
	rows, err := h.ch.QueryJSON(ctx, fmt.Sprintf(`SELECT span_id, lado, papel, ordem, texto, truncado, redigido
		FROM genai_conteudo WHERE tenant_id = 'default' AND trace_id = %[1]s
		AND span_id IN (SELECT span_id FROM genai_spans WHERE tenant_id = 'default' AND trace_id = %[1]s%[2]s)
		ORDER BY ts, span_id, lado = 'saida', ordem`, quote(traceID), pred))
	if err != nil {
		return nil, err
	}
	out := make([]Mensagem, 0, len(rows))
	for _, r := range rows {
		out = append(out, Mensagem{SpanID: texto(r["span_id"]), Lado: texto(r["lado"]), Papel: texto(r["papel"]),
			Ordem: inteiro(r["ordem"]), Texto: texto(r["texto"]),
			Truncado: inteiro(r["truncado"]) == 1, Redigido: inteiro(r["redigido"]) == 1})
	}
	return out, nil
}

func (h *Handler) auditarLeitura(r *http.Request, traceID string, n int) {
	if h.audit == nil {
		return
	}
	e := store.AuditEntry{Method: r.Method, Path: r.URL.Path, Resource: "ia_conteudo", Target: traceID,
		Status: http.StatusOK, Payload: map[string]any{"mensagens": n}, Origem: "ui"}
	if c, ok := auth.ClaimsFrom(r.Context()); ok {
		id := c.Sub
		e.ActorID, e.ActorName, e.ActorRole = &id, c.Name, c.Role
	}
	h.audit.Registrar(e)
}

package ia

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/eduardorarruda/revoada/server/internal/audit"
)

// FraseApagarConteudo é o que o admin digita para apagar TODO o conteúdo gravado.
const FraseApagarConteudo = "APAGAR TODO O CONTEÚDO DE IA"

type pedidoPurge struct {
	Alvo  string `json:"alvo"` // conversa | trace | todo_conteudo
	Valor string `json:"valor"`
	Frase string `json:"frase"`
}

// PurgeHTTP atende POST /api/ia/purge (admin). Apagar por conversa ou por trace tira
// as chamadas E o conteúdo (pedido de titular de dado, LGPD); todo_conteudo esvazia só
// genai_conteudo (o custo e os tokens seguem). O agregado por minuto não guarda texto,
// por isso não é tocado. As mutations do ClickHouse são assíncronas: a resposta diz o
// que foi pedido, e as linhas somem em segundos.
func (h *Handler) PurgeHTTP(w http.ResponseWriter, r *http.Request) {
	var p pedidoPurge
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&p); err != nil {
		http.Error(w, "corpo inválido", http.StatusBadRequest)
		return
	}
	var traces []string
	if p.Alvo == "conversa" {
		var err error
		if traces, err = h.tracesDaConversa(r, strings.TrimSpace(p.Valor)); err != nil {
			erroInterno(w, err)
			return
		}
	}
	stmts, msg := comandosPurge(p, traces)
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	for _, s := range stmts {
		if err := h.ch.Exec(r.Context(), s); err != nil {
			erroInterno(w, err)
			return
		}
	}
	audit.Annotate(r.Context(), "comandos", len(stmts))
	writeJSON(w, http.StatusAccepted, map[string]any{"alvo": p.Alvo, "comandos": len(stmts)})
}

// maxTracesPorConversa limita a lista literal do DELETE; conversa maior que isso é
// apagada em mais de um pedido.
const maxTracesPorConversa = 10_000

// tracesDaConversa resolve os traces ANTES de apagar: as duas mutations são assíncronas,
// e uma subconsulta em genai_spans dentro do DELETE do conteúdo podia rodar depois de
// genai_spans já ter sido esvaziada — e o conteúdo ficaria para trás.
func (h *Handler) tracesDaConversa(r *http.Request, conversa string) ([]string, error) {
	if conversa == "" || len(conversa) > 256 {
		return nil, nil
	}
	rows, err := h.ch.QueryJSON(r.Context(), fmt.Sprintf(`SELECT DISTINCT trace_id FROM genai_spans
		WHERE tenant_id = 'default' AND conversa_id = %s LIMIT %d`, quote(conversa), maxTracesPorConversa))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if t := texto(r["trace_id"]); ehTraceID(t) {
			out = append(out, t)
		}
	}
	return out, nil
}

// comandosPurge devolve as instruções do pedido, ou a mensagem de erro.
func comandosPurge(p pedidoPurge, traces []string) ([]string, string) {
	valor := strings.TrimSpace(p.Valor)
	switch p.Alvo {
	case "todo_conteudo":
		if p.Frase != FraseApagarConteudo {
			return nil, fmt.Sprintf("para apagar todo o conteúdo, digite exatamente: %s", FraseApagarConteudo)
		}
		return []string{"TRUNCATE TABLE genai_conteudo"}, ""
	case "trace":
		if !ehTraceID(valor) {
			return nil, "trace_id inválido"
		}
		w := "tenant_id = 'default' AND trace_id = " + quote(valor)
		return []string{"ALTER TABLE genai_conteudo DELETE WHERE " + w, "ALTER TABLE genai_spans DELETE WHERE " + w}, ""
	case "conversa":
		if valor == "" || len(valor) > 256 {
			return nil, "informe o id da conversa"
		}
		if len(traces) == 0 {
			return nil, "nenhuma execução encontrada para esta conversa"
		}
		lista := make([]string, len(traces))
		for i, t := range traces {
			lista[i] = quote(t)
		}
		w := "tenant_id = 'default' AND trace_id IN (" + strings.Join(lista, ",") + ")"
		return []string{"ALTER TABLE genai_conteudo DELETE WHERE " + w, "ALTER TABLE genai_spans DELETE WHERE " + w}, ""
	}
	return nil, "alvo deve ser conversa, trace ou todo_conteudo"
}

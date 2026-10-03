package logs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/eduardorarruda/revoada/server/internal/authz"
)

// Este arquivo cobre o MEDIDOR de armazenamento de logs no ClickHouse: uso atual
// (global e por host) vs. um teto configurável pelo admin. O expurgo em si — que
// apaga DE VERDADE, com mutation ALTER … DELETE — mora em purge.go.

// asInt64 extrai um inteiro de um valor JSON do ClickHouse. UInt64/Int64 vêm como
// string (para não perder precisão); UInt32 e menores vêm como número.
func asInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	case json.Number:
		n, _ := t.Int64()
		return n
	}
	return 0
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// Storage devolve o uso de armazenamento de logs (global) e o detalhamento do host
// informado (opcional), mais o limite configurado.
func (h *Handler) Storage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	host := r.URL.Query().Get("host")

	// Enforcement por usuário (plano §5: storage é [F]): o detalhe por host só sai para
	// quem pode ver aquele host, e os totais globais da tabela (volumetria de TODOS os
	// servidores) são exclusivos do admin — usuário comum recebe zeros (a UI só mostra o
	// medidor global e o expurgo para admin; para os demais exibe apenas o detalhe do host).
	scoped := false
	if scope, ok := authz.ScopeFrom(ctx); ok && !scope.Admin {
		scoped = true
		if strings.TrimSpace(host) != "" && !scope.CanView(host) {
			http.Error(w, "sem acesso a este servidor", http.StatusForbidden)
			return
		}
	}

	limit, err := h.st.GetLogsStorageLimit(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if scoped {
		limit = 0 // o teto configurado também é assunto do medidor global (admin)
	}

	var totalBytes, totalRows, oldest, newest int64
	if !scoped {
		// Tamanho e linhas totais da tabela: metadados das parts (barato e exato).
		partRows, err := h.ch.QueryJSON(ctx, `SELECT sum(bytes_on_disk) AS bytes, sum(rows) AS rows
			FROM system.parts WHERE database=currentDatabase() AND table='logs' AND active`)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(partRows) > 0 {
			totalBytes = asInt64(partRows[0]["bytes"])
			totalRows = asInt64(partRows[0]["rows"])
		}

		// Período coberto (mais antigo/mais novo) — min/max resolvidos por metadados.
		spanRows, err := h.ch.QueryJSON(ctx, `SELECT toUnixTimestamp(min(ts)) AS oldest,
			toUnixTimestamp(max(ts)) AS newest FROM logs WHERE tenant_id='default'`)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(spanRows) > 0 {
			oldest = asInt64(spanRows[0]["oldest"])
			newest = asInt64(spanRows[0]["newest"])
		}
	}

	resp := map[string]any{
		"limit_bytes": limit,
		"total_bytes": totalBytes,
		"total_rows":  totalRows,
		"oldest_ts":   oldest,
		"newest_ts":   newest,
	}
	if limit > 0 {
		resp["pct"] = float64(totalBytes) / float64(limit)
	} else {
		resp["pct"] = 0.0
	}

	// Detalhe do host (linhas + período).
	//
	// O custo NÃO é proporcional às linhas do host, como este comentário já afirmou:
	// `labels['host']` não está na chave de ordenação (tenant_id, service, ts) nem na
	// chave de partição, então o ClickHouse lê a coluna Map de TODAS as linhas para
	// decidir quais são do host. Medido em produção: 695 ms lendo 2,68 GiB sobre 30
	// milhões de linhas. Não dá para tornar barato sem mudar o esquema — o que dá é
	// não deixar a tela pendurada: o orçamento de tempo devolve erro do ClickHouse em
	// vez de segurar a requisição indefinidamente enquanto o operador olha um spinner.
	if strings.TrimSpace(host) != "" {
		hostRows, err := h.ch.QueryJSON(ctx, fmt.Sprintf(`SELECT count() AS rows,
			toUnixTimestamp(min(ts)) AS oldest, toUnixTimestamp(max(ts)) AS newest
			FROM logs WHERE tenant_id='default' AND labels['host']=%s
			SETTINGS max_execution_time=15`, quote(host)))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp["host"] = host
		if len(hostRows) > 0 {
			resp["host_rows"] = asInt64(hostRows[0]["rows"])
			resp["host_oldest_ts"] = asInt64(hostRows[0]["oldest"])
			resp["host_newest_ts"] = asInt64(hostRows[0]["newest"])
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// SetStorageLimit grava o teto de armazenamento de logs (bytes). 0 = sem limite.
func (h *Handler) SetStorageLimit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Bytes int64 `json:"bytes"`
	}
	if err := decode(r, &body); err != nil {
		http.Error(w, "corpo inválido", http.StatusBadRequest)
		return
	}
	if body.Bytes < 0 {
		http.Error(w, "o limite não pode ser negativo", http.StatusBadRequest)
		return
	}
	if err := h.st.SetLogsStorageLimit(r.Context(), body.Bytes); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bytes": body.Bytes})
}

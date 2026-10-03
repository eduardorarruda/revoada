package logs

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"net/http"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/authz"
	"github.com/eduardorarruda/revoada/server/internal/config"
)

// originPatterns restringe o Origin aceito no handshake ao host público do painel.
// Era `[]string{"*"}` — a checagem de Origin simplesmente desligada, deixando
// qualquer página da web abrir um live tail de LOGS (o conteúdo mais sensível que o
// painel serve). A biblioteca já libera sozinha a mesma origem e as conexões sem
// Origin; isto acrescenta a origem do front quando ela é outro host que não o da API.
func originPatterns() []string {
	u, err := url.Parse(config.PublicURL())
	if err != nil || u.Host == "" {
		return nil
	}
	return []string{u.Host}
}

// Tail serve /api/logs/tail: envia novas linhas a cada 1,5s (live tail).
// Auth por query-param `access` (o handshake WS não manda Authorization).
func (h *Handler) Tail(w http.ResponseWriter, r *http.Request) {
	claims, err := auth.ParseAccessToken(h.secret, r.URL.Query().Get("access"))
	if err != nil {
		http.Error(w, "não autenticado", http.StatusUnauthorized)
		return
	}
	ctx := r.Context()
	// Enforcement por usuário: resolve o escopo e injeta no contexto para que parseFilters
	// já pegue o predicado de host. Fail-closed: sem conseguir resolver, encerra. O live
	// tail deixa de exigir editor+ (o filtro por host substitui a restrição por papel):
	// o usuário acompanha em tempo real só os logs dos servidores que pode ver.
	if h.authz != nil {
		scope, err := h.authz.Resolve(ctx, claims.Sub, claims.Role)
		if err != nil {
			http.Error(w, "erro ao resolver permissões", http.StatusInternalServerError)
			return
		}
		ctx = authz.WithScope(ctx, scope)
		r = r.WithContext(ctx)
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: originPatterns()})
	if err != nil {
		return
	}
	defer c.CloseNow()

	f := parseFilters(r)
	// começa do "agora": só linhas novas.
	lastMs := time.Now().UnixMilli()

	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sql := fmt.Sprintf(`SELECT toUnixTimestamp64Milli(ts) AS t, service, severity, severity_num,
				body, labels, trace_id, span_id FROM logs
				WHERE %s AND toUnixTimestamp64Milli(ts) > %d ORDER BY ts ASC LIMIT 500%s`, f.whereTail(), lastMs, f.noLog())
			rows, err := h.ch.QueryJSON(ctx, sql)
			if err != nil {
				continue
			}
			if len(rows) == 0 {
				continue
			}
			for _, row := range rows {
				if t, ok := toInt64(row["t"]); ok && t > lastMs {
					lastMs = t
				}
			}
			wctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err = wsjson.Write(wctx, c, map[string]any{"type": "logs", "rows": rows})
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// whereTail é como where() mas com o corte superior de tempo aberto ao futuro
// (o live tail acompanha linhas novas, filtradas depois por ts > lastMs).
func (f filters) whereTail() string {
	f2 := f
	f2.to = time.Now().Add(100 * 365 * 24 * time.Hour)
	return f2.where()
}

func toInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return int64(x), true
	case int64:
		return x, true
	case string:
		var n int64
		_, err := fmt.Sscan(x, &n)
		return n, err == nil
	}
	return 0, false
}

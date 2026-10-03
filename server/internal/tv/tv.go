// Package tv implementa o modo TV/Kiosk: tokens somente-leitura por dashboard.
package tv

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/authz"
	"github.com/eduardorarruda/revoada/server/internal/chquery"
	"github.com/eduardorarruda/revoada/server/internal/dashboards"
	"github.com/eduardorarruda/revoada/server/internal/inventory"
	"github.com/eduardorarruda/revoada/server/internal/query"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// WallUID é o dashboard_uid sentinela que marca uma TV como Health Wall (em vez de um
// dashboard). O Resolve devolve {wall:true} e a TV consome /api/tv/wall (por token).
const WallUID = "__wall__"

type Handler struct {
	st *store.Store
	q  *query.Handler
	ch *chquery.Client
}

func NewHandler(st *store.Store, q *query.Handler, ch *chquery.Client) *Handler {
	return &Handler{st: st, q: q, ch: ch}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// --- Admin ---

type createReq struct {
	Name         string `json:"name"`
	Location     string `json:"location"`
	DashboardUID string `json:"dashboard_uid"`
}

// Create gera um token de TV (o valor em claro só aparece aqui, uma vez).
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.DashboardUID == "" {
		http.Error(w, "name e dashboard_uid obrigatórios", http.StatusBadRequest)
		return
	}
	token, err := auth.RandomToken()
	if err != nil {
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	id, err := h.st.CreateTVToken(r.Context(), auth.HashToken(token), token, req.Name, req.Location, req.DashboardUID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "token": token, "url": "/tv/" + token})
}

// Regenerate rotaciona o token de uma TV existente e devolve o novo link. Serve
// para recuperar o link de tokens legados (criados antes de o valor em claro ser
// guardado): o link antigo deixa de valer e um novo passa a valer imediatamente.
func (h *Handler) Regenerate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == 0 {
		http.Error(w, "id obrigatório", http.StatusBadRequest)
		return
	}
	token, err := auth.RandomToken()
	if err != nil {
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	if err := h.st.RegenerateTVToken(r.Context(), body.ID, auth.HashToken(token), token); err != nil {
		http.Error(w, "não encontrado", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": body.ID, "token": token, "url": "/tv/" + token})
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	tokens, err := h.st.ListTVTokens(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if tokens == nil {
		tokens = []store.TVToken{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
}

func (h *Handler) Revoke(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == 0 {
		http.Error(w, "id obrigatório", http.StatusBadRequest)
		return
	}
	if err := h.st.RevokeTVToken(r.Context(), body.ID); err != nil {
		http.Error(w, "não encontrado", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteToken apaga o token de vez (hard-delete), removendo a TV da lista.
func (h *Handler) DeleteToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	if err := h.st.DeleteTVToken(r.Context(), id); err != nil {
		http.Error(w, "não encontrado", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// --- Público (só por token) ---

func (h *Handler) token(r *http.Request) (store.TVToken, error) {
	tok := r.URL.Query().Get("token")
	if tok == "" {
		return store.TVToken{}, errors.New("sem token")
	}
	return h.st.ResolveTVToken(r.Context(), auth.HashToken(tok), r.URL.Query().Get("v"))
}

// --- Escopo do token de TV -------------------------------------------------
//
// O token de TV é vendido como "somente-leitura POR DASHBOARD", mas até aqui ele
// não restringia NADA além do desenho da tela: as quatro rotas públicas
// (resolve/wall/status/query) só conferiam que o token existia. Medido em dev com um
// token emitido para o dashboard de UM host (`host-dev-agent-host`):
//
//	GET  /api/tv/wall   → 64 cartões, a frota inteira;
//	GET  /api/tv/status → 64 alertas ativos, de servidores que nada têm a ver com a TV;
//	POST /api/tv/query  → séries de 6 hosts (`notebook-dev`, `qa-*`, …), nenhum deles o do token.
//
// Esse token viaja na URL de um telão de kiosk: barra de endereço, histórico do
// navegador, log de proxy. Quem o pegar do vidro da recepção não deve levar junto a
// telemetria e os alertas da frota.
//
// A correção deriva o escopo do ALVO do token (o dashboard, ou os dashboards da
// playlist) e o injeta como authz.Scope — o mesmo mecanismo do usuário comum, de modo
// que os construtores de SQL do ClickHouse já filtram sem mudança nenhuma.
//
// Derivar (em vez de gravar o escopo no token na hora da emissão) foi deliberado:
//   - os tokens JÁ EMITIDOS passam a ser restritos retroativamente, sem regeneração,
//     sem migração e sem quebrar o link colado na TV — regenerar em massa significaria
//     visitar cada telão fisicamente;
//   - editar o dashboard (trocar o host do painel) reescala o token junto, em vez de
//     deixar um escopo congelado divergindo do que a tela mostra.
//
// Um token cujo alvo NÃO é preso a hosts (dashboard global, o genérico com $host, ou o
// Health Wall) continua sem restrição: é literalmente o que o operador pediu ao apontar
// a TV para lá. Esses são os únicos que ainda dão a frota inteira — e para eles a
// recomendação continua sendo rotacionar (POST /api/tv/tokens/regenerate) se o link
// tiver circulado.

// escopoDoToken devolve o escopo do token, ou nil quando o alvo não é preso a hosts
// (sem restrição, comportamento histórico).
func (h *Handler) escopoDoToken(ctx context.Context, t store.TVToken) *authz.Scope {
	hosts, restrito := h.hostsDoAlvo(ctx, t)
	if !restrito {
		return nil
	}
	perms := make([]store.ServerPerm, 0, len(hosts))
	for _, host := range hosts {
		perms = append(perms, store.ServerPerm{Hostname: host, CanView: true})
	}
	// UserID 0: não é um usuário, é uma TV. O Scope só é usado para filtrar leitura.
	return authz.NewScope(0, perms)
}

// hostsDoAlvo resolve os hosts do alvo do token. restrito=false significa "esta TV
// não é de servidor nenhum em particular" (dashboard global / Wall) — aí não há o que
// restringir sem quebrar a tela.
func (h *Handler) hostsDoAlvo(ctx context.Context, t store.TVToken) (hosts []string, restrito bool) {
	if t.PlaylistID != nil {
		pl, err := h.st.GetPlaylist(ctx, *t.PlaylistID)
		if err != nil {
			// Playlist ilegível: fail-closed. Uma TV sem alvo conhecido não recebe frota.
			return nil, true
		}
		var items []playlistItem
		_ = json.Unmarshal(pl.Items, &items)
		set := map[string]bool{}
		for _, it := range items {
			hs, r := h.hostsDoDashboard(ctx, it.DashboardUID)
			if !r {
				// Uma tela global na playlist já obriga a TV a ver além de um host.
				return nil, false
			}
			for _, x := range hs {
				set[x] = true
			}
		}
		return ordenado(set), true
	}
	if t.DashboardUID == WallUID {
		// Health Wall é, por definição, a visão da frota. Restringi-lo esvaziaria a
		// tela.
		return nil, false
	}
	return h.hostsDoDashboard(ctx, t.DashboardUID)
}

// hostsDoDashboard extrai os hosts a que um dashboard está preso: pelo UID
// ("host-<h>" / "svc-<serviço>-<h>") e pelos filtros dos painéis.
//
// Regra do "restrito": TODO painel que consulta precisa ter um filtro de host
// concreto. Basta um painel sem filtro (ou com o placeholder $host do dashboard
// genérico) para a tela ser de frota — e aí restringir apagaria gráficos legítimos.
func (h *Handler) hostsDoDashboard(ctx context.Context, uid string) (hosts []string, restrito bool) {
	set := map[string]bool{}
	if host := hostDoUID(uid); host != "" {
		set[host] = true
	}
	d, err := h.st.GetDashboard(ctx, uid)
	if err != nil {
		// Dashboard sumiu: se o UID já nomeava um host, vale ele; senão, fail-closed
		// (nenhum host ⇒ predicado 1=0 ⇒ a TV não recebe dado de ninguém).
		return ordenado(set), true
	}
	var m dashboards.Model
	if err := json.Unmarshal(d.Model, &m); err != nil {
		return ordenado(set), true
	}
	return hostsDoModelo(m, set)
}

// hostsDoModelo é a parte pura da derivação (sem banco), para ser testável.
func hostsDoModelo(m dashboards.Model, set map[string]bool) (hosts []string, restrito bool) {
	if set == nil {
		set = map[string]bool{}
	}
	for _, v := range m.Variables {
		if v.Name == "host" && v.Current != "" && v.Current != dashboards.HostVarPlaceholder {
			set[v.Current] = true
		}
	}
	for _, p := range m.Panels {
		if p.Query.Metric == "" {
			continue // painel decorativo (texto), não consulta nada
		}
		host := p.Query.Filters["host"]
		if host == "" || host == dashboards.HostVarPlaceholder {
			return nil, false
		}
		set[host] = true
	}
	return ordenado(set), true
}

// hostDoUID lê o hostname embutido na convenção de UID dos dashboards gerados.
func hostDoUID(uid string) string {
	if uid == dashboards.GenericHostUID {
		return ""
	}
	return dashboards.HostOfUID(uid)
}

func ordenado(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type playlistItem struct {
	DashboardUID    string `json:"dashboard_uid"`
	DurationSeconds int    `json:"duration_seconds"`
}

// Resolve devolve o alvo do token (dashboard único ou playlist) + o sinal de reload.
func (h *Handler) Resolve(w http.ResponseWriter, r *http.Request) {
	t, err := h.token(r)
	if err != nil {
		http.Error(w, "token inválido ou revogado", http.StatusForbidden)
		return
	}
	resp := map[string]any{"name": t.Name, "reload_at": t.ReloadAt}

	if t.PlaylistID != nil {
		pl, err := h.st.GetPlaylist(r.Context(), *t.PlaylistID)
		if err != nil {
			http.Error(w, "playlist não encontrada", http.StatusNotFound)
			return
		}
		var items []playlistItem
		_ = json.Unmarshal(pl.Items, &items)
		screens := []map[string]any{}
		for _, it := range items {
			d, err := h.st.GetDashboard(r.Context(), it.DashboardUID)
			if err != nil {
				continue
			}
			dur := it.DurationSeconds
			if dur <= 0 {
				dur = 15
			}
			screens = append(screens, map[string]any{"dashboard": d, "duration_seconds": dur})
		}
		resp["playlist"] = map[string]any{"name": pl.Name, "screens": screens}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	if t.DashboardUID == WallUID {
		resp["wall"] = true
		writeJSON(w, http.StatusOK, resp)
		return
	}
	dash, err := h.st.GetDashboard(r.Context(), t.DashboardUID)
	if err != nil {
		http.Error(w, "dashboard não encontrado", http.StatusNotFound)
		return
	}
	resp["dashboard"] = dash
	writeJSON(w, http.StatusOK, resp)
}

// Wall alimenta a TV do tipo Health Wall (público, por token): cartões de saúde por
// host, os mesmos do /api/health-wall, mas sem login (auth só pelo token da TV).
func (h *Handler) Wall(w http.ResponseWriter, r *http.Request) {
	t, err := h.token(r)
	if err != nil {
		http.Error(w, "token inválido ou revogado", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	cards, err := inventory.BuildWallCards(ctx, h.st, h.ch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Cartão é de UM host: com token restrito, só entram os hosts do alvo da TV.
	if scope := h.escopoDoToken(ctx, t); scope != nil {
		filtrados := make([]inventory.HealthCard, 0, len(cards))
		for _, c := range cards {
			if scope.CanView(c.Host) {
				filtrados = append(filtrados, c)
			}
		}
		cards = filtrados
	}
	writeJSON(w, http.StatusOK, map[string]any{"cards": cards})
}

// Status alimenta o alert takeover e o ticker da TV (público, por token):
// alertas críticos ativos (para o takeover), warnings ativos e deploys recentes
// (para o ticker de rodapé).
// alertTarget resolve QUEM disparou o alerta a partir dos labels, na ordem
// host → instance → site → url. Assim a TV sempre consegue nomear o servidor/alvo,
// mesmo em regras que não usam o label `host` (ex.: checagem de site/URL). Vazio
// só quando nenhum desses labels existe.
func alertTarget(labels map[string]string) string {
	for _, k := range []string{"host", "instance", "site", "url"} {
		if v := labels[k]; v != "" {
			return v
		}
	}
	return ""
}

func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	t, err := h.token(r)
	if err != nil {
		http.Error(w, "token inválido ou revogado", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	// Alerta ativo nomeia servidor, regra e diagnóstico. Com token restrito, só passam
	// os do alvo da TV; alerta sem alvo identificável (sem host/instance/site/url) é
	// escondido — a mesma regra do escopo de usuário, onde linha sem host só admin vê.
	scope := h.escopoDoToken(ctx, t)
	visivel := func(labels map[string]string) bool {
		if scope == nil {
			return true
		}
		alvo := alertTarget(labels)
		return alvo != "" && scope.CanView(alvo)
	}

	// plantonista atual (primeira rotação), exibido no takeover.
	oncall := ""
	if rots, err := h.st.ListOncallRotations(ctx); err == nil && len(rots) > 0 {
		oncall, _ = h.st.CurrentOncall(ctx, rots[0].ID, time.Now())
	}

	// A métrica de cada alerta (a.Metric, do JOIN com a regra) vai junto: sem ela a TV
	// mostrava o valor cru ("valor atual 761,7") sem unidade, ilegível a 5 m.
	type takeover struct {
		Rule      string            `json:"rule"`
		Metric    string            `json:"metric,omitempty"`
		Severity  string            `json:"severity"`
		Since     string            `json:"since"`
		Value     float64           `json:"value"`
		Labels    map[string]string `json:"labels"`
		Diagnosis string            `json:"diagnosis"`
		Oncall    string            `json:"oncall"`
	}
	// alert é um alerta ativo (qualquer severidade) para a notificação por evento
	// na TV. `id` = fingerprint estável, para a TV detectar quando é NOVO.
	type alert struct {
		ID       string  `json:"id"`
		Rule     string  `json:"rule"`
		Host     string  `json:"host"`
		Metric   string  `json:"metric,omitempty"`
		Severity string  `json:"severity"`
		Since    string  `json:"since"`
		Value    float64 `json:"value"`
	}
	criticals := []takeover{}
	warnings := []takeover{}
	alertsOut := []alert{}
	if alerts, err := h.st.ListAlerts(ctx, true); err == nil {
		for _, a := range alerts {
			if !visivel(a.Labels) {
				continue
			}
			since := a.StartedAt.Format(time.RFC3339)
			// Todos os alertas ativos vão para `alerts` (não descartamos mais `info`).
			alertsOut = append(alertsOut, alert{
				ID: a.Fingerprint, Rule: a.RuleName, Metric: a.Metric, Host: alertTarget(a.Labels),
				Severity: a.Severity, Since: since, Value: a.Value,
			})
			t := takeover{
				Rule: a.RuleName, Metric: a.Metric, Severity: a.Severity, Since: since,
				Value: a.Value, Labels: a.Labels, Diagnosis: a.Labels["diagnóstico"], Oncall: oncall,
			}
			switch a.Severity {
			case "critical":
				criticals = append(criticals, t)
			case "warning":
				warnings = append(warnings, t)
			}
		}
	}

	deploys := []map[string]any{}
	rows, err := h.ch.QueryJSON(ctx,
		"SELECT toString(ts) AS ts, title FROM events WHERE kind='deploy' ORDER BY ts DESC LIMIT 10")
	if err == nil {
		for _, row := range rows {
			deploys = append(deploys, map[string]any{"ts": row["ts"], "title": row["title"]})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"criticals": criticals, "warnings": warnings, "deploys": deploys,
		"alerts": alertsOut,
	})
}

// --- Playlists (admin) ---

func (h *Handler) CreatePlaylist(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string          `json:"name"`
		Items json.RawMessage `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		http.Error(w, "name obrigatório", http.StatusBadRequest)
		return
	}
	if len(body.Items) == 0 {
		body.Items = json.RawMessage("[]")
	}
	id, err := h.st.CreatePlaylist(r.Context(), body.Name, body.Items)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (h *Handler) ListPlaylists(w http.ResponseWriter, r *http.Request) {
	pls, err := h.st.ListPlaylists(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if pls == nil {
		pls = []store.Playlist{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"playlists": pls})
}

// DeletePlaylist apaga a playlist; as TVs que a usavam voltam ao dashboard fixo
// (o desvínculo é feito no store, dentro da mesma transação do delete).
func (h *Handler) DeletePlaylist(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	if err := h.st.DeletePlaylist(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// --- Ações remotas sobre TVs (admin) ---

func (h *Handler) Reload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == 0 {
		http.Error(w, "id obrigatório", http.StatusBadRequest)
		return
	}
	if err := h.st.RequestReload(r.Context(), body.ID); err != nil {
		http.Error(w, "não encontrado", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) SetPlaylist(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID         int64 `json:"id"`
		PlaylistID int64 `json:"playlist_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == 0 {
		http.Error(w, "id obrigatório", http.StatusBadRequest)
		return
	}
	if err := h.st.SetTokenPlaylist(r.Context(), body.ID, body.PlaylistID); err != nil {
		http.Error(w, "não encontrado", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Query executa uma consulta autenticada apenas pelo token de TV (read-only).
//
// O corpo é do cliente, então a métrica e os filtros pedidos são o que a TV quiser —
// o que impede a TV de ler outro servidor NÃO é o corpo, é o escopo injetado no
// contexto: o QuerySeries monta o WHERE do ClickHouse a partir dele.
func (h *Handler) Query(w http.ResponseWriter, r *http.Request) {
	t, err := h.token(r)
	if err != nil {
		http.Error(w, "token inválido ou revogado", http.StatusForbidden)
		return
	}
	var req query.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if scope := h.escopoDoToken(ctx, t); scope != nil {
		ctx = authz.WithScope(ctx, scope)
	}
	resp, err := h.q.QuerySeries(ctx, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

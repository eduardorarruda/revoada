package sitecheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

// Handler expõe a API de checks de site.
type Handler struct{ st *store.Store }

func NewHandler(st *store.Store) *Handler { return &Handler{st: st} }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// uptimeView é o uptime como a API passou a publicá-lo: o percentual só existe
// (não-nulo) quando a COBERTURA da medição autoriza afirmá-lo.
//
// Antes o JSON era `uptime_day: 100` mesmo com 3,4% do período medido, e o front
// não tinha como saber a diferença entre "ficou no ar" e "não olhamos". `null`
// obriga a tela a dizer "sem dados suficientes".
type uptimeView struct {
	Percent    *float64 `json:"percent"`     // null = sem dados suficientes
	Coverage   float64  `json:"coverage"`    // % do período efetivamente sondado
	Samples    int      `json:"samples"`     // sondagens observadas
	Expected   int      `json:"expected"`    // sondagens esperadas pelo tier
	WindowFull bool     `json:"window_full"` // o histórico cobre a janela inteira
}

func newUptimeView(st store.UptimeStat) uptimeView {
	v := uptimeView{
		Coverage: round1(st.Coverage), Samples: st.Samples,
		Expected: st.Expected, WindowFull: st.WindowFull,
	}
	if st.Sufficient {
		// PISO em 2 casas, nunca arredondamento para cima: round1(99,996) daria
		// 100,0 e o front (que também usa piso) exibiria "100,00%" para um site que
		// caiu. O truncamento tem de acontecer aqui, antes de qualquer formatação.
		p := floor2(st.Percent)
		v.Percent = &p
	}
	return v
}

func floor2(v float64) float64 { return float64(int(v*100)) / 100 }

// consensusView diz de ONDE veio a medição. Um check sem probe_locations é de
// origem única (só a sonda central) e nada na tela dizia isso; um check COM
// sondas designadas que pararam de reportar precisa aparecer como "sem consenso",
// não como se a medição estivesse normal.
type consensusView struct {
	Configured int      `json:"configured"` // sondas designadas no check
	Reporting  int      `json:"reporting"`  // designadas com reporte fresco
	Silent     []string `json:"silent"`     // designadas caladas
	Source     string   `json:"source"`     // central | consenso | sem_consenso
}

// checkRow é o tronco comum das duas listagens (uptime honesto + consenso).
type checkRow struct {
	store.SiteCheck
	UptimeDay   uptimeView         `json:"uptime"`
	UptimeMonth uptimeView         `json:"uptime_30d"`
	Consensus   consensusView      `json:"consensus"`
	Pages       *store.ChildStates `json:"pages,omitempty"` // só para kind='sitemap'
	TimeoutMs   int                `json:"timeout_ms"`      // limite de resposta em vigor
}

// checkView acrescenta a sparkline de latência à linha (tela de Checks).
type checkView struct {
	checkRow
	Sparkline []float64 `json:"sparkline"` // total_ms recentes (mais antigo → mais novo)
}

// overviewRow é uma linha leve do dashboard (sem sparkline por check) —
// calculada em lote para aguentar centenas de páginas.
type overviewRow struct {
	checkRow
	LastLatency float64 `json:"last_latency"`
}

// Overview devolve todos os checks (pais e filhos de sitemap) com uptime e última
// latência, para a aba "Visão geral" — usa queries agregadas, não N por check.
func (h *Handler) Overview(w http.ResponseWriter, r *http.Request) {
	checks, err := h.st.ListSiteChecksTree(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now()
	day, _ := h.st.UptimeCountBatch(r.Context(), now.Add(-24*time.Hour))
	month, _ := h.st.UptimeCountBatch(r.Context(), now.Add(-30*24*time.Hour))
	earliest, _ := h.st.EarliestBatch(r.Context())
	pages, _ := h.st.ChildStatesBatch(r.Context())
	lat, _ := h.st.LastLatencyBatch(r.Context())
	out := make([]overviewRow, 0, len(checks))
	for _, c := range checks {
		row := h.rowFor(r, c, now, day[c.ID], month[c.ID], earliest[c.ID], pages)
		out = append(out, overviewRow{checkRow: row, LastLatency: lat[c.ID]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": out})
}

// timeoutDoCheck é o limite QUE VALE para este check — o dele, se configurado, ou o
// default do pacote. A API publicava sempre o default, então um check com limite
// próprio era exibido com o número errado na tela e no rótulo "sem resposta em N s".
func timeoutDoCheck(c store.SiteCheck) time.Duration {
	return Target{TimeoutMS: c.TimeoutMS}.Timeout()
}

// rowFor monta a linha de um check: uptime com cobertura, origem da medição e,
// para sitemap, a contagem de páginas no lugar do uptime.
func (h *Handler) rowFor(r *http.Request, c store.SiteCheck, now time.Time,
	day, month store.UptimeCount, earliest time.Time, pages map[int64]store.ChildStates,
) checkRow {
	row := checkRow{SiteCheck: c, TimeoutMs: int(timeoutDoCheck(c).Milliseconds())}
	if c.Kind == "sitemap" {
		// O pai de um sitemap NUNCA chama InsertSiteCheckResult (quem sonda são os
		// filhos), mas passava por UptimePercent e caía no `total == 0 → 100`: um
		// site inteiro fora do ar exibia "Uptime 24h 100,00%" ao lado do estado DOWN.
		// Sitemap não tem uptime próprio — tem contagem de páginas.
		st := pages[c.ID]
		row.Pages = &st
		row.Consensus = consensusView{Source: "central"}
		return row
	}
	row.UptimeDay = newUptimeView(store.MakeUptimeStat(day, earliest, now.Add(-24*time.Hour), now, c.Tier))
	row.UptimeMonth = newUptimeView(store.MakeUptimeStat(month, earliest, now.Add(-30*24*time.Hour), now, c.Tier))
	row.Consensus = h.consensusFor(r, c, now)
	return row
}

// consensusFor descreve a origem da medição do check. Só consulta o banco quando
// o check TEM sondas designadas (o caso comum — inclusive as centenas de páginas
// de um sitemap — não custa query nenhuma).
func (h *Handler) consensusFor(r *http.Request, c store.SiteCheck, now time.Time) consensusView {
	v := consensusView{Configured: len(c.ProbeLocations), Source: "central"}
	if v.Configured == 0 {
		return v
	}
	probes, err := h.st.FreshProbeResults(r.Context(), c.TenantID, c.URL, c.ProbeLocations, now.Add(-ProbeFreshness))
	if err != nil {
		return v
	}
	vistas := make(map[string]bool, len(probes))
	for _, p := range probes {
		vistas[p.Location] = true
	}
	for _, loc := range c.ProbeLocations {
		if vistas[loc] {
			v.Reporting++
		} else {
			v.Silent = append(v.Silent, loc)
		}
	}
	if v.Reporting == 0 {
		v.Source = "sem_consenso"
	} else {
		v.Source = "consenso"
	}
	return v
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	checks, err := h.st.ListSiteChecks(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now()
	pages, _ := h.st.ChildStatesBatch(r.Context())
	out := make([]checkView, 0, len(checks))
	for _, c := range checks {
		day, _ := h.st.UptimeSince(r.Context(), c.ID, now.Add(-24*time.Hour), c.Tier)
		month, _ := h.st.UptimeSince(r.Context(), c.ID, now.Add(-30*24*time.Hour), c.Tier)
		hist, _ := h.st.SiteCheckHistory(r.Context(), c.ID, 30)
		spark := make([]float64, 0, len(hist))
		for i := len(hist) - 1; i >= 0; i-- { // histórico vem desc; inverte p/ cronológico
			spark = append(spark, hist[i].TotalMs)
		}
		row := checkRow{SiteCheck: c, TimeoutMs: int(timeoutDoCheck(c).Milliseconds())}
		if c.Kind == "sitemap" {
			st := pages[c.ID]
			row.Pages = &st
			row.Consensus = consensusView{Source: "central"}
		} else {
			row.UptimeDay = newUptimeView(day)
			row.UptimeMonth = newUptimeView(month)
			row.Consensus = h.consensusFor(r, c, now)
		}
		out = append(out, checkView{checkRow: row, Sparkline: spark})
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": out})
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	req, timeout, ok := lerCheck(w, r)
	if !ok {
		return
	}
	if timeout != nil {
		req.TimeoutMS = *timeout
	}
	sanitizeUserCheck(&req)
	id, err := h.st.CreateSiteCheck(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	req, timeout, ok := lerCheck(w, r)
	if !ok {
		return
	}
	sanitizeUserCheck(&req)
	if err := h.st.UpdateSiteCheck(r.Context(), id, req, timeout); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "não encontrado", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	if err := h.st.DeleteSiteCheck(r.Context(), id); err != nil {
		http.Error(w, "não encontrado", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Probes devolve os resultados por sonda de uma URL (P6.3), para o mapa de regiões.
func (h *Handler) Probes(w http.ResponseWriter, r *http.Request) {
	url := r.URL.Query().Get("url")
	if url == "" {
		http.Error(w, "url obrigatória", http.StatusBadRequest)
		return
	}
	// A URL sozinha não basta para saber DE QUEM são as sondas: `probe_results` tem
	// chave (tenant, url, location), e ler só por url devolvia o reporte de outro
	// cliente que monitora o mesmo endereço — foi assim que dois votos forjados
	// derrubaram um check que a sonda central via no ar. Resolvemos o check primeiro
	// e só então pedimos as sondas DELE; URL que não é de nenhum check devolve lista
	// vazia, em vez de virar uma consulta aberta a `probe_results`.
	checks, err := h.st.ListSiteChecks(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var probes []store.ProbeResult
	for _, c := range checks {
		if c.URL != url {
			continue
		}
		probes, err = h.st.FreshProbeResults(r.Context(), c.TenantID, c.URL, c.ProbeLocations, time.Now().Add(-5*time.Minute))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		break
	}
	if probes == nil {
		probes = []store.ProbeResult{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"probes": probes})
}

func (h *Handler) History(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	// Filtro opcional por intervalo: ?from&to (RFC3339). Ambos presentes → um dia
	// (ou faixa) específico; ausentes → as sondagens mais recentes. O front manda
	// os limites do dia no fuso do navegador, então a fronteira bate com o que o
	// usuário vê na tela.
	q := r.URL.Query()
	var hist []store.SiteCheckResultView
	if fromStr := q.Get("from"); fromStr != "" {
		from, errF := time.Parse(time.RFC3339, fromStr)
		to, errT := time.Parse(time.RFC3339, q.Get("to"))
		if errF != nil || errT != nil || !to.After(from) {
			http.Error(w, "from/to inválidos (use RFC3339, to > from)", http.StatusBadRequest)
			return
		}
		hist, err = h.st.SiteCheckHistoryRange(r.Context(), id, from, to, 2000)
	} else {
		hist, err = h.st.SiteCheckHistory(r.Context(), id, 100)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if hist == nil {
		hist = []store.SiteCheckResultView{}
	}
	now := time.Now()
	tier := "padrao"
	var chk store.SiteCheck
	if c, e := h.st.SiteCheckByID(r.Context(), id); e == nil {
		tier, chk = c.Tier, c
	}
	day, _ := h.st.UptimeSince(r.Context(), id, now.Add(-24*time.Hour), tier)
	month, _ := h.st.UptimeSince(r.Context(), id, now.Add(-30*24*time.Hour), tier)
	resp := map[string]any{
		"history": hist,
		"uptime":  newUptimeView(day), "uptime_30d": newUptimeView(month),
		"timeout_ms": int(timeoutDoCheck(chk).Milliseconds()),
	}
	// Data da sondagem mais antiga retida → limita o seletor de dia no front.
	if earliest, ok, _ := h.st.SiteCheckEarliest(r.Context(), id); ok {
		resp["min_ts"] = earliest.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }

// sanitizeUserCheck normaliza um check vindo da API: todo check do usuário alerta
// (só os filhos de sitemap, criados internamente, ficam silenciosos), nunca é
// filho, e o kind fica restrito a http|sitemap.
// Faixa aceita para o timeout por check. 0 = o default do pacote (20 s).
const (
	minTimeoutMS = 1000
	maxTimeoutMS = 60000
)

// lerCheck decodifica o corpo de criação/edição. timeout_ms vem À PARTE, como
// ponteiro, porque ausente e zero significam coisas diferentes na edição: o
// formulário da tela não manda o campo, e isso não pode zerar o limite gravado.
//
// Antes o campo era aceito no JSON e descartado em silêncio pelo INSERT/UPDATE:
// medido na validação de 02/10/2026, um check criado com timeout_ms=5000 seguia
// esperando 20 s (total_ms = 20000) e a API devolvia timeout_ms=20000 — o
// timeout por check existia na coluna, no Target e na tela, mas não tinha porta
// de entrada.
func lerCheck(w http.ResponseWriter, r *http.Request) (store.SiteCheck, *int, bool) {
	var req store.SiteCheck
	corpo, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err == nil {
		err = json.Unmarshal(corpo, &req)
	}
	if err != nil || req.Name == "" || req.URL == "" {
		http.Error(w, "name e url obrigatórios", http.StatusBadRequest)
		return req, nil, false
	}
	var extra struct {
		TimeoutMS *int `json:"timeout_ms"`
	}
	_ = json.Unmarshal(corpo, &extra)
	if t := extra.TimeoutMS; t != nil && *t != 0 && (*t < minTimeoutMS || *t > maxTimeoutMS) {
		http.Error(w, fmt.Sprintf("timeout_ms deve ser 0 (padrão de %d s) ou estar entre %d e %d",
			int(DefaultTimeout.Seconds()), minTimeoutMS, maxTimeoutMS), http.StatusBadRequest)
		return req, nil, false
	}
	return req, extra.TimeoutMS, true
}

func sanitizeUserCheck(c *store.SiteCheck) {
	c.Alerting = true
	c.ParentID = nil
	if c.Kind != "sitemap" {
		c.Kind = "http"
	}
}

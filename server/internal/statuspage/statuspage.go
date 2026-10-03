// Package statuspage serve a status page pública (P6.4): estado dos sites e uptime,
// sem login, com cache curto em memória.
package statuspage

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

type Handler struct {
	st  *store.Store
	mu  sync.Mutex
	buf []byte
	exp time.Time
}

func NewHandler(st *store.Store) *Handler { return &Handler{st: st} }

// uptimeView é o uptime publicado: percentual APENAS quando a cobertura da
// medição autoriza afirmá-lo.
//
// Esta é a página mais perigosa do sistema para o zero silencioso: é pública, sem
// login, e o antigo `if total == 0 { return 100 }` publicava 100% justamente para
// os períodos em que não houve sondagem nenhuma. Medido no dev: com o monitor
// parado por 12,6 dias e 3,4% de cobertura, /api/status publicava
// uptime_day/uptime_month/uptime_90d = 100. `percent: null` obriga a página a
// dizer "sem dados suficientes".
type uptimeView struct {
	Percent  *float64 `json:"percent"`  // null = cobertura insuficiente
	Coverage float64  `json:"coverage"` // % do período efetivamente sondado
	Samples  int      `json:"samples"`
}

type siteView struct {
	Name  string `json:"name"`
	Group string `json:"group"`
	URL   string `json:"url"`
	State string `json:"state"`
	// Sitemap não sonda nada: mostra páginas no ar, não percentual.
	Kind      string             `json:"kind"`
	Pages     *store.ChildStates `json:"pages,omitempty"`
	UptimeDay *uptimeView        `json:"uptime_day"`
	UptimeMon *uptimeView        `json:"uptime_month"`
	// Uptime90d é NULO quando o histórico retido é menor que 90 dias. Publicar
	// "90d" sobre 26 dias de histórico (com um buraco de 12) é inventar o resto do
	// trimestre — era exatamente o que /api/status fazia.
	Uptime90d *uptimeView `json:"uptime_90d"`
}

// Status devolve o snapshot público (cache de 30s). Sem autenticação.
func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	if time.Now().Before(h.exp) && h.buf != nil {
		buf := h.buf
		h.mu.Unlock()
		writeCached(w, buf)
		return
	}
	h.mu.Unlock()

	buf, err := h.build(r.Context())
	if err != nil {
		http.Error(w, "erro", http.StatusInternalServerError)
		return
	}
	h.mu.Lock()
	h.buf = buf
	h.exp = time.Now().Add(30 * time.Second)
	h.mu.Unlock()
	writeCached(w, buf)
}

func (h *Handler) build(ctx context.Context) ([]byte, error) {
	checks, err := h.st.ListSiteChecks(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	pages, _ := h.st.ChildStatesBatch(ctx)
	sites := make([]siteView, 0, len(checks))
	overall := "operacional"
	for _, c := range checks {
		// Esta página é PÚBLICA e sem login: o que entra aqui está entregue a quem
		// souber o endereço. A URL completa de um check — caminho e query inclusive —
		// é reconhecimento pronto quando o alvo é endpoint interno, staging ou
		// formulário. A coluna `public` existia e NÃO era consultada: medido em
		// produção, os 4 checks com public=false apareciam com URL inteira num
		// `curl` sem credencial nenhuma.
		//
		// O estado geral também é calculado só sobre o que é público: senão a página
		// anunciaria "interrupção" por causa de um serviço que ela não mostra, e o
		// visitante veria tudo verde ao lado de um aviso vermelho sem explicação.
		if !c.Public {
			continue
		}
		group := c.GroupName
		if group == "" {
			group = "Geral"
		}
		sv := siteView{Name: c.Name, Group: group, URL: c.URL, State: c.State, Kind: c.Kind}
		if c.Kind == "sitemap" {
			// O pai de um sitemap nunca grava resultado de sondagem; passar por
			// UptimePercent devolvia 100% e um site inteiro fora do ar aparecia como
			// "100,00%" ao lado do estado DOWN.
			st := pages[c.ID]
			sv.Pages = &st
		} else {
			day, _ := h.st.UptimeSince(ctx, c.ID, now.Add(-24*time.Hour), c.Tier)
			mon, _ := h.st.UptimeSince(ctx, c.ID, now.Add(-30*24*time.Hour), c.Tier)
			d90, _ := h.st.UptimeSince(ctx, c.ID, now.Add(-90*24*time.Hour), c.Tier)
			sv.UptimeDay = newUptimeView(day)
			sv.UptimeMon = newUptimeView(mon)
			// Só publica "90d" quando há 90 dias de histórico de verdade.
			if d90.WindowFull {
				sv.Uptime90d = newUptimeView(d90)
			}
		}
		sites = append(sites, sv)
		if c.State == "DOWN" {
			overall = "interrupção"
		} else if c.State == "DEGRADADO" && overall == "operacional" {
			overall = "degradado"
		}
	}

	return json.Marshal(map[string]any{
		"overall":    overall,
		"updated_at": now.Format(time.RFC3339),
		"sites":      sites,
	})
}

// newUptimeView converte o UptimeStat do store no que a página pode afirmar.
func newUptimeView(st store.UptimeStat) *uptimeView {
	v := &uptimeView{Coverage: round1(st.Coverage), Samples: st.Samples}
	if st.Sufficient {
		p := round2(st.Percent)
		v.Percent = &p
	}
	return v
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }

// round2 TRUNCA (piso) em 2 casas: arredondar para cima faria 99,996% virar
// "100,00%" e a página pública afirmaria que nada caiu. Mesma regra do
// formatUptimePercent do front.
func round2(v float64) float64 { return float64(int(v*100)) / 100 }

func writeCached(w http.ResponseWriter, buf []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=30")
	_, _ = w.Write(buf)
}

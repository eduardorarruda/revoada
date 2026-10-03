package scrape

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/eduardorarruda/revoada/gateway/internal/model"
	"github.com/eduardorarruda/revoada/gateway/internal/pg"
)

// Publisher publica lotes de métricas (implementado por queue.Queue).
type Publisher interface {
	PublishMetrics(ctx context.Context, metrics []model.Metric) error
}

// Scraper coleta periodicamente os alvos Prometheus cadastrados no PostgreSQL.
type Scraper struct {
	log         *slog.Logger
	store       *pg.Store
	pub         Publisher
	http        *http.Client
	maxInFlight int
}

// New cria o scraper. maxInFlight limita o paralelismo de coletas simultâneas.
func New(log *slog.Logger, store *pg.Store, pub Publisher, maxInFlight int) *Scraper {
	if maxInFlight <= 0 {
		maxInFlight = 8
	}
	return &Scraper{
		log:         log,
		store:       store,
		pub:         pub,
		http:        &http.Client{Timeout: 10 * time.Second},
		maxInFlight: maxInFlight,
	}
}

// Run executa o loop de scrape até o ctx ser cancelado. Recarrega os alvos a cada
// reload e coleta cada alvo respeitando seu interval_seconds.
func (s *Scraper) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	last := map[string]time.Time{} // url -> última coleta
	var targets []pg.ScrapeTarget
	reloadEvery := 30
	tick := 0

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if tick%reloadEvery == 0 {
				if t, err := s.store.ListScrapeTargets(ctx); err != nil {
					s.log.Warn("listando scrape_targets", "err", err)
				} else {
					targets = t
				}
			}
			tick++

			sem := make(chan struct{}, s.maxInFlight)
			var wg sync.WaitGroup
			for _, t := range targets {
				due := last[t.URL].IsZero() || now.Sub(last[t.URL]) >= time.Duration(t.IntervalSeconds)*time.Second
				if !due {
					continue
				}
				last[t.URL] = now
				wg.Add(1)
				sem <- struct{}{}
				go func(t pg.ScrapeTarget) {
					defer wg.Done()
					defer func() { <-sem }()
					s.scrapeOne(ctx, t, now)
				}(t)
			}
			wg.Wait()
		}
	}
}

func (s *Scraper) scrapeOne(ctx context.Context, t pg.ScrapeTarget, now time.Time) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL, nil)
	if err != nil {
		s.log.Warn("scrape: request inválido", "url", t.URL, "err", err)
		return
	}
	resp, err := s.http.Do(req)
	if err != nil {
		s.log.Warn("scrape: falha", "url", t.URL, "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		s.log.Warn("scrape: status inesperado", "url", t.URL, "status", resp.StatusCode)
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // teto de 8 MiB por alvo
	if err != nil {
		s.log.Warn("scrape: leitura", "url", t.URL, "err", err)
		return
	}
	metrics := parseExposition(t.TenantID, string(body), t.Labels, now.UTC())
	if len(metrics) == 0 {
		return
	}
	if err := s.pub.PublishMetrics(ctx, metrics); err != nil {
		s.log.Error("scrape: publicando", "url", t.URL, "err", err)
	}
}

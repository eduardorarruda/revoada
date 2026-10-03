package sitecheck

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxSitemapURLs limita quantas páginas um sitemap pode expandir (guarda contra
// sitemaps enormes que gerariam sondagem excessiva).
const maxSitemapURLs = 500

// sitemapDoc casa tanto <urlset> (páginas) quanto <sitemapindex> (sub-sitemaps).
// O parser do Go casa pelo nome local, ignorando o namespace do sitemaps.org.
type sitemapDoc struct {
	URLs     []locEntry `xml:"url"`
	Sitemaps []locEntry `xml:"sitemap"`
}
type locEntry struct {
	Loc string `xml:"loc"`
}

// FetchSitemapURLs busca um sitemap.xml e devolve as URLs de página, deduplicadas
// e limitadas a maxSitemapURLs. Se for um índice de sitemaps, resolve cada
// sub-sitemap em um nível. TLS/SSRF ficam a cargo do client passado.
func (p *Prober) FetchSitemapURLs(ctx context.Context, rawURL string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	if err := collectSitemap(ctx, p.client, rawURL, 1, seen, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func collectSitemap(ctx context.Context, client *http.Client, rawURL string, depth int, seen map[string]bool, out *[]string) error {
	doc, err := fetchSitemapDoc(ctx, client, rawURL)
	if err != nil {
		return err
	}
	for _, e := range doc.URLs {
		loc := strings.TrimSpace(e.Loc)
		if loc == "" || seen[loc] || !isHTTPURL(loc) {
			continue
		}
		seen[loc] = true
		*out = append(*out, loc)
		if len(*out) >= maxSitemapURLs {
			return nil
		}
	}
	// Índice de sitemaps: desce um nível (depth>0), respeitando o cap global.
	if depth > 0 {
		for _, sm := range doc.Sitemaps {
			loc := strings.TrimSpace(sm.Loc)
			if loc == "" || !isHTTPURL(loc) {
				continue
			}
			if len(*out) >= maxSitemapURLs {
				return nil
			}
			if err := collectSitemap(ctx, client, loc, depth-1, seen, out); err != nil {
				// um sub-sitemap ruim não invalida os demais.
				continue
			}
		}
	}
	return nil
}

func fetchSitemapDoc(ctx context.Context, client *http.Client, rawURL string) (*sitemapDoc, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Revoada-Next/synthetic")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sitemap http %d", resp.StatusCode)
	}
	// cap de 10 MiB: sitemaps grandes existem, mas há um teto de segurança.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, err
	}
	var doc sitemapDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("sitemap inválido: %w", err)
	}
	return &doc, nil
}

func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

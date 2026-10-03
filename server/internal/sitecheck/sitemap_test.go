package sitecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const urlsetXML = `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://ex.com/</loc></url>
  <url><loc>https://ex.com/a</loc></url>
  <url><loc>https://ex.com/a</loc></url>
  <url><loc>ftp://ex.com/skip</loc></url>
  <url><loc></loc></url>
</urlset>`

func TestCollectSitemapURLset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(urlsetXML))
	}))
	defer srv.Close()

	var out []string
	seen := map[string]bool{}
	if err := collectSitemap(context.Background(), srv.Client(), srv.URL, 1, seen, &out); err != nil {
		t.Fatalf("collectSitemap: %v", err)
	}
	// dedupe (a aparece 1x), ignora esquema não-http e loc vazio.
	if len(out) != 2 {
		t.Fatalf("esperava 2 URLs, veio %d: %v", len(out), out)
	}
}

func TestCollectSitemapIndex(t *testing.T) {
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
			<url><loc>https://ex.com/p1</loc></url>
			<url><loc>https://ex.com/p2</loc></url>
		</urlset>`))
	}))
	defer child.Close()

	indexXML := `<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
		<sitemap><loc>` + child.URL + `</loc></sitemap>
	</sitemapindex>`
	index := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(indexXML))
	}))
	defer index.Close()

	var out []string
	seen := map[string]bool{}
	if err := collectSitemap(context.Background(), index.Client(), index.URL, 1, seen, &out); err != nil {
		t.Fatalf("collectSitemap: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("esperava 2 URLs do sub-sitemap, veio %d: %v", len(out), out)
	}
}

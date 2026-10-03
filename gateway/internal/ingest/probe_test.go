package ingest

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eduardorarruda/revoada/gateway/internal/pg"
)

// storeFake é o PostgreSQL do endpoint de sondas, em memória.
type storeFake struct {
	// vinculo: serverkey -> localização já registrada (o vínculo 1:1 real vive no banco;
	// aqui basta reproduzir a REGRA para o teste do endpoint).
	vinculo   map[string]string
	gravado   []pg.ProbeResult
	tenant    string
	revogada  bool
	descon    bool
	erroBind  error
	chamouUps int
}

func novoStore() *storeFake {
	return &storeFake{vinculo: map[string]string{}, tenant: "default"}
}

func (s *storeFake) Authenticate(_ context.Context, key string) (pg.Agent, error) {
	if s.descon {
		return pg.Agent{}, pg.ErrUnknownAgent
	}
	return pg.Agent{TenantID: s.tenant, Revoked: s.revogada, Hostname: "sonda"}, nil
}

func (s *storeFake) AssignedChecks(context.Context, string, string) ([]pg.AssignedURL, error) {
	return nil, nil
}

func (s *storeFake) BindProbeLocation(_ context.Context, _, serverkey, location string) error {
	if s.erroBind != nil {
		return s.erroBind
	}
	if loc, ok := s.vinculo[serverkey]; ok && loc != location {
		return pg.ErrProbeLocationDenied
	}
	s.vinculo[serverkey] = location
	return nil
}

func (s *storeFake) UpsertProbeResult(_ context.Context, _ string, r pg.ProbeResult) error {
	s.chamouUps++
	s.gravado = append(s.gravado, r)
	return nil
}

func postProbe(h http.Handler, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/ingest/probe-result", strings.NewReader(body))
	req.Header.Set("X-Revoada-Key", key)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestProbeGuardaTruncatedEStatus: os dois campos VINHAM do agente-sonda e o probeReq
// não os declarava, então o json.Decode os descartava EM SILÊNCIO. Sem eles o painel
// publica um total_ms que é só um PISO como se fosse a medida completa, e não sabe
// dizer POR QUE a asserção falhou.
func TestProbeGuardaTruncatedEStatus(t *testing.T) {
	st := novoStore()
	h := NewProbeResult(slog.Default(), st)

	rec := postProbe(h, "chave-sonda", `{
		"url":"https://exemplo.tld/",
		"probe_location":"sp-saopaulo",
		"up":false,
		"total_ms":1234.5,
		"diagnostic":"keyword_indeterminado",
		"truncated":true,
		"status":503
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d corpo=%s", rec.Code, rec.Body.String())
	}
	if len(st.gravado) != 1 {
		t.Fatalf("gravações=%d, quer 1", len(st.gravado))
	}
	g := st.gravado[0]
	if !g.Truncated {
		t.Fatal("truncated foi descartado: total_ms vira 'medida completa' na tela sendo um piso")
	}
	if g.Status != 503 {
		t.Fatalf("status gravado=%d, quer 503", g.Status)
	}
	if g.TotalMs != 1234.5 || g.Diagnostic != "keyword_indeterminado" || g.Up {
		t.Fatalf("campos antigos regrediram: %+v", g)
	}
}

// TestProbeRecusaLocalizacaoForjada: o endpoint aceitava `probe_location` LIVRE de
// qualquer serverkey válida. Provado por exploração: com UMA ÚNICA chave foram forjados
// dois votos (`caos-fake-tokyo` e `caos-fake-berlim`) e um check de origem única, que a
// sonda central via no ar, virou `DOWN — down em 2 sonda(s)`. Voto é voto: uma chave,
// uma localização.
func TestProbeRecusaLocalizacaoForjada(t *testing.T) {
	st := novoStore()
	h := NewProbeResult(slog.Default(), st)
	corpo := func(loc string) string {
		return `{"url":"https://exemplo.tld/","probe_location":"` + loc + `","up":false,"diagnostic":"connect_timeout"}`
	}

	if rec := postProbe(h, "uma-chave-so", corpo("caos-fake-tokyo")); rec.Code != http.StatusOK {
		t.Fatalf("primeiro reporte (registra o vínculo) = %d", rec.Code)
	}
	rec := postProbe(h, "uma-chave-so", corpo("caos-fake-berlim"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("segundo voto forjado pela MESMA chave = %d, quer 403", rec.Code)
	}
	if st.chamouUps != 1 {
		t.Fatalf("o voto forjado foi gravado assim mesmo: gravações=%d, quer 1", st.chamouUps)
	}
}

// TestProbeChaveOutraLocalizacaoNaoAfeta: chaves distintas continuam podendo reportar
// por localizações distintas — a correção não pode matar a sonda multi-região.
func TestProbeChaveOutraLocalizacaoNaoAfeta(t *testing.T) {
	st := novoStore()
	h := NewProbeResult(slog.Default(), st)
	corpo := `{"url":"https://exemplo.tld/","probe_location":"%s","up":true,"total_ms":100}`
	if rec := postProbe(h, "chave-tokyo", strings.Replace(corpo, "%s", "tokyo", 1)); rec.Code != http.StatusOK {
		t.Fatalf("tokyo=%d", rec.Code)
	}
	if rec := postProbe(h, "chave-berlim", strings.Replace(corpo, "%s", "berlim", 1)); rec.Code != http.StatusOK {
		t.Fatalf("berlim=%d", rec.Code)
	}
	if st.chamouUps != 2 {
		t.Fatalf("gravações=%d, quer 2", st.chamouUps)
	}
}

// TestProbeMesmaLocalizacaoRepetida: a sonda legítima reporta a cada 30 s pela MESMA
// localização — isso não pode virar 403 no segundo tick.
func TestProbeMesmaLocalizacaoRepetida(t *testing.T) {
	st := novoStore()
	h := NewProbeResult(slog.Default(), st)
	corpo := `{"url":"https://exemplo.tld/","probe_location":"sp","up":true,"total_ms":90}`
	for i := 0; i < 5; i++ {
		if rec := postProbe(h, "chave-sp", corpo); rec.Code != http.StatusOK {
			t.Fatalf("tick %d = %d", i, rec.Code)
		}
	}
	if st.chamouUps != 5 {
		t.Fatalf("gravações=%d, quer 5", st.chamouUps)
	}
}

// TestProbeChaveRevogadaOuDesconhecida: as recusas de auth continuam valendo.
func TestProbeChaveRevogadaOuDesconhecida(t *testing.T) {
	corpo := `{"url":"https://exemplo.tld/","probe_location":"sp"}`

	st := novoStore()
	st.revogada = true
	if rec := postProbe(NewProbeResult(slog.Default(), st), "k", corpo); rec.Code != http.StatusForbidden {
		t.Fatalf("chave revogada = %d, quer 403", rec.Code)
	}
	st = novoStore()
	st.descon = true
	if rec := postProbe(NewProbeResult(slog.Default(), st), "k", corpo); rec.Code != http.StatusUnauthorized {
		t.Fatalf("chave desconhecida = %d, quer 401", rec.Code)
	}
}

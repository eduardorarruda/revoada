package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eduardorarruda/revoada/gateway/internal/health"
)

func newTestServer(checkers ...health.Checker) *Server {
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return New(log, checkers...)
}

func TestHealthz(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d, quer 200", rec.Code)
	}
}

func TestReadyzAllUp(t *testing.T) {
	ok := health.CheckerFunc{Label: "dep", Fn: func(context.Context) error { return nil }}
	s := newTestServer(ok)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz (tudo up) = %d, quer 200", rec.Code)
	}
	var res health.Result
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if !res.Ready || res.Checks["dep"] != "ok" {
		t.Fatalf("resultado inesperado: %+v", res)
	}
}

func TestReadyzDependencyDown(t *testing.T) {
	down := health.CheckerFunc{Label: "clickhouse", Fn: func(context.Context) error {
		return errors.New("conexão recusada")
	}}
	s := newTestServer(down)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz (dep down) = %d, quer 503", rec.Code)
	}
	var res health.Result
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Ready {
		t.Fatalf("esperava ready=false, veio true")
	}
}

// Package server monta o HTTP server do gateway (health, e nas fases seguintes,
// os endpoints de ingestão).
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/eduardorarruda/revoada/gateway/internal/health"
)

// Server encapsula o roteador HTTP e suas dependências.
type Server struct {
	log      *slog.Logger
	checkers []health.Checker
	mux      *http.ServeMux
}

// New cria o Server com os checkers de readiness informados.
func New(log *slog.Logger, checkers ...health.Checker) *Server {
	s := &Server{log: log, checkers: checkers, mux: http.NewServeMux()}
	s.routes()
	return s
}

// Handler expõe o roteador (útil em testes).
func (s *Server) Handler() http.Handler { return s.mux }

// Handle registra uma rota adicional (ex.: ingestão) antes do Run.
func (s *Server) Handle(pattern string, h http.Handler) { s.mux.Handle(pattern, h) }

func (s *Server) routes() {
	// Liveness: o processo está de pé.
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Readiness: dependências (ClickHouse, NATS) estão acessíveis.
	s.mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		res := health.Evaluate(ctx, s.checkers...)
		code := http.StatusOK
		if !res.Ready {
			code = http.StatusServiceUnavailable
		}
		writeJSON(w, code, res)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Run sobe o servidor e faz graceful shutdown ao cancelar o ctx.
func (s *Server) Run(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:    addr,
		Handler: s.mux,
		// Anti-slowloris / exaustão de conexão. Logs e traces agora enfileiram
		// (batcher assíncrono) e respondem rápido, então WriteTimeout modesto basta.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		s.log.Info("gateway ouvindo", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		s.log.Info("encerrando gateway (graceful)")
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	case err := <-errCh:
		return err
	}
}

// Package health provê checagens de readiness para dependências externas.
package health

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Checker verifica se uma dependência está pronta.
type Checker interface {
	Name() string
	Check(ctx context.Context) error
}

// CheckerFunc adapta uma função para Checker.
type CheckerFunc struct {
	Label string
	Fn    func(ctx context.Context) error
}

func (c CheckerFunc) Name() string                    { return c.Label }
func (c CheckerFunc) Check(ctx context.Context) error { return c.Fn(ctx) }

// HTTPReady devolve um checker que considera pronto se a URL responde 2xx.
// Usado para o endpoint de monitoramento do NATS (porta 8222 /healthz).
func HTTPReady(label, url string) Checker {
	client := &http.Client{Timeout: 3 * time.Second}
	return CheckerFunc{Label: label, Fn: func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("%s respondeu %d", label, resp.StatusCode)
		}
		return nil
	}}
}

// Result agrega o resultado de todos os checkers.
type Result struct {
	Ready  bool              `json:"ready"`
	Checks map[string]string `json:"checks"` // nome -> "ok" ou mensagem de erro
}

// Evaluate roda todos os checkers e monta o Result.
func Evaluate(ctx context.Context, checkers ...Checker) Result {
	res := Result{Ready: true, Checks: map[string]string{}}
	for _, c := range checkers {
		if err := c.Check(ctx); err != nil {
			res.Ready = false
			res.Checks[c.Name()] = err.Error()
		} else {
			res.Checks[c.Name()] = "ok"
		}
	}
	return res
}

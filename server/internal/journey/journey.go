// Package journey executa jornadas de browser multi-passo (P6.4): navegar →
// preencher formulário → asserir. Detecta form quebrado que o check HTTP simples
// não pega (POST + asserção de sucesso), com estado e alerta.
//
// Nota: esta versão executa passos HTTP (com cookie jar) — cobre formulários
// server-side. Jornadas com JS pesado (Playwright headless) ficam como follow-up.
package journey

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/agenda"
	"github.com/eduardorarruda/revoada/server/internal/alerting"
	"github.com/eduardorarruda/revoada/server/internal/chquery"
	"github.com/eduardorarruda/revoada/server/internal/safehttp"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

type Runner struct {
	st       *store.Store
	ch       *chquery.Client
	notifier alerting.Notifier
	log      *slog.Logger
	voo      *agenda.EmVoo // jornadas em andamento (ver tick)
}

// maxJornadasSimultaneas é o teto de jornadas rodando em paralelo.
const maxJornadasSimultaneas = 4

func NewRunner(st *store.Store, ch *chquery.Client, notifier alerting.Notifier, log *slog.Logger) *Runner {
	return &Runner{st: st, ch: ch, notifier: notifier, log: log, voo: agenda.NovoEmVoo(maxJornadasSimultaneas)}
}

func (r *Runner) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tick(ctx)
		}
	}
}

func (r *Runner) tick(ctx context.Context) {
	// Mesma correção do sitecheck (ver internal/agenda): folga de 1 s na consulta e
	// disparo sem esperar. Antes as jornadas rodavam em fila dentro do tick — uma
	// jornada lenta (até 15 s por passo) atrasava todas as outras — e a de "30 s"
	// rodava a cada 35 s, medido contra um servidor que registrava cada passo.
	due, err := r.st.DueJourneys(ctx, time.Now().Add(agenda.Folga))
	if err != nil {
		r.log.Warn("journey: listando", "err", err)
		return
	}
	for _, j := range due {
		j := j
		r.voo.Disparar(ctx, j.ID, func() { r.runOne(ctx, j) })
	}
}

// runOne executa todos os passos de uma jornada com cookie jar compartilhado.
func (r *Runner) runOne(ctx context.Context, j store.Journey) {
	inicio := time.Now()
	ok, diag, totalMs := r.execute(ctx, j)
	now := time.Now()
	// Cadência de início a início: o NextCheckAt calculado a partir de `now` (fim)
	// é reancorado em `inicio` antes de gravar (ver internal/agenda).
	j.LastCheckedAt = &now
	interval := time.Duration(j.IntervalSeconds) * time.Second

	up := 0.0
	if ok {
		up = 1
	}
	_ = r.ch.InsertMetrics(ctx, "default", []chquery.MetricPoint{
		{Metric: "synthetic.journey.up", Labels: map[string]string{"journey": j.Name}, Value: up},
		{Metric: "synthetic.journey.total_ms", Labels: map[string]string{"journey": j.Name}, Value: totalMs},
	})

	if ok {
		if j.State == "DOWN" {
			r.notify(j, "resolved", "")
		}
		j.State = "UP"
		j.ConsecutiveFails = 0
		j.LastDiagnosis = ""
	} else {
		j.ConsecutiveFails++
		j.LastDiagnosis = diag
		if j.ConsecutiveFails == 1 {
			j.State = "SUSPEITO"
			next := now.Add(30 * time.Second)
			j.NextCheckAt = agenda.Reancorar(&next, inicio, now)
			_ = r.st.UpdateJourneyState(ctx, j)
			r.log.Info("journey: SUSPEITO", "journey", j.Name, "diag", diag)
			return
		}
		if j.State != "DOWN" {
			r.notify(j, "firing", diag)
			r.log.Info("journey: DOWN", "journey", j.Name, "diag", diag)
		}
		j.State = "DOWN"
	}
	next := now.Add(interval)
	j.NextCheckAt = agenda.Reancorar(&next, inicio, now)
	if err := r.st.UpdateJourneyState(ctx, j); err != nil {
		r.log.Warn("journey: atualizando estado", "err", err)
	}
}

// execute roda os passos; devolve (ok, diagnóstico, duração total ms).
func (r *Runner) execute(ctx context.Context, j store.Journey) (bool, string, float64) {
	jar, _ := cookiejar.New(nil)
	// Transport endurecido contra SSRF (dialer guardado + CheckRedirect).
	client := &http.Client{
		Jar:           jar,
		Timeout:       15 * time.Second,
		Transport:     safehttp.Transport(),
		CheckRedirect: safehttp.CheckRedirect,
	}
	start := time.Now()

	for i, step := range j.Steps {
		var req *http.Request
		var err error
		method := strings.ToUpper(step.Method)
		if method == "" {
			method = http.MethodGet
		}
		if method == http.MethodPost {
			form := url.Values{}
			for k, v := range step.Form {
				form.Set(k, v)
			}
			req, err = http.NewRequestWithContext(ctx, http.MethodPost, step.URL, strings.NewReader(form.Encode()))
			if req != nil {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
		} else {
			req, err = http.NewRequestWithContext(ctx, method, step.URL, nil)
		}
		if err != nil {
			return false, fmt.Sprintf("passo %d (%s): URL inválida", i+1, step.Name), msSince(start)
		}
		resp, err := client.Do(req)
		if err != nil {
			return false, fmt.Sprintf("passo %d (%s): %s", i+1, step.Name, classify(err)), msSince(start)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()

		if step.AssertStatus != 0 && resp.StatusCode != step.AssertStatus {
			return false, fmt.Sprintf("passo %d (%s): status %d ≠ %d", i+1, step.Name, resp.StatusCode, step.AssertStatus), msSince(start)
		}
		if step.AssertStatus == 0 && resp.StatusCode >= 400 {
			return false, fmt.Sprintf("passo %d (%s): status %d", i+1, step.Name, resp.StatusCode), msSince(start)
		}
		if step.AssertContains != "" && !strings.Contains(string(body), step.AssertContains) {
			return false, fmt.Sprintf("passo %d (%s): texto %q ausente", i+1, step.Name, step.AssertContains), msSince(start)
		}
	}
	return true, "", msSince(start)
}

func (r *Runner) notify(j store.Journey, state, diag string) {
	if r.notifier == nil {
		return
	}
	r.notifier.Notify(context.Background(), alerting.Notification{
		Rule: store.AlertRule{
			Name: "Jornada falhou: " + j.Name, Metric: "synthetic.journey.up",
			Agg: "jornada", ConditionOp: "==", Threshold: 0, Severity: "critical",
		},
		Labels: map[string]string{"journey": j.Name, "diagnóstico": diag},
		Value:  0, State: state, Since: time.Now(),
	})
}

func msSince(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }

func classify(err error) string {
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "no such host"):
		return "dns_error"
	case strings.Contains(s, "timeout") || strings.Contains(s, "deadline"):
		return "timeout"
	case strings.Contains(s, "refused"):
		return "connect_refused"
	default:
		return "unreachable"
	}
}

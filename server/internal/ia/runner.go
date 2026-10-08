package ia

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/eduardorarruda/revoada/core/genai"
	"github.com/eduardorarruda/revoada/server/internal/chquery"
)

// O runner fecha, a cada minuto, o minuto de `atrasoRunner` atrás e grava as séries
// llm.* em `metrics`. A partir daí as regras de alerta comuns (com prévia retroativa,
// plantão, silêncio) e os dashboards funcionam sem nada específico de IA no motor.
//
// O atraso existe porque o span de um agente só é exportado quando ele termina: sem
// esperar, o minuto seria fechado sem as chamadas de uma execução que ainda estava em
// curso, e o custo daquele minuto ficaria para sempre menor.
const (
	atrasoRunner       = 5 * time.Minute
	maxMinutosPorTique = 10 // depois de uma parada longa, não tenta recuperar o dia inteiro
	marcadorMinuto     = "llm.chamadas"
)

// Runner grava as séries llm.* por minuto.
type Runner struct {
	h      *Handler
	log    *slog.Logger
	ultimo time.Time // último minuto fechado
}

func NewRunner(h *Handler, log *slog.Logger) *Runner { return &Runner{h: h, log: log} }

// Run fecha um minuto por tique até o contexto acabar.
func (r *Runner) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		r.tique(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (r *Runner) tique(ctx context.Context) {
	alvo := r.h.agora().Add(-atrasoRunner).Truncate(time.Minute).Add(-time.Minute)
	if r.ultimo.IsZero() {
		r.ultimo = alvo.Add(-time.Minute)
	}
	if alvo.Sub(r.ultimo) > maxMinutosPorTique*time.Minute {
		r.ultimo = alvo.Add(-maxMinutosPorTique * time.Minute)
	}
	for m := r.ultimo.Add(time.Minute); !m.After(alvo); m = m.Add(time.Minute) {
		if err := r.FecharMinuto(ctx, m); err != nil {
			r.log.Warn("ia: fechando minuto das séries llm.*", "minuto", m, "err", err)
			return // tenta de novo no próximo tique, a partir deste minuto
		}
		r.ultimo = m
	}
}

// FecharMinuto calcula e grava as séries do minuto m. Idempotente: se o minuto já foi
// gravado (reinício do painel no meio do caminho), não grava de novo — senão o custo
// daquele minuto contaria em dobro numa soma de alerta.
func (r *Runner) FecharMinuto(ctx context.Context, m time.Time) error {
	ja, err := r.h.ch.QueryJSON(ctx, fmt.Sprintf(`SELECT count() AS n FROM metrics WHERE tenant_id = 'default'
		AND metric = '%s' AND ts = toDateTime64(%d, 3)`, marcadorMinuto, m.Unix()))
	if err != nil {
		return err
	}
	if len(ja) > 0 && inteiro(ja[0]["n"]) > 0 {
		return nil
	}
	pontos, err := r.h.pontosDoMinuto(ctx, m)
	if err != nil {
		return err
	}
	return r.h.ch.InsertMetricsAt(ctx, "default", m, pontos)
}

// pontosDoMinuto: uso e custo por (agente, service, provedor, modelo) e o maior número
// de chamadas numa execução que terminou no minuto.
func (h *Handler) pontosDoMinuto(ctx context.Context, m time.Time) ([]chquery.MetricPoint, error) {
	tab, _, err := h.tabela(ctx)
	if err != nil {
		return nil, err
	}
	f := Filtros{De: m, Ate: m.Add(time.Minute)}
	rows, err := h.ch.QueryJSON(ctx, fmt.Sprintf(`SELECT ex.ag AS agente, s.service AS service,
		s.provedor AS provedor, s.modelo AS modelo, %s, quantile(0.95)(s.duracao_ms) AS p95
		FROM genai_spans AS s INNER JOIN (%s) AS ex ON s.trace_id = ex.trace_id
		WHERE %s AND s.operacao IN %s GROUP BY agente, service, provedor, modelo`,
		colunasUso("s."), agentePorTrace(f), f.onde("s."), opsDeModelo))
	if err != nil {
		return nil, err
	}
	var pts []chquery.MetricPoint
	for _, row := range rows {
		pts = append(pts, pontosDeUso(row, lerUso(row, m), tab)...)
	}
	passos, err := h.passosMaximos(ctx, m)
	if err != nil {
		return nil, err
	}
	return append(pts, passos...), nil
}

func pontosDeUso(row map[string]any, l linhaUso, tab *genai.Tabela) []chquery.MetricPoint {
	rot := rotulos(texto(row["agente"]), texto(row["service"]))
	rot["provedor"], rot["modelo"] = l.Provedor, l.Modelo
	ponto := func(metrica string, v float64) chquery.MetricPoint {
		return chquery.MetricPoint{Metric: metrica, Labels: rot, Value: v}
	}
	pts := []chquery.MetricPoint{
		ponto(marcadorMinuto, float64(l.Chamadas)),
		ponto("llm.erros", float64(l.Erros)),
		ponto("llm.latencia_p95_ms", numero(row["p95"])),
	}
	if l.ComTokens > 0 {
		pts = append(pts, ponto("llm.tokens.entrada", float64(l.Uso.Entrada)), ponto("llm.tokens.saida", float64(l.Uso.Saida)))
	}
	c := calcular(tab, l)
	if c.Calculado {
		pts = append(pts, ponto("llm.custo_usd", c.USD))
	}
	if c.SemPreco > 0 {
		pts = append(pts, ponto("llm.sem_preco", float64(c.SemPreco)))
	}
	return pts
}

// rotulos monta o mapa de rótulos; agente vazio fica de fora (um rótulo "" não filtra
// nada e polui a lista de valores do construtor de regras).
func rotulos(agente, service string) map[string]string {
	r := map[string]string{"service": service}
	if agente != "" {
		r["agente"] = agente
	}
	return r
}

// passosMaximos: execução cujo último span de IA caiu no minuto m, com quantas chamadas
// de modelo ela fez. Um agente que normalmente faz 4 e de repente faz 40 está em loop.
func (h *Handler) passosMaximos(ctx context.Context, m time.Time) ([]chquery.MetricPoint, error) {
	rows, err := h.ch.QueryJSON(ctx, fmt.Sprintf(`SELECT ag AS agente, svc AS service, max(n) AS passos FROM (
		SELECT trace_id, any(service) AS svc,
			%[5]s AS ag,
			countIf(operacao IN %[1]s) AS n, max(ts) AS ultimo
		FROM genai_spans WHERE tenant_id = 'default'
			AND ts >= fromUnixTimestamp64Milli(%[2]d) AND ts < fromUnixTimestamp64Milli(%[3]d)
		GROUP BY trace_id
		HAVING ultimo >= fromUnixTimestamp64Milli(%[4]d) AND n > 0
	) GROUP BY agente, svc`, opsDeModelo, m.Add(-time.Hour).UnixMilli(), m.Add(time.Minute).UnixMilli(), m.UnixMilli(),
		sqlAgenteDoTrace))
	if err != nil {
		return nil, err
	}
	pts := make([]chquery.MetricPoint, 0, len(rows))
	for _, r := range rows {
		pts = append(pts, chquery.MetricPoint{Metric: "llm.execucao.passos_max",
			Labels: rotulos(texto(r["agente"]), texto(r["service"])), Value: float64(inteiro(r["passos"]))})
	}
	return pts, nil
}

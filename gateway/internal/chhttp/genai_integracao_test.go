package chhttp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/core/genai"
	"github.com/eduardorarruda/revoada/gateway/internal/model"
)

// Integração contra um ClickHouse de verdade com a migração 009 aplicada. Roda só
// quando REVOADA_TEST_CH_ADDR aponta para ele (ex.: http://127.0.0.1:58999), com
// REVOADA_TEST_CH_USER/REVOADA_TEST_CH_PASS. Prova o que mock nenhum prova: que as
// linhas cabem nas colunas, que o retry do mesmo bloco não conta duas vezes e que a MV
// agrega certo (com tokens não informados de fora da soma, e não como zero).
func TestGenAIIntegracao(t *testing.T) {
	addr := os.Getenv("REVOADA_TEST_CH_ADDR")
	if addr == "" {
		t.Skip("REVOADA_TEST_CH_ADDR não definido")
	}
	c := New(Config{Addr: addr, User: os.Getenv("REVOADA_TEST_CH_USER"), Pass: os.Getenv("REVOADA_TEST_CH_PASS")})
	ctx := context.Background()
	tenant := "teste-" + time.Now().Format("150405.000000")
	ts := time.Date(2026, 10, 7, 12, 0, 30, 0, time.UTC)
	n := func(v int64) *int64 { return &v }
	custo := 0.5
	linhas := []model.GenAISpan{
		{TenantID: tenant, TS: ts, TraceID: "t1", SpanID: "s1", Service: "agente", Host: "h1", DuracaoMs: 100,
			Chamada: genai.Chamada{Convencao: genai.ConvOTel, Operacao: "chat", Provedor: "openai", Modelo: "gpt-4o-mini",
				TokensEntrada: n(120), TokensSaida: n(30), TokensCacheLeitura: n(100), MotivosFim: []string{"stop"}}},
		{TenantID: tenant, TS: ts.Add(time.Second), TraceID: "t1", SpanID: "s2", Service: "agente", Host: "h1", DuracaoMs: 300,
			Chamada: genai.Chamada{Operacao: "chat", Provedor: "openai", Modelo: "gpt-4o-mini", Erro: "Timeout"}},
		{TenantID: tenant, TS: ts.Add(2 * time.Second), TraceID: "t1", SpanID: "s3", Service: "agente", Host: "h1", DuracaoMs: 200,
			Chamada: genai.Chamada{Operacao: "chat", Provedor: "openai", Modelo: "gpt-4o-mini",
				TokensEntrada: n(1000), TokensSaida: n(10), CustoInformadoUSD: &custo}},
	}
	for range 2 { // o retry do batcher reenvia o MESMO bloco
		if err := c.InsertGenAISpans(ctx, linhas); err != nil {
			t.Fatal(err)
		}
	}
	conteudo := []model.GenAIConteudo{{TenantID: tenant, TS: ts, TraceID: "t1", SpanID: "s1", Lado: "entrada", Papel: "user", Texto: "oi", Redigido: true}}
	if err := c.InsertGenAIConteudo(ctx, conteudo); err != nil {
		t.Fatal(err)
	}

	q := func(sql string) string {
		t.Helper()
		out, err := c.Exec(ctx, strings.ReplaceAll(sql, "$T", "'"+tenant+"'"))
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	if got := q("SELECT count() FROM genai_spans WHERE tenant_id = $T"); got != "3" {
		t.Fatalf("genai_spans tem %s linhas: o retry duplicou", got)
	}
	if got := q("SELECT countIf(tokens_entrada IS NULL) FROM genai_spans WHERE tenant_id = $T"); got != "1" {
		t.Fatalf("token não informado virou zero: %s", got)
	}
	got := q(`SELECT sum(chamadas), sum(erros), sum(com_tokens), sum(tokens_entrada), sum(estimar_entrada),
		round(sum(custo_informado_usd), 3), sum(com_custo_informado), round(quantilesTDigestMerge(0.5, 0.95, 0.99)(latencia)[2])
		FROM genai_1m WHERE tenant_id = $T FORMAT TSV`)
	// 3 chamadas, 1 erro, 2 com tokens, 1120 de entrada, 120 a estimar (a outra tem custo informado)
	if quer := "3\t1\t2\t1120\t120\t0.5\t1\t300"; got != quer {
		t.Fatalf("genai_1m = %q, quer %q", got, quer)
	}
	if got := q("SELECT texto, redigido FROM genai_conteudo WHERE tenant_id = $T FORMAT TSV"); got != "oi\t1" {
		t.Fatalf("genai_conteudo = %q", got)
	}
}

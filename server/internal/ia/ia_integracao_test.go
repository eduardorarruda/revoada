package ia

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/chquery"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// Integração contra um ClickHouse com as migrações aplicadas (REVOADA_TEST_CH_ADDR,
// REVOADA_TEST_CH_USER, REVOADA_TEST_CH_PASS). É o teste que prova o SQL de verdade:
// junção do agente, MV, quantis, custo com vigência, idempotência do runner.
//
// Cenário (um service só deste teste, para não misturar com outras rodadas):
//   - execução A, agente Meteorologista: 4 chats gpt-4o-mini (um com erro) e a
//     ferramenta clima chamada 3 vezes seguidas (loop), com conteúdo gravado;
//   - execução B, sem agente: um chat num modelo sem preço, um chat sem tokens e a
//     ferramenta busca com TimeoutError.
func TestIntegracaoClickHouse(t *testing.T) {
	addr := os.Getenv("REVOADA_TEST_CH_ADDR")
	if addr == "" {
		t.Skip("REVOADA_TEST_CH_ADDR não definido")
	}
	ch := chquery.New(addr, os.Getenv("REVOADA_TEST_CH_USER"), os.Getenv("REVOADA_TEST_CH_PASS"), "default")
	ctx := context.Background()
	service := fmt.Sprintf("teste-ia-%d", time.Now().UnixNano())
	base := time.Now().Add(-20 * time.Minute).Truncate(time.Minute)
	ta, tb := hexDe(service, "a"), hexDe(service, "b")
	inserirCenario(t, ch, service, base, ta, tb)

	loja := &lojaFalsa{precos: []store.PrecoLLM{{ID: 1, Provedor: "openai", Modelo: "gpt-4o-mini*",
		EntradaPor1M: 0.15, SaidaPor1M: 0.60, CacheLeituraPor1M: f64(0.075), VigenteDesde: jan2025, Origem: "manual"}}}
	h := New(ch, loja, nil)
	f := Filtros{De: base.Add(-time.Minute), Ate: time.Now().Add(time.Minute), Service: service}

	t.Run("resumo pelo agregado", func(t *testing.T) {
		r, err := h.Resumo(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		tt := r.Totais
		if tt.Chamadas != 6 || tt.Erros != 1 || tt.Execucoes != 2 || tt.FerramentasChamadas != 4 || tt.FerramentasErros != 1 {
			t.Fatalf("totais: %+v", tt)
		}
		if tt.CustoUSD == nil || math.Abs(*tt.CustoUSD-0.0001665) > 1e-10 || !tt.CustoParcial ||
			tt.ChamadasSemPreco != 1 || tt.SemTokens != 1 || tt.LatenciaP95Ms == nil {
			t.Fatalf("custo: %+v (custo=%v)", tt, deref(tt.CustoUSD))
		}
		if len(r.PorAgente) != 2 || r.PorAgente[0].Agente != "Meteorologista" || r.PorAgente[0].Chamadas != 4 {
			t.Fatalf("por agente: %+v", r.PorAgente)
		}
		if len(r.Ferramentas) != 2 || r.Ferramentas[0].Ferramenta != "busca" || r.Ferramentas[0].Erros != 1 {
			t.Fatalf("ferramentas: %+v", r.Ferramentas)
		}
		if len(r.PorModelo) != 3 || r.PorModelo[0].Modelo != "gpt-4o-mini-2024-07-18" || r.PorModelo[0].Preco == nil {
			t.Fatalf("por modelo: %+v", r.PorModelo)
		}
	})
	t.Run("resumo filtrado por agente usa as linhas", func(t *testing.T) {
		g := f
		g.Agente = "Meteorologista"
		r, err := h.Resumo(ctx, g)
		if err != nil {
			t.Fatal(err)
		}
		if r.Totais.Chamadas != 4 || r.Totais.CustoParcial || math.Abs(deref(r.Totais.CustoUSD)-0.0001665) > 1e-10 {
			t.Fatalf("agente: %+v", r.Totais)
		}
	})
	t.Run("execuções e replay", func(t *testing.T) {
		ex, err := h.Execucoes(ctx, FiltroExecucoes{Filtros: f, Limite: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(ex) != 2 {
			t.Fatalf("%d execuções", len(ex))
		}
		porID := map[string]Execucao{ex[0].TraceID: ex[0], ex[1].TraceID: ex[1]}
		a, b := porID[ta], porID[tb]
		if a.Agente != "Meteorologista" || a.ChamadasModelo != 4 || a.ChamadasFerramenta != 3 || a.Status != "erro" || a.CustoUSD == nil {
			t.Fatalf("execução A: %+v", a)
		}
		if b.Agente != "" || !b.CustoParcial || b.CustoUSD != nil {
			t.Fatalf("execução B: %+v", b)
		}
		so, _ := h.Execucoes(ctx, FiltroExecucoes{Filtros: f, Limite: 10, CustoMin: 0.0001})
		if len(so) != 1 || so[0].TraceID != ta {
			t.Fatalf("custo mínimo: %+v", so)
		}
		rep, err := h.Execucao(ctx, ta, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Passos) != 8 || len(rep.Repeticoes) != 1 || rep.Repeticoes[0].Vezes != 3 || !rep.Conteudo.Disponivel ||
			rep.Passos[1].Profundidade != 1 || rep.Passos[1].CustoOrigem != "estimado" || rep.Passos[1].PrecoData != "2025-01-01" {
			t.Fatalf("replay: %+v", rep)
		}
		if _, err := h.Execucao(ctx, "ffff", nil); err != ErrExecucaoNaoEncontrada {
			t.Fatalf("trace inexistente: %v", err)
		}
		ms, err := h.Conteudo(ctx, ta, nil)
		if err != nil || len(ms) != 2 || ms[0].Lado != "entrada" || ms[1].Lado != "saida" {
			t.Fatalf("conteúdo: %v %+v", err, ms)
		}
	})
	t.Run("runner grava uma vez por minuto", func(t *testing.T) {
		r := NewRunner(h, nil)
		m := base
		for range 2 {
			if err := r.FecharMinuto(ctx, m); err != nil {
				t.Fatal(err)
			}
		}
		rows, err := ch.QueryJSON(ctx, fmt.Sprintf(`SELECT metric, sum(value) AS v, count() AS n FROM metrics
			WHERE tenant_id = 'default' AND labels['service'] = '%s' GROUP BY metric ORDER BY metric`, service))
		if err != nil {
			t.Fatal(err)
		}
		got := map[string][2]float64{}
		for _, row := range rows {
			got[texto(row["metric"])] = [2]float64{numero(row["v"]), numero(row["n"])}
		}
		if got["llm.chamadas"][0] != 6 || got["llm.chamadas"][1] != 3 || got["llm.sem_preco"][0] != 1 ||
			got["llm.execucao.passos_max"][0] != 6 || math.Abs(got["llm.custo_usd"][0]-0.0001665) > 1e-10 {
			b, _ := json.Marshal(got)
			t.Fatalf("séries: %s", b)
		}
	})
	t.Run("purge do trace", func(t *testing.T) {
		stmts, msg := comandosPurge(pedidoPurge{Alvo: "trace", Valor: tb}, nil)
		if msg != "" {
			t.Fatal(msg)
		}
		for _, s := range stmts {
			if err := ch.Exec(ctx, s+" SETTINGS mutations_sync = 1"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := h.Execucao(ctx, tb, nil); err != ErrExecucaoNaoEncontrada {
			t.Fatalf("depois do purge: %v", err)
		}
	})
}

func deref(p *float64) float64 {
	if p == nil {
		return math.NaN()
	}
	return *p
}

// hexDe gera um trace_id hex estável por rodada.
func hexDe(service, sufixo string) string {
	var sb strings.Builder
	for _, c := range []byte(service + sufixo) {
		fmt.Fprintf(&sb, "%02x", c)
	}
	s := sb.String()
	return s[len(s)-32:]
}

type spanTeste struct {
	trace, span, pai, op, modelo, agente, ferramenta, erro string
	te, tsa, tcl                                           any
	seg                                                    int
}

func inserirCenario(t *testing.T, ch *chquery.Client, service string, base time.Time, ta, tb string) {
	t.Helper()
	m := "gpt-4o-mini-2024-07-18"
	spans := []spanTeste{
		{ta, "a0", "", "invoke_agent", "", "Meteorologista", "", "", nil, nil, nil, 0},
		{ta, "a1", "a0", "chat", m, "", "", "", 120, 30, 100, 1},
		{ta, "a2", "a0", "execute_tool", "", "", "clima", "", nil, nil, nil, 2},
		{ta, "a3", "a0", "chat", m, "", "", "", 200, 20, nil, 3},
		{ta, "a4", "a0", "execute_tool", "", "", "clima", "", nil, nil, nil, 4},
		{ta, "a5", "a0", "chat", m, "", "", "RateLimitError", 300, 50, nil, 5},
		{ta, "a6", "a0", "execute_tool", "", "", "clima", "", nil, nil, nil, 6},
		{ta, "a7", "a0", "chat", m, "", "", "", 100, 10, nil, 7},
		{tb, "b1", "", "chat", "modelo-sem-preco", "", "", "", 50, 5, nil, 8},
		{tb, "b2", "", "chat", "gpt-4o-mini", "", "", "", nil, nil, nil, 9},
		{tb, "b3", "", "execute_tool", "", "", "busca", "TimeoutError", nil, nil, nil, 10},
	}
	var sb strings.Builder
	for _, s := range spans {
		linha, _ := json.Marshal(map[string]any{
			"tenant_id": "default", "ts": base.Add(time.Duration(s.seg) * time.Second).UTC().Format("2006-01-02 15:04:05.000"),
			"trace_id": s.trace, "span_id": s.span, "parent_span_id": s.pai, "service": service, "host": "h1",
			"nome": s.op, "duracao_ms": 100 + s.seg*10, "convencao": "otel-genai", "operacao": s.op, "provedor": "openai",
			"modelo": s.modelo, "agente": s.agente, "ferramenta": s.ferramenta, "erro": s.erro,
			"tokens_entrada": s.te, "tokens_saida": s.tsa, "tokens_cache_leitura": s.tcl, "motivos_fim": []string{},
			"com_conteudo": boolNum(s.span == "a1"),
		})
		sb.Write(linha)
		sb.WriteByte('\n')
	}
	if err := ch.Exec(context.Background(), "INSERT INTO genai_spans FORMAT JSONEachRow\n"+sb.String()); err != nil {
		t.Fatal(err)
	}
	conteudo := ""
	for i, lado := range []string{"entrada", "saida"} {
		l, _ := json.Marshal(map[string]any{"tenant_id": "default", "ts": base.Add(time.Second).UTC().Format("2006-01-02 15:04:05.000"),
			"trace_id": ta, "span_id": "a1", "lado": lado, "papel": "user", "ordem": i, "texto": "texto " + lado, "truncado": 0, "redigido": 0})
		conteudo += string(l) + "\n"
	}
	if err := ch.Exec(context.Background(), "INSERT INTO genai_conteudo FORMAT JSONEachRow\n"+conteudo); err != nil {
		t.Fatal(err)
	}
}

func boolNum(b bool) int {
	if b {
		return 1
	}
	return 0
}

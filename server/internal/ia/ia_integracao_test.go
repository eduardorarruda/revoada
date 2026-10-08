package ia

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
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

// ---------------------------------------------------------------- utilitários de integração

func clickhouseDeTeste(t *testing.T) *chquery.Client {
	t.Helper()
	addr := os.Getenv("REVOADA_TEST_CH_ADDR")
	if addr == "" {
		t.Skip("REVOADA_TEST_CH_ADDR não definido")
	}
	return chquery.New(addr, os.Getenv("REVOADA_TEST_CH_USER"), os.Getenv("REVOADA_TEST_CH_PASS"), "default")
}

func fmtTS(ts time.Time) string { return ts.UTC().Format("2006-01-02 15:04:05.000") }

// linhaGenAI monta uma linha de genai_spans com valores neutros (chat, sem tokens, sem
// custo); extra sobrescreve as colunas do caso.
func linhaGenAI(service string, ts time.Time, trace, span string, extra map[string]any) map[string]any {
	l := map[string]any{"tenant_id": "default", "ts": fmtTS(ts), "trace_id": trace, "span_id": span,
		"parent_span_id": "", "service": service, "host": "h1", "nome": "chat", "duracao_ms": 100,
		"convencao": "otel-genai", "operacao": "chat", "provedor": "openai", "modelo": "", "agente": "",
		"conversa_id": "", "ferramenta": "", "erro": "", "tokens_entrada": nil, "tokens_saida": nil,
		"tokens_cache_leitura": nil, "custo_informado_usd": nil, "motivos_fim": []string{}, "com_conteudo": 0}
	for k, v := range extra {
		l[k] = v
	}
	return l
}

func linhaConteudo(ts time.Time, trace, span string) map[string]any {
	return map[string]any{"tenant_id": "default", "ts": fmtTS(ts), "trace_id": trace, "span_id": span,
		"lado": "entrada", "papel": "user", "ordem": 0, "texto": "dado pessoal de " + trace, "truncado": 0, "redigido": 0}
}

func inserirJSON(t *testing.T, ch *chquery.Client, tabela string, linhas []map[string]any) {
	t.Helper()
	var sb strings.Builder
	for _, l := range linhas {
		b, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if err := ch.Exec(context.Background(), "INSERT INTO "+tabela+" FORMAT JSONEachRow\n"+sb.String()); err != nil {
		t.Fatal(err)
	}
}

func contar(t *testing.T, ch *chquery.Client, sql string) int64 {
	t.Helper()
	rows, err := ch.QueryJSON(context.Background(), sql)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		return 0
	}
	return inteiro(rows[0]["n"])
}

// esperarContagem espera a mutation assíncrona do ClickHouse (o purge responde 202
// antes de as linhas sumirem) até a contagem chegar ao valor esperado.
func esperarContagem(t *testing.T, ch *chquery.Client, sql string, quer int64) {
	t.Helper()
	limite := time.Now().Add(30 * time.Second)
	for {
		n := contar(t, ch, sql)
		if n == quer {
			return
		}
		if time.Now().After(limite) {
			t.Fatalf("depois de 30 s, %d linhas (quer %d): %s", n, quer, sql)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func listaSQL(ids ...string) string {
	q := make([]string, len(ids))
	for i, id := range ids {
		q[i] = quote(id)
	}
	return strings.Join(q, ",")
}

func purgar(h *Handler, corpo string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.PurgeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/ia/purge", strings.NewReader(corpo)))
	return rec
}

// ---------------------------------------------------------------- purge de ponta a ponta

// TestIntegracaoPurge executa o PurgeHTTP de verdade (pedido de titular de dado, LGPD):
// apagar a conversa tira as chamadas E o conteúdo de TODOS os traces dela e deixa os
// outros; apagar o trace pelo handler faz o mesmo para um trace só.
func TestIntegracaoPurge(t *testing.T) {
	ch := clickhouseDeTeste(t)
	service := fmt.Sprintf("teste-ia-purge-%d", time.Now().UnixNano())
	conversa := "conv-" + service
	base := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	t1, t2, t3 := hexDe(service, "p1"), hexDe(service, "p2"), hexDe(service, "p3")
	inserirJSON(t, ch, "genai_spans", []map[string]any{
		linhaGenAI(service, base, t1, "s1", map[string]any{"conversa_id": conversa, "com_conteudo": 1}),
		linhaGenAI(service, base.Add(time.Second), t1, "s2", map[string]any{"conversa_id": conversa}),
		linhaGenAI(service, base.Add(2*time.Second), t2, "s3", map[string]any{"conversa_id": conversa, "com_conteudo": 1}),
		linhaGenAI(service, base.Add(3*time.Second), t3, "s4", map[string]any{"conversa_id": "outra-" + service, "com_conteudo": 1}),
	})
	inserirJSON(t, ch, "genai_conteudo", []map[string]any{
		linhaConteudo(base, t1, "s1"), linhaConteudo(base.Add(2*time.Second), t2, "s3"), linhaConteudo(base.Add(3*time.Second), t3, "s4"),
	})
	h := New(ch, &lojaFalsa{}, nil)
	spansDe := func(ids ...string) string {
		return "SELECT count() AS n FROM genai_spans WHERE tenant_id = 'default' AND trace_id IN (" + listaSQL(ids...) + ")"
	}
	conteudoDe := func(ids ...string) string {
		return "SELECT count() AS n FROM genai_conteudo WHERE tenant_id = 'default' AND trace_id IN (" + listaSQL(ids...) + ")"
	}
	if contar(t, ch, spansDe(t1, t2, t3)) != 4 || contar(t, ch, conteudoDe(t1, t2, t3)) != 3 {
		t.Fatal("cenário não foi gravado")
	}

	t.Run("conversa inexistente explica e não apaga nada", func(t *testing.T) {
		rec := purgar(h, `{"alvo":"conversa","valor":"nao-existe-`+service+`"}`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "nenhuma execução") {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	})
	t.Run("conversa apaga spans e conteúdo de todos os traces dela", func(t *testing.T) {
		rec := purgar(h, `{"alvo":"conversa","valor":"`+conversa+`"}`)
		if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"comandos":2`) {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		esperarContagem(t, ch, spansDe(t1, t2), 0)
		esperarContagem(t, ch, conteudoDe(t1, t2), 0)
		if contar(t, ch, spansDe(t3)) != 1 || contar(t, ch, conteudoDe(t3)) != 1 {
			t.Fatal("o purge da conversa apagou um trace de outra conversa")
		}
	})
	t.Run("trace pelo handler", func(t *testing.T) {
		if rec := purgar(h, `{"alvo":"trace","valor":"x' OR 1=1 --"}`); rec.Code != http.StatusBadRequest {
			t.Fatalf("trace inválido: %d", rec.Code)
		}
		rec := purgar(h, `{"alvo":"trace","valor":"`+t3+`"}`)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		esperarContagem(t, ch, spansDe(t3), 0)
		esperarContagem(t, ch, conteudoDe(t3), 0)
	})
}

// ---------------------------------------------------------------- custo: origem e vigência

// TestIntegracaoCustoPorOrigem: um proxy informa o custo SEM tokens numa chamada e
// tokens SEM custo noutra, do mesmo modelo e no mesmo minuto. As contagens sem_tok e
// estimaveis vêm do SQL linha a linha (no agregado, da MV); deduzi-las no Go das outras
// contagens deixaria a chamada com tokens sem estimativa. Os dois caminhos (genai_1m e
// as linhas, com filtro de agente) têm de dar exatamente o mesmo número.
func TestIntegracaoCustoPorOrigem(t *testing.T) {
	ch := clickhouseDeTeste(t)
	ctx := context.Background()
	service := fmt.Sprintf("teste-ia-proxy-%d", time.Now().UnixNano())
	base := time.Now().Add(-20 * time.Minute).Truncate(time.Minute)
	tr := hexDe(service, "px")
	const comPreco, semPreco = "proxy-modelo", "proxy-sem-preco"
	inserirJSON(t, ch, "genai_spans", []map[string]any{
		linhaGenAI(service, base, tr, "a0", map[string]any{"operacao": "invoke_agent", "agente": "Proxy", "nome": "agente"}),
		// c1: custo informado, sem tokens
		linhaGenAI(service, base.Add(time.Second), tr, "c1", map[string]any{"parent_span_id": "a0", "modelo": comPreco, "custo_informado_usd": 0.3}),
		// c2: tokens, sem custo → estimado pelo preço (1 US$/1M de entrada)
		linhaGenAI(service, base.Add(2*time.Second), tr, "c2", map[string]any{"parent_span_id": "a0", "modelo": comPreco,
			"tokens_entrada": 1_000_000, "tokens_saida": 0}),
		// c3: tokens, modelo sem preço
		linhaGenAI(service, base.Add(3*time.Second), tr, "c3", map[string]any{"parent_span_id": "a0", "modelo": semPreco,
			"tokens_entrada": 500, "tokens_saida": 50}),
		// c4: nem tokens nem custo
		linhaGenAI(service, base.Add(4*time.Second), tr, "c4", map[string]any{"parent_span_id": "a0", "modelo": comPreco}),
		// c5: tokens E custo informado → vale o informado
		linhaGenAI(service, base.Add(5*time.Second), tr, "c5", map[string]any{"parent_span_id": "a0", "modelo": comPreco,
			"tokens_entrada": 1000, "tokens_saida": 100, "custo_informado_usd": 0.2}),
	})
	loja := &lojaFalsa{precos: []store.PrecoLLM{{ID: 1, Provedor: "openai", Modelo: comPreco, EntradaPor1M: 1, SaidaPor1M: 0,
		VigenteDesde: jan2025, Origem: "manual"}}}
	h := New(ch, loja, nil)
	f := Filtros{De: base.Add(-time.Minute), Ate: time.Now().Add(time.Minute), Service: service}
	const custoTotal = 0.3 + 1.0 + 0.2

	for _, agente := range []string{"", "Proxy"} {
		nome := "pelo agregado (genai_1m)"
		if agente != "" {
			nome = "pelas linhas (filtro de agente)"
		}
		t.Run(nome, func(t *testing.T) {
			g := f
			g.Agente = agente
			r, err := h.Resumo(ctx, g)
			if err != nil {
				t.Fatal(err)
			}
			tt := r.Totais
			if tt.Chamadas != 5 || tt.SemTokens != 1 || tt.ChamadasSemPreco != 1 || !tt.CustoParcial ||
				math.Abs(deref(tt.CustoUSD)-custoTotal) > 1e-9 || math.Abs(tt.CustoInformadoUSD-0.5) > 1e-9 ||
				tt.TokensEntrada != 1_001_500 {
				t.Fatalf("totais: %+v (custo=%v)", tt, deref(tt.CustoUSD))
			}
			modelos := map[string]PorModelo{}
			for _, m := range r.PorModelo {
				modelos[m.Modelo] = m
			}
			if m := modelos[comPreco]; m.SemPreco || math.Abs(deref(m.CustoUSD)-custoTotal) > 1e-9 || m.Chamadas != 4 {
				t.Fatalf("modelo com preço: %+v (custo=%v)", m, deref(m.CustoUSD))
			}
			if m := modelos[semPreco]; !m.SemPreco || m.CustoUSD != nil {
				t.Fatalf("modelo sem preço: %+v", m)
			}
		})
	}
	t.Run("lista de execuções", func(t *testing.T) {
		ex, err := h.Execucoes(ctx, FiltroExecucoes{Filtros: f, Limite: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(ex) != 1 || ex[0].Agente != "Proxy" || ex[0].ChamadasModelo != 5 || !ex[0].CustoParcial ||
			math.Abs(deref(ex[0].CustoUSD)-custoTotal) > 1e-9 {
			t.Fatalf("execuções: %+v", ex)
		}
	})
	t.Run("replay marca a origem de cada custo", func(t *testing.T) {
		rep, err := h.Execucao(ctx, tr, nil)
		if err != nil {
			t.Fatal(err)
		}
		quer := []struct {
			origem string
			usd    *float64
			data   string
		}{
			{"", nil, ""}, {"informado", f64(0.3), ""}, {"estimado", f64(1.0), "2025-01-01"},
			{"sem_preco", nil, ""}, {"sem_tokens", nil, ""}, {"informado", f64(0.2), ""},
		}
		if len(rep.Passos) != len(quer) {
			t.Fatalf("%d passos", len(rep.Passos))
		}
		for i, q := range quer {
			p := rep.Passos[i]
			if p.CustoOrigem != q.origem || p.PrecoData != q.data || (p.CustoUSD == nil) != (q.usd == nil) ||
				(q.usd != nil && math.Abs(*p.CustoUSD-*q.usd) > 1e-9) {
				t.Fatalf("passo %s: origem=%q usd=%v data=%q, quer %+v", p.SpanID, p.CustoOrigem, deref(p.CustoUSD), p.PrecoData, q)
			}
		}
		if !rep.Totais.CustoParcial || math.Abs(deref(rep.Totais.CustoUSD)-custoTotal) > 1e-9 || rep.Agente != "Proxy" {
			t.Fatalf("totais do replay: %+v", rep.Totais)
		}
	})
}

// TestIntegracaoPrecoMudaNoMeioDaJanela: o preço mudou no meio da janela consultada.
// Cada intervalo (no resumo) e cada execução (na lista e no replay) usa o preço vigente
// NAQUELE momento — nem o de hoje para tudo, nem o antigo para tudo.
func TestIntegracaoPrecoMudaNoMeioDaJanela(t *testing.T) {
	ch := clickhouseDeTeste(t)
	ctx := context.Background()
	service := fmt.Sprintf("teste-ia-vigencia-%d", time.Now().UnixNano())
	base := time.Now().Add(-2 * time.Hour).Truncate(time.Minute)
	troca := base.Add(time.Hour)
	antes, depois := hexDe(service, "va"), hexDe(service, "vd")
	const modelo = "vig-modelo"
	var linhas []map[string]any
	for _, x := range []struct {
		trace string
		ts    time.Time
	}{{antes, troca.Add(-10 * time.Minute)}, {depois, troca.Add(10 * time.Minute)}} {
		linhas = append(linhas,
			linhaGenAI(service, x.ts, x.trace, "ag", map[string]any{"operacao": "invoke_agent", "agente": "Vig", "nome": "agente"}),
			linhaGenAI(service, x.ts.Add(time.Second), x.trace, "ch", map[string]any{"parent_span_id": "ag", "modelo": modelo,
				"tokens_entrada": 1_000_000, "tokens_saida": 0}))
	}
	inserirJSON(t, ch, "genai_spans", linhas)
	loja := &lojaFalsa{precos: []store.PrecoLLM{
		{ID: 1, Provedor: "openai", Modelo: modelo, EntradaPor1M: 1, VigenteDesde: jan2025, Origem: "manual"},
		{ID: 2, Provedor: "openai", Modelo: modelo, EntradaPor1M: 10, VigenteDesde: troca, Origem: "manual"},
	}}
	h := New(ch, loja, nil)
	f := Filtros{De: base.Add(-time.Minute), Ate: time.Now().Add(time.Minute), Service: service}

	for _, agente := range []string{"", "Vig"} {
		t.Run("resumo agente="+agente, func(t *testing.T) {
			g := f
			g.Agente = agente
			r, err := h.Resumo(ctx, g)
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(deref(r.Totais.CustoUSD)-11) > 1e-9 || r.Totais.CustoParcial {
				t.Fatalf("total = %v, quer 11 (1 antes da troca + 10 depois): %+v", deref(r.Totais.CustoUSD), r.Totais)
			}
			if len(r.PorModelo) != 1 || math.Abs(deref(r.PorModelo[0].CustoUSD)-11) > 1e-9 ||
				r.PorModelo[0].Preco == nil || r.PorModelo[0].Preco.EntradaPor1M != 10 {
				t.Fatalf("por modelo: %+v", r.PorModelo)
			}
			if len(r.PorAgente) != 1 || r.PorAgente[0].Agente != "Vig" || math.Abs(deref(r.PorAgente[0].CustoUSD)-11) > 1e-9 {
				t.Fatalf("por agente: %+v", r.PorAgente)
			}
		})
	}
	t.Run("execuções e replay", func(t *testing.T) {
		ex, err := h.Execucoes(ctx, FiltroExecucoes{Filtros: f, Limite: 10})
		if err != nil {
			t.Fatal(err)
		}
		custos := map[string]float64{}
		for _, e := range ex {
			custos[e.TraceID] = deref(e.CustoUSD)
		}
		if len(ex) != 2 || math.Abs(custos[antes]-1) > 1e-9 || math.Abs(custos[depois]-10) > 1e-9 {
			t.Fatalf("custos por execução: %v", custos)
		}
		for trace, quer := range map[string]struct {
			usd  float64
			data string
		}{antes: {1, "2025-01-01"}, depois: {10, troca.UTC().Format("2006-01-02")}} {
			rep, err := h.Execucao(ctx, trace, nil)
			if err != nil {
				t.Fatal(err)
			}
			p := rep.Passos[1]
			if math.Abs(deref(p.CustoUSD)-quer.usd) > 1e-9 || p.PrecoData != quer.data {
				t.Fatalf("replay %s: usd=%v data=%q, quer %+v", trace, deref(p.CustoUSD), p.PrecoData, quer)
			}
		}
	})
}

// ---------------------------------------------------------------- por último: esvaziar o conteúdo

// TestIntegracaoPurgeTodoConteudo roda por último entre os testes do pacote que usam o
// ClickHouse: o alvo todo_conteudo esvazia a tabela genai_conteudo INTEIRA, de qualquer
// teste. REVOADA_TEST_CH_ADDR aponta, por contrato, para um ClickHouse descartável.
// Frase errada não apaga nada; a frase certa esvazia genai_conteudo e deixa
// genai_spans (custo e tokens seguem).
func TestIntegracaoPurgeTodoConteudo(t *testing.T) {
	ch := clickhouseDeTeste(t)
	service := fmt.Sprintf("teste-ia-todo-conteudo-%d", time.Now().UnixNano())
	tr := hexDe(service, "tc")
	agora := time.Now().Add(-time.Minute)
	inserirJSON(t, ch, "genai_spans", []map[string]any{linhaGenAI(service, agora, tr, "s1", map[string]any{"com_conteudo": 1})})
	inserirJSON(t, ch, "genai_conteudo", []map[string]any{linhaConteudo(agora, tr, "s1")})
	h := New(ch, &lojaFalsa{}, nil)
	doTrace := "SELECT count() AS n FROM genai_conteudo WHERE trace_id = " + quote(tr)

	rec := purgar(h, `{"alvo":"todo_conteudo","frase":"apagar tudo"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), FraseApagarConteudo) {
		t.Fatalf("frase errada: %d %s", rec.Code, rec.Body)
	}
	if contar(t, ch, doTrace) != 1 {
		t.Fatal("frase errada apagou conteúdo")
	}
	rec = purgar(h, `{"alvo":"todo_conteudo","frase":"`+FraseApagarConteudo+`"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("frase certa: %d %s", rec.Code, rec.Body)
	}
	esperarContagem(t, ch, "SELECT count() AS n FROM genai_conteudo", 0)
	if contar(t, ch, "SELECT count() AS n FROM genai_spans WHERE trace_id = "+quote(tr)) != 1 {
		t.Fatal("todo_conteudo não pode apagar as chamadas (custo e tokens seguem)")
	}
}

// Troca de preço no MEIO de um intervalo do gráfico. Numa janela de 24 h o intervalo é
// de 15 min; as duas chamadas caem no mesmo intervalo, uma antes e outra depois da
// troca. Sem partir o intervalo na fronteira de vigência, as duas seriam cobradas pelo
// preço antigo (2 em vez de 11).
func TestIntegracaoTrocaDePrecoNoMeioDoIntervalo(t *testing.T) {
	ch := clickhouseDeTeste(t)
	ctx := context.Background()
	service := fmt.Sprintf("teste-ia-trecho-%d", time.Now().UnixNano())
	troca := time.Now().Add(-2 * time.Hour).Truncate(15 * time.Minute).Add(7 * time.Minute)
	const modelo = "trecho-modelo"
	var linhas []map[string]any
	for i, ts := range []time.Time{troca.Add(-3 * time.Minute), troca.Add(3 * time.Minute)} {
		tr := hexDe(service, fmt.Sprintf("t%d", i))
		linhas = append(linhas,
			linhaGenAI(service, ts, tr, "ag", map[string]any{"operacao": "invoke_agent", "agente": "Trecho", "nome": "agente"}),
			linhaGenAI(service, ts.Add(time.Second), tr, "ch", map[string]any{"parent_span_id": "ag", "modelo": modelo,
				"tokens_entrada": 1_000_000, "tokens_saida": 0}))
	}
	inserirJSON(t, ch, "genai_spans", linhas)
	loja := &lojaFalsa{precos: []store.PrecoLLM{
		{ID: 1, Provedor: "openai", Modelo: modelo, EntradaPor1M: 1, VigenteDesde: jan2025, Origem: "manual"},
		{ID: 2, Provedor: "openai", Modelo: modelo, EntradaPor1M: 10, VigenteDesde: troca, Origem: "manual"},
	}}
	h := New(ch, loja, nil)
	f := Filtros{De: time.Now().Add(-24 * time.Hour), Ate: time.Now(), Service: service}
	if p := escolherPasso(f.Ate.Sub(f.De)); p != 900 {
		t.Fatalf("o teste supõe intervalos de 15 min, veio %d s", p)
	}
	for _, agente := range []string{"", "Trecho"} { // agregado por minuto e linhas
		g := f
		g.Agente = agente
		r, err := h.Resumo(ctx, g)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(deref(r.Totais.CustoUSD)-11) > 1e-9 {
			t.Fatalf("agente=%q: total = %v, quer 11 (1 antes + 10 depois da troca)", agente, deref(r.Totais.CustoUSD))
		}
		if len(r.PorAgente) != 1 || math.Abs(deref(r.PorAgente[0].CustoUSD)-11) > 1e-9 {
			t.Fatalf("agente=%q: por agente = %+v", agente, r.PorAgente)
		}
		var serie float64
		for _, p := range r.Serie {
			serie += p.CustoUSD
		}
		if math.Abs(serie-11) > 1e-9 {
			t.Fatalf("agente=%q: soma da série = %v", agente, serie)
		}
	}
}

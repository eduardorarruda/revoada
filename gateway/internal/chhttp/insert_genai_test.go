package chhttp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/core/genai"
	"github.com/eduardorarruda/revoada/gateway/internal/model"
)

// pedidoCapturado é o que o ClickHouse falso recebeu.
type pedidoCapturado struct {
	query  url.Values
	user   string
	linhas []map[string]any
}

// clickhouseFalso captura o INSERT (query string, cabeçalhos e as linhas JSONEachRow).
func clickhouseFalso(t *testing.T, status int) (*Client, *[]pedidoCapturado) {
	t.Helper()
	var mu sync.Mutex
	var pedidos []pedidoCapturado
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		corpo, _ := io.ReadAll(r.Body)
		p := pedidoCapturado{query: r.URL.Query(), user: r.Header.Get("X-ClickHouse-User")}
		sc := bufio.NewScanner(bytes.NewReader(corpo))
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var l map[string]any
			if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
				t.Errorf("linha não é JSON: %q", sc.Text())
			}
			p.linhas = append(p.linhas, l)
		}
		mu.Lock()
		pedidos = append(pedidos, p)
		mu.Unlock()
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = w.Write([]byte("Code: 27. Cannot parse input"))
		}
	}))
	t.Cleanup(srv.Close)
	return New(Config{Addr: srv.URL, User: "u", Pass: "p", DB: "revoada"}), &pedidos
}

// TestInsertGenAISpansJSON: o formato que o ClickHouse recebe. Token não informado vai
// como null (Nullable: "não informado", nunca zero), lista vazia como [] (Array não
// aceita null), booleanos como 0/1 (UInt8) e o INSERT de genai_spans leva a
// deduplicação também na MV (senão o retry conta o custo duas vezes em genai_1m).
func TestInsertGenAISpansJSON(t *testing.T) {
	c, pedidos := clickhouseFalso(t, http.StatusOK)
	n := func(v int64) *int64 { return &v }
	custo := 0.25
	ts := time.Date(2026, 10, 7, 12, 0, 30, 123_000_000, time.FixedZone("BRT", -3*3600))
	linhas := []model.GenAISpan{
		{TenantID: "default", TS: ts, TraceID: "t1", SpanID: "s1", Host: "h1", Nome: "chat", DuracaoMs: 12.5,
			Chamada: genai.Chamada{Operacao: "chat", Modelo: "gpt-4o-mini", MotivosFim: nil}},
		{TenantID: "default", TS: ts, TraceID: "t1", SpanID: "s2", ComConteudo: true,
			Chamada: genai.Chamada{Operacao: "chat", TokensEntrada: n(120), TokensSaida: n(0), CustoInformadoUSD: &custo,
				MotivosFim: []string{"stop"}}},
	}
	if err := c.InsertGenAISpans(context.Background(), linhas); err != nil {
		t.Fatal(err)
	}
	if len(*pedidos) != 1 {
		t.Fatalf("%d pedidos", len(*pedidos))
	}
	p := (*pedidos)[0]
	if p.query.Get("deduplicate_blocks_in_dependent_materialized_views") != "1" {
		t.Fatalf("faltou a deduplicação na MV: %v", p.query)
	}
	if p.query.Get("query") != "INSERT INTO genai_spans FORMAT JSONEachRow" || p.query.Get("database") != "revoada" || p.user != "u" {
		t.Fatalf("query/database/usuário: %v %q", p.query, p.user)
	}
	if len(p.linhas) != 2 {
		t.Fatalf("%d linhas", len(p.linhas))
	}
	sem, com := p.linhas[0], p.linhas[1]
	casos := []struct {
		nome string
		got  any
		quer any
	}{
		{"tokens_entrada nulo", sem["tokens_entrada"], nil},
		{"tokens_saida nulo", sem["tokens_saida"], nil},
		{"tokens_cache_leitura nulo", sem["tokens_cache_leitura"], nil},
		{"tokens_cache_escrita nulo", sem["tokens_cache_escrita"], nil},
		{"custo nulo", sem["custo_informado_usd"], nil},
		{"com_conteudo 0", sem["com_conteudo"], float64(0)},
		{"ts em UTC com milissegundos", sem["ts"], "2026-10-07 15:00:30.123"},
		{"tokens_entrada informado", com["tokens_entrada"], float64(120)},
		{"tokens_saida zero informado não é nulo", com["tokens_saida"], float64(0)},
		{"custo informado", com["custo_informado_usd"], 0.25},
		{"com_conteudo 1", com["com_conteudo"], float64(1)},
	}
	for _, k := range casos {
		if k.got != k.quer {
			t.Errorf("%s: %#v, quer %#v", k.nome, k.got, k.quer)
		}
	}
	if m, ok := sem["motivos_fim"].([]any); !ok || len(m) != 0 {
		t.Errorf("motivos_fim nil tem de ir como [], veio %#v", sem["motivos_fim"])
	}
	if m, ok := com["motivos_fim"].([]any); !ok || len(m) != 1 || m[0] != "stop" {
		t.Errorf("motivos_fim = %#v", com["motivos_fim"])
	}
	if _, tem := sem["tokens_entrada"]; !tem {
		t.Error("a chave tokens_entrada tem de existir (null), não sumir")
	}
}

func TestInsertGenAIConteudoJSON(t *testing.T) {
	c, pedidos := clickhouseFalso(t, http.StatusOK)
	ts := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	rows := []model.GenAIConteudo{
		{TenantID: "default", TS: ts, TraceID: "t1", SpanID: "s1", Lado: "entrada", Papel: "user", Ordem: 0, Texto: "oi"},
		{TenantID: "default", TS: ts, TraceID: "t1", SpanID: "s1", Lado: "saida", Papel: "assistant", Ordem: 1, Texto: "«redigido»",
			Truncado: true, Redigido: true},
	}
	if err := c.InsertGenAIConteudo(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	p := (*pedidos)[0]
	if p.query.Get("query") != "INSERT INTO genai_conteudo FORMAT JSONEachRow" {
		t.Fatalf("query: %v", p.query)
	}
	// genai_conteudo não tem MV dependente: o parâmetro não é necessário ali
	if p.query.Has("deduplicate_blocks_in_dependent_materialized_views") {
		t.Fatalf("parâmetro inesperado no conteúdo: %v", p.query)
	}
	if len(p.linhas) != 2 {
		t.Fatalf("%d linhas", len(p.linhas))
	}
	a, b := p.linhas[0], p.linhas[1]
	if a["truncado"] != float64(0) || a["redigido"] != float64(0) || b["truncado"] != float64(1) || b["redigido"] != float64(1) {
		t.Fatalf("booleanos: %v / %v", a, b)
	}
	if a["ordem"] != float64(0) || b["ordem"] != float64(1) || b["texto"] != "«redigido»" || b["lado"] != "saida" {
		t.Fatalf("campos: %v", b)
	}
}

// TestInsertGenAIVazioEErro: lote vazio não faz pedido; resposta não-200 vira erro com
// a mensagem do ClickHouse (o batcher decide se reenvia).
func TestInsertGenAIVazioEErro(t *testing.T) {
	c, pedidos := clickhouseFalso(t, http.StatusOK)
	if err := c.InsertGenAISpans(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := c.InsertGenAIConteudo(context.Background(), []model.GenAIConteudo{}); err != nil {
		t.Fatal(err)
	}
	if len(*pedidos) != 0 {
		t.Fatalf("lote vazio fez %d pedido(s)", len(*pedidos))
	}
	ruim, _ := clickhouseFalso(t, http.StatusBadRequest)
	err := ruim.InsertGenAISpans(context.Background(), []model.GenAISpan{{TraceID: "t"}})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("Cannot parse input")) {
		t.Fatalf("erro do ClickHouse: %v", err)
	}
	err = ruim.InsertGenAIConteudo(context.Background(), []model.GenAIConteudo{{TraceID: "t"}})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("genai_conteudo 400")) {
		t.Fatalf("erro do conteúdo: %v", err)
	}
}

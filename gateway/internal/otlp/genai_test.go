package otlp

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/eduardorarruda/revoada/core/genai"
	"github.com/eduardorarruda/revoada/core/redacao"
	"github.com/eduardorarruda/revoada/gateway/internal/model"
	cmnpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// fixtureSpans lê os .pb gravados de uma biblioteca real (tests/genai-fixtures) e
// devolve os spans convertidos, ordenados pelo nome do arquivo (a ordem de envio).
func fixtureSpans(t *testing.T, prefixo string) []model.Span {
	t.Helper()
	arqs, err := filepath.Glob(filepath.Join("testdata", "genai", prefixo+"-[0-9][0-9].pb"))
	if err != nil || len(arqs) == 0 {
		t.Fatalf("fixtures %s: %v (%d arquivos)", prefixo, err, len(arqs))
	}
	sort.Strings(arqs)
	var out []model.Span
	for _, a := range arqs {
		b, err := os.ReadFile(a)
		if err != nil {
			t.Fatal(err)
		}
		req, err := parseTraceRequest(b, false)
		if err != nil {
			t.Fatalf("%s: %v", a, err)
		}
		out = append(out, FromResourceSpans("default", req.GetResourceSpans())...)
	}
	return out
}

func chamadas(spans []model.Span) []genai.Chamada {
	var out []genai.Chamada
	for _, s := range spans {
		if s.GenAI != nil {
			out = append(out, *s.GenAI)
		}
	}
	return out
}

func tokens(p *int64) int64 {
	if p == nil {
		return -1
	}
	return *p
}

// TestFixturesReais é a prova de que as três bibliotecas são entendidas: cada chamada
// gravada vira exatamente as colunas esperadas.
func TestFixturesReais(t *testing.T) {
	t.Run("instrumentação oficial do OpenTelemetry", func(t *testing.T) {
		cs := chamadas(fixtureSpans(t, "otel-oficial"))
		var ops []string
		for _, c := range cs {
			ops = append(ops, c.Operacao)
		}
		quer := []string{"chat", "execute_tool", "chat", "invoke_agent", "chat"}
		if strings.Join(ops, ",") != strings.Join(quer, ",") {
			t.Fatalf("operações = %v, quer %v", ops, quer)
		}
		chat := cs[0]
		if chat.Convencao != genai.ConvOTel || chat.Provedor != "openai" || chat.Modelo != "gpt-4o-mini-2024-07-18" ||
			tokens(chat.TokensEntrada) != 120 || tokens(chat.TokensSaida) != 30 || chat.MotivosFim[0] != "tool_calls" {
			t.Fatalf("chat: %+v", chat)
		}
		if len(chat.Mensagens) != 2 || chat.Mensagens[1].Texto != `→ clima({"cidade":"Recife"})` {
			t.Fatalf("mensagens do chat: %+v", chat.Mensagens)
		}
		if cs[1].Ferramenta != "clima" || cs[1].ChamadaID != "call_1" {
			t.Fatalf("ferramenta: %+v", cs[1])
		}
		if cs[3].Agente != "Meteorologista" || cs[3].ConversaID != "conversa-abc" {
			t.Fatalf("agente: %+v", cs[3])
		}
		if cs[4].Erro != "InternalServerError" || cs[4].TokensEntrada != nil {
			t.Fatalf("erro: %+v", cs[4])
		}
	})
	t.Run("modo EVENT_ONLY não traz conteúdo no trace", func(t *testing.T) {
		for _, c := range chamadas(fixtureSpans(t, "otel-eventos")) {
			if len(c.Mensagens) != 0 {
				t.Fatalf("EVENT_ONLY trouxe mensagens no span: %+v", c.Mensagens)
			}
		}
	})
	t.Run("OpenLLMetry com OpenAI e Anthropic", func(t *testing.T) {
		cs := chamadas(fixtureSpans(t, "openllmetry"))
		if len(cs) != 3 {
			t.Fatalf("%d chamadas", len(cs))
		}
		oa, an := cs[0], cs[2]
		if tokens(oa.TokensCacheLeitura) != 100 || tokens(oa.TokensEntrada) != 120 {
			t.Fatalf("cache OpenAI: %+v", oa)
		}
		// A Anthropic respondeu 20 novos + 100 lidos + 50 escritos; o OpenLLMetry já soma.
		if an.Provedor != "anthropic" || tokens(an.TokensEntrada) != 170 ||
			tokens(an.TokensCacheLeitura) != 100 || tokens(an.TokensCacheEscrita) != 50 {
			t.Fatalf("anthropic: %+v", an)
		}
	})
	t.Run("OpenInference", func(t *testing.T) {
		cs := chamadas(fixtureSpans(t, "openinference"))
		if len(cs) != 2 {
			t.Fatalf("%d chamadas", len(cs))
		}
		final := cs[1]
		if final.Convencao != genai.ConvOpenInference || final.Operacao != genai.OpChat ||
			tokens(final.TokensEntrada) != 120 || final.MotivosFim[0] != "stop" || len(final.Mensagens) != 5 {
			t.Fatalf("openinference: %+v", final)
		}
	})
}

// Vercel AI SDK: a integração nova (@ai-sdk/otel) emite a semconv; a antiga, ai.*.
func TestFixturesVercel(t *testing.T) {
	t.Run("integração nova", func(t *testing.T) {
		cs := chamadas(fixtureSpans(t, "vercel"))
		var ops []string
		for _, c := range cs {
			ops = append(ops, c.Operacao)
		}
		if strings.Join(ops, ",") != "chat,execute_tool,chat,agent_step,agent_step,invoke_agent" {
			t.Fatalf("operações: %v", ops)
		}
		if cs[5].Agente != "Meteorologista" || cs[1].Ferramenta != "clima" || tokens(cs[0].TokensCacheLeitura) != 100 {
			t.Fatalf("chamadas: %+v", cs)
		}
	})
	t.Run("integração antiga (ai.*)", func(t *testing.T) {
		spans := fixtureSpans(t, "vercel-legado")
		cs := chamadas(spans)
		if len(cs) != 4 {
			t.Fatalf("%d chamadas reconhecidas", len(cs))
		}
		ferr, chat, ag := cs[0], cs[1], cs[3]
		if ferr.Convencao != genai.ConvVercel || ferr.Operacao != genai.OpFerramenta || ferr.Ferramenta != "clima" ||
			ferr.ChamadaID != "call_1" || len(ferr.Mensagens) != 2 {
			t.Fatalf("ferramenta: %+v", ferr)
		}
		if chat.Operacao != genai.OpChat || chat.Provedor != "openai" || tokens(chat.TokensEntrada) != 120 ||
			tokens(chat.TokensCacheLeitura) != 100 || chat.MotivosFim[0] != "tool-calls" || len(chat.Mensagens) != 2 {
			t.Fatalf("chat: %+v", chat)
		}
		if ag.Operacao != genai.OpAgente || ag.Agente != "Meteorologista" {
			t.Fatalf("agente: %+v", ag)
		}
		for _, s := range spans {
			for k := range s.Labels {
				if strings.HasPrefix(k, "ai.prompt") || strings.HasPrefix(k, "ai.response.text") || strings.HasPrefix(k, "ai.toolCall.args") {
					t.Fatalf("conteúdo do Vercel ficou no rótulo %s", k)
				}
			}
		}
	})
}

// O conteúdo nunca sobra nos rótulos do span: com o modo desligado, o prompt não pode
// ir parar em spans.labels por outro caminho.
func TestConteudoSaiDosRotulos(t *testing.T) {
	for _, pref := range []string{"otel-oficial", "openllmetry", "openinference", "vercel", "vercel-legado"} {
		for _, s := range fixtureSpans(t, pref) {
			if s.GenAI == nil {
				continue
			}
			for k, v := range s.Labels {
				if genai.EhChaveDeConteudo(k) || strings.Contains(v, "Recife?") {
					t.Fatalf("%s: conteúdo ficou no rótulo %s=%q", pref, k, v)
				}
			}
			if len(s.Labels) > maxLabelsPerPoint {
				t.Fatalf("%s: %d rótulos, acima do teto", pref, len(s.Labels))
			}
		}
	}
}

func TestListaViraJSONEmSpan(t *testing.T) {
	spans := fixtureSpans(t, "otel-oficial")
	if got := spans[0].Labels["gen_ai.response.finish_reasons"]; got != `["tool_calls"]` {
		t.Fatalf("finish_reasons = %q, quer a lista em JSON", got)
	}
	v := &cmnpb.AnyValue{Value: &cmnpb.AnyValue_KvlistValue{KvlistValue: &cmnpb.KeyValueList{Values: []*cmnpb.KeyValue{
		{Key: "a", Value: &cmnpb.AnyValue{Value: &cmnpb.AnyValue_IntValue{IntValue: 1}}},
		{Key: "b", Value: &cmnpb.AnyValue{Value: &cmnpb.AnyValue_BytesValue{BytesValue: []byte("oi")}}},
	}}}}
	if got := anyValueToText(v); got != `{"a":1,"b":"b2k="}` {
		t.Fatalf("mapa = %q", got)
	}
	// Métrica continua como antes: lista vira "" para não mudar a identidade da série.
	arr := &cmnpb.AnyValue{Value: &cmnpb.AnyValue_ArrayValue{ArrayValue: &cmnpb.ArrayValue{}}}
	if anyValueToString(arr) != "" {
		t.Fatal("anyValueToString mudou de comportamento para métricas")
	}
}

func TestModosDeConteudo(t *testing.T) {
	spans := fixtureSpans(t, "openinference")
	desl, cDesl := linhasGenAI(spans, ConteudoDesligado)
	if len(desl) != 2 || len(cDesl) != 0 || desl[0].ComConteudo {
		t.Fatalf("desligado: %d linhas, %d de conteúdo", len(desl), len(cDesl))
	}
	if desl[0].Host != "fixture-host" || desl[0].Service != "agente-openinference" {
		t.Fatalf("linha sem host/serviço do span: %+v", desl[0])
	}
	comp, cComp := linhasGenAI(spans, ConteudoCompleto)
	if !comp[0].ComConteudo || len(cComp) == 0 {
		t.Fatal("completo deveria gravar conteúdo")
	}
	_, cRed := linhasGenAI(spans, ConteudoRedigido)
	var tudo strings.Builder
	redigidas := 0
	for _, c := range cRed {
		tudo.WriteString(c.Texto)
		if c.Redigido {
			redigidas++
		}
	}
	if strings.Contains(tudo.String(), "4111 1111 1111 1111") || strings.Contains(tudo.String(), "sk-teste-falso-de-fixture-0000") {
		t.Fatal("modo redigido gravou o cartão ou a chave")
	}
	if redigidas == 0 || !strings.Contains(tudo.String(), redacao.Marca) {
		t.Fatal("nenhuma mensagem marcada como redigida")
	}
	var compTudo strings.Builder
	for _, c := range cComp {
		compTudo.WriteString(c.Texto)
	}
	if !strings.Contains(compTudo.String(), "4111 1111 1111 1111") {
		t.Fatal("modo completo deveria gravar o texto como chegou")
	}
}

func TestParseConteudoIA(t *testing.T) {
	casos := map[string]ConteudoIA{"": ConteudoDesligado, "REDIGIDO": ConteudoRedigido, " completo ": ConteudoCompleto, "sim": ConteudoDesligado}
	for in, quer := range casos {
		if got := ParseConteudoIA(in); got != quer {
			t.Errorf("%q → %q, quer %q", in, got, quer)
		}
	}
}

func TestLimitesDeConteudo(t *testing.T) {
	grande := strings.Repeat("é", maxBytesMensagemIA) // 2x o limite em bytes
	ms := []genai.Mensagem{}
	for i := 0; i < 12; i++ {
		ms = append(ms, genai.Mensagem{Lado: "entrada", Ordem: i, Texto: grande})
	}
	s := model.Span{TraceID: "t", SpanID: "s", Labels: map[string]string{}, GenAI: &genai.Chamada{Operacao: "chat", Mensagens: ms}}
	_, cs := linhasGenAI([]model.Span{s}, ConteudoCompleto)
	total := 0
	for _, c := range cs {
		if len(c.Texto) > maxBytesMensagemIA || !c.Truncado {
			t.Fatalf("mensagem de %d bytes, truncado=%v", len(c.Texto), c.Truncado)
		}
		if strings.ContainsRune(c.Texto, '�') || !strings.HasPrefix(grande, c.Texto) {
			t.Fatal("corte partiu um caractere")
		}
		total += len(c.Texto)
	}
	if total > maxBytesSpanIA || len(cs) >= len(ms) {
		t.Fatalf("teto do span: %d bytes em %d mensagens", total, len(cs))
	}
}

// Trace com chamada de IA escapa da amostragem: com fração 0, um trace comum some e o
// trace de IA fica inteiro.
func TestAmostragemNuncaDescartaIA(t *testing.T) {
	rc := &Receiver{sampleFrac: 0, slowMs: 1e9, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	comum := model.Span{TraceID: "aaaa", SpanID: "1", StatusCode: "OK", DurationMs: 1}
	ia := model.Span{TraceID: "bbbb", SpanID: "2", StatusCode: "OK", DurationMs: 1, GenAI: &genai.Chamada{Operacao: "chat"}}
	irmao := model.Span{TraceID: "bbbb", SpanID: "3", StatusCode: "OK", DurationMs: 1}
	out := rc.sampleSpans([]model.Span{comum, ia, irmao})
	if len(out) != 2 || out[0].TraceID != "bbbb" || out[1].TraceID != "bbbb" {
		t.Fatalf("amostragem: %+v", out)
	}
}

type sinkIA struct {
	mu       sync.Mutex
	linhas   []model.GenAISpan
	conteudo []model.GenAIConteudo
}

func (s *sinkIA) InsertGenAISpans(_ context.Context, r []model.GenAISpan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.linhas = append(s.linhas, r...)
	return nil
}

func (s *sinkIA) InsertGenAIConteudo(_ context.Context, r []model.GenAIConteudo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conteudo = append(s.conteudo, r...)
	return nil
}

func TestIngestGenAISincrono(t *testing.T) {
	sink := &sinkIA{}
	rc := &Receiver{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rc.ingestGenAI(context.Background(), fixtureSpans(t, "openllmetry")) // desligado: no-op
	rc.SetGenAI(sink, nil, nil, ConteudoRedigido)
	rc.ingestGenAI(context.Background(), fixtureSpans(t, "openllmetry"))
	if len(sink.linhas) != 3 || len(sink.conteudo) == 0 {
		t.Fatalf("linhas=%d conteudo=%d", len(sink.linhas), len(sink.conteudo))
	}
	// span sem IA não gera linha
	rc.ingestGenAI(context.Background(), []model.Span{{TraceID: "x", Labels: map[string]string{}}})
	if len(sink.linhas) != 3 {
		t.Fatal("span comum gerou linha de IA")
	}
}

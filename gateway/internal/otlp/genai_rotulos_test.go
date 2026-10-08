package otlp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/eduardorarruda/revoada/core/genai"
	"github.com/eduardorarruda/revoada/core/redacao"
	"github.com/eduardorarruda/revoada/gateway/internal/model"
	cmnpb "go.opentelemetry.io/proto/otlp/common/v1"
	rpb "go.opentelemetry.io/proto/otlp/resource/v1"
	tpb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// filaCheia é o batcher que recusa tudo (fila cheia) e conta quantas vezes foi chamado.
type filaCheia struct {
	mu sync.Mutex
	n  int
}

func (f *filaCheia) Enqueue([]model.GenAISpan) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	return false
}

type filaConteudoCheia struct {
	mu sync.Mutex
	n  int
}

func (f *filaConteudoCheia) Enqueue([]model.GenAIConteudo) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	return false
}

// TestIngestGenAIFilaCheia: com as filas cheias, a requisição segue (os spans já foram
// aceitos), o descarte é avisado no log e o insert síncrono NÃO é tentado no lugar —
// senão a fila cheia viraria um insert por requisição justamente na hora de pico.
func TestIngestGenAIFilaCheia(t *testing.T) {
	var log bytes.Buffer
	rc := &Receiver{log: slog.New(slog.NewTextHandler(&log, nil))}
	sink := &sinkIA{}
	spans, conteudo := &filaCheia{}, &filaConteudoCheia{}
	rc.SetGenAI(sink, spans, conteudo, ConteudoCompleto)
	s := model.Span{TraceID: "t", SpanID: "s", Labels: map[string]string{"host": "h1"},
		GenAI: &genai.Chamada{Operacao: "chat", Mensagens: []genai.Mensagem{{Lado: "entrada", Texto: "oi"}}}}

	rc.ingestGenAI(context.Background(), []model.Span{s})

	if spans.n != 1 || conteudo.n != 1 {
		t.Fatalf("enfileirou spans=%d conteúdo=%d, quer 1 e 1", spans.n, conteudo.n)
	}
	if len(sink.linhas) != 0 || len(sink.conteudo) != 0 {
		t.Fatal("com batcher configurado, a fila cheia não pode cair no insert síncrono")
	}
	for _, aviso := range []string{"fila de chamadas de IA cheia", "fila de conteúdo de IA cheia"} {
		if !strings.Contains(log.String(), aviso) {
			t.Errorf("faltou o aviso %q no log: %s", aviso, log.String())
		}
	}
}

// TestTetoDoSpanMarcaAMensagemAnterior: mensagens abaixo do limite individual, mas que
// somadas passam do teto do span. As que cabem vão inteiras; a última gravada é marcada
// como truncada (a conversa continuava) e as seguintes são descartadas.
func TestTetoDoSpanMarcaAMensagemAnterior(t *testing.T) {
	const tamanho = 30 << 10 // abaixo de maxBytesMensagemIA
	cabem := maxBytesSpanIA / tamanho
	var ms []genai.Mensagem
	for i := 0; i < cabem+3; i++ {
		ms = append(ms, genai.Mensagem{Lado: "entrada", Ordem: i, Texto: strings.Repeat("a", tamanho)})
	}
	s := model.Span{TraceID: "t", SpanID: "s", GenAI: &genai.Chamada{Operacao: "chat", Mensagens: ms}}
	out := conteudoDoSpan(s, ConteudoCompleto)
	if len(out) != cabem {
		t.Fatalf("%d mensagens gravadas, quer %d", len(out), cabem)
	}
	for i, c := range out {
		querTruncado := i == len(out)-1
		if c.Truncado != querTruncado || len(c.Texto) != tamanho {
			t.Fatalf("mensagem %d: truncado=%v (%d bytes), quer truncado=%v inteira", i, c.Truncado, len(c.Texto), querTruncado)
		}
	}
	// uma mensagem sozinha maior que o teto do span: cortada no limite individual, gravada e marcada
	enorme := model.Span{GenAI: &genai.Chamada{Mensagens: []genai.Mensagem{{Texto: strings.Repeat("b", maxBytesSpanIA+1)}}}}
	if out := conteudoDoSpan(enorme, ConteudoCompleto); len(out) != 1 || !out[0].Truncado || len(out[0].Texto) > maxBytesMensagemIA {
		t.Fatalf("mensagem única enorme: %d trechos", len(out))
	}
}

func kv(k, v string) *cmnpb.KeyValue {
	return &cmnpb.KeyValue{Key: k, Value: &cmnpb.AnyValue{Value: &cmnpb.AnyValue_StringValue{StringValue: v}}}
}

// converter passa um span pelo mesmo caminho do receptor (FromResourceSpans).
func converter(t *testing.T, sp *tpb.Span) model.Span {
	t.Helper()
	sp.TraceId, sp.SpanId = bytes.Repeat([]byte{1}, 16), bytes.Repeat([]byte{2}, 8)
	sp.StartTimeUnixNano, sp.EndTimeUnixNano = 1_700_000_000_000_000_000, 1_700_000_000_500_000_000
	out := FromResourceSpans("default", []*tpb.ResourceSpans{{
		Resource:   &rpb.Resource{Attributes: []*cmnpb.KeyValue{kv("service.name", "app"), kv("host.name", "h1")}},
		ScopeSpans: []*tpb.ScopeSpans{{Spans: []*tpb.Span{sp}}},
	}})
	if len(out) != 1 {
		t.Fatalf("%d spans convertidos", len(out))
	}
	return out[0]
}

// TestConteudoSaiDosRotulosDeQualquerSpan: as chaves de conteúdo saem dos rótulos
// mesmo quando o span não é reconhecido como IA (instrumentação sem o marcador), e o
// texto dos documentos de um span RETRIEVER do OpenInference (RAG) também sai.
func TestConteudoSaiDosRotulosDeQualquerSpan(t *testing.T) {
	casos := []struct {
		nome        string
		attrs       []*cmnpb.KeyValue
		reconhecido bool
		ficam       []string
		saem        []string
	}{
		{"span comum com input.value", []*cmnpb.KeyValue{kv("input.value", "CPF 123.456.789-00 do cliente"),
			kv("output.value", "resposta"), kv("http.method", "POST"), kv("http.url", "https://api.exemplo/v1")},
			false, []string{"http.method", "http.url", "host"}, []string{"input.value", "output.value"}},
		// gen_ai.conversation.id propagado como baggage não faz o span ser de IA, mas o
		// conteúdo achatado do OpenInference sem o marcador sai do mesmo jeito.
		{"span HTTP com baggage e conteúdo achatado", []*cmnpb.KeyValue{kv("gen_ai.conversation.id", "conv-1"),
			kv("llm.input_messages.0.message.content", "segredo"), kv("tool.parameters", `{"cpf":"1"}`),
			kv("db.system", "postgresql")}, false, []string{"db.system", "gen_ai.conversation.id"},
			[]string{"llm.input_messages.0.message.content", "tool.parameters"}},
		{"RETRIEVER do OpenInference", []*cmnpb.KeyValue{kv("openinference.span.kind", "RETRIEVER"),
			kv("input.value", "qual o saldo da Maria?"),
			kv("retrieval.documents.0.document.content", "extrato da Maria"),
			kv("retrieval.documents.0.document.id", "doc-1"),
			kv("embedding.embeddings.0.embedding.text", "texto embutido"),
			kv("reranker.query", "consulta")},
			true, []string{"openinference.span.kind"},
			[]string{"input.value", "retrieval.documents.0.document.content", "retrieval.documents.0.document.id",
				"embedding.embeddings.0.embedding.text", "reranker.query"}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			s := converter(t, &tpb.Span{Name: "operacao", Attributes: c.attrs})
			if (s.GenAI != nil) != c.reconhecido {
				t.Fatalf("reconhecido como IA = %v, quer %v", s.GenAI != nil, c.reconhecido)
			}
			for _, k := range c.saem {
				if v, tem := s.Labels[k]; tem {
					t.Errorf("conteúdo ficou no rótulo %s=%q", k, v)
				}
			}
			for _, k := range c.ficam {
				if _, tem := s.Labels[k]; !tem {
					t.Errorf("o rótulo %s (não é conteúdo) sumiu: %v", k, s.Labels)
				}
			}
		})
	}
}

// TestExcecaoDeSpanDeIAERedigida: provedor ecoa a chave (e trecho do pedido) no corpo
// do erro 4xx. Num span de IA a mensagem da exceção passa pela redação; num span comum
// fica como chegou (o contrato dos rótulos de exceção não muda para o resto).
func TestExcecaoDeSpanDeIAERedigida(t *testing.T) {
	const chave = "sk-proj-AbCdEf0123456789XyZ"
	excecao := func() []*tpb.Span_Event {
		return []*tpb.Span_Event{{Name: "exception", Attributes: []*cmnpb.KeyValue{
			kv("exception.type", "AuthenticationError"),
			kv("exception.message", "Incorrect API key provided: "+chave),
			kv("exception.stacktrace", "Traceback: api_key="+chave),
		}}}
	}
	erro := &tpb.Status{Code: tpb.Status_STATUS_CODE_ERROR}
	ia := converter(t, &tpb.Span{Name: "chat gpt-4o-mini", Status: erro, Events: excecao(), Attributes: []*cmnpb.KeyValue{
		kv("gen_ai.operation.name", "chat"), kv("gen_ai.system", "openai"), kv("gen_ai.request.model", "gpt-4o-mini")}})
	if ia.GenAI == nil {
		t.Fatal("o span de IA não foi reconhecido")
	}
	for _, k := range []string{"exception.message", "exception.stacktrace"} {
		if v := ia.Labels[k]; strings.Contains(v, chave) || !strings.Contains(v, redacao.Marca) {
			t.Errorf("span de IA: %s = %q, quer a chave redigida", k, v)
		}
	}
	if ia.Labels["exception.type"] != "AuthenticationError" {
		t.Errorf("o tipo da exceção não é conteúdo e deve ficar: %q", ia.Labels["exception.type"])
	}
	comum := converter(t, &tpb.Span{Name: "GET /pedidos", Status: erro, Events: excecao(),
		Attributes: []*cmnpb.KeyValue{kv("http.method", "GET")}})
	if comum.GenAI != nil || comum.Labels["exception.message"] != "Incorrect API key provided: "+chave {
		t.Fatalf("span comum: exception.message = %q (IA=%v)", comum.Labels["exception.message"], comum.GenAI != nil)
	}
}

// sinkQuebrado é o ClickHouse fora do ar no caminho síncrono (sem batcher).
type sinkQuebrado struct{}

func (sinkQuebrado) InsertGenAISpans(context.Context, []model.GenAISpan) error {
	return errors.New("clickhouse fora")
}

func (sinkQuebrado) InsertGenAIConteudo(context.Context, []model.GenAIConteudo) error {
	return errors.New("clickhouse fora")
}

// TestIngestGenAISincronoComErro: sem batcher, o erro do insert vira aviso no log e a
// requisição segue — os spans já foram aceitos, e recusar faria o SDK reenviar tudo.
func TestIngestGenAISincronoComErro(t *testing.T) {
	var log bytes.Buffer
	rc := &Receiver{log: slog.New(slog.NewTextHandler(&log, nil))}
	rc.SetGenAI(sinkQuebrado{}, nil, nil, ConteudoCompleto)
	s := model.Span{TraceID: "t", SpanID: "s", Labels: map[string]string{},
		GenAI: &genai.Chamada{Operacao: "chat", Mensagens: []genai.Mensagem{{Lado: "entrada", Texto: "oi"}}}}
	rc.ingestGenAI(context.Background(), []model.Span{s})
	for _, aviso := range []string{"gravando chamadas de IA", "gravando conteúdo de IA"} {
		if !strings.Contains(log.String(), aviso) {
			t.Errorf("faltou o aviso %q no log: %s", aviso, log.String())
		}
	}
}

package genai

import (
	"reflect"
	"strings"
	"testing"
)

func val(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// Atributos copiados dos spans gravados das bibliotecas reais (os .pb completos estão
// em gateway/internal/otlp/testdata/genai e são testados de ponta a ponta lá).
var (
	otelOficialChat = map[string]string{
		"gen_ai.input.messages":          `[{"role":"user","parts":[{"content":"Clima em Recife?","type":"text"}]}]`,
		"gen_ai.operation.name":          "chat",
		"gen_ai.output.messages":         `[{"role":"assistant","parts":[{"arguments":{"cidade":"Recife"},"name":"clima","id":"call_1","type":"tool_call"}],"finish_reason":"tool_calls"}]`,
		"gen_ai.provider.name":           "openai",
		"gen_ai.request.model":           "gpt-4o-mini",
		"gen_ai.response.finish_reasons": `["tool_calls"]`,
		"gen_ai.response.model":          "gpt-4o-mini-2024-07-18",
		"gen_ai.usage.input_tokens":      "120",
		"gen_ai.usage.output_tokens":     "30",
		"server.address":                 "127.0.0.1",
	}
	openllmetryAnthropic = map[string]string{
		"gen_ai.operation.name":                    "chat",
		"gen_ai.provider.name":                     "anthropic",
		"gen_ai.request.model":                     "claude-sonnet-4-5",
		"gen_ai.response.model":                    "claude-sonnet-4-5",
		"gen_ai.response.finish_reasons":           `["tool_call"]`,
		"gen_ai.usage.cache_creation.input_tokens": "50",
		"gen_ai.usage.cache_read.input_tokens":     "100",
		"gen_ai.usage.input_tokens":                "170",
		"gen_ai.usage.output_tokens":               "30",
		"gen_ai.tool.definitions":                  `[{"name":"clima"}]`,
	}
	openinferenceLLM = map[string]string{
		"openinference.span.kind":                                                "LLM",
		"llm.system":                                                             "openai",
		"llm.model_name":                                                         "gpt-4o-mini-2024-07-18",
		"llm.finish_reason":                                                      "stop",
		"llm.token_count.prompt":                                                 "120",
		"llm.token_count.completion":                                             "30",
		"llm.token_count.prompt_details.cache_read":                              "100",
		"llm.input_messages.0.message.role":                                      "system",
		"llm.input_messages.0.message.content":                                   "Você é um assistente.",
		"llm.input_messages.1.message.role":                                      "user",
		"llm.input_messages.1.message.content":                                   "Clima?",
		"llm.input_messages.2.message.role":                                      "assistant",
		"llm.input_messages.2.message.tool_calls.0.tool_call.function.name":      "clima",
		"llm.input_messages.2.message.tool_calls.0.tool_call.function.arguments": `{"cidade": "Recife"}`,
		"llm.input_messages.10.message.role":                                     "user",
		"llm.input_messages.10.message.content":                                  "décima primeira",
		"llm.output_messages.0.message.role":                                     "assistant",
		"llm.output_messages.0.message.content":                                  "Faz 29 graus.",
		"input.value":                                                            `{"messages": []}`,
		"output.value":                                                           `{"id": "x"}`,
	}
)

func TestNormalizarDialetosReais(t *testing.T) {
	casos := []struct {
		nome                       string
		span                       Span
		conv, op, provedor, modelo string
		entrada, saida, cacheL     any
		motivos                    []string
	}{
		{"otel oficial", Span{Nome: "chat gpt-4o-mini", Atributos: otelOficialChat},
			ConvOTel, OpChat, "openai", "gpt-4o-mini-2024-07-18", int64(120), int64(30), nil, []string{"tool_calls"}},
		{"openllmetry anthropic", Span{Nome: "anthropic.chat", Atributos: openllmetryAnthropic},
			ConvOTel, OpChat, "anthropic", "claude-sonnet-4-5", int64(170), int64(30), int64(100), []string{"tool_call"}},
		{"openinference", Span{Nome: "ChatCompletion", Atributos: openinferenceLLM},
			ConvOpenInference, OpChat, "openai", "gpt-4o-mini-2024-07-18", int64(120), int64(30), int64(100), []string{"stop"}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			ch, ok := Normalizar(c.span)
			if !ok {
				t.Fatal("span de IA não reconhecido")
			}
			if ch.Convencao != c.conv || ch.Operacao != c.op || ch.Provedor != c.provedor || ch.Modelo != c.modelo {
				t.Fatalf("got conv=%q op=%q provedor=%q modelo=%q", ch.Convencao, ch.Operacao, ch.Provedor, ch.Modelo)
			}
			if val(ch.TokensEntrada) != c.entrada || val(ch.TokensSaida) != c.saida || val(ch.TokensCacheLeitura) != c.cacheL {
				t.Fatalf("tokens: entrada=%v saida=%v cache=%v", val(ch.TokensEntrada), val(ch.TokensSaida), val(ch.TokensCacheLeitura))
			}
			if !reflect.DeepEqual(ch.MotivosFim, c.motivos) {
				t.Fatalf("motivos = %v, quer %v", ch.MotivosFim, c.motivos)
			}
		})
	}
}

func TestSpanComumNaoEhDeIA(t *testing.T) {
	comuns := []map[string]string{
		{"http.method": "GET", "http.route": "/api"},
		// baggage propagado não transforma uma requisição HTTP em chamada de IA
		{"http.method": "POST", "gen_ai.conversation.id": "c1"},
		{},
	}
	for _, a := range comuns {
		if _, ok := Normalizar(Span{Atributos: a}); ok {
			t.Fatalf("span comum reconhecido como IA: %v", a)
		}
	}
}

func TestLegadoTraceloop(t *testing.T) {
	a := map[string]string{
		"gen_ai.system":                     "OpenAI",
		"llm.request.type":                  "chat",
		"gen_ai.request.model":              "gpt-4o",
		"gen_ai.usage.prompt_tokens":        "11",
		"gen_ai.usage.completion_tokens":    "7",
		"gen_ai.prompt.0.role":              "user",
		"gen_ai.prompt.0.content":           "oi",
		"gen_ai.completion.0.role":          "assistant",
		"gen_ai.completion.0.content":       "olá",
		"gen_ai.completion.0.finish_reason": "stop",
	}
	ch, ok := Normalizar(Span{Atributos: a})
	if !ok || ch.Convencao != ConvLegado || ch.Operacao != OpChat || ch.Provedor != "openai" {
		t.Fatalf("legado: ok=%v %+v", ok, ch)
	}
	if val(ch.TokensEntrada) != int64(11) || val(ch.TokensSaida) != int64(7) {
		t.Fatalf("tokens legados: %v %v", val(ch.TokensEntrada), val(ch.TokensSaida))
	}
	if len(ch.Mensagens) != 2 || ch.Mensagens[0].Texto != "oi" || ch.Mensagens[1].Lado != ladoSaida {
		t.Fatalf("mensagens legadas: %+v", ch.Mensagens)
	}
	if !reflect.DeepEqual(ch.MotivosFim, []string{"stop"}) {
		t.Fatalf("motivo legado: %v", ch.MotivosFim)
	}
}

func TestAgenteEFerramenta(t *testing.T) {
	ag, _ := Normalizar(Span{Nome: "invoke_agent Meteorologista", Atributos: map[string]string{
		"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "Meteorologista",
		"gen_ai.agent.id": "agente-42", "gen_ai.conversation.id": "conversa-abc",
	}})
	if ag.Operacao != OpAgente || ag.Agente != "Meteorologista" || ag.AgenteID != "agente-42" || ag.ConversaID != "conversa-abc" {
		t.Fatalf("agente: %+v", ag)
	}
	ft, _ := Normalizar(Span{Nome: "execute_tool clima", Atributos: map[string]string{
		"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "clima", "gen_ai.tool.call.id": "call_1",
		"gen_ai.tool.call.arguments": `{"cidade":"Recife"}`, "gen_ai.tool.call.result": `{"graus":29}`,
	}})
	if ft.Operacao != OpFerramenta || ft.Ferramenta != "clima" || ft.ChamadaID != "call_1" || len(ft.Mensagens) != 2 {
		t.Fatalf("ferramenta: %+v", ft)
	}
	// OpenInference: o nome da ferramenta e do agente vêm do nome do span.
	oi, _ := Normalizar(Span{Nome: "buscar_pedido", Atributos: map[string]string{"openinference.span.kind": "TOOL", "input.value": "{}"}})
	if oi.Ferramenta != "buscar_pedido" || oi.Mensagens[0].Papel != papelFerramenta {
		t.Fatalf("ferramenta OI: %+v", oi)
	}
	oa, _ := Normalizar(Span{Nome: "Atendente", Atributos: map[string]string{"openinference.span.kind": "AGENT"}})
	if oa.Operacao != OpAgente || oa.Agente != "Atendente" {
		t.Fatalf("agente OI: %+v", oa)
	}
}

func TestErro(t *testing.T) {
	com, _ := Normalizar(Span{Erro: true, Atributos: map[string]string{"gen_ai.operation.name": "chat", "error.type": "InternalServerError"}})
	if com.Erro != "InternalServerError" {
		t.Fatalf("error.type: %q", com.Erro)
	}
	exc, _ := Normalizar(Span{Erro: true, Atributos: map[string]string{"gen_ai.operation.name": "chat", "exception.type": "Timeout"}})
	if exc.Erro != "Timeout" {
		t.Fatalf("exception.type: %q", exc.Erro)
	}
	sem, _ := Normalizar(Span{Erro: true, Atributos: map[string]string{"gen_ai.operation.name": "chat"}})
	if sem.Erro != erroSemTipoMarca {
		t.Fatalf("erro sem tipo: %q", sem.Erro)
	}
	ok, _ := Normalizar(Span{Atributos: map[string]string{"gen_ai.operation.name": "chat"}})
	if ok.Erro != "" {
		t.Fatalf("chamada ok com erro %q", ok.Erro)
	}
}

func TestTokensNaoInformadosFicamNil(t *testing.T) {
	ch, _ := Normalizar(Span{Atributos: map[string]string{"gen_ai.operation.name": "chat", "gen_ai.usage.input_tokens": "abc", "gen_ai.usage.output_tokens": "-3"}})
	if ch.TokensEntrada != nil || ch.TokensSaida != nil {
		t.Fatalf("token inválido virou número: %v %v", val(ch.TokensEntrada), val(ch.TokensSaida))
	}
	f, _ := Normalizar(Span{Atributos: map[string]string{"gen_ai.operation.name": "chat", "gen_ai.usage.input_tokens": "12.0"}})
	if val(f.TokensEntrada) != int64(12) {
		t.Fatalf("12.0 deveria valer 12: %v", val(f.TokensEntrada))
	}
}

func TestCacheSeparadoEhSomadoNaEntrada(t *testing.T) {
	// API crua da Anthropic: 20 novos, 100 lidos do cache, 50 escritos — informados à parte.
	ch, _ := Normalizar(Span{Atributos: map[string]string{
		"gen_ai.operation.name": "chat", "gen_ai.usage.input_tokens": "20",
		"gen_ai.usage.cache_read_input_tokens": "100", "gen_ai.usage.cache_creation_input_tokens": "50",
	}})
	if val(ch.TokensEntrada) != int64(170) {
		t.Fatalf("entrada deveria incluir o cache (170), veio %v", val(ch.TokensEntrada))
	}
}

func TestMensagensOpenInferenceEmOrdemNumerica(t *testing.T) {
	ch, _ := Normalizar(Span{Atributos: openinferenceLLM})
	var papeis, textos []string
	for _, m := range ch.Mensagens {
		papeis = append(papeis, m.Lado+":"+m.Papel)
		textos = append(textos, m.Texto)
	}
	quer := []string{"entrada:system", "entrada:user", "entrada:assistant", "entrada:user", "saida:assistant"}
	if !reflect.DeepEqual(papeis, quer) {
		t.Fatalf("papéis = %v, quer %v", papeis, quer)
	}
	if textos[2] != `→ clima({"cidade": "Recife"})` || textos[3] != "décima primeira" {
		t.Fatalf("textos = %q", textos)
	}
	for i, m := range ch.Mensagens {
		if m.Ordem != i {
			t.Fatalf("ordem %d na posição %d", m.Ordem, i)
		}
	}
}

func TestMensagensOTelComPartes(t *testing.T) {
	ch, _ := Normalizar(Span{Atributos: map[string]string{
		"gen_ai.operation.name":      "chat",
		"gen_ai.system_instructions": `[{"type":"text","content":"Seja breve."}]`,
		"gen_ai.input.messages":      `[{"role":"user","parts":[{"type":"text","content":"oi"}]},{"role":"tool","parts":[{"type":"tool_call_response","id":"c1","response":{"graus":29}}]}]`,
		"gen_ai.output.messages":     `[{"role":"assistant","parts":[{"type":"text","content":"29 graus"},{"type":"image","uri":"x"}]}]`,
	}})
	if len(ch.Mensagens) != 4 {
		t.Fatalf("mensagens: %+v", ch.Mensagens)
	}
	if ch.Mensagens[0].Papel != papelSistema || ch.Mensagens[0].Texto != "Seja breve." {
		t.Fatalf("instrução de sistema: %+v", ch.Mensagens[0])
	}
	if ch.Mensagens[2].Texto != `← {"graus":29}` {
		t.Fatalf("resposta de ferramenta: %q", ch.Mensagens[2].Texto)
	}
	if !strings.HasPrefix(ch.Mensagens[3].Texto, "29 graus\n{") {
		t.Fatalf("parte desconhecida deveria virar JSON: %q", ch.Mensagens[3].Texto)
	}
	invalido, _ := Normalizar(Span{Atributos: map[string]string{"gen_ai.operation.name": "chat", "gen_ai.input.messages": "texto solto"}})
	if len(invalido.Mensagens) != 1 || invalido.Mensagens[0].Texto != "texto solto" {
		t.Fatalf("formato inesperado deveria ser preservado: %+v", invalido.Mensagens)
	}
}

func TestMensagensDeEventos(t *testing.T) {
	ch, _ := Normalizar(Span{
		Atributos: map[string]string{"gen_ai.operation.name": "chat"},
		Eventos: []Evento{
			{Nome: "gen_ai.user.message", Atributos: map[string]string{"content": "oi"}},
			{Nome: "gen_ai.choice", Atributos: map[string]string{"message": `{"content":"olá"}`}},
			{Nome: "outra.coisa", Atributos: map[string]string{"content": "x"}},
		},
	})
	if len(ch.Mensagens) != 2 || ch.Mensagens[0].Papel != "user" || ch.Mensagens[1].Lado != ladoSaida {
		t.Fatalf("eventos: %+v", ch.Mensagens)
	}
}

func TestEhChaveDeConteudo(t *testing.T) {
	sim := []string{"gen_ai.input.messages", "gen_ai.prompt.0.content", "llm.input_messages.3.message.content",
		"input.value", "llm.tools.0.tool.json_schema", "gen_ai.tool.call.result", "traceloop.entity.output"}
	nao := []string{"gen_ai.request.model", "llm.token_count.prompt", "llm.invocation_parameters", "http.route", "input"}
	for _, k := range sim {
		if !EhChaveDeConteudo(k) {
			t.Errorf("%s deveria ser conteúdo", k)
		}
	}
	for _, k := range nao {
		if EhChaveDeConteudo(k) {
			t.Errorf("%s não é conteúdo", k)
		}
	}
}

func TestCampoLongoCortadoSemPartirCaractere(t *testing.T) {
	longo := strings.Repeat("ã", 200) // 400 bytes
	ch, _ := Normalizar(Span{Atributos: map[string]string{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": longo}})
	if len(ch.Agente) > maxCampoCurto || !strings.HasPrefix(longo, ch.Agente) || strings.ContainsRune(ch.Agente, '�') {
		t.Fatalf("corte inválido: %d bytes", len(ch.Agente))
	}
}

func TestEhChamadaDeModelo(t *testing.T) {
	for _, op := range []string{OpChat, OpTexto, OpGerarConteudo, OpEmbeddings} {
		if !EhChamadaDeModelo(op) {
			t.Errorf("%s é chamada de modelo", op)
		}
	}
	for _, op := range []string{OpAgente, OpFerramenta, OpCadeia, ""} {
		if EhChamadaDeModelo(op) {
			t.Errorf("%s não é chamada de modelo", op)
		}
	}
}

func TestVercelLegado(t *testing.T) {
	ops := map[string]string{"ai.toolCall": OpFerramenta, "ai.streamText.doStream": OpChat, "ai.embedMany": OpEmbeddings,
		"ai.generateObject": OpAgente, "ai.algoNovo": OpOutro}
	for id, quer := range ops {
		c, ok := Normalizar(Span{Atributos: map[string]string{"ai.operationId": id}})
		if !ok || c.Convencao != ConvVercel || c.Operacao != quer {
			t.Errorf("%s → %q (ok=%v), quer %q", id, c.Operacao, ok, quer)
		}
	}
	c, _ := Normalizar(Span{Atributos: map[string]string{
		"ai.operationId": "ai.generateText", "ai.telemetry.functionId": "Atendente",
		"ai.prompt":        `{"system":"Seja breve.","prompt":"oi"}`,
		"ai.response.text": "olá",
	}})
	if c.Agente != "Atendente" || len(c.Mensagens) != 3 || c.Mensagens[0].Papel != papelSistema || c.Mensagens[2].Lado != ladoSaida {
		t.Fatalf("generateText: %+v", c)
	}
	d, _ := Normalizar(Span{Atributos: map[string]string{
		"ai.operationId": "ai.generateText.doGenerate", "ai.model.provider": "anthropic.messages", "ai.model.id": "claude-x",
		"ai.usage.promptTokens": "10", "ai.usage.completionTokens": "5",
		"ai.prompt.messages": `[{"role":"user","content":"oi"},{"role":"assistant","content":[{"type":"tool-call","toolName":"t","input":{"a":1}}]},{"role":"tool","content":[{"type":"tool-result","output":{"ok":true}}]}]`,
	}})
	if d.Provedor != "anthropic" || d.Modelo != "claude-x" || val(d.TokensEntrada) != int64(10) || val(d.TokensSaida) != int64(5) {
		t.Fatalf("doGenerate: %+v", d)
	}
	if len(d.Mensagens) != 3 || d.Mensagens[1].Texto != `→ t({"a":1})` || d.Mensagens[2].Texto != `← {"ok":true}` {
		t.Fatalf("mensagens: %+v", d.Mensagens)
	}
	for _, k := range []string{"ai.prompt", "ai.prompt.messages", "ai.response.text", "ai.toolCall.result"} {
		if !EhChaveDeConteudo(k) {
			t.Errorf("%s deveria ser conteúdo", k)
		}
	}
}

func TestSoConteudoLegadoEhReconhecido(t *testing.T) {
	// Instrumentação parcial: só o conteúdo, sem nenhum marcador de IA.
	c, ok := Normalizar(Span{Atributos: map[string]string{"gen_ai.prompt.0.role": "user", "gen_ai.prompt.0.content": "oi"}})
	if !ok || c.Convencao != ConvLegado || len(c.Mensagens) != 1 {
		t.Fatalf("ok=%v %+v", ok, c)
	}
}

func TestChavesDeConteudoDoOpenInferenceRAG(t *testing.T) {
	for _, k := range []string{"retrieval.documents.0.document.content", "reranker.input_documents.2.document.content",
		"reranker.output_documents.0.document.content", "reranker.query", "embedding.embeddings.0.embedding.text"} {
		if !EhChaveDeConteudo(k) {
			t.Errorf("%s deveria ser conteúdo", k)
		}
	}
}

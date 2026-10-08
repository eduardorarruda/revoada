// Package genai reconhece spans de chamadas de IA (LLM, agente, ferramenta) e os
// normaliza num formato só, qualquer que seja a biblioteca que os emitiu.
//
// Três dialetos convivem na prática, e o Revoada aceita os três:
//
//   - otel-genai: a convenção semântica GenAI do OpenTelemetry (gen_ai.provider.name,
//     gen_ai.usage.input_tokens, gen_ai.input.messages…). É o que a instrumentação
//     oficial e as versões recentes do OpenLLMetry emitem.
//   - otel-genai-legado: a mesma família antes das renomeações (gen_ai.system,
//     gen_ai.usage.prompt_tokens, gen_ai.prompt.N.content, traceloop.*).
//   - openinference: o padrão da Arize/Phoenix (openinference.span.kind,
//     llm.token_count.prompt, llm.input_messages.N.message.content).
//
// A convenção do OpenTelemetry ainda não é estável — já mudou nomes uma vez e vai
// mudar de novo. Por isso cada chamada guarda o dialeto que chegou (Convencao) e os
// testes deste pacote rodam contra spans gravados de bibliotecas reais.
//
// Regra de ouro herdada do painel: nunca afirmar o que não foi medido. Token que a
// biblioteca não informou fica nil — e é mostrado como "não informado", nunca como 0.
package genai

import (
	"strings"
)

// Dialetos reconhecidos.
const (
	ConvOTel          = "otel-genai"
	ConvLegado        = "otel-genai-legado"
	ConvOpenInference = "openinference"
)

// Operações normalizadas (valores de gen_ai.operation.name, mais os tipos de span do
// OpenInference que não têm par na convenção do OpenTelemetry).
const (
	OpChat           = "chat"
	OpTexto          = "text_completion"
	OpGerarConteudo  = "generate_content"
	OpEmbeddings     = "embeddings"
	OpAgente         = "invoke_agent"
	OpCriarAgente    = "create_agent"
	OpFerramenta     = "execute_tool"
	OpCadeia         = "chain"
	OpRecuperacao    = "retrieval"
	OpReordenacao    = "rerank"
	OpGuardrail      = "guardrail"
	OpOutro          = "outro"
	ladoEntrada      = "entrada"
	ladoSaida        = "saida"
	papelFerramenta  = "tool"
	papelSistema     = "system"
	maxMotivosDeFim  = 8
	maxCampoCurto    = 256 // nome de agente, ferramenta, modelo, ids
	erroSemTipoMarca = "erro"
)

// Evento é um span event, com os atributos já convertidos em texto.
type Evento struct {
	Nome      string
	Atributos map[string]string
}

// Span é o recorte de um span OTLP que o normalizador precisa. Atributos traz os do
// recurso e os do span fundidos (os do span vencem), com listas e mapas em JSON.
type Span struct {
	Nome      string
	Erro      bool // status ERROR
	Atributos map[string]string
	Eventos   []Evento
}

// Mensagem é um trecho do conteúdo de uma chamada: o que entrou (prompt, histórico,
// argumentos de ferramenta) ou o que saiu (resposta, resultado).
type Mensagem struct {
	Lado  string // "entrada" ou "saida"
	Papel string // system, user, assistant, tool… ("" quando o dialeto não diz)
	Ordem int
	Texto string
}

// Chamada é um span de IA normalizado.
type Chamada struct {
	Convencao  string
	Operacao   string
	Provedor   string
	Modelo     string
	Agente     string
	AgenteID   string
	ConversaID string
	Ferramenta string
	ChamadaID  string // id da chamada de ferramenta (liga o pedido do modelo à execução)

	// Tokens de ENTRADA sempre incluem os de cache (ver normalizarCache). nil = a
	// biblioteca não informou.
	TokensEntrada      *int64
	TokensSaida        *int64
	TokensCacheLeitura *int64
	TokensCacheEscrita *int64

	// CustoInformadoUSD vem quando a biblioteca ou um proxy (LiteLLM, OpenRouter) já
	// manda o custo pronto; nesse caso ele vence a estimativa pela tabela de preços.
	CustoInformadoUSD *float64

	Erro       string // error.type, ou "" quando a chamada deu certo
	MotivosFim []string
	Mensagens  []Mensagem
}

// EhChamadaDeModelo diz se a operação é uma chamada a um modelo (a que custa tokens).
func EhChamadaDeModelo(op string) bool {
	switch op {
	case OpChat, OpTexto, OpGerarConteudo, OpEmbeddings:
		return true
	}
	return false
}

// Normalizar reconhece o dialeto e devolve a chamada normalizada. ok=false quando o
// span não é de IA — o caminho mais comum, que precisa ser barato.
func Normalizar(s Span) (Chamada, bool) {
	a := s.Atributos
	conv := Dialeto(a)
	if conv == "" {
		return Chamada{}, false
	}
	c := Chamada{Convencao: conv}
	c.Operacao = operacao(conv, a)
	c.Provedor = strings.ToLower(curto(primeiro(a, "gen_ai.provider.name", "gen_ai.system", "llm.provider", "llm.system")))
	c.Modelo = curto(primeiro(a, "gen_ai.response.model", "gen_ai.request.model", "llm.model_name", "llm.response.model", "llm.request.model"))
	c.Agente, c.Ferramenta = nomes(conv, c.Operacao, s.Nome, a)
	c.AgenteID = curto(primeiro(a, "gen_ai.agent.id", "agent.id"))
	c.ConversaID = curto(primeiro(a, "gen_ai.conversation.id", "session.id",
		"traceloop.association.properties.conversation_id", "traceloop.association.properties.session_id"))
	c.ChamadaID = curto(primeiro(a, "gen_ai.tool.call.id", "tool_call.id", "tool.call.id"))

	c.TokensEntrada = inteiro(a, "gen_ai.usage.input_tokens", "gen_ai.usage.prompt_tokens", "llm.usage.prompt_tokens", "llm.token_count.prompt")
	c.TokensSaida = inteiro(a, "gen_ai.usage.output_tokens", "gen_ai.usage.completion_tokens", "llm.usage.completion_tokens", "llm.token_count.completion")
	c.TokensCacheLeitura = inteiro(a, "gen_ai.usage.cache_read.input_tokens", "gen_ai.usage.cache_read_input_tokens", "llm.token_count.prompt_details.cache_read")
	c.TokensCacheEscrita = inteiro(a, "gen_ai.usage.cache_creation.input_tokens", "gen_ai.usage.cache_creation_input_tokens", "llm.token_count.prompt_details.cache_write")
	normalizarCache(&c)
	c.CustoInformadoUSD = decimal(a, "gen_ai.usage.cost", "llm.cost.total", "gen_ai.cost")

	c.MotivosFim = motivosFim(a)
	if v := primeiro(a, "error.type"); v != "" {
		c.Erro = curto(v)
	} else if s.Erro {
		c.Erro = curto(primeiro(a, "exception.type"))
		if c.Erro == "" {
			c.Erro = erroSemTipoMarca
		}
	}
	c.Mensagens = mensagens(conv, c.Operacao, a, s.Eventos)
	return c, true
}

// normalizarCache deixa TokensEntrada sempre INCLUINDO os tokens de cache, para que o
// custo seja uma conta linear (e some certo no agregado por minuto).
//
// As bibliotecas discordam: a semconv atual, o OpenLLMetry e o OpenInference contam o
// cache dentro de input_tokens (gravado: Anthropic com 20 novos + 100 lidos + 50
// escritos chega como input_tokens=170); a API da Anthropic, crua, informa os três
// separados. Quando o cache é MAIOR que a entrada, a entrada não pode tê-lo incluído —
// então ele é somado.
func normalizarCache(c *Chamada) {
	if c.TokensEntrada == nil {
		return
	}
	cache := valor(c.TokensCacheLeitura) + valor(c.TokensCacheEscrita)
	if cache > *c.TokensEntrada {
		total := *c.TokensEntrada + cache
		c.TokensEntrada = &total
	}
}

func valor(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// curto limita campos que viram coluna LowCardinality ou chave de agrupamento: um
// nome de agente de 10 KB é defeito da instrumentação, não informação.
func curto(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxCampoCurto {
		return s
	}
	return cortarUTF8(s, maxCampoCurto)
}

// cortarUTF8 corta s em no máximo n bytes sem partir um caractere no meio.
func cortarUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

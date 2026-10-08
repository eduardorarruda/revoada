package genai

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// sinaisOTel são as chaves que, sozinhas, fazem um span ser de IA. Não basta ter
// QUALQUER chave gen_ai.*: frameworks propagam gen_ai.conversation.id como baggage
// para spans HTTP, e esses spans não são chamadas de IA.
var sinaisOTel = []string{
	"gen_ai.operation.name", "gen_ai.provider.name", "gen_ai.system",
	"gen_ai.request.model", "gen_ai.response.model", "gen_ai.agent.name",
	"gen_ai.tool.name", "gen_ai.usage.input_tokens", "gen_ai.usage.prompt_tokens",
	"llm.request.type", "traceloop.span.kind",
}

// Dialeto diz qual convenção o span segue, ou "" quando não é um span de IA.
func Dialeto(a map[string]string) string {
	if a["openinference.span.kind"] != "" {
		return ConvOpenInference
	}
	if strings.HasPrefix(a["ai.operationId"], "ai.") {
		return ConvVercel
	}
	for _, k := range sinaisOTel {
		if a[k] == "" {
			continue
		}
		if ehLegado(a) {
			return ConvLegado
		}
		return ConvOTel
	}
	// Instrumentação antiga ou parcial que só manda o conteúdo (gen_ai.prompt.N.*)
	// continua sendo uma chamada de IA: reconhecê-la é o que leva o conteúdo para a
	// tabela certa em vez de deixá-lo sumir.
	if ehLegado(a) {
		return ConvLegado
	}
	return ""
}

// ehLegado: os nomes de antes da renomeação da semconv (gen_ai.system no lugar de
// gen_ai.provider.name, prompt/completion no lugar de input/output) ou as chaves
// próprias do Traceloop.
func ehLegado(a map[string]string) bool {
	if a["gen_ai.provider.name"] != "" {
		return false
	}
	if a["gen_ai.system"] != "" || a["gen_ai.usage.prompt_tokens"] != "" ||
		a["traceloop.span.kind"] != "" || a["llm.request.type"] != "" {
		return true
	}
	for k := range a {
		if strings.HasPrefix(k, "gen_ai.prompt.") || strings.HasPrefix(k, "gen_ai.completion.") {
			return true
		}
	}
	return false
}

// tiposOpenInference mapeia openinference.span.kind para a operação normalizada.
var tiposOpenInference = map[string]string{
	"LLM":       OpChat,
	"EMBEDDING": OpEmbeddings,
	"AGENT":     OpAgente,
	"TOOL":      OpFerramenta,
	"CHAIN":     OpCadeia,
	"RETRIEVER": OpRecuperacao,
	"RERANKER":  OpReordenacao,
	"GUARDRAIL": OpGuardrail,
}

func operacao(conv string, a map[string]string) string {
	if conv == ConvVercel {
		return operacaoVercel(a["ai.operationId"])
	}
	if conv == ConvOpenInference {
		if op, ok := tiposOpenInference[strings.ToUpper(a["openinference.span.kind"])]; ok {
			return op
		}
		return OpOutro
	}
	if op := strings.ToLower(strings.TrimSpace(a["gen_ai.operation.name"])); op != "" {
		return curto(op)
	}
	switch strings.ToLower(a["llm.request.type"]) {
	case "chat":
		return OpChat
	case "completion":
		return OpTexto
	case "embedding", "embeddings":
		return OpEmbeddings
	case "rerank":
		return OpReordenacao
	}
	switch strings.ToLower(a["traceloop.span.kind"]) {
	case "agent", "workflow":
		return OpAgente
	case "tool":
		return OpFerramenta
	case "task":
		return OpCadeia
	}
	switch {
	case a["gen_ai.tool.name"] != "":
		return OpFerramenta
	case a["gen_ai.agent.name"] != "" && a["gen_ai.request.model"] == "":
		return OpAgente
	case a["gen_ai.request.model"] != "" || a["gen_ai.response.model"] != "":
		return OpChat
	}
	return OpOutro
}

// operacaoVercel: no Vercel AI SDK, ai.generateText (e irmãos) é a orquestração com
// vários passos — o equivalente a um agente —, ai.*.doGenerate/doStream é a chamada ao
// modelo e ai.toolCall é a ferramenta.
func operacaoVercel(id string) string {
	switch {
	case id == "ai.toolCall":
		return OpFerramenta
	case strings.HasSuffix(id, ".doGenerate"), strings.HasSuffix(id, ".doStream"):
		return OpChat
	case strings.HasPrefix(id, "ai.embed"):
		return OpEmbeddings
	case id == "ai.generateText", id == "ai.streamText", id == "ai.generateObject", id == "ai.streamObject":
		return OpAgente
	}
	return OpOutro
}

// provedor: o Vercel AI SDK grava "openai.chat", "anthropic.messages"; o que importa
// para preço e agrupamento é a parte antes do ponto.
func provedor(conv string, a map[string]string) string {
	p := strings.ToLower(curto(primeiro(a, "gen_ai.provider.name", "gen_ai.system", "llm.provider", "llm.system", "ai.model.provider")))
	if conv == ConvVercel {
		if antes, _, ok := strings.Cut(p, "."); ok {
			return antes
		}
	}
	return p
}

// nomes devolve o agente e a ferramenta do span. No OpenInference e no Traceloop o
// nome mora em chaves diferentes (ou no nome do próprio span).
func nomes(conv, op, nomeSpan string, a map[string]string) (agente, ferramenta string) {
	agente = primeiro(a, "gen_ai.agent.name", "agent.name")
	ferramenta = primeiro(a, "gen_ai.tool.name", "tool.name", "ai.toolCall.name")
	if agente == "" && op == OpAgente && conv == ConvVercel {
		agente = a["ai.telemetry.functionId"]
	}
	entidade := a["traceloop.entity.name"]
	switch {
	case agente == "" && op == OpAgente && entidade != "":
		agente = entidade
	case agente == "" && op == OpAgente && conv == ConvOpenInference:
		agente = nomeSpan
	}
	if ferramenta == "" && op == OpFerramenta {
		if entidade != "" {
			ferramenta = entidade
		} else if conv == ConvOpenInference {
			ferramenta = nomeSpan
		}
	}
	return curto(agente), curto(ferramenta)
}

// motivosFim lê os motivos de término. Na semconv é uma LISTA (gravada em JSON pelo
// gateway); no OpenInference e no legado, um texto por resposta.
func motivosFim(a map[string]string) []string {
	if v := a["gen_ai.response.finish_reasons"]; v != "" {
		var lista []string
		if strings.HasPrefix(v, "[") && json.Unmarshal([]byte(v), &lista) == nil {
			return limitarMotivos(lista)
		}
		return limitarMotivos(strings.Split(v, ","))
	}
	if v := primeiro(a, "llm.finish_reason", "gen_ai.completion.0.finish_reason", "ai.response.finishReason"); v != "" {
		return []string{curto(v)}
	}
	return nil
}

func limitarMotivos(l []string) []string {
	out := make([]string, 0, len(l))
	for _, m := range l {
		if m = curto(m); m != "" && len(out) < maxMotivosDeFim {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// primeiro devolve o valor da primeira chave presente e não vazia.
func primeiro(a map[string]string, chaves ...string) string {
	for _, k := range chaves {
		if v := strings.TrimSpace(a[k]); v != "" {
			return v
		}
	}
	return ""
}

// inteiro lê um contador de tokens. Valor negativo, fracionário absurdo ou texto é
// tratado como "não informado" — melhor do que gravar um número inventado.
func inteiro(a map[string]string, chaves ...string) *int64 {
	v := primeiro(a, chaves...)
	if v == "" {
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(v, 64)
		if ferr != nil || f != math.Trunc(f) {
			return nil
		}
		n = int64(f)
	}
	if n < 0 {
		return nil
	}
	return &n
}

func decimal(a map[string]string, chaves ...string) *float64 {
	v := primeiro(a, chaves...)
	if v == "" {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return &f
}

package genai

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// Chaves que carregam CONTEÚDO (prompt, resposta, argumentos e resultados de
// ferramenta, esquemas). Elas nunca ficam nos rótulos do span: o gateway as tira,
// e o conteúdo só é gravado — na tabela própria, com redação e prazo curto — quando o
// operador liga o modo de conteúdo. Esquecer uma chave aqui é vazar prompt para uma
// tabela que fica 15 dias e que todo leitor do painel enxerga.
var chavesDeConteudo = map[string]bool{
	"gen_ai.input.messages":        true,
	"gen_ai.output.messages":       true,
	"gen_ai.system_instructions":   true,
	"gen_ai.tool.definitions":      true,
	"gen_ai.tool.call.arguments":   true,
	"gen_ai.tool.call.result":      true,
	"gen_ai.prompt":                true,
	"gen_ai.completion":            true,
	"input.value":                  true,
	"output.value":                 true,
	"traceloop.entity.input":       true,
	"traceloop.entity.output":      true,
	"tool.parameters":              true,
	"llm.prompt_template.template": true,
	"ai.response.text":             true,
	"ai.response.toolCalls":        true,
	"ai.response.object":           true,
	"ai.response.reasoning":        true,
	"ai.toolCall.args":             true,
	"ai.toolCall.result":           true,
	"ai.value":                     true,
	"ai.values":                    true,
	"ai.embedding":                 true,
	"ai.embeddings":                true,
	"reranker.query":               true,
}

var prefixosDeConteudo = []string{
	"gen_ai.prompt.", "gen_ai.completion.",
	"llm.input_messages.", "llm.output_messages.",
	"llm.prompts.", "llm.completions.",
	"llm.tools.", "llm.prompt_template.",
	"ai.prompt", // ai.prompt, ai.prompt.messages, ai.prompt.tools…
	// OpenInference: os spans de busca, embedding e reordenação carregam o TEXTO dos
	// documentos e das consultas (em RAG, quase sempre dado do cliente).
	"retrieval.documents.", "reranker.input_documents.", "reranker.output_documents.",
	"embedding.embeddings.",
}

// EhChaveDeConteudo diz se a chave carrega conteúdo de prompt/resposta. O gateway a
// aplica a TODO span, reconhecido como IA ou não: um span sem marcador de IA que ainda
// assim traga gen_ai.prompt.0.content não pode deixar o prompt nos rótulos.
func EhChaveDeConteudo(k string) bool {
	if chavesDeConteudo[k] {
		return true
	}
	for _, p := range prefixosDeConteudo {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// mensagens extrai o conteúdo no formato do dialeto. A ordem dentro de cada lado é a
// da conversa; entrada vem antes de saída.
func mensagens(conv, op string, a map[string]string, eventos []Evento) []Mensagem {
	var ms []Mensagem
	switch conv {
	case ConvOpenInference:
		ms = mensagensOpenInference(op, a)
	case ConvVercel:
		ms = mensagensVercel(a)
	default:
		ms = mensagensOTel(a)
		if len(ms) == 0 {
			ms = mensagensIndexadas(a, "gen_ai.prompt.", "gen_ai.completion.", ".content")
		}
		if len(ms) == 0 {
			ms = mensagensIndexadas(a, "llm.prompts.", "llm.completions.", ".content")
		}
		if len(ms) == 0 {
			ms = soltas(a, "traceloop.entity.input", "traceloop.entity.output", "")
		}
	}
	if len(ms) == 0 {
		ms = mensagensDeEventos(eventos)
	}
	return numerar(ms)
}

// mensagensOTel lê o formato atual da semconv: listas JSON de {role, parts}.
func mensagensOTel(a map[string]string) []Mensagem {
	var ms []Mensagem
	if v := a["gen_ai.system_instructions"]; v != "" {
		ms = append(ms, Mensagem{Lado: ladoEntrada, Papel: papelSistema, Texto: textoDePartes(v)})
	}
	ms = append(ms, listaOTel(ladoEntrada, a["gen_ai.input.messages"])...)
	ms = append(ms, listaOTel(ladoSaida, a["gen_ai.output.messages"])...)
	ms = append(ms, soltas(a, "gen_ai.tool.call.arguments", "gen_ai.tool.call.result", papelFerramenta)...)
	return ms
}

type mensagemOTel struct {
	Role  string            `json:"role"`
	Parts []json.RawMessage `json:"parts"`
}

func listaOTel(lado, bruto string) []Mensagem {
	if bruto == "" {
		return nil
	}
	var lista []mensagemOTel
	if err := json.Unmarshal([]byte(bruto), &lista); err != nil {
		// Formato inesperado: guardar o texto cru é melhor que perder a mensagem.
		return []Mensagem{{Lado: lado, Texto: bruto}}
	}
	out := make([]Mensagem, 0, len(lista))
	for _, m := range lista {
		partes := make([]string, 0, len(m.Parts))
		for _, p := range m.Parts {
			partes = append(partes, textoDeParte(p))
		}
		out = append(out, Mensagem{Lado: lado, Papel: m.Role, Texto: strings.Join(partes, "\n")})
	}
	return out
}

// textoDePartes aceita tanto uma lista de partes quanto um texto simples.
func textoDePartes(bruto string) string {
	var partes []json.RawMessage
	if json.Unmarshal([]byte(bruto), &partes) != nil {
		return bruto
	}
	out := make([]string, 0, len(partes))
	for _, p := range partes {
		out = append(out, textoDeParte(p))
	}
	return strings.Join(out, "\n")
}

// textoDeParte transforma uma parte da semconv em texto legível. Texto vira texto;
// pedido de ferramenta vira "→ nome(argumentos)"; resposta de ferramenta, "← resultado".
// Qualquer outro tipo fica em JSON — sem inventar uma leitura para ele.
func textoDeParte(p json.RawMessage) string {
	var parte struct {
		Type      string          `json:"type"`
		Content   json.RawMessage `json:"content"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Response  json.RawMessage `json:"response"`
		Result    json.RawMessage `json:"result"`
	}
	if json.Unmarshal(p, &parte) != nil {
		return string(p)
	}
	switch parte.Type {
	case "text", "reasoning", "thinking":
		return comoTexto(parte.Content)
	case "tool_call":
		return "→ " + parte.Name + "(" + comoTexto(parte.Arguments) + ")"
	case "tool_call_response":
		r := parte.Response
		if len(r) == 0 {
			r = parte.Result
		}
		return "← " + comoTexto(r)
	}
	return string(p)
}

// comoTexto devolve uma string JSON sem as aspas, e qualquer outro valor como JSON.
func comoTexto(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	return string(v)
}

// mensagensIndexadas lê o formato achatado do legado: <prefixo>N.role / N.content.
func mensagensIndexadas(a map[string]string, prefEntrada, prefSaida, sufixo string) []Mensagem {
	ms := indexadas(a, ladoEntrada, prefEntrada, ".role", sufixo)
	return append(ms, indexadas(a, ladoSaida, prefSaida, ".role", sufixo)...)
}

func indexadas(a map[string]string, lado, prefixo, chavePapel, chaveTexto string) []Mensagem {
	idx := indices(a, prefixo)
	out := make([]Mensagem, 0, len(idx))
	for _, i := range idx {
		base := prefixo + strconv.Itoa(i)
		texto := a[base+chaveTexto]
		if texto == "" {
			continue
		}
		out = append(out, Mensagem{Lado: lado, Papel: a[base+chavePapel], Texto: texto})
	}
	return out
}

// mensagensOpenInference: llm.input_messages.N.message.{role,content}, com pedidos de
// ferramenta em …message.tool_calls.K.tool_call.function.{name,arguments}. Sem as
// mensagens estruturadas (spans de ferramenta, de cadeia), usa input.value/output.value.
func mensagensOpenInference(op string, a map[string]string) []Mensagem {
	ms := mensagensOI(a, ladoEntrada, "llm.input_messages.")
	ms = append(ms, mensagensOI(a, ladoSaida, "llm.output_messages.")...)
	if len(ms) > 0 {
		return ms
	}
	papel := ""
	if op == OpFerramenta {
		papel = papelFerramenta
	}
	return soltas(a, "input.value", "output.value", papel)
}

func mensagensOI(a map[string]string, lado, prefixo string) []Mensagem {
	idx := indices(a, prefixo)
	out := make([]Mensagem, 0, len(idx))
	for _, i := range idx {
		base := prefixo + strconv.Itoa(i) + ".message."
		partes := []string{}
		if t := a[base+"content"]; t != "" {
			partes = append(partes, t)
		}
		for _, j := range indices(a, base+"contents.") {
			if t := a[base+"contents."+strconv.Itoa(j)+".message_content.text"]; t != "" {
				partes = append(partes, t)
			}
		}
		for _, k := range indices(a, base+"tool_calls.") {
			tc := base + "tool_calls." + strconv.Itoa(k) + ".tool_call.function."
			partes = append(partes, "→ "+a[tc+"name"]+"("+a[tc+"arguments"]+")")
		}
		if len(partes) == 0 {
			continue
		}
		out = append(out, Mensagem{Lado: lado, Papel: a[base+"role"], Texto: strings.Join(partes, "\n")})
	}
	return out
}

// soltas devolve a entrada e a saída guardadas como um texto único cada.
func soltas(a map[string]string, chaveEntrada, chaveSaida, papel string) []Mensagem {
	var ms []Mensagem
	if v := a[chaveEntrada]; v != "" {
		ms = append(ms, Mensagem{Lado: ladoEntrada, Papel: papel, Texto: v})
	}
	if v := a[chaveSaida]; v != "" {
		ms = append(ms, Mensagem{Lado: ladoSaida, Papel: papel, Texto: v})
	}
	return ms
}

// mensagensDeEventos cobre as versões da semconv que mandavam o conteúdo em span
// events: o evento de detalhes da operação (com as mesmas listas JSON dos atributos)
// e os antigos gen_ai.<papel>.message / gen_ai.choice.
func mensagensDeEventos(eventos []Evento) []Mensagem {
	var ms []Mensagem
	for _, e := range eventos {
		a := e.Atributos
		if a["gen_ai.input.messages"] != "" || a["gen_ai.output.messages"] != "" {
			ms = append(ms, mensagensOTel(a)...)
			continue
		}
		papel, ok := papelDoEvento(e.Nome)
		if !ok {
			continue
		}
		texto := primeiro(a, "content", "message", "gen_ai.event.content")
		if texto == "" {
			continue
		}
		lado := ladoEntrada
		if e.Nome == "gen_ai.choice" {
			lado = ladoSaida
		}
		ms = append(ms, Mensagem{Lado: lado, Papel: papel, Texto: texto})
	}
	return ms
}

func papelDoEvento(nome string) (string, bool) {
	switch nome {
	case "gen_ai.system.message":
		return papelSistema, true
	case "gen_ai.user.message":
		return "user", true
	case "gen_ai.assistant.message", "gen_ai.choice":
		return "assistant", true
	case "gen_ai.tool.message":
		return papelFerramenta, true
	}
	return "", false
}

// indices devolve, em ordem numérica, os N distintos das chaves "<prefixo>N.…".
// Ordem numérica e não de texto: "10" vem depois de "9".
func indices(a map[string]string, prefixo string) []int {
	vistos := map[int]bool{}
	for k := range a {
		if !strings.HasPrefix(k, prefixo) {
			continue
		}
		resto := k[len(prefixo):]
		fim := strings.IndexByte(resto, '.')
		if fim < 0 {
			fim = len(resto)
		}
		if n, err := strconv.Atoi(resto[:fim]); err == nil && n >= 0 {
			vistos[n] = true
		}
	}
	out := make([]int, 0, len(vistos))
	for n := range vistos {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// numerar dá a cada mensagem a sua posição final (entrada antes de saída).
func numerar(ms []Mensagem) []Mensagem {
	sort.SliceStable(ms, func(i, j int) bool {
		return ms[i].Lado == ladoEntrada && ms[j].Lado != ladoEntrada
	})
	for i := range ms {
		ms[i].Ordem = i
	}
	return ms
}

// mensagensVercel lê o formato da integração antiga do Vercel AI SDK: ai.prompt.messages
// (lista {role, content}), ou ai.prompt ({"messages": …} / {"prompt": "…"}), mais
// ai.response.text; na ferramenta, ai.toolCall.args/result.
func mensagensVercel(a map[string]string) []Mensagem {
	ms := soltas(a, "ai.toolCall.args", "ai.toolCall.result", papelFerramenta)
	if len(ms) > 0 {
		return ms
	}
	ms = listaVercel(a["ai.prompt.messages"])
	if len(ms) == 0 && a["ai.prompt"] != "" {
		var p struct {
			Messages json.RawMessage `json:"messages"`
			Prompt   string          `json:"prompt"`
			System   string          `json:"system"`
		}
		if json.Unmarshal([]byte(a["ai.prompt"]), &p) != nil {
			ms = []Mensagem{{Lado: ladoEntrada, Texto: a["ai.prompt"]}}
		} else {
			if p.System != "" {
				ms = append(ms, Mensagem{Lado: ladoEntrada, Papel: papelSistema, Texto: p.System})
			}
			ms = append(ms, listaVercel(string(p.Messages))...)
			if p.Prompt != "" {
				ms = append(ms, Mensagem{Lado: ladoEntrada, Papel: "user", Texto: p.Prompt})
			}
		}
	}
	if t := a["ai.response.text"]; t != "" {
		ms = append(ms, Mensagem{Lado: ladoSaida, Papel: "assistant", Texto: t})
	}
	if t := a["ai.response.toolCalls"]; t != "" {
		ms = append(ms, Mensagem{Lado: ladoSaida, Papel: "assistant", Texto: t})
	}
	return ms
}

// listaVercel: [{role, content}] em que content é texto ou lista de partes
// ({type: text, text} | {type: tool-call, toolName, input} | {type: tool-result, output}).
func listaVercel(bruto string) []Mensagem {
	if bruto == "" || bruto == "null" {
		return nil
	}
	var lista []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal([]byte(bruto), &lista) != nil {
		return []Mensagem{{Lado: ladoEntrada, Texto: bruto}}
	}
	out := make([]Mensagem, 0, len(lista))
	for _, m := range lista {
		out = append(out, Mensagem{Lado: ladoEntrada, Papel: m.Role, Texto: textoVercel(m.Content)})
	}
	return out
}

func textoVercel(c json.RawMessage) string {
	var s string
	if json.Unmarshal(c, &s) == nil {
		return s
	}
	var partes []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		ToolName string          `json:"toolName"`
		Input    json.RawMessage `json:"input"`
		Output   json.RawMessage `json:"output"`
	}
	if json.Unmarshal(c, &partes) != nil {
		return string(c)
	}
	out := make([]string, 0, len(partes))
	for _, p := range partes {
		switch p.Type {
		case "text", "reasoning":
			out = append(out, p.Text)
		case "tool-call":
			out = append(out, "→ "+p.ToolName+"("+string(p.Input)+")")
		case "tool-result":
			out = append(out, "← "+string(p.Output))
		default:
			b, _ := json.Marshal(p)
			out = append(out, string(b))
		}
	}
	return strings.Join(out, "\n")
}

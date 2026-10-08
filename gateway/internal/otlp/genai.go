package otlp

import (
	"context"
	"strings"

	"github.com/eduardorarruda/revoada/core/genai"
	"github.com/eduardorarruda/revoada/core/redacao"
	"github.com/eduardorarruda/revoada/gateway/internal/model"
)

// ConteudoIA é o que o gateway faz com o CONTEÚDO das chamadas de IA (prompt,
// resposta, argumentos de ferramenta). Os metadados — modelo, tokens, latência, erro —
// são gravados sempre; o conteúdo, só quando o operador decide.
type ConteudoIA string

const (
	// ConteudoDesligado descarta o conteúdo antes de gravar qualquer coisa. Padrão:
	// prompt costuma trazer dado pessoal, e o painel entrega custo, latência e erro
	// sem ele.
	ConteudoDesligado ConteudoIA = "desligado"
	// ConteudoRedigido grava o conteúdo com as mesmas regras de redação dos logs
	// (chaves, tokens, JWT, senhas em URL, cartões).
	ConteudoRedigido ConteudoIA = "redigido"
	// ConteudoCompleto grava o conteúdo como chegou.
	ConteudoCompleto ConteudoIA = "completo"
)

// ParseConteudoIA lê REVOADA_GENAI_CONTEUDO. Valor desconhecido cai em desligado: um
// erro de digitação não pode ligar a gravação de prompts.
func ParseConteudoIA(v string) ConteudoIA {
	switch ConteudoIA(strings.ToLower(strings.TrimSpace(v))) {
	case ConteudoRedigido:
		return ConteudoRedigido
	case ConteudoCompleto:
		return ConteudoCompleto
	}
	return ConteudoDesligado
}

// Limites do conteúdo. Uma mensagem maior é cortada e marcada; quando o span inteiro
// passa do teto, as mensagens seguintes são descartadas e a última gravada é marcada.
// Nunca se recusa o span por causa do conteúdo: os metadados valem mais que o texto.
const (
	maxBytesMensagemIA = 32 << 10
	maxBytesSpanIA     = 256 << 10
)

// GenAISink grava as linhas normalizadas (implementado por chhttp.Client).
type GenAISink interface {
	InsertGenAISpans(ctx context.Context, rows []model.GenAISpan) error
	InsertGenAIConteudo(ctx context.Context, rows []model.GenAIConteudo) error
}

// GenAIEnqueuer e ConteudoEnqueuer enfileiram no batcher assíncrono.
type GenAIEnqueuer interface {
	Enqueue(rows []model.GenAISpan) bool
}

type ConteudoEnqueuer interface {
	Enqueue(rows []model.GenAIConteudo) bool
}

// SetGenAI liga a gravação das chamadas de IA. Os batchers são opcionais (sem eles, o
// insert é síncrono, como nos testes).
func (rc *Receiver) SetGenAI(sink GenAISink, spans GenAIEnqueuer, conteudo ConteudoEnqueuer, modo ConteudoIA) {
	rc.genaiSink, rc.genaiBatch, rc.conteudoBatch, rc.conteudo = sink, spans, conteudo, modo
}

// ingestGenAI grava as chamadas de IA dos spans já aceitos (depois da amostragem e da
// amarração de host — a linha herda o host final do span). Falha aqui não derruba a
// requisição: os spans já foram aceitos, e o SDK reenviaria tudo, duplicando-os.
func (rc *Receiver) ingestGenAI(ctx context.Context, spans []model.Span) {
	if rc.genaiSink == nil && rc.genaiBatch == nil {
		return
	}
	linhas, conteudo := linhasGenAI(spans, rc.conteudo)
	if len(linhas) == 0 {
		return
	}
	if rc.genaiBatch != nil {
		if !rc.genaiBatch.Enqueue(linhas) {
			rc.log.Warn("otlp: fila de chamadas de IA cheia; linhas descartadas", "linhas", len(linhas))
		}
	} else if err := rc.genaiSink.InsertGenAISpans(ctx, linhas); err != nil {
		rc.log.Warn("otlp: gravando chamadas de IA", "err", err)
	}
	if len(conteudo) == 0 {
		return
	}
	switch {
	case rc.conteudoBatch != nil:
		if !rc.conteudoBatch.Enqueue(conteudo) {
			rc.log.Warn("otlp: fila de conteúdo de IA cheia; conteúdo descartado", "linhas", len(conteudo))
		}
	case rc.genaiSink != nil:
		if err := rc.genaiSink.InsertGenAIConteudo(ctx, conteudo); err != nil {
			rc.log.Warn("otlp: gravando conteúdo de IA", "err", err)
		}
	default:
		rc.log.Warn("otlp: conteúdo de IA sem destino configurado; descartado", "linhas", len(conteudo))
	}
}

// linhasGenAI monta as linhas de genai_spans e, conforme o modo, as de genai_conteudo.
func linhasGenAI(spans []model.Span, modo ConteudoIA) ([]model.GenAISpan, []model.GenAIConteudo) {
	var linhas []model.GenAISpan
	var conteudo []model.GenAIConteudo
	for _, s := range spans {
		if s.GenAI == nil {
			continue
		}
		var trechos []model.GenAIConteudo
		if modo == ConteudoRedigido || modo == ConteudoCompleto {
			trechos = conteudoDoSpan(s, modo)
		}
		linhas = append(linhas, model.GenAISpan{
			TenantID: s.TenantID, TS: s.TS, TraceID: s.TraceID, SpanID: s.SpanID,
			ParentID: s.ParentID, Service: s.Service, Host: s.Labels["host"], Nome: s.Name,
			DuracaoMs: s.DurationMs, Chamada: *s.GenAI, ComConteudo: len(trechos) > 0,
		})
		conteudo = append(conteudo, trechos...)
	}
	return linhas, conteudo
}

func conteudoDoSpan(s model.Span, modo ConteudoIA) []model.GenAIConteudo {
	out := make([]model.GenAIConteudo, 0, len(s.GenAI.Mensagens))
	usado := 0
	for _, m := range s.GenAI.Mensagens {
		texto := m.Texto
		redigido := false
		if modo == ConteudoRedigido {
			r := redacao.Texto(texto)
			redigido = r != texto
			texto = r
		}
		truncado := false
		if len(texto) > maxBytesMensagemIA {
			texto, truncado = genai.CortarUTF8(texto, maxBytesMensagemIA), true
		}
		if usado+len(texto) > maxBytesSpanIA {
			if len(out) > 0 {
				out[len(out)-1].Truncado = true
			}
			break
		}
		usado += len(texto)
		out = append(out, model.GenAIConteudo{
			TenantID: s.TenantID, TS: s.TS, TraceID: s.TraceID, SpanID: s.SpanID,
			Lado: m.Lado, Papel: m.Papel, Ordem: m.Ordem, Texto: texto,
			Truncado: truncado, Redigido: redigido,
		})
	}
	return out
}

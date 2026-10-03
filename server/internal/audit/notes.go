package audit

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// Anotações de RESULTADO na trilha.
//
// O middleware sabe o que foi PEDIDO (corpo da requisição) e o código HTTP. Não
// sabe o que ACONTECEU: `deleted_rows` só existe no corpo da RESPOSTA e era
// descartado. Sem esse número, meses depois ninguém distingue uma limpeza de 3
// linhas de uma que levou 400 mil — que é a diferença entre higiene e
// encobrimento. Este arquivo dá ao handler um canal de volta para a trilha:
//
//	audit.Annotate(ctx, "deleted_rows", n)
//
// O middleware funde as anotações no payload sob a chave "_resultado". Chamar
// Annotate fora do middleware (teste, job interno) é no-op — nunca entra no
// caminho de erro do handler.

// maxNotes limita quantas chaves um handler pode anotar. A trilha guarda um
// resumo do resultado, não um dump: um handler com laço não pode transformar
// uma linha de auditoria num blob.
const maxNotes = 32

// notesKey é a chave (privada) do bloco de anotações no contexto.
type notesKey struct{}

// notes é o bloco de anotações de uma requisição. Tem mutex porque um handler
// pode anotar de dentro de goroutines (ex.: expurgo por tabela em paralelo).
type notes struct {
	mu sync.Mutex
	m  map[string]any
}

// withNotes devolve um contexto com bloco de anotações. Chamado só pelo middleware.
func withNotes(ctx context.Context) context.Context {
	return context.WithValue(ctx, notesKey{}, &notes{m: make(map[string]any, 4)})
}

// Annotate registra um dado do RESULTADO da operação na trilha de auditoria.
// Seguro para chamar sempre: sem middleware (ou com chave vazia) é no-op.
func Annotate(ctx context.Context, key string, val any) {
	if ctx == nil || key == "" {
		return
	}
	n, ok := ctx.Value(notesKey{}).(*notes)
	if !ok || n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, exists := n.m[key]; !exists && len(n.m) >= maxNotes {
		return
	}
	n.m[key] = val
}

// mergeNotes funde as anotações no payload já redigido, sob "_resultado". As
// anotações também passam pela redação: um handler pode, sem querer, anotar um
// campo sensível, e a regra "a trilha nunca guarda segredo" vale para os dois
// lados.
func mergeNotes(ctx context.Context, payload map[string]any) map[string]any {
	n, ok := ctx.Value(notesKey{}).(*notes)
	if !ok || n == nil {
		return payload
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.m) == 0 {
		return payload
	}
	if payload == nil {
		payload = map[string]any{}
	}
	out := make(map[string]any, len(n.m))
	keys := make([]string, 0, len(n.m))
	for k := range n.m {
		keys = append(keys, k)
	}
	sort.Strings(keys) // ordem estável só para facilitar leitura/diff da trilha
	for _, k := range keys {
		if redactedFields[strings.ToLower(k)] {
			out[k] = redactedMark
			continue
		}
		out[k] = redactValue(n.m[k])
	}
	payload["_resultado"] = out
	return payload
}

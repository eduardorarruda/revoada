package dashboards

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

// Travessão virou vírgula nos textos do painel (decisão de estilo).
// Os títulos gerados já nascem com vírgula; esta rotina traz os painéis gravados
// antes disso para o mesmo padrão.
//
// Duas regras, as duas aprendidas numa revisão:
//   - só mexe no que o SISTEMA gerou. O UID "host-*" não prova origem (qualquer
//     pessoa pode criar "host-notas"); a prova é a versão 1 ter sido criada pelo
//     "sistema", ou o título seguir o padrão exato do gerador;
//   - salva pelo UpdateDashboard: versão nova, registrada no histórico, assinada
//     pelo "sistema". Um UPDATE cru deixaria a versão N com um conteúdo que o
//     histórico da versão N não tem, e "restaurar a versão atual" desfaria a troca.
const (
	travessao     = " — "
	virgula       = ", "
	autorSistema  = "sistema"
	prefixoTitulo = "Visão do Host" + travessao
)

type normalizacaoStore interface {
	PaineisComTravessao(ctx context.Context) ([]store.PainelComTravessao, error)
	UpdateDashboard(ctx context.Context, uid, title, folder string, model json.RawMessage, by string) (int, error)
}

// geradoPeloSistema diz se o painel nasceu do gerador: versão 1 do "sistema"
// (reconcile, semeadura) ou título no padrão do botão "Criar Visão do Host", que
// grava a versão 1 no nome de quem clicou.
func geradoPeloSistema(p store.PainelComTravessao) bool {
	if p.AutorV1 == autorSistema {
		return true
	}
	return strings.HasPrefix(p.UID, HostDashboardPrefix) && strings.HasPrefix(p.Title, prefixoTitulo)
}

// NormalizarTravessao troca " — " por ", " nos painéis gerados pelo sistema e
// devolve quantos salvou. Idempotente: sem travessão, não há candidato.
func NormalizarTravessao(ctx context.Context, st normalizacaoStore) (int, error) {
	candidatos, err := st.PaineisComTravessao(ctx)
	if err != nil {
		return 0, fmt.Errorf("listando painéis com travessão: %w", err)
	}
	n := 0
	for _, p := range candidatos {
		if !geradoPeloSistema(p) {
			continue
		}
		// O travessão nunca é caractere estrutural do JSON, então a troca no texto
		// do modelo só alcança conteúdo de string (títulos e descrições).
		modelo := bytes.ReplaceAll(p.Model, []byte(travessao), []byte(virgula))
		titulo := strings.ReplaceAll(p.Title, travessao, virgula)
		if _, err := st.UpdateDashboard(ctx, p.UID, titulo, p.Folder, modelo, autorSistema); err != nil {
			return n, fmt.Errorf("salvando %s: %w", p.UID, err)
		}
		n++
	}
	return n, nil
}

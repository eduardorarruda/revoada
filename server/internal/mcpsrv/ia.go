package mcpsrv

import (
	"context"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/ia"
)

// Ferramentas de IA: um agente (inclusive o próprio agente monitorado, depurando a si
// mesmo) lê custo, erros e o passo a passo das execuções. Nenhuma altera nada.

const (
	horasPadraoIA = 24
	horasMaximoIA = 24 * 90
)

type janelaIA struct {
	Horas int `json:"horas,omitempty" jsonschema:"janela em horas até agora (padrão 24, máximo 2160)"`
}

type execucoesIA struct {
	Horas   int    `json:"horas,omitempty" jsonschema:"janela em horas até agora (padrão 24, máximo 2160)"`
	Agente  string `json:"agente,omitempty" jsonschema:"só execuções deste agente"`
	SoErros bool   `json:"so_erros,omitempty" jsonschema:"só execuções com algum erro"`
	Limite  int    `json:"limite,omitempty" jsonschema:"máximo de execuções (padrão 50, máximo 500)"`
}

type porTrace struct {
	TraceID string `json:"trace_id" jsonschema:"trace_id da execução (veja listar_execucoes)"`
}

func filtrosIA(horas int) ia.Filtros {
	if horas <= 0 {
		horas = horasPadraoIA
	}
	agora := time.Now()
	return ia.Filtros{De: agora.Add(-time.Duration(min(horas, horasMaximoIA)) * time.Hour), Ate: agora}
}

func (s *Servidor) registrarIA() {
	ferramenta(s, "ia_resumo", EscopoLeitura,
		"Resumo das chamadas de IA das aplicações monitoradas: custo (estimado pela tabela de preços, ou informado), chamadas, "+
			"erros, tokens e latência, por modelo, por agente e por ferramenta. custo_usd null = sem preço ou sem tokens, nunca zero.", true,
		func(ctx context.Context, _ string, in janelaIA) (ia.Resumo, error) {
			return s.ia.Resumo(ctx, filtrosIA(in.Horas))
		})
	ferramenta(s, "listar_execucoes", EscopoLeitura,
		"Lista as execuções de agentes de IA mais recentes (um trace cada): agente, duração, chamadas de modelo e de ferramenta, "+
			"erros, tokens e custo.", true,
		func(ctx context.Context, _ string, in execucoesIA) (Lista[ia.Execucao], error) {
			lim := in.Limite
			if lim <= 0 {
				lim = 50
			}
			f := filtrosIA(in.Horas)
			f.Agente = in.Agente
			ex, err := s.ia.Execucoes(ctx, ia.FiltroExecucoes{Filtros: f, SoErros: in.SoErros, Limite: min(lim, 500)})
			return lista(ex), err
		})
	ferramenta(s, "ver_execucao", EscopoLeitura,
		"Passo a passo de uma execução de agente de IA: cada chamada de modelo e de ferramenta, em ordem, com tokens, custo, "+
			"latência e erro, e as ferramentas repetidas em sequência (possível loop). Sem o texto da conversa.", true,
		func(ctx context.Context, _ string, in porTrace) (ia.Replay, error) {
			return s.ia.Execucao(ctx, in.TraceID, nil)
		})
	ferramenta(s, "ler_conteudo_execucao", EscopoIAConteudo,
		"Texto gravado da conversa de uma execução (prompts, respostas, argumentos de ferramenta), se o painel grava conteúdo. "+
			"Exige o escopo ia_conteudo; o texto pode vir redigido.", true,
		func(ctx context.Context, _ string, in porTrace) (Lista[ia.Mensagem], error) {
			ms, err := s.ia.Conteudo(ctx, in.TraceID, nil)
			return lista(ms), err
		})
}

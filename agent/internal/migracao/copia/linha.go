package copia

import (
	"github.com/eduardorarruda/revoada/core/migracao/esquema"
	"github.com/eduardorarruda/revoada/core/migracao/plano"
	"github.com/eduardorarruda/revoada/core/migracao/transformar"
)

// colunasDestino são as colunas gravadas, na ordem do mapeamento.
func colunasDestino(p plano.Passo) []esquema.Coluna {
	out := make([]esquema.Coluna, 0, len(p.Map.Colunas))
	for _, mc := range p.Map.Colunas {
		c, _ := p.Destino.Coluna(mc.ColunaDestino) // Passos já garantiu que existe
		out = append(out, *c)
	}
	return out
}

// problema de uma linha: em qual coluna e por quê.
type problema struct {
	coluna string
	vio    *transformar.Violacao
}

// converter aplica as transformações e ajusta cada valor ao destino. É o MESMO
// caminho na simulação e na execução.
func converter(p plano.Passo, dest []esquema.Coluna, ln transformar.Linha) ([]any, []string, *problema) {
	valores := make([]any, len(p.Map.Colunas))
	var perdas []string
	for i, mc := range p.Map.Colunas {
		v, err := transformar.Aplicar(mc, ln)
		if err != nil {
			return nil, nil, &problema{coluna: mc.ColunaDestino,
				vio: &transformar.Violacao{Tipo: transformar.VioTransformar, Mensagem: mc.ColunaDestino + ": " + err.Error()}}
		}
		aj, perda, vio := transformar.Ajustar(v, dest[i])
		if vio != nil {
			return nil, nil, &problema{coluna: mc.ColunaDestino, vio: vio}
		}
		if perda != "" {
			perdas = append(perdas, perda)
		}
		valores[i] = aj
	}
	return valores, perdas, nil
}

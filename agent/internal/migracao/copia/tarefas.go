package copia

import (
	"context"

	"github.com/eduardorarruda/revoada/core/migracao/plano"
)

// Manipulador roda um tipo de tarefa a partir do JSON da especificação.
type Manipulador func(ctx context.Context, especificacao []byte, r Relator) (any, error)

// Tarefas são os tipos de tarefa da migração. O main liga cada um no executor do
// canal; só rodam os que o dono do servidor liberou em canal.tarefas_permitidas.
func Tarefas() map[string]Manipulador {
	return map[string]Manipulador{
		plano.TarefaSimular:  Simular,
		plano.TarefaExecutar: Executar,
		plano.TarefaReverter: Reverter,
		// só leitura nos dois lados: compara origem e destino linha a linha
		plano.TarefaVerificar: Verificar,
	}
}

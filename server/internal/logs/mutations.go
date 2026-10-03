package logs

import (
	"context"
	"fmt"

	"github.com/eduardorarruda/revoada/server/internal/chquery"
)

// Estado de mutation do ClickHouse, em um lugar só.
//
// Existe porque "o comando foi aceito" NÃO é "os dados sumiram": um ALTER … DELETE
// devolve 200 assim que a mutation entra na fila e pode falhar depois, em silêncio
// (part corrompida, disco cheio, `latest_fail_reason` preenchido). Quem apaga dado
// de cliente precisa CONFERIR. Este helper é o que o expurgo de logs já fazia e o
// que a exclusão de servidor (hostadmin) passou a fazer também — mesmo código, para
// os dois contarem a mesma verdade.

// MutationState é o estado de UMA mutation.
type MutationState struct {
	Table      string `json:"table"`
	MutationID string `json:"mutation_id"`
	Done       bool   `json:"done"`
	PartsToDo  int64  `json:"parts_to_do"`
	FailReason string `json:"fail_reason"`
}

// MutationStatus lê o estado de uma mutation em `table`. Com id vazio devolve a
// mais recente da tabela (que é a que acabamos de disparar). Sem nenhuma mutation
// registrada, devolve Done=true — não há nada pendente.
func MutationStatus(ctx context.Context, ch *chquery.Client, table, id string) (MutationState, error) {
	return mutationStatus(ctx, ch, table, id, "")
}

// MutationStatusDe é a versão que sabe QUAL mutation é a nossa quando não temos o id.
//
// "A mais recente da tabela" mente quando há concorrência, e há: apagar um servidor
// dispara seis mutations e a varredura de rescaldo dispara mais seis aos 90 s e aos
// 8 min, tudo nas MESMAS tabelas. Dois servidores apagados em paralelo — ou um
// rescaldo caindo dentro da janela de espera de outra exclusão — faziam o painel ler
// o estado da mutation ALHEIA. Se a alheia fosse pequena e já concluída, a resposta
// dizia "apagado" para uma exclusão cujos dados continuavam inteiros. Agrava: a coluna
// `create_time` do ClickHouse tem granularidade de 1 s, então duas mutations criadas
// no mesmo segundo desempatam de forma indefinida.
//
// `trecho` é procurado no texto do comando (o hostname, por exemplo), o que separa a
// nossa mutation da do vizinho sem depender de relógio.
func MutationStatusDe(ctx context.Context, ch *chquery.Client, table, trecho string) (MutationState, error) {
	return mutationStatus(ctx, ch, table, "", trecho)
}

func mutationStatus(ctx context.Context, ch *chquery.Client, table, id, trecho string) (MutationState, error) {
	st := MutationState{Table: table}
	where := "database=currentDatabase() AND table=" + quote(table)
	if id != "" {
		where += " AND mutation_id=" + quote(id)
	}
	if trecho != "" {
		where += " AND command LIKE " + quote("%"+escapeLike(trecho)+"%")
	}
	rows, err := ch.QueryJSON(ctx, fmt.Sprintf(`SELECT mutation_id, is_done, parts_to_do,
		latest_fail_reason FROM system.mutations WHERE %s ORDER BY create_time DESC LIMIT 1`, where))
	if err != nil {
		return st, err
	}
	if len(rows) == 0 {
		st.Done = true
		return st, nil
	}
	m := rows[0]
	st.MutationID = asString(m["mutation_id"])
	st.Done = asInt64(m["is_done"]) == 1
	st.PartsToDo = asInt64(m["parts_to_do"])
	st.FailReason = asString(m["latest_fail_reason"])
	return st, nil
}

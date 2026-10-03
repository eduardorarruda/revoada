package alerting

import (
	"context"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

// motivoFusaoFingerprint é o que fica gravado em alert_events.resolved_by quando duas
// linhas abertas viram a mesma identidade depois do recálculo. Texto, e não NULL, para
// a auditoria não confundir com resolução manual de operador.
const motivoFusaoFingerprint = "migração de fingerprint (duplicata do mesmo incidente)"

// fpAction é o que fazer com UMA linha aberta de alert_events na migração.
type fpAction struct {
	ID   int64
	From string // fingerprint gravado hoje
	To   string // fingerprint no esquema atual
	// Merge=true quando outra linha aberta MAIS ANTIGA já ocupa `To`: esta linha é
	// redundante e fecha calada (MergedInto é a que fica).
	Merge      bool
	MergedInto int64
}

// planFingerprintMigration decide, sem tocar no banco, o que fazer com cada alerta
// aberto. Fica separado da execução porque é a parte perigosa (reescrever a IDENTIDADE
// de um alerta ativo) e precisa ser testável sem Postgres.
//
// `rows` tem de vir ordenado por (started_at, id) — é assim que ListOpenAlertRows
// entrega — para que o dono de uma identidade fundida seja sempre o alerta MAIS ANTIGO:
// é ele que carrega o started_at verdadeiro do incidente.
//
// Linhas já no esquema atual não geram ação: é isso que torna a migração idempotente e
// barata a cada boot (a partir do segundo boot o plano sai vazio).
func planFingerprintMigration(rows []store.OpenAlertRow) []fpAction {
	dono := make(map[string]int64, len(rows))
	var out []fpAction
	for _, row := range rows {
		novo := fingerprint(row.RuleID, row.Labels)
		if first, existe := dono[novo]; existe {
			out = append(out, fpAction{ID: row.ID, From: row.Fingerprint, To: novo, Merge: true, MergedInto: first})
			continue
		}
		dono[novo] = row.ID
		if row.Fingerprint == novo {
			continue // já no esquema atual (o caso normal a partir do segundo boot)
		}
		out = append(out, fpAction{ID: row.ID, From: row.Fingerprint, To: novo})
	}
	return out
}

// MigrateOpenFingerprints reescreve o fingerprint dos alertas AINDA ABERTOS para o
// esquema atual (rule_id + labels de identidade, ver stableLabels/volatileLabels).
//
// Por que isto existe — e por que roda no boot, antes do primeiro ciclo:
//
// O fingerprint é a identidade do alerta, e o esquema dele MUDA. Nesta entrega ele
// passou a ignorar volatileLabels (state/health/exit_code/image/…) e o gateway passou a
// não mandar mais agent.version/kernel/arch/platform/os/host.cpu.cores/host.ips. As
// linhas de alert_events que já estavam abertas continuavam gravadas com o fingerprint
// ANTIGO — e nada as reescrevia. O resultado, medido em dev com série contínua (6
// amostras/min em todos os baldes) e uma linha pré-existente no esquema antigo:
//
//   - 20:30:15 — evalRule calcula o fingerprint NOVO, não acha alerta ativo com ele e
//     abre uma DUPLICATA: duas linhas `firing` para o mesmo problema, "DISPAROU" de
//     novo no canal (notificações 229/230);
//   - 20:33:45 — o reconcile, que enxerga o fingerprint ANTIGO fora de `seen`, cumpre a
//     carência e encerra a linha velha com motivo "sem dados": um "📡 SEM DADOS"
//     FALSO para um host que nunca parou de reportar (notificações 231/232).
//
// Ou seja: no primeiro ciclo pós-deploy, CADA alerta ativo em produção rendia uma
// duplicata e um falso "sem dados". A correção é reescrever o fingerprint a partir da
// coluna `labels`, que já está gravada — o dado necessário nunca faltou.
//
// Propriedades exigidas (todas testadas em planFingerprintMigration):
//   - IDEMPOTENTE: no segundo boot os fingerprints já batem e nada é escrito;
//   - SEGURA a cada boot: o UPDATE é condicionado ao valor antigo e a `ended_at IS
//     NULL`, então uma linha já encerrada (ou já migrada) nunca é tocada;
//   - determinística quanto a FUSÕES: o novo esquema pode colapsar duas linhas abertas
//     numa identidade só (é exatamente o caso do container.running com state/exit_code
//     diferentes). Vence a MAIS ANTIGA e as demais fecham caladas, sem notificar: não
//     houve mudança no mundo, só a fusão de linhas que sempre foram o mesmo incidente.
//
// Devolve quantas linhas foram reescritas e quantas foram fundidas.
func (e *Evaluator) MigrateOpenFingerprints(ctx context.Context) (reescritos, fundidos int, err error) {
	rows, err := e.st.ListOpenAlertRows(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, a := range planFingerprintMigration(rows) {
		if a.Merge {
			if err := e.st.CloseRedundantAlert(ctx, a.ID, motivoFusaoFingerprint); err != nil {
				return reescritos, fundidos, err
			}
			fundidos++
			e.log.Info("migração de fingerprint: alerta redundante encerrado",
				"id", a.ID, "mantido", a.MergedInto, "fingerprint", a.To)
			continue
		}
		if err := e.st.SetAlertFingerprint(ctx, a.ID, a.From, a.To); err != nil {
			return reescritos, fundidos, err
		}
		reescritos++
		e.log.Info("migração de fingerprint: alerta aberto reindexado", "id", a.ID, "de", a.From, "para", a.To)
	}
	return reescritos, fundidos, nil
}

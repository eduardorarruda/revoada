package alerting

import (
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

// fpAntigo reproduz o esquema ANTIGO do fingerprint: o mapa de labels INTEIRO, sem
// tirar volatileLabels. É com isso que as linhas abertas em produção estão gravadas.
func fpAntigo(ruleID int64, labels map[string]string) string {
	semExclusao := map[string]string{}
	for k, v := range labels {
		semExclusao[k] = v
	}
	// fingerprint() já exclui volatileLabels; para simular o esquema antigo, renomeia as
	// chaves voláteis para nomes que ele não conhece — o efeito é o mesmo: elas entram.
	out := map[string]string{}
	for k, v := range semExclusao {
		if volatileLabels[k] {
			k = "z" + k
		}
		out[k] = v
	}
	return fingerprint(ruleID, out)
}

// TestPlanFingerprintMigration é a prova do item 1: as linhas JÁ ABERTAS têm de mudar
// de identidade no boot, antes do primeiro ciclo.
//
// Sem isso, no primeiro ciclo pós-deploy, para CADA alerta ativo em produção: o evalRule
// calcula o fingerprint NOVO, não acha alerta ativo com ele e abre uma DUPLICATA
// ("🟠 ALERTA" de novo); em paralelo o reconcile vê o fingerprint ANTIGO fora de `seen`
// e, passada a carência, encerra com "sem dados" — "📡 SEM DADOS" falso para um host que
// nunca parou de reportar. Medido ao vivo em dev com série contínua (6 amostras/min em
// todos os baldes): duas linhas firing e quatro notificações para um problema só.
//
// Em produção: servidor com "CPU > 90" disparado às 03h, deploy às 04h → 04h02
// "DISPAROU" de novo, 04h05 "SEM DADOS", com a CPU ainda em 100%.
func TestPlanFingerprintMigration(t *testing.T) {
	t0 := time.Date(2026, 8, 9, 3, 0, 0, 0, time.UTC)
	labels := map[string]string{
		"host": "srv1", "container": "api",
		// Labels que o esquema NOVO ignora e o antigo carregava dentro da identidade.
		"state": "running", "agent.version": "1.4.2", "kernel": "6.1.0",
	}
	linha := store.OpenAlertRow{ID: 10, RuleID: 7, Fingerprint: fpAntigo(7, labels), Labels: labels, StartedAt: t0}
	if linha.Fingerprint == fingerprint(7, labels) {
		t.Fatal("o cenário é inválido: o fingerprint antigo tem de ser diferente do atual")
	}

	acoes := planFingerprintMigration([]store.OpenAlertRow{linha})
	if len(acoes) != 1 {
		t.Fatalf("esperava 1 reescrita, veio %d", len(acoes))
	}
	if acoes[0].Merge {
		t.Fatal("uma linha sozinha nunca é duplicata")
	}
	if acoes[0].To != fingerprint(7, labels) {
		t.Fatalf("destino = %q; esperava o fingerprint do esquema ATUAL %q", acoes[0].To, fingerprint(7, labels))
	}

	// IDEMPOTENTE: com o fingerprint já no esquema atual, o segundo boot não escreve
	// nada. É o que torna seguro rodar a migração a cada boot.
	linha.Fingerprint = acoes[0].To
	if got := planFingerprintMigration([]store.OpenAlertRow{linha}); len(got) != 0 {
		t.Fatalf("segundo boot deveria não fazer nada, planejou %d ações", len(got))
	}
}

// TestPlanFingerprintMigrationColapso cobre a colisão: o esquema novo pode fundir duas
// linhas abertas numa identidade só (é exatamente o caso do container.running com
// state/exit_code diferentes). Quem fica é a MAIS ANTIGA — é ela que carrega o
// started_at verdadeiro, e o started_at é o que mantém a continuidade do incidente na
// tela e na duração da mensagem de resolução.
func TestPlanFingerprintMigrationColapso(t *testing.T) {
	t0 := time.Date(2026, 8, 9, 3, 0, 0, 0, time.UTC)
	base := map[string]string{"host": "srv1", "container": "api"}
	rodando := map[string]string{"host": "srv1", "container": "api", "state": "running"}
	morto := map[string]string{"host": "srv1", "container": "api", "state": "exited", "exit_code": "137"}

	// Ordenadas por (started_at, id), como ListOpenAlertRows entrega.
	rows := []store.OpenAlertRow{
		{ID: 10, RuleID: 7, Fingerprint: fpAntigo(7, rodando), Labels: rodando, StartedAt: t0},
		{ID: 11, RuleID: 7, Fingerprint: fpAntigo(7, morto), Labels: morto, StartedAt: t0.Add(time.Hour)},
	}
	acoes := planFingerprintMigration(rows)
	if len(acoes) != 2 {
		t.Fatalf("esperava 2 ações (reescrever a antiga, fundir a nova), veio %d", len(acoes))
	}
	if acoes[0].ID != 10 || acoes[0].Merge {
		t.Fatalf("a linha MAIS ANTIGA tem de ficar com a identidade: %+v", acoes[0])
	}
	if acoes[0].To != fingerprint(7, base) {
		t.Fatalf("a identidade nova tem de ignorar state/exit_code, veio %q", acoes[0].To)
	}
	if !acoes[1].Merge || acoes[1].MergedInto != 10 {
		t.Fatalf("a linha mais nova tem de fechar como duplicata da 10: %+v", acoes[1])
	}
}

package agents

import (
	"strings"
	"testing"
)

// TestMaskKeyNaoDevolveChaveUsavel: a listagem de chaves devolvia a frota inteira em
// claro — uma sessão de admin comprometida colhia num GET a credencial de ingestão de
// TODOS os servidores (medido em dev: 6 chaves completas em `GET /api/agents`). O
// mascarado precisa deixar reconhecer a chave e NÃO deixar usá-la.
func TestMaskKeyNaoDevolveChaveUsavel(t *testing.T) {
	const key = "dev-local-dae73d010bf2771e63183546"
	got := maskKey(key)
	if got == key {
		t.Fatal("a chave saiu inteira")
	}
	if strings.Contains(key, got) {
		t.Fatalf("o mascarado é um pedaço contíguo da chave: %q", got)
	}
	if !strings.HasPrefix(got, "dev-") {
		t.Errorf("precisa dar para reconhecer a chave na lista: %q", got)
	}
	if len(got) > 12 {
		t.Errorf("mascarado longo demais (%d chars): %q", len(got), got)
	}
	// Chave curta não pode vazar por outro caminho.
	if maskKey("abc") != "***" {
		t.Error("chave curta tem de virar ***")
	}
	if maskKey("") != "" {
		t.Error("chave vazia continua vazia")
	}
}

// TestAgentIDEstavelEDerivado: o id público substitui a serverkey nas rotas de
// revogar/apagar/segurar. Precisa ser estável (a UI o guarda entre telas), único por
// chave e NÃO reversível — senão trocamos o vazamento de lugar.
func TestAgentIDEstavelEDerivado(t *testing.T) {
	const a, b = "chave-do-web01", "chave-do-web02"
	// Duas chamadas separadas, guardadas em variáveis: comparar `agentID(a)` com ele
	// mesmo na mesma expressão é comparação que o compilador enxerga como trivial e o
	// staticcheck reprova — e, pior, não provaria estabilidade nenhuma.
	primeira, segunda := agentID(a), agentID(a)
	if primeira != segunda {
		t.Errorf("o id tem de ser estável entre chamadas: %q e %q", primeira, segunda)
	}
	if agentID(a) == agentID(b) {
		t.Error("chaves diferentes não podem ter o mesmo id")
	}
	id := agentID(a)
	if len(id) != 16 {
		t.Errorf("id deveria ter 16 hex, veio %d (%q)", len(id), id)
	}
	if strings.Contains(id, "chave") || strings.Contains(a, id) {
		t.Errorf("o id não pode carregar pedaço da chave: %q", id)
	}
}

// TestVersaoPlausivel: um pin com lixo dentro é PIOR que pin nenhum — o painel
// responderia "frota fixada em <lixo>" para sempre e a frota pararia de atualizar sem
// ninguém entender por quê.
func TestVersaoPlausivel(t *testing.T) {
	bons := []string{"0.9.1", "1.0", "v1.2.3", "0.9.1-rc1", "10.20.30"}
	ruins := []string{"", "latest", "0.9.x", "1.2.3.4", "..", "v", "1.2.3-", "0,9,1"}
	for _, v := range bons {
		if !versaoPlausivel(v) {
			t.Errorf("%q deveria ser aceita", v)
		}
	}
	for _, v := range ruins {
		if versaoPlausivel(v) {
			t.Errorf("%q NÃO deveria ser aceita", v)
		}
	}
}

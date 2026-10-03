package migracao

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/eduardorarruda/revoada/core/migracao/plano"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// A verificação lê a origem E o destino: recebe as duas senhas (reverter, só a do
// destino).
func TestCredenciaisDaVerificacao(t *testing.T) {
	dest := "cx_destino"
	p := store.ProjetoMigracao{OrigemID: "cx_origem", DestinoID: &dest}
	got := credenciaisDaTarefa(plano.TarefaVerificar, p)
	if len(got) != 2 || got[0][0] != "origem" || got[1][0] != "destino" {
		t.Fatalf("verificar: %v", got)
	}
	if got := credenciaisDaTarefa(plano.TarefaReverter, p); len(got) != 1 || got[0][0] != "destino" {
		t.Fatalf("reverter: %v", got)
	}
}

// Quem não pode executar migração vê o placar, nunca as chaves das linhas.
func TestSemChavesParaQuemNaoExecuta(t *testing.T) {
	res := plano.ResumoVerificacao{ConteudoOK: false, Divergentes: 1, Tabelas: []plano.ResumoVerificacaoTabela{{
		Destino: "clientes", Divergentes: 1, Divergencias: []plano.Divergencia{{Chave: "maria@x.com", Colunas: []string{"email"}}}}}}
	b, _ := json.Marshal(res)
	e := semChaves(store.ExecucaoMigracao{Tipo: "verificar", Tarefa: store.Tarefa{Resumo: b,
		Erro: "clientes: divergente. Divergências na linha de chave maria@x.com"}})
	tudo, _ := json.Marshal(e)
	if strings.Contains(string(tudo), "maria@x.com") {
		t.Fatalf("vazou a chave: %s", tudo)
	}
	if !strings.Contains(string(tudo), `"divergentes":1`) || !strings.Contains(string(tudo), "email") {
		t.Fatalf("o placar e a coluna ficam: %s", tudo)
	}
}

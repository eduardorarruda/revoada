package plano

import (
	"bytes"
	"encoding/json"
	"testing"
)

// Quem não pode ver chaves (MCP, perfis só de leitura) recebe o resumo sem nenhuma —
// nas amostras da simulação e nas divergências da execução e da verificação.
func TestOcultarChaves(t *testing.T) {
	const cpf = "123.456.789-00"
	div := []Divergencia{{Chave: cpf, Colunas: []string{"nome"}}, {Chave: cpf, Ausente: true}}
	casos := map[string]any{
		"simular":  RelatorioSimulacao{Tabelas: []RelatorioTabela{{Amostras: []Amostra{{Chave: cpf, Coluna: "nome", Tipo: "nulo"}}}}},
		"executar": ResumoExecucao{Tabelas: []ResumoTabela{{Destino: "clientes", Divergencias: div, ColunasConferidas: []string{"nome"}}}},
		"verificar": ResumoVerificacao{Tabelas: []ResumoVerificacaoTabela{{Destino: "clientes", Divergencias: div, PorColuna: map[string]int64{"nome": 1},
			Texto: &ConferenciaTexto{Divergencias: div, Colunas: []string{"nome"}}}}},
	}
	for tipo, v := range casos {
		b, _ := json.Marshal(v)
		out := OcultarChaves(tipo, b)
		if bytes.Contains(out, []byte(cpf)) {
			t.Fatalf("%s: vazou a chave: %s", tipo, out)
		}
		if !bytes.Contains(out, []byte("nome")) {
			t.Fatalf("%s: o nome da coluna pode (e deve) ficar: %s", tipo, out)
		}
	}
	if out := OcultarChaves("verificar", []byte(`{"tabelas":"formato-novo","chave":"`+cpf+`"}`)); out != nil {
		t.Fatalf("formato inesperado tem de sair vazio: %s", out)
	}
	if out := OcultarChaves("reverter", []byte(`{"tabelas":["x"]}`)); string(out) != `{"tabelas":["x"]}` {
		t.Fatalf("resumo sem chave passa como está: %s", out)
	}
	if e := OcultarChaveErro("clientes: conteúdo divergente. Divergências na linha de chave Maria: Ltda, 7", "(oculta)"); e != "clientes: conteúdo divergente. Divergências na linha de chave (oculta)" {
		t.Fatalf("erro: %q", e)
	}
}

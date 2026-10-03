package sugestao

import (
	"testing"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
	"github.com/eduardorarruda/revoada/core/migracao/modelo"
)

func col(nome string, tipo esquema.TipoLogico, tam int, nulavel bool) esquema.Coluna {
	return esquema.Coluna{Nome: nome, Tipo: tipo, Tamanho: tam, Nulavel: nulavel}
}

// origem: um ERP Firebird típico; destino: um schema PostgreSQL novo, em inglês/minúsculas.
func fixtures() (esquema.Esquema, esquema.Esquema) {
	origem := esquema.Esquema{Motor: "firebird", Charset: "WIN1252", Tabelas: []esquema.Tabela{
		{Nome: "TB_CLIENTES", ChavePrimaria: []string{"CODIGO"}, Colunas: []esquema.Coluna{
			col("CODIGO", esquema.Inteiro, 0, false), col("NOME", esquema.Texto, 60, false),
			col("DT_CADASTRO", esquema.Data, 0, true), col("FONE", esquema.Texto, 20, true),
		}},
		{Nome: "PEDIDOS", ChavePrimaria: []string{"ID"}, Estrangeiras: []esquema.ChaveEstrangeira{{TabelaRef: "TB_CLIENTES"}},
			Colunas: []esquema.Coluna{col("ID", esquema.Inteiro, 0, false), col("VALOR", esquema.Decimal, 0, true)}},
		{Nome: "LOG_SISTEMA", Colunas: []esquema.Coluna{col("MSG", esquema.TextoLongo, 0, true)}},
	}}
	destino := esquema.Esquema{Motor: "postgres", Charset: "UTF8", Tabelas: []esquema.Tabela{
		{Nome: "customers", ChavePrimaria: []string{"id"}, Colunas: []esquema.Coluna{
			col("id", esquema.Inteiro, 0, false), col("name", esquema.Texto, 40, false),
			col("created_at", esquema.DataHora, 0, true), col("phone", esquema.Texto, 30, true),
		}},
		{Nome: "pedidos", ChavePrimaria: []string{"id"}, Colunas: []esquema.Coluna{
			col("id", esquema.Inteiro, 0, false), col("valor", esquema.Decimal, 0, true),
		}},
	}}
	return origem, destino
}

func TestNormalizarESimilaridade(t *testing.T) {
	casos := map[[2]string]bool{
		{"TB_CLIENTES", "customers"}: true, {"DT_CADASTRO", "created_at"}: true, {"FONE", "phone"}: true,
		{"Descrição", "description"}: true, {"NOME", "valor"}: false,
		{"CLIENTE", "customer_id"}: true, {"COD_PRODUTO", "product_id"}: true, {"ID", "customer_id"}: false,
	}
	for par, igual := range casos {
		s := Similaridade(par[0], par[1])
		if igual && s < 0.9 || !igual && s >= 0.6 {
			t.Errorf("%v: similaridade %.2f", par, s)
		}
	}
}

func TestSugerirMapeiaTabelasEColunas(t *testing.T) {
	origem, destino := fixtures()
	m := Sugerir(origem, destino)
	if len(m.Tabelas) != 3 {
		t.Fatalf("todas as tabelas de origem: %d", len(m.Tabelas))
	}
	por := map[string]modelo.MapTabela{}
	for _, mt := range m.Tabelas {
		por[mt.TabelaOrigem] = mt
	}
	cli := por["TB_CLIENTES"]
	if cli.Acao != modelo.Copiar || cli.TabelaDestino != "customers" || cli.ColunaLote != "CODIGO" {
		t.Fatalf("clientes: %+v", cli)
	}
	pares := map[string]string{}
	for _, c := range cli.Colunas {
		pares[c.ColunaOrigem] = c.ColunaDestino
	}
	esperado := map[string]string{"CODIGO": "id", "NOME": "name", "DT_CADASTRO": "created_at", "FONE": "phone"}
	for o, d := range esperado {
		if pares[o] != d {
			t.Errorf("%s → %q, esperado %q (%v)", o, pares[o], d, pares)
		}
	}
	for _, c := range cli.Colunas {
		if c.ColunaOrigem == "NOME" && c.Alerta == "" {
			t.Error("NOME 60 → name 40 deveria alertar truncamento")
		}
	}
	if por["LOG_SISTEMA"].Acao != modelo.CriarNoDestino || por["LOG_SISTEMA"].TabelaDestino != "log_sistema" {
		t.Fatalf("tabela sem par deveria ser criada no destino: %+v", por["LOG_SISTEMA"])
	}
	// clientes antes de pedidos (FK)
	if por["TB_CLIENTES"].Ordem > por["PEDIDOS"].Ordem {
		t.Fatal("ordem de carga deveria respeitar a FK")
	}
	// a sugestão em si passa na validação (pode ter avisos, não erros)
	if ps := m.Validar(origem, destino); modelo.TemErro(ps) {
		t.Fatalf("a sugestão deveria ser válida: %+v", ps)
	}
}

func TestValidarPegaErrosComuns(t *testing.T) {
	origem, destino := fixtures()
	m := modelo.Mapeamento{Tabelas: []modelo.MapTabela{{
		TabelaOrigem: "TB_CLIENTES", TabelaDestino: "customers", Acao: modelo.Copiar, ColunaLote: "NOME",
		Colunas: []modelo.MapColuna{
			{ColunaOrigem: "CODIGO", ColunaDestino: "id", Transformacao: modelo.Nenhuma},
			{ColunaOrigem: "NAO_EXISTE", ColunaDestino: "phone", Transformacao: modelo.Nenhuma},
			{ColunaOrigem: "DT_CADASTRO", ColunaDestino: "id", Transformacao: "sql_livre"},
		},
	}}}
	ps := m.Validar(origem, destino)
	quer := map[string]bool{"coluna de origem": false, "obrigatória": false, "desconhecida": false, "não é única": false, "sem decisão": false}
	for _, p := range ps {
		for k := range quer {
			if contains(p.Mensagem, k) {
				quer[k] = true
			}
		}
	}
	for k, achou := range quer {
		if !achou {
			t.Errorf("esperava problema com %q em %+v", k, ps)
		}
	}
	if !modelo.TemErro(ps) {
		t.Fatal("deveria ter erro")
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

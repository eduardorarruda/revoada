package captura

import (
	"context"
	"os"
	"testing"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
	"github.com/eduardorarruda/revoada/core/migracao/sugestao"
)

// Integração com bancos de verdade (Docker). Rodam só com as variáveis:
//
//	REVOADA_TEST_FB=127.0.0.1:53050  (banco /firebird/data/erp.fdb, SYSDBA/masterkey)
//	REVOADA_TEST_PG_HOST=127.0.0.1:55432 (usuário teste/teste, banco revoada)
func TestCapturarFirebird25(t *testing.T) {
	end := os.Getenv("REVOADA_TEST_FB")
	if end == "" {
		t.Skip("REVOADA_TEST_FB não definido")
	}
	e, err := Capturar(context.Background(), Conexao{Motor: "firebird", Endereco: end, Banco: "/firebird/data/erp.fdb",
		Usuario: "SYSDBA", Senha: "masterkey", Opcoes: map[string]string{"charset": "WIN1252"}})
	if err != nil {
		t.Fatal(err)
	}
	if e.Charset != "WIN1252" || e.Dialeto != 3 || len(e.Versao) < 3 || e.Versao[:3] != "2.5" {
		t.Fatalf("metadados do banco: versão %q charset %q dialeto %d", e.Versao, e.Charset, e.Dialeto)
	}
	cli, ok := e.Tabela("TB_CLIENTES")
	if !ok || len(cli.ChavePrimaria) != 1 || cli.ChavePrimaria[0] != "CODIGO" {
		t.Fatalf("TB_CLIENTES: %+v", cli)
	}
	esperado := map[string]esquema.TipoLogico{"CODIGO": esquema.Inteiro, "NOME": esquema.Texto, "DT_CADASTRO": esquema.Data,
		"LIMITE": esquema.Decimal, "OBS": esquema.TextoLongo}
	for nome, tipo := range esperado {
		c, ok := cli.Coluna(nome)
		if !ok || c.Tipo != tipo {
			t.Errorf("%s: %+v (quer %s)", nome, c, tipo)
		}
	}
	if c, _ := cli.Coluna("NOME"); c.Tamanho != 60 || c.Nulavel || c.Charset != "WIN1252" {
		t.Errorf("NOME: %+v", c)
	}
	if c, _ := cli.Coluna("LIMITE"); c.Precisao != 15 || c.Escala != 2 || c.TipoNativo != "NUMERIC(15,2)" {
		t.Errorf("LIMITE: %+v", c)
	}
	itens, _ := e.Tabela("PEDIDO_ITENS")
	if len(itens.ChavePrimaria) != 2 || len(itens.Estrangeiras) != 2 {
		t.Fatalf("PEDIDO_ITENS: pk %v fks %+v", itens.ChavePrimaria, itens.Estrangeiras)
	}
	refs := map[string]bool{}
	for _, fk := range itens.Estrangeiras {
		refs[fk.TabelaRef] = true
	}
	if !refs["PEDIDOS"] || !refs["PRODUTOS"] {
		t.Fatalf("FKs de PEDIDO_ITENS: %+v", itens.Estrangeiras)
	}
	prod, _ := e.Tabela("PRODUTOS")
	if !prod.Unica("CODBARRAS") {
		t.Error("CODBARRAS tem UNIQUE")
	}
	if c, _ := prod.Coluna("FOTO"); c.Tipo != esquema.Binario {
		t.Errorf("FOTO: %+v", c)
	}
	if e.Hash() == "" {
		t.Fatal("hash vazio")
	}
}

func TestCapturarPostgresESugerirAPartirDoFirebird(t *testing.T) {
	fb, pg := os.Getenv("REVOADA_TEST_FB"), os.Getenv("REVOADA_TEST_PG_HOST")
	if fb == "" || pg == "" {
		t.Skip("REVOADA_TEST_FB/REVOADA_TEST_PG_HOST não definidos")
	}
	ctx := context.Background()
	dst := Conexao{Motor: "postgres", Endereco: pg, Banco: "revoada", Usuario: "teste", Senha: "teste", Opcoes: map[string]string{"sslmode": "disable", "schema": "erp_destino"}}
	db, err := Abrir(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `
		DROP SCHEMA IF EXISTS erp_destino CASCADE; CREATE SCHEMA erp_destino;
		CREATE TABLE erp_destino.customers (id integer PRIMARY KEY, name varchar(40) NOT NULL, created_at timestamp, phone varchar(30));
		CREATE TABLE erp_destino.orders (id integer PRIMARY KEY, customer_id integer NOT NULL REFERENCES erp_destino.customers(id), total numeric(15,2));`); err != nil {
		t.Fatal(err)
	}
	destino, err := Capturar(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	ord, _ := destino.Tabela("orders")
	if len(ord.Estrangeiras) != 1 || ord.Estrangeiras[0].TabelaRef != "customers" || ord.Estrangeiras[0].ColunasRef[0] != "id" {
		t.Fatalf("FK no Postgres: %+v", ord.Estrangeiras)
	}
	origem, err := Capturar(ctx, Conexao{Motor: "firebird", Endereco: fb, Banco: "/firebird/data/erp.fdb", Usuario: "SYSDBA", Senha: "masterkey"})
	if err != nil {
		t.Fatal(err)
	}
	m := sugestao.Sugerir(origem, destino)
	pares := map[string]string{}
	for _, mt := range m.Tabelas {
		pares[mt.TabelaOrigem] = string(mt.Acao) + ":" + mt.TabelaDestino
	}
	if pares["TB_CLIENTES"] != "copiar:customers" || pares["PEDIDOS"] != "copiar:orders" || pares["PRODUTOS"] != "criar_no_destino:produtos" {
		t.Fatalf("sugestão de tabelas: %v", pares)
	}
}

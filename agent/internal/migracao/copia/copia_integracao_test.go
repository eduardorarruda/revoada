package copia

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/eduardorarruda/revoada/agent/internal/migracao/captura"
	"github.com/eduardorarruda/revoada/core/migracao/modelo"
	"github.com/eduardorarruda/revoada/core/migracao/plano"
	"github.com/eduardorarruda/revoada/core/migracao/sugestao"
	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
)

// Integração Firebird 2.5 → PostgreSQL com bancos de verdade (Docker). Roda só com:
//
//	REVOADA_TEST_FB=127.0.0.1:53050       (banco /firebird/data/erp.fdb, SYSDBA/masterkey)
//	REVOADA_TEST_PG_HOST=127.0.0.1:55432  (usuário teste/teste, banco revoada)
//
// Cria um schema de destino descartável e uma tabela extra no Firebird (RV_LOTE,
// 1 234 linhas) para exercitar lotes, cancelamento no meio e retomada.
//
// Rode com `go test -p 1 ./internal/migracao/...`: estes testes criam tabelas no
// mesmo erp.fdb que o teste de upgrade copia e conta, e em paralelo um vê as tabelas
// do outro pela metade (o upgrade acusa "tudo_confere": false).

type relatorTeste struct {
	mu          sync.Mutex
	senhas      map[string]string
	eventos     []string
	checkpoints int
	pausas      int
	pararNa     int // >0: a N-ésima Pausa devolve cancelamento (simula queda)
}

func (r *relatorTeste) Evento(_ string, _ agentev1.EventoTarefa_Nivel, msg string, _ float64, _ map[string]float64) {
	r.mu.Lock()
	r.eventos = append(r.eventos, msg)
	r.mu.Unlock()
}

func (r *relatorTeste) Pausa(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pausas++
	if r.pararNa > 0 && r.pausas == r.pararNa {
		return context.Canceled
	}
	return nil
}

func (r *relatorTeste) Credencial(nome string) ([]byte, error) {
	s, ok := r.senhas[nome]
	if !ok {
		return nil, fmt.Errorf("sem credencial %s", nome)
	}
	return []byte(s), nil
}

func (r *relatorTeste) Checkpoint(string, string, int64) {
	r.mu.Lock()
	r.checkpoints++
	r.mu.Unlock()
}

func novoRelator() *relatorTeste {
	return &relatorTeste{senhas: map[string]string{"origem": "masterkey", "destino": "teste"}}
}

func preparar(t *testing.T) (plano.Especificacao, *sql.DB, *sql.DB) {
	t.Helper()
	return prepararCom(t, extras{})
}

// extras: tabelas a mais para um teste (DDL/DML no Firebird, DDL no PostgreSQL com
// %[1]s no lugar do schema de destino) — e o que apagar do Firebird no fim.
type extras struct {
	fb      []string
	pg      []string
	apagaFB []string
}

func prepararCom(t *testing.T, ex extras) (plano.Especificacao, *sql.DB, *sql.DB) {
	t.Helper()
	fb, pg := os.Getenv("REVOADA_TEST_FB"), os.Getenv("REVOADA_TEST_PG_HOST")
	if fb == "" || pg == "" {
		t.Skip("REVOADA_TEST_FB / REVOADA_TEST_PG_HOST não definidos")
	}
	ctx := context.Background()
	esquemaPG := fmt.Sprintf("copia_teste_%d", os.Getpid())
	bo := plano.Banco{Motor: "firebird", Endereco: fb, Banco: "/firebird/data/erp.fdb", Usuario: "SYSDBA", Opcoes: map[string]string{"charset": "WIN1252"}}
	bd := plano.Banco{Motor: "postgres", Endereco: pg, Banco: "revoada", Usuario: "teste", Opcoes: map[string]string{"sslmode": "disable", "schema": esquemaPG}}
	dbo, err := captura.Abrir(captura.Conexao{Motor: bo.Motor, Endereco: bo.Endereco, Banco: bo.Banco, Usuario: bo.Usuario, Senha: "masterkey", Opcoes: bo.Opcoes})
	if err != nil {
		t.Fatal(err)
	}
	dbd, err := captura.Abrir(captura.Conexao{Motor: bd.Motor, Endereco: bd.Endereco, Banco: bd.Banco, Usuario: bd.Usuario, Senha: "teste", Opcoes: bd.Opcoes})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = dbd.Exec("DROP SCHEMA IF EXISTS " + esquemaPG + " CASCADE")
		_, _ = dbo.Exec("DROP TABLE RV_LOTE")
		for _, tb := range ex.apagaFB {
			_, _ = dbo.Exec("DROP TABLE " + tb)
		}
		dbo.Close()
		dbd.Close()
	})

	// origem: tabela extra com 1 234 linhas
	_, _ = dbo.Exec("DROP TABLE RV_LOTE")
	if _, err := dbo.Exec("CREATE TABLE RV_LOTE (ID INTEGER NOT NULL PRIMARY KEY, NOME VARCHAR(30) NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	tx, err := dbo.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 1234; i++ {
		if _, err := tx.Exec("INSERT INTO RV_LOTE VALUES (?, ?)", i, fmt.Sprintf("item número %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, tb := range ex.apagaFB {
		_, _ = dbo.Exec("DROP TABLE " + tb)
	}
	for _, q := range ex.fb {
		if _, err := dbo.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}

	// destino: customers COM dados (vai por staging), orders vazia (esvaziar)
	for _, q := range []string{
		"CREATE SCHEMA " + esquemaPG,
		"CREATE TABLE " + esquemaPG + ".customers (id integer PRIMARY KEY, name varchar(40) NOT NULL, created_at timestamp, phone varchar(30))",
		"CREATE TABLE " + esquemaPG + ".orders (id integer PRIMARY KEY, customer_id integer NOT NULL REFERENCES " + esquemaPG + ".customers(id), total numeric(15,2))",
		"INSERT INTO " + esquemaPG + ".customers VALUES (999, 'Cliente que já estava lá', NULL, NULL)",
	} {
		if _, err := dbd.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	for _, q := range ex.pg {
		if _, err := dbd.Exec(fmt.Sprintf(q, esquemaPG)); err != nil {
			t.Fatal(q, err)
		}
	}
	eo, err := captura.Capturar(ctx, captura.Conexao{Motor: bo.Motor, Endereco: bo.Endereco, Banco: bo.Banco, Usuario: bo.Usuario, Senha: "masterkey", Opcoes: bo.Opcoes})
	if err != nil {
		t.Fatal(err)
	}
	ed, err := captura.Capturar(ctx, captura.Conexao{Motor: bd.Motor, Endereco: bd.Endereco, Banco: bd.Banco, Usuario: bd.Usuario, Senha: "teste", Opcoes: bd.Opcoes})
	if err != nil {
		t.Fatal(err)
	}
	m := sugestao.Sugerir(eo, ed)
	for i := range m.Tabelas { // o vínculo que a pessoa faz no editor
		mt := &m.Tabelas[i]
		if mt.TabelaOrigem == "PEDIDOS" && !temDestino(mt, "customer_id") {
			mt.Colunas = append(mt.Colunas, modelo.MapColuna{ColunaOrigem: "CLIENTE", ColunaDestino: "customer_id", Transformacao: modelo.Nenhuma})
		}
	}
	if ps := m.Validar(eo, ed); modelo.TemErro(ps) {
		t.Fatalf("mapeamento com erro: %+v", ps)
	}
	esp := plano.Especificacao{Execucao: "ex_teste_" + esquemaPG, Origem: bo, Destino: bd, Mapeamento: m,
		HashMapeamento: m.Hash(), EsquemaOrigem: eo, EsquemaDestino: ed, Lote: 100}
	return esp, dbo, dbd
}

func temDestino(mt *modelo.MapTabela, col string) bool {
	for _, c := range mt.Colunas {
		if c.ColunaDestino == col {
			return true
		}
	}
	return false
}

func contar(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(q, err)
	}
	return n
}

func TestSimularExecutarRetomarReverter(t *testing.T) {
	esp, _, dbd := preparar(t)
	ctx := context.Background()
	s := esp.Destino.Opcoes["schema"]
	bruto, _ := json.Marshal(esp)

	// 1. Dry-run: nada é gravado e nenhuma linha seria recusada.
	r := novoRelator()
	out, err := Simular(ctx, bruto, r)
	if err != nil {
		t.Fatal(err)
	}
	rel := out.(*plano.RelatorioSimulacao)
	if rel.Bloqueantes != 0 || rel.TotalLinhas != 1234+5 {
		b, _ := json.MarshalIndent(rel, "", " ")
		t.Fatalf("simulação: %s", b)
	}
	estr := map[string]string{}
	for _, tb := range rel.Tabelas {
		estr[tb.Destino] = tb.Estrategia
		if tb.Destino == "customers" && tb.Perdas["suspeita_charset"] != 2 {
			t.Fatalf("a base de teste tem UTF-8 gravado em WIN1252; a simulação deveria avisar: %+v", tb.Perdas)
		}
	}
	if estr["customers"] != plano.EstrategiaStaging || estr["orders"] != plano.EstrategiaEsvaziar || estr["produtos"] != plano.EstrategiaApagarTabela {
		t.Fatalf("estratégias: %v", estr)
	}
	if n := contar(t, dbd, "SELECT count(*) FROM information_schema.tables WHERE table_schema = '"+s+"'"); n != 2 {
		t.Fatalf("a simulação não pode criar nada no destino (tabelas: %d)", n)
	}

	// A pessoa liga "reparar" no NOME (o editor mostra o aviso da simulação).
	for i := range esp.Mapeamento.Tabelas {
		for j := range esp.Mapeamento.Tabelas[i].Colunas {
			if c := &esp.Mapeamento.Tabelas[i].Colunas[j]; c.ColunaOrigem == "NOME" {
				c.Transformacao, c.Parametros = modelo.Charset, map[string]string{"de": "WIN1252", "reparar": "sim"}
			}
		}
	}
	esp.HashMapeamento = esp.Mapeamento.Hash()
	bruto, _ = json.Marshal(esp)

	// 2. Execução cai no meio (6ª pausa = 5 lotes confirmados de RV_LOTE ou antes).
	r = novoRelator()
	r.pararNa = 9
	if _, err := Executar(ctx, bruto, r); err == nil {
		t.Fatal("a execução deveria ter parado")
	}
	if r.checkpoints == 0 {
		t.Fatal("nenhum checkpoint antes da queda")
	}

	// 3. Retomada: mesma execução, continua do último lote, sem duplicar.
	r = novoRelator()
	out, err = Executar(ctx, bruto, r)
	if err != nil {
		t.Fatalf("retomada: %v (eventos %v)", err, r.eventos)
	}
	res := out.(*plano.ResumoExecucao)
	for _, tb := range res.Tabelas {
		if !tb.ChecksumOK {
			t.Errorf("%s sem checksum", tb.Destino)
		}
		// (c) retomada depois da queda: o conteúdo continua conferido, coluna a coluna
		if !tb.ConteudoOK || len(tb.ColunasConferidas) == 0 || len(tb.ColunasNaoConferidas) > 0 || len(tb.Divergencias) > 0 {
			t.Errorf("%s: conteúdo não conferido depois da retomada: %+v", tb.Destino, tb)
		}
	}
	if !strings.Contains(strings.Join(r.eventos, "\n"), "conteúdo conferidos") {
		t.Errorf("o fim da execução deveria dizer que o conteúdo foi conferido: %v", r.eventos)
	}
	if res.Manifesto.Fase != "concluido" {
		t.Fatalf("manifesto: %+v", res.Manifesto)
	}
	if n := contar(t, dbd, "SELECT count(*) FROM "+s+".rv_lote"); n != 1234 {
		t.Fatalf("rv_lote: %d linhas (queria 1234, sem duplicar)", n)
	}
	if n := contar(t, dbd, "SELECT count(*) FROM "+s+".customers"); n != 3 {
		t.Fatalf("customers: %d (999 + 2 migrados)", n)
	}
	var nome string
	if err := dbd.QueryRow("SELECT name FROM " + s + ".customers WHERE id = 1").Scan(&nome); err != nil || nome != "José da Conceição" {
		t.Fatalf("acentos: %q %v", nome, err)
	}
	if n := contar(t, dbd, "SELECT count(*) FROM information_schema.table_constraints WHERE table_schema = '"+s+"' AND table_name = 'pedido_itens' AND constraint_type = 'FOREIGN KEY'"); n != 2 {
		t.Fatalf("FKs recriadas em pedido_itens: %d", n)
	}
	if n := contar(t, dbd, "SELECT count(*) FROM information_schema.tables WHERE table_schema = '"+s+"' AND table_name LIKE 'revoada_stg%'"); n != 0 {
		t.Fatal("a staging deveria ter sido apagada")
	}

	// (d) Verificar depois da conclusão: zero divergências contra as tabelas reais
	// (customers tem a linha 999 que já estava lá: não entra na conta)...
	ver := verificar(t, bruto)
	if !ver.ConteudoOK || ver.Divergentes != 0 || ver.Ausentes != 0 || ver.Linhas != 1234+5 || ver.Identicas != ver.Linhas || ver.Fase != "concluido" {
		b, _ := json.MarshalIndent(ver, "", " ")
		t.Fatalf("verificação depois de concluir: %s", b)
	}
	for _, tb := range ver.Tabelas {
		if tb.Onde != "tabela" || len(tb.ColunasConferidas) == 0 {
			t.Fatalf("%s: %+v", tb.Destino, tb)
		}
	}
	// a dupla conferência pelo texto dos bancos também bate (sem falso alarme)
	if !ver.TextoOK || ver.ColunasTexto == 0 {
		b, _ := json.MarshalIndent(ver, "", " ")
		t.Fatalf("conferência pelo texto depois de concluir: %s", b)
	}
	// ... e acusa um UPDATE manual no destino, com a chave e a coluna certas.
	if _, err := dbd.Exec("UPDATE " + s + ".customers SET phone = '(11) 0000-0000' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	ver = verificar(t, bruto)
	cust := tabelaVerificada(t, ver, "customers")
	if ver.ConteudoOK || cust.ConteudoOK || cust.Divergentes != 1 || len(cust.Divergencias) != 1 ||
		cust.Divergencias[0].Chave != "1" || strings.Join(cust.Divergencias[0].Colunas, ",") != "phone" || cust.PorColuna["phone"] != 1 {
		b, _ := json.MarshalIndent(cust, "", " ")
		t.Fatalf("o UPDATE manual em customers.phone deveria ser acusado na chave 1: %s", b)
	}
	if b, _ := json.Marshal(ver); strings.Contains(string(b), "0000-0000") || strings.Contains(string(b), "Conceição") {
		t.Fatal("o relatório da verificação não pode trazer valores das linhas")
	}

	// 4. Uma execução concluída não roda de novo.
	if _, err := Executar(ctx, bruto, novoRelator()); err == nil || !strings.Contains(err.Error(), "concluída") {
		t.Fatalf("reexecutar: %v", err)
	}

	// 5. Reverter: o destino volta exatamente ao que era.
	out, err = Reverter(ctx, bruto, novoRelator())
	if err != nil {
		t.Fatal(err)
	}
	if n := contar(t, dbd, "SELECT count(*) FROM "+s+".customers"); n != 1 {
		t.Fatalf("customers depois de reverter: %d", n)
	}
	if n := contar(t, dbd, "SELECT count(*) FROM "+s+".orders"); n != 0 {
		t.Fatalf("orders depois de reverter: %d", n)
	}
	var sobrou []string
	rows, err := dbd.Query("SELECT table_name FROM information_schema.tables WHERE table_schema = $1 AND table_name NOT LIKE 'revoada_%' ORDER BY 1", s)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		sobrou = append(sobrou, n)
	}
	rows.Close()
	if strings.Join(sobrou, ",") != "customers,orders" {
		t.Fatalf("depois de reverter sobraram: %v", sobrou)
	}
	if _, err := Reverter(ctx, bruto, novoRelator()); err == nil {
		t.Fatal("reverter duas vezes deveria falhar")
	}
	_ = out
}

func TestSimulacaoApontaProblemasSemVazarDados(t *testing.T) {
	esp, _, dbd := preparar(t)
	s := esp.Destino.Opcoes["schema"]
	// destino mais estreito: os nomes migrados não cabem em 12 caracteres
	for _, q := range []string{"UPDATE " + s + ".customers SET name = 'Antigo'", "ALTER TABLE " + s + ".customers ALTER COLUMN name TYPE varchar(12)"} {
		if _, err := dbd.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for i := range esp.EsquemaDestino.Tabelas {
		if esp.EsquemaDestino.Tabelas[i].Nome == "customers" {
			c, _ := esp.EsquemaDestino.Tabelas[i].Coluna("name")
			c.Tamanho, c.TipoNativo = 12, "varchar(12)"
		}
	}
	bruto, _ := json.Marshal(esp)
	out, err := Simular(context.Background(), bruto, novoRelator())
	if err != nil {
		t.Fatal(err)
	}
	rel := out.(*plano.RelatorioSimulacao)
	if rel.Bloqueantes < 2 {
		t.Fatalf("queria os 2 clientes recusados: %+v", rel)
	}
	b, _ := json.Marshal(rel)
	if strings.Contains(string(b), "Conceição") || strings.Contains(string(b), "Padaria") {
		t.Fatal("o relatório não pode trazer o conteúdo das linhas")
	}
	// os pedidos do cliente 1 apontam para um pai que não entra: FK também acusada
	var fk bool
	for _, tb := range rel.Tabelas {
		if tb.Destino == "orders" && tb.Violacoes["estrangeira"] > 0 {
			fk = true
		}
	}
	if !fk {
		t.Fatalf("a FK de orders deveria ser acusada: %s", b)
	}
}

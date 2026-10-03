package copia

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/agent/internal/migracao/captura"
	"github.com/eduardorarruda/revoada/core/migracao/esquema"
	"github.com/eduardorarruda/revoada/core/migracao/modelo"
	"github.com/eduardorarruda/revoada/core/migracao/plano"
	"github.com/eduardorarruda/revoada/core/migracao/sugestao"
	"github.com/eduardorarruda/revoada/core/migracao/transformar"
)

// Integração da conferência de conteúdo (Firebird 2.5 → PostgreSQL). Mesmas
// variáveis de ambiente de copia_integracao_test.go.

func verificar(t *testing.T, bruto []byte) *plano.ResumoVerificacao {
	t.Helper()
	out, err := Verificar(context.Background(), bruto, novoRelator())
	if err != nil {
		t.Fatalf("verificar: %v", err)
	}
	return out.(*plano.ResumoVerificacao)
}

func tabelaVerificada(t *testing.T, v *plano.ResumoVerificacao, destino string) plano.ResumoVerificacaoTabela {
	t.Helper()
	for _, tb := range v.Tabelas {
		if tb.Destino == destino {
			return tb
		}
	}
	t.Fatalf("a verificação não trouxe %s", destino)
	return plano.ResumoVerificacaoTabela{}
}

func tabelaExecutada(t *testing.T, res *plano.ResumoExecucao, destino string) plano.ResumoTabela {
	t.Helper()
	for _, tb := range res.Tabelas {
		if tb.Destino == destino {
			return tb
		}
	}
	t.Fatalf("a execução não trouxe %s", destino)
	return plano.ResumoTabela{}
}

// (b) Um destino que guardou OUTRA coisa (aqui: uma célula mexida na staging entre a
// carga e a releitura) é pego antes da troca, com a chave e a coluna — e as tabelas
// reais ficam intactas.
func TestConteudoCorrompidoNaStagingBarraATroca(t *testing.T) {
	esp, _, dbd := preparar(t)
	s := esp.Destino.Opcoes["schema"]
	bruto, _ := json.Marshal(esp)
	ganchoAntesDeReler = func(ctx context.Context, db *sql.DB, alvo, tabela string) {
		if tabela != "customers" {
			return
		}
		if _, err := db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s.%q SET name = 'Outro nome' WHERE id = 1`, s, alvo)); err != nil {
			t.Errorf("corrompendo a staging: %v", err)
		}
	}
	t.Cleanup(func() { ganchoAntesDeReler = nil })

	out, err := Executar(context.Background(), bruto, novoRelator())
	if err == nil {
		t.Fatal("a execução tinha de falhar: o conteúdo da staging não é o que foi gravado")
	}
	if !strings.Contains(err.Error(), "name") || !strings.Contains(err.Error(), plano.MarcadorChave+"1") || !strings.Contains(err.Error(), "nada foi trocado") {
		t.Fatalf("o erro tem de dizer a coluna e a chave: %v", err)
	}
	res := out.(*plano.ResumoExecucao)
	cust := tabelaExecutada(t, res, "customers")
	if cust.ConteudoOK || len(cust.Divergencias) != 1 || cust.Divergencias[0].Chave != "1" || strings.Join(cust.Divergencias[0].Colunas, ",") != "name" {
		t.Fatalf("resumo de customers: %+v", cust)
	}
	if b, _ := json.Marshal(res); strings.Contains(string(b), "Outro nome") {
		t.Fatal("o resumo não pode trazer o valor da célula")
	}
	if n := contar(t, dbd, "SELECT count(*) FROM "+s+".customers"); n != 1 {
		t.Fatalf("a tabela real não podia ter sido tocada: %d linhas", n)
	}
	ganchoAntesDeReler = nil
	if _, err := Reverter(context.Background(), bruto, novoRelator()); err != nil {
		t.Fatalf("reverter depois da falha: %v", err)
	}
}

// (c') Checkpoint de um agente antigo (sem as somas do conteúdo): a tabela que estava
// no meio recomeça e é conferida; as que já estavam concluídas saem como "conteúdo
// não conferido" com o motivo — nunca um OK falso.
func TestCheckpointAntigoNuncaViraOK(t *testing.T) {
	esp, _, dbd := preparar(t)
	s := esp.Destino.Opcoes["schema"]
	bruto, _ := json.Marshal(esp)
	r := novoRelator()
	r.pararNa = 9
	if _, err := Executar(context.Background(), bruto, r); err == nil {
		t.Fatal("a execução deveria ter parado")
	}
	// como um agente antigo teria deixado: sem a coluna de conteúdo preenchida
	if _, err := dbd.Exec("UPDATE " + s + ".revoada_controle SET conteudo = ''"); err != nil {
		t.Fatal(err)
	}
	var meio int
	if err := dbd.QueryRow("SELECT count(*) FROM " + s + ".revoada_controle WHERE NOT concluida AND linhas > 0").Scan(&meio); err != nil {
		t.Fatal(err)
	}
	r = novoRelator()
	out, err := Executar(context.Background(), bruto, r)
	if err != nil {
		t.Fatalf("retomada: %v", err)
	}
	res := out.(*plano.ResumoExecucao)
	concluidasAntes := 0
	for _, tb := range res.Tabelas {
		if tb.ConteudoOK {
			if len(tb.ColunasConferidas) == 0 {
				t.Errorf("%s: OK sem coluna conferida", tb.Destino)
			}
			continue
		}
		concluidasAntes++
		if tb.ConteudoMotivo == "" || len(tb.ColunasConferidas) > 0 {
			t.Errorf("%s: sem conferência tem de vir com motivo e sem colunas conferidas: %+v", tb.Destino, tb)
		}
	}
	if concluidasAntes == 0 || concluidasAntes == len(res.Tabelas) {
		t.Fatalf("queria tabelas concluídas pelo 'agente antigo' (sem OK) e tabelas conferidas: %+v", res.Tabelas)
	}
	if meio > 0 && !strings.Contains(strings.Join(r.eventos, "\n"), "recomeçando a tabela para conferir o conteúdo") {
		t.Errorf("a tabela que estava no meio deveria recomeçar: %v", r.eventos)
	}
	if n := contar(t, dbd, "SELECT count(*) FROM "+s+".rv_lote"); n != 1234 {
		t.Fatalf("rv_lote: %d linhas (sem duplicar ao recomeçar)", n)
	}
	// e a verificação sob demanda cobre o que a execução não conferiu
	if ver := verificar(t, bruto); !ver.ConteudoOK || ver.Divergentes != 0 {
		t.Fatalf("verificação: %+v", ver)
	}
}

// (a) Muitos tipos de uma vez: o que o PostgreSQL faz de legítimo ao gravar (char(n)
// completa com espaços, numeric(12,2) arredonda, real vira float32, timestamptz,
// timestamp(0) arredonda — inclusive antes de 2000 —, uuid em minúsculas, jsonb
// reordena) NÃO é divergência; todas as colunas saem conferidas. Depois, mexer em
// uma célula é acusado pela verificação sob demanda.
func TestTiposVariadosConteudoConferido(t *testing.T) {
	esp, _, dbd := prepararCom(t, extras{
		apagaFB: []string{"RV_TIPOS"},
		fb: []string{
			`CREATE TABLE RV_TIPOS (ID INTEGER NOT NULL PRIMARY KEY, COD CHAR(6), VALOR NUMERIC(15,4), FATOR DOUBLE PRECISION,
			  MOMENTO TIMESTAMP, INSTANTE TIMESTAMP, HORA TIME, DIA DATE, OBS BLOB SUB_TYPE TEXT, ATIVO SMALLINT,
			  UID VARCHAR(36), DADOS BLOB SUB_TYPE TEXT, BIN BLOB SUB_TYPE BINARY)`,
			`INSERT INTO RV_TIPOS VALUES (1, 'ação', 12.3450, 0.1, '2024-03-09 10:30:00.1234', '2024-01-02 10:00:00.5678',
			  '15:04:05.1234', '2024-03-09', 'linha 1', 1, 'A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11', '{"b": 1, "a": [1.50, "x"]}', x'0001FF')`,
			`INSERT INTO RV_TIPOS VALUES (2, 'AB', -0.0050, 1234.5678901, '1999-12-31 23:59:58.5000', '1999-12-31 23:59:58.5000',
			  '00:00:00', '1999-12-31', 'segunda', 0, 'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a12', '[]', x'00')`,
			`INSERT INTO RV_TIPOS (ID) VALUES (3)`,
		},
		pg: []string{`CREATE TABLE %[1]s.rv_tipos (id integer PRIMARY KEY, cod character(8), valor numeric(12,2), fator real,
			momento timestamptz, instante timestamp(0), hora time, dia date, obs text, ativo boolean, uid uuid, dados jsonb, bin bytea)`},
	})
	s := esp.Destino.Opcoes["schema"]
	for i := range esp.Mapeamento.Tabelas { // texto → uuid: o vínculo que a pessoa faz no editor
		if mt := &esp.Mapeamento.Tabelas[i]; mt.TabelaOrigem == "RV_TIPOS" && !temDestino(mt, "uid") {
			mt.Colunas = append(mt.Colunas, modelo.MapColuna{ColunaOrigem: "UID", ColunaDestino: "uid", Transformacao: modelo.ConverterTipo})
		}
	}
	esp.HashMapeamento = esp.Mapeamento.Hash()
	bruto, _ := json.Marshal(esp)
	r := novoRelator()
	out, err := Executar(context.Background(), bruto, r)
	if err != nil {
		t.Fatalf("executar: %v (eventos %v)", err, r.eventos)
	}
	res := out.(*plano.ResumoExecucao)
	tipos := tabelaExecutada(t, res, "rv_tipos")
	if !tipos.ConteudoOK || len(tipos.ColunasConferidas) != 13 || len(tipos.ColunasNaoConferidas) != 0 {
		b, _ := json.MarshalIndent(tipos, "", " ")
		t.Fatalf("rv_tipos: todas as 13 colunas deveriam sair conferidas: %s", b)
	}
	// o que o PG guardou de fato (prova que a regra canônica modelou o arredondamento)
	var valor, instante string
	if err := dbd.QueryRow("SELECT valor::text, instante::text FROM "+s+".rv_tipos WHERE id = 2").Scan(&valor, &instante); err != nil {
		t.Fatal(err)
	}
	if valor != "-0.01" || instante != "1999-12-31 23:59:58" {
		t.Fatalf("o PG guardou valor=%s instante=%s", valor, instante)
	}

	ver := verificar(t, bruto)
	vt := tabelaVerificada(t, ver, "rv_tipos")
	if !ver.ConteudoOK || vt.Identicas != 3 || vt.Divergentes != 0 || len(vt.ColunasConferidas) != 13 {
		b, _ := json.MarshalIndent(vt, "", " ")
		t.Fatalf("verificação de rv_tipos: %s", b)
	}
	// a dupla conferência pelo texto dos bancos também bate em todos esses tipos: o que
	// o Firebird imprime (NUMERIC(15,4), DOUBLE com 16 algarismos, TIMESTAMP com 1/10000 s,
	// TIME) contra o que o PostgreSQL imprime (numeric(12,2), real, timestamptz no fuso
	// do agente, timestamp(0) — inclusive antes de 2000 —, time, date)
	if tx := vt.Texto; tx == nil || !tx.OK || !ver.TextoOK || tx.Identicas != 3 ||
		!contem(tx.Colunas, "id", "valor", "fator", "momento", "instante", "hora", "dia") || !contemFora(tx.Parciais, "bin") {
		b, _ := json.MarshalIndent(vt.Texto, "", " ")
		t.Fatalf("conferência pelo texto de rv_tipos: %s", b)
	}
	// um centavo a mais e uma hora de deslocamento são pegos, cada um na sua coluna;
	// regravar o char(8) com outro preenchimento de espaços NÃO é divergência
	for _, q := range []string{
		"UPDATE " + s + ".rv_tipos SET valor = valor + 0.01 WHERE id = 1",
		"UPDATE " + s + ".rv_tipos SET momento = momento + interval '1 hour', cod = 'AB ' WHERE id = 2",
	} {
		if _, err := dbd.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	vt = tabelaVerificada(t, verificar(t, bruto), "rv_tipos")
	if vt.ConteudoOK || vt.Divergentes != 2 || vt.Identicas != 1 || len(vt.PorColuna) != 2 ||
		vt.PorColuna["valor"] != 1 || vt.PorColuna["momento"] != 1 || len(vt.Divergencias) != 2 ||
		vt.Divergencias[0].Chave != "1" || strings.Join(vt.Divergencias[0].Colunas, ",") != "valor" ||
		vt.Divergencias[1].Chave != "2" || strings.Join(vt.Divergencias[1].Colunas, ",") != "momento" {
		b, _ := json.MarshalIndent(vt, "", " ")
		t.Fatalf("queria valor na chave 1 e momento na chave 2: %s", b)
	}
}

// A verificação recusa o que não faz sentido: execução revertida.
func TestVerificarExecucaoRevertida(t *testing.T) {
	esp, _, _ := preparar(t)
	bruto, _ := json.Marshal(esp)
	if _, err := Executar(context.Background(), bruto, novoRelator()); err != nil {
		t.Fatal(err)
	}
	if _, err := Reverter(context.Background(), bruto, novoRelator()); err != nil {
		t.Fatal(err)
	}
	if _, err := Verificar(context.Background(), bruto, novoRelator()); err == nil || !strings.Contains(err.Error(), "revertida") {
		t.Fatalf("verificar execução revertida: %v", err)
	}
}

func contem(xs []string, quer ...string) bool {
	for _, q := range quer {
		achou := false
		for _, x := range xs {
			achou = achou || x == q
		}
		if !achou {
			return false
		}
	}
	return true
}

func contemFora(xs []plano.ColunaNaoConferida, quer ...string) bool {
	nomes := make([]string, len(xs))
	for i, x := range xs {
		nomes[i] = x.Coluna
	}
	return contem(nomes, quer...)
}

// A dupla conferência pelo texto existe para o erro que as outras não veem: um erro
// de LEITURA (como o driver que tirava um dia das datas no início do horário de
// verão). Aqui um gancho desloca a DATA da linha 1 assim que o driver a entrega —
// nas duas pontas (execução e verificação), como faria um driver com defeito. As
// somas da execução e a comparação canônica batem (comparam o lido-e-convertido com
// o gravado); só o texto renderizado pelos próprios bancos acusa, na chave e na coluna.
func TestTextoDosBancosPegaErroDeLeitura(t *testing.T) {
	esp, _, dbd := prepararCom(t, extras{
		apagaFB: []string{"RV_LEITURA"},
		fb: []string{
			`CREATE TABLE RV_LEITURA (ID INTEGER NOT NULL PRIMARY KEY, DIA DATE, MOMENTO TIMESTAMP, VALOR NUMERIC(15,2),
			  NOME VARCHAR(20), COD CHAR(4), FATOR DOUBLE PRECISION, HORA TIME, OBS BLOB SUB_TYPE TEXT, BIN BLOB SUB_TYPE BINARY)`,
			`INSERT INTO RV_LEITURA VALUES (1, '2005-10-16', '2005-10-16 12:30:00.1234', 10.5, 'Ana ', 'AB', 1.0/3.0, '08:00:00',
			  'observação longa', x'00FF')`,
			`INSERT INTO RV_LEITURA VALUES (2, '2024-06-01', '2024-06-01 23:59:59.9999', -0.01, 'Bia', 'XYZW', 1e300, '23:59:59.9999',
			  NULL, NULL)`,
		},
		pg: []string{`CREATE TABLE %[1]s.rv_leitura (id integer PRIMARY KEY, dia date, momento timestamptz, valor numeric(15,2),
			nome varchar(20), cod character(6), fator double precision, hora time, obs text, bin bytea)`},
	})
	for i := range esp.Mapeamento.Tabelas { // sem transformação: a conferência pelo texto vale para todas
		if mt := &esp.Mapeamento.Tabelas[i]; mt.TabelaOrigem == "RV_LEITURA" {
			for j := range mt.Colunas {
				mt.Colunas[j].Transformacao, mt.Colunas[j].Parametros = modelo.Nenhuma, nil
			}
		}
	}
	esp.HashMapeamento = esp.Mapeamento.Hash()
	bruto, _ := json.Marshal(esp)
	s := esp.Destino.Opcoes["schema"]

	ganchoLeitura = func(tabela string, ln transformar.Linha) {
		if tabela != "RV_LEITURA" || fmt.Sprint(ln["ID"]) != "1" {
			return
		}
		if d, ok := ln["DIA"].(time.Time); ok { // o "driver" erra: um dia antes
			ln["DIA"] = d.AddDate(0, 0, -1)
		}
	}
	t.Cleanup(func() { ganchoLeitura = nil })

	out, err := Executar(context.Background(), bruto, novoRelator())
	if err != nil {
		t.Fatalf("executar: %v", err)
	}
	if rt := tabelaExecutada(t, out.(*plano.ResumoExecucao), "rv_leitura"); !rt.ConteudoOK {
		t.Fatalf("as somas da execução comparam o lido com o gravado: aqui batem (é o ponto cego): %+v", rt)
	}
	var dia string
	if err := dbd.QueryRow("SELECT dia::text FROM " + s + ".rv_leitura WHERE id = 1").Scan(&dia); err != nil || dia != "2005-10-15" {
		t.Fatalf("o gancho deveria ter gravado o dia errado: %q %v", dia, err)
	}

	ver := verificar(t, bruto)
	vt := tabelaVerificada(t, ver, "rv_leitura")
	if !vt.ConteudoOK || vt.Divergentes != 0 {
		t.Fatalf("a comparação canônica também lê pelo driver: bate (ponto cego): %+v", vt)
	}
	tx := vt.Texto
	if tx == nil || tx.OK || ver.TextoOK || tx.Divergentes != 1 || tx.Identicas != 1 || len(tx.Divergencias) != 1 ||
		tx.Divergencias[0].Chave != "1" || strings.Join(tx.Divergencias[0].Colunas, ",") != "dia" || tx.PorColuna["dia"] != 1 {
		b, _ := json.MarshalIndent(tx, "", " ")
		t.Fatalf("o texto dos bancos tinha de acusar dia na chave 1: %s", b)
	}
	if !contem(tx.Colunas, "id", "dia", "momento", "valor", "nome", "cod", "fator", "hora", "obs") || !contemFora(tx.Parciais, "bin") {
		b, _ := json.MarshalIndent(tx, "", " ")
		t.Fatalf("colunas conferidas pelo texto: %s", b)
	}
	if b, _ := json.Marshal(ver); strings.Contains(string(b), "2005-10-1") || strings.Contains(string(b), "observação") {
		t.Fatal("o relatório não pode trazer valores das linhas")
	}

	// sem o defeito na leitura, a comparação canônica também vê o dia errado no destino
	ganchoLeitura = nil
	if vt := tabelaVerificada(t, verificar(t, bruto), "rv_leitura"); vt.ConteudoOK || vt.PorColuna["dia"] != 1 {
		t.Fatalf("lendo direito, a canônica acusa o dia: %+v", vt)
	}
}

// PostgreSQL → PostgreSQL: a origem também renderiza o texto (::text/to_char), então a
// dupla conferência vale igual — inclusive timestamptz, booleano, uuid e jsonb.
func TestTextoDosBancosPostgresParaPostgres(t *testing.T) {
	pg := os.Getenv("REVOADA_TEST_PG_HOST")
	if pg == "" || os.Getenv("REVOADA_TEST_FB") == "" {
		t.Skip("REVOADA_TEST_FB / REVOADA_TEST_PG_HOST não definidos")
	}
	ctx := context.Background()
	so, sd := fmt.Sprintf("pgo_%d", os.Getpid()), fmt.Sprintf("pgd_%d", os.Getpid())
	banco := func(s string) plano.Banco {
		return plano.Banco{Motor: "postgres", Endereco: pg, Banco: "revoada", Usuario: "teste", Opcoes: map[string]string{"sslmode": "disable", "schema": s}}
	}
	bo, bd := banco(so), banco(sd)
	db, err := captura.Abrir(captura.Conexao{Motor: "postgres", Endereco: pg, Banco: "revoada", Usuario: "teste", Senha: "teste", Opcoes: bo.Opcoes})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("DROP SCHEMA IF EXISTS " + so + " CASCADE")
		_, _ = db.Exec("DROP SCHEMA IF EXISTS " + sd + " CASCADE")
		db.Close()
	})
	ddl := "(id integer PRIMARY KEY, nome varchar(30), valor numeric(12,2), quando timestamptz, dia date, ativo boolean, uid uuid, dados jsonb, hora time)"
	for _, q := range []string{
		"CREATE SCHEMA " + so, "CREATE SCHEMA " + sd,
		"CREATE TABLE " + so + ".itens " + ddl, "CREATE TABLE " + sd + ".itens " + ddl,
		"INSERT INTO " + so + ".itens VALUES (1, 'ação', 12.50, '2005-10-16 00:30:00-03', '2005-10-16', true, 'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11', '{\"b\":1,\"a\":[1.5]}', '08:00:00.25')",
		"INSERT INTO " + so + ".itens VALUES (2, NULL, -0.01, NULL, NULL, false, NULL, NULL, NULL)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	captar := func(b plano.Banco) esquema.Esquema {
		e, err := captura.Capturar(ctx, captura.Conexao{Motor: "postgres", Endereco: pg, Banco: "revoada", Usuario: "teste", Senha: "teste", Opcoes: b.Opcoes})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	eo, ed := captar(bo), captar(bd)
	m := sugestao.Sugerir(eo, ed)
	if ps := m.Validar(eo, ed); modelo.TemErro(ps) {
		t.Fatalf("mapeamento: %+v", ps)
	}
	esp := plano.Especificacao{Execucao: "ex_pg_" + sd, Origem: bo, Destino: bd, Mapeamento: m, HashMapeamento: m.Hash(),
		EsquemaOrigem: eo, EsquemaDestino: ed, Lote: 100}
	bruto, _ := json.Marshal(esp)
	r := &relatorTeste{senhas: map[string]string{"origem": "teste", "destino": "teste"}}
	if _, err := Executar(ctx, bruto, r); err != nil {
		t.Fatalf("executar: %v", err)
	}
	out, err := Verificar(ctx, bruto, r)
	if err != nil {
		t.Fatal(err)
	}
	ver := out.(*plano.ResumoVerificacao)
	vt := tabelaVerificada(t, ver, "itens")
	if tx := vt.Texto; !ver.ConteudoOK || !ver.TextoOK || tx == nil || tx.Identicas != 2 || len(tx.Colunas) != 9 {
		b, _ := json.MarshalIndent(vt, "", " ")
		t.Fatalf("PG → PG: %s (eventos %v)", b, r.eventos)
	}
	if _, err := db.Exec("UPDATE " + sd + ".itens SET quando = quando - interval '1 hour' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	out, _ = Verificar(ctx, bruto, r)
	tx := tabelaVerificada(t, out.(*plano.ResumoVerificacao), "itens").Texto
	if tx == nil || tx.OK || tx.PorColuna["quando"] != 1 || tx.Divergencias[0].Chave != "1" {
		t.Fatalf("a hora a menos no timestamptz tinha de ser acusada: %+v", tx)
	}
}

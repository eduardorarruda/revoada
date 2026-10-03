package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/eduardorarruda/revoada/agent/internal/migracao/captura"
	"github.com/eduardorarruda/revoada/core/migracao/plano"
	up "github.com/eduardorarruda/revoada/core/migracao/upgrade"
	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
)

// Integração Firebird 2.5 → Firebird 5 com servidores de verdade (Docker). Só roda com:
//
//	REVOADA_TEST_FB=127.0.0.1:53050    (2.5, banco /firebird/data/erp.fdb, SYSDBA/masterkey)
//	REVOADA_TEST_FB5=127.0.0.1:53051   (5.0, SYSDBA/masterkey)
//	REVOADA_TEST_FB_DIR=/firebird/upgrade  (diretório visível para os dois servidores)

type relator struct{ eventos []string }

func (r *relator) Evento(_ string, _ agentev1.EventoTarefa_Nivel, m string, _ float64, _ map[string]float64) {
	r.eventos = append(r.eventos, m)
}
func (r *relator) Pausa(context.Context) error { return nil }
func (r *relator) Credencial(string) ([]byte, error) {
	return []byte("masterkey"), nil
}
func (r *relator) Checkpoint(string, string, int64) {}

func especificacao(t *testing.T, sufixo string) up.Especificacao {
	t.Helper()
	fb25, fb5, dir := os.Getenv("REVOADA_TEST_FB"), os.Getenv("REVOADA_TEST_FB5"), os.Getenv("REVOADA_TEST_FB_DIR")
	if fb25 == "" || fb5 == "" || dir == "" {
		t.Skip("REVOADA_TEST_FB / REVOADA_TEST_FB5 / REVOADA_TEST_FB_DIR não definidos")
	}
	return up.Especificacao{Execucao: fmt.Sprintf("ex_%d_%s", os.Getpid(), sufixo), ProjetoID: fmt.Sprintf("pj%d", os.Getpid()),
		Origem: plano.Banco{Motor: "firebird", Endereco: fb25, Banco: "/firebird/data/erp.fdb", Usuario: "SYSDBA", Opcoes: map[string]string{"charset": "WIN1252"}},
		Destino: &plano.Banco{Motor: "firebird", Endereco: fb5, Banco: fmt.Sprintf("%s/erp5_%d_%s.fdb", dir, os.Getpid(), sufixo), Usuario: "SYSDBA",
			Opcoes: map[string]string{"wire_crypt": "true"}},
		Diretorio: dir, ValidarPaginas: true, Workers: 2}
}

func rodar(t *testing.T, f func(context.Context, []byte, Relator) (any, error), e up.Especificacao) (any, error) {
	t.Helper()
	b, _ := json.Marshal(e)
	return f(context.Background(), b, &relator{})
}

func TestDiagnosticoUpgradeEDescarte(t *testing.T) {
	esp := especificacao(t, "ok")
	out, err := rodar(t, Diagnosticar, esp)
	if err != nil {
		t.Fatal(err)
	}
	d := out.(up.Diagnostico)
	if !strings.HasPrefix(d.Versao, "2.5") || d.Dialeto != 3 || d.Charset != "WIN1252" || d.Tamanho <= 0 {
		t.Fatalf("básico: %+v", d)
	}
	if d.Bloqueios != 0 || d.Ensaio == nil || !d.Ensaio.OK {
		b, _ := json.MarshalIndent(d, "", " ")
		t.Fatalf("a base de teste não tem bloqueio e o ensaio deveria passar: %s", b)
	}
	var charset bool
	for _, a := range d.Achados {
		if a.Categoria == up.CatCharset && a.Quantos >= 2 {
			charset = true // "JosÃ©": UTF-8 gravado em WIN1252 na base de teste
		}
	}
	if !charset || !strings.Contains(d.FixSQL, "fix.sql") || d.ParadaS <= 0 {
		t.Fatalf("achados: %+v", d.Achados)
	}

	out, err = rodar(t, Upgrade, esp)
	if err != nil {
		b, _ := json.MarshalIndent(out, "", " ")
		t.Fatalf("upgrade: %v\n%s", err, b)
	}
	res := out.(*up.ResumoUpgrade)
	if !res.TudoConfere || !strings.HasPrefix(res.Versao, "5.") || len(res.Tabelas) < 4 {
		t.Fatalf("resumo: %+v", res)
	}
	// o banco novo abre no FB5 e tem os dados
	db, err := captura.Abrir(captura.Conexao{Motor: "firebird", Endereco: esp.Destino.Endereco, Banco: esp.Destino.Banco, Usuario: "SYSDBA", Senha: "masterkey",
		Opcoes: map[string]string{"charset": "WIN1252", "wire_crypt": "true"}})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM TB_CLIENTES").Scan(&n); err != nil || n != 2 {
		t.Fatalf("clientes no FB5: %d %v", n, err)
	}
	db.Close()

	// de novo no MESMO arquivo: create nunca sobrescreve
	if _, err := rodar(t, Upgrade, esp); err == nil {
		t.Fatal("o restore não pode sobrescrever um banco existente")
	}

	if _, err := rodar(t, Descartar, esp); err != nil {
		t.Fatal(err)
	}
	if db, err := captura.Abrir(captura.Conexao{Motor: "firebird", Endereco: esp.Destino.Endereco, Banco: esp.Destino.Banco, Usuario: "SYSDBA", Senha: "masterkey",
		Opcoes: map[string]string{"wire_crypt": "true"}}); err == nil {
		if db.Ping() == nil {
			t.Fatal("o banco descartado não pode aceitar conexão")
		}
		db.Close()
	}
}

// NOT NULL ligado "por fora" (UPDATE no catálogo, hábito antigo) deixa nulos que
// derrubam o restore. O diagnóstico tem de bloquear e o upgrade tem de falhar sem
// tocar no original.
func TestNuloEscondidoBloqueia(t *testing.T) {
	esp := especificacao(t, "nulo")
	db, err := captura.Abrir(captura.Conexao{Motor: "firebird", Endereco: esp.Origem.Endereco, Banco: esp.Origem.Banco, Usuario: "SYSDBA", Senha: "masterkey"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec("DROP TABLE RV_NULOS")
	t.Cleanup(func() {
		// conexão nova: no Firebird, DROP falha ("in use") na conexão que leu a tabela
		db.Close()
		if limpa, err := captura.Abrir(captura.Conexao{Motor: "firebird", Endereco: esp.Origem.Endereco, Banco: esp.Origem.Banco, Usuario: "SYSDBA", Senha: "masterkey"}); err == nil {
			if _, err := limpa.Exec("DROP TABLE RV_NULOS"); err != nil {
				t.Errorf("limpando RV_NULOS: %v", err)
			}
			limpa.Close()
		}
	})
	for _, q := range []string{
		"CREATE TABLE RV_NULOS (ID INTEGER NOT NULL PRIMARY KEY, NOME VARCHAR(10))",
		"INSERT INTO RV_NULOS VALUES (1, NULL)",
		"UPDATE RDB$RELATION_FIELDS SET RDB$NULL_FLAG = 1 WHERE RDB$RELATION_NAME = 'RV_NULOS' AND RDB$FIELD_NAME = 'NOME'",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	out, err := rodar(t, Diagnosticar, esp)
	if err != nil {
		t.Fatal(err)
	}
	d := out.(up.Diagnostico)
	achou := false
	for _, a := range d.Achados {
		if a.Categoria == up.CatNulo && a.Objeto == "RV_NULOS.NOME" && a.Quantos == 1 && a.Nivel == up.Bloqueio {
			achou = true
		}
	}
	if !achou || d.Risco != "alto" || !strings.Contains(d.FixSQL, `UPDATE "RV_NULOS" SET "NOME"`) {
		t.Fatalf("diagnóstico deveria bloquear: %+v", d.Achados)
	}
	if _, err := rodar(t, Upgrade, esp); err == nil {
		t.Fatal("o restore com nulo em NOT NULL deveria falhar")
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM RV_NULOS").Scan(&n); err != nil || n != 1 {
		t.Fatalf("o original tem de seguir intacto: %d %v", n, err)
	}
}

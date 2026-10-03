package main

import (
	"strings"
	"testing"
	"time"
)

// A allowlist é a trava que impede o comando de zerar o estado das MIGRAÇÕES. Se
// alguém acrescentar `schema_migrations` a tabelasDeDados, o próximo apagamento
// faria o boot reaplicar migrações sobre um banco já migrado. Este teste quebra
// antes disso chegar em produção.
func TestAllowlistNaoAlcancaEstadoDeMigracao(t *testing.T) {
	proibidas := []string{"schema_migrations", "legacy_migration_checkpoint", "metrics_1m_mv", "metrics_1h_mv"}
	for _, p := range proibidas {
		for _, permitida := range tabelasDeDados {
			if permitida == p {
				t.Fatalf("%q não pode estar na lista de tabelas apagáveis", p)
			}
		}
		if _, err := resolverTabelas(p); err == nil {
			t.Errorf("-tabelas %s deveria ser recusado", p)
		}
	}
}

func TestResolverTabelas(t *testing.T) {
	todas, err := resolverTabelas("")
	if err != nil || len(todas) != len(tabelasDeDados) {
		t.Fatalf("sem -tabelas o padrão é a lista inteira; veio %v (%v)", todas, err)
	}
	got, err := resolverTabelas(" metrics , logs ")
	if err != nil || len(got) != 2 || got[0] != "metrics" || got[1] != "logs" {
		t.Errorf("lista com espaços deveria ser aceita e aparada; veio %v (%v)", got, err)
	}
	// Nome desconhecido é ERRO, não aviso: ignorar em silêncio deixaria o operador
	// convencido de que apagou o que pediu.
	if _, err := resolverTabelas("metrics,inexistente"); err == nil {
		t.Error("tabela desconhecida deveria ser recusada, não ignorada")
	}
	if _, err := resolverTabelas(" , "); err == nil {
		t.Error("-tabelas só com vírgulas deveria ser recusado")
	}
}

func TestResolverCorte(t *testing.T) {
	if c, err := resolverCorte(""); err != nil || c != nil {
		t.Errorf("sem -antes-de o corte é nulo (apaga tudo); veio %v (%v)", c, err)
	}
	c, err := resolverCorte("2026-08-10T13:13:00Z")
	if err != nil || c == nil || c.UTC().Format(time.RFC3339) != "2026-08-10T13:13:00Z" {
		t.Errorf("RFC3339 válido deveria ser aceito; veio %v (%v)", c, err)
	}
	// Data solta é o erro provável de quem digita com pressa, e aceitar "meia data"
	// apagaria uma janela diferente da pedida.
	for _, ruim := range []string{"2026-08-10", "10/08/2026", "ontem", "1786367760"} {
		if _, err := resolverCorte(ruim); err == nil {
			t.Errorf("%q deveria ser recusado", ruim)
		}
	}
}

// O escopo é o que o operador lê antes de confirmar. Um rótulo que diz "TUDO"
// quando o comando vai apagar um host só (ou o contrário) é pior que não ter rótulo.
func TestDescreverEscopo(t *testing.T) {
	corte := time.Date(2026, 8, 10, 13, 13, 0, 0, time.UTC)
	if s := descreverEscopo(nil, ""); !strings.Contains(s, "TUDO") {
		t.Errorf("sem recorte o escopo tem de gritar TUDO; veio %q", s)
	}
	if s := descreverEscopo(&corte, ""); !strings.Contains(s, "2026-08-10T13:13:00Z") {
		t.Errorf("o corte tem de aparecer no escopo; veio %q", s)
	}
	if s := descreverEscopo(nil, "srv-02"); !strings.Contains(s, "srv-02") {
		t.Errorf("o servidor tem de aparecer no escopo; veio %q", s)
	}
	s := descreverEscopo(&corte, "srv-02")
	if !strings.Contains(s, "srv-02") || !strings.Contains(s, "2026-08-10") {
		t.Errorf("com os dois recortes, os dois têm de aparecer; veio %q", s)
	}
}

// Hostname com aspa simples quebraria a query — e o hábito de escapar não pode
// depender de confiar em quem digitou.
func TestAspasEscapa(t *testing.T) {
	if got := aspas("srv-02"); got != "'srv-02'" {
		t.Errorf("aspas simples = %q", got)
	}
	if got := aspas("o'brien"); got != `'o\'brien'` {
		t.Errorf("aspa simples tem de ser escapada; veio %q", got)
	}
	if got := aspas(`c:\tmp`); got != `'c:\\tmp'` {
		t.Errorf("barra invertida tem de ser escapada; veio %q", got)
	}
}

func TestInteiroAceitaOsDoisFormatosDoClickHouse(t *testing.T) {
	// UInt64/Int64 vêm como STRING no JSON do ClickHouse (para não perder precisão);
	// os tipos menores vêm como número. Ler só um dos dois daria contagem zero — e o
	// relatório diria "vazia" sobre uma tabela cheia.
	if got := inteiro("30669263"); got != 30669263 {
		t.Errorf("string = %d", got)
	}
	if got := inteiro(float64(365)); got != 365 {
		t.Errorf("número = %d", got)
	}
	if got := inteiro(nil); got != 0 {
		t.Errorf("nulo = %d", got)
	}
}

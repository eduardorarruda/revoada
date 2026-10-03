package transformar

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
	"github.com/eduardorarruda/revoada/core/migracao/modelo"
)

func mc(t modelo.Transformacao, origem string, p map[string]string) modelo.MapColuna {
	return modelo.MapColuna{ColunaOrigem: origem, ColunaDestino: "d", Transformacao: t, Parametros: p}
}

func TestAplicarTransformacoes(t *testing.T) {
	l := Linha{"NOME": "  Ana  ", "SOBRENOME": "Souza", "ATIVO": "S", "NULO": nil, "FONE": "11-9999-0000",
		"DT": "15/01/2020", "WIN": []byte{'J', 'o', 's', 0xE9}, "DUPLO": "JosÃ© ConceiÃ§Ã£o", "CERTO": "Ação"}
	casos := []struct {
		nome string
		m    modelo.MapColuna
		quer any
	}{
		{"nenhuma", mc(modelo.Nenhuma, "SOBRENOME", nil), "Souza"},
		{"aparar", mc(modelo.Aparar, "NOME", nil), "Ana"},
		{"aparar nulo", mc(modelo.Aparar, "NULO", nil), nil},
		{"constante", mc(modelo.Constante, "", map[string]string{"valor": "legado"}), "legado"},
		{"padrão quando nulo", mc(modelo.ValorPadrao, "NULO", map[string]string{"valor": "x"}), "x"},
		{"padrão com valor", mc(modelo.ValorPadrao, "SOBRENOME", map[string]string{"valor": "x"}), "Souza"},
		{"mapa", mc(modelo.MapaValores, "ATIVO", map[string]string{"S": "true", "N": "false"}), "true"},
		{"mapa sem par fica igual", mc(modelo.MapaValores, "SOBRENOME", map[string]string{"S": "true"}), "Souza"},
		{"mapa curinga", mc(modelo.MapaValores, "SOBRENOME", map[string]string{"*": "?"}), "?"},
		{"mapa nulo", mc(modelo.MapaValores, "NULO", map[string]string{"<nulo>": "N"}), "N"},
		{"concatenar", mc(modelo.Concatenar, "", map[string]string{"colunas": "NOME, SOBRENOME", "separador": " "}), "Ana Souza"},
		{"concatenar pula nulo", mc(modelo.Concatenar, "", map[string]string{"colunas": "NULO,SOBRENOME", "separador": "-"}), "Souza"},
		{"dividir", mc(modelo.Dividir, "FONE", map[string]string{"separador": "-", "parte": "2"}), "9999"},
		{"dividir além", mc(modelo.Dividir, "FONE", map[string]string{"separador": "-", "parte": "9"}), nil},
		{"data", mc(modelo.DataFormato, "DT", map[string]string{"formato": "DD/MM/AAAA"}), time.Date(2020, 1, 15, 0, 0, 0, 0, time.UTC)},
		{"charset", mc(modelo.Charset, "WIN", map[string]string{"de": "WIN1252"}), "José"},
		{"charset já UTF-8", mc(modelo.Charset, "SOBRENOME", map[string]string{"de": "WIN1252"}), "Souza"},
		{"charset reparar dupla codificação", mc(modelo.Charset, "DUPLO", map[string]string{"de": "WIN1252", "reparar": "sim"}), "José Conceição"},
		{"charset reparar não mexe no que está certo", mc(modelo.Charset, "CERTO", map[string]string{"de": "WIN1252", "reparar": "sim"}), "Ação"},
		{"charset sem reparar mantém", mc(modelo.Charset, "DUPLO", map[string]string{"de": "WIN1252"}), "JosÃ© ConceiÃ§Ã£o"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got, err := Aplicar(c.m, l)
			if err != nil {
				t.Fatal(err)
			}
			if gt, ok := got.(time.Time); ok {
				if !gt.Equal(c.quer.(time.Time)) {
					t.Fatalf("quer %v, veio %v", c.quer, got)
				}
				return
			}
			if got != c.quer {
				t.Fatalf("quer %#v, veio %#v", c.quer, got)
			}
		})
	}
}

func TestAplicarErros(t *testing.T) {
	l := Linha{"A": "x", "WIN": []byte{0xE9}}
	for _, m := range []modelo.MapColuna{
		mc(modelo.Dividir, "A", map[string]string{"parte": "1"}),
		mc(modelo.Dividir, "A", map[string]string{"separador": ",", "parte": "0"}),
		mc(modelo.Concatenar, "", map[string]string{"colunas": "A,NAO_LIDA"}),
		mc(modelo.Charset, "WIN", map[string]string{"de": "KLINGON"}),
		mc(modelo.DataFormato, "A", map[string]string{"formato": "DD/MM/AAAA"}),
		mc("sql_livre", "A", nil),
	} {
		if _, err := Aplicar(m, l); err == nil {
			t.Errorf("%s deveria falhar", m.Transformacao)
		}
	}
	if _, err := Aplicar(mc(modelo.Dividir, "A", nil), l); !errors.Is(err, ErrParametro) {
		t.Fatalf("erro de parâmetro: %v", err)
	}
}

func col(tipo esquema.TipoLogico, nativo string, f func(*esquema.Coluna)) esquema.Coluna {
	c := esquema.Coluna{Nome: "c", Tipo: tipo, TipoNativo: nativo, Nulavel: true}
	if f != nil {
		f(&c)
	}
	return c
}

func TestAjustar(t *testing.T) {
	obrig := func(c *esquema.Coluna) { c.Nulavel = false }
	varchar5 := col(esquema.Texto, "varchar(5)", func(c *esquema.Coluna) { c.Tamanho = 5 })
	num := col(esquema.Decimal, "numeric(5,2)", func(c *esquema.Coluna) { c.Precisao, c.Escala = 5, 2 })
	casos := []struct {
		nome  string
		v     any
		c     esquema.Coluna
		quer  any
		perda string
		vio   string
	}{
		{"nulo em nulável", nil, col(esquema.Texto, "text", nil), nil, "", ""},
		{"nulo em obrigatória", nil, col(esquema.Texto, "text", obrig), nil, "", VioNulo},
		{"nulo com padrão", nil, col(esquema.Inteiro, "integer", func(c *esquema.Coluna) { c.Nulavel, c.Padrao = false, "0" }), UsarPadrao, "", ""},
		{"nulo em nulável com padrão continua nulo", nil, col(esquema.Inteiro, "integer", func(c *esquema.Coluna) { c.Padrao = "0" }), nil, "", ""},
		{"texto cabe", "Olá!", varchar5, "Olá!", "", ""},
		{"texto conta caracteres, não bytes", "ççççç", varchar5, "ççççç", "", ""},
		{"texto grande", "abcdef", varchar5, nil, "", VioTamanho},
		{"bytes inválidos", []byte{0xE9}, varchar5, nil, "", VioTextoInvalid},
		{"caractere nulo", "a\x00b", varchar5, nil, "", VioTextoInvalid},
		{"inteiro", int64(7), col(esquema.Inteiro, "integer", nil), int64(7), "", ""},
		{"inteiro de texto", " 42 ", col(esquema.Inteiro, "integer", nil), int64(42), "", ""},
		{"inteiro fora de smallint", int64(40000), col(esquema.Inteiro, "smallint", nil), nil, "", VioNumero},
		{"decimal em inteiro perde casas", "10.50", col(esquema.Inteiro, "integer", nil), int64(10), PerdaCasas, ""},
		{"inteiro inválido", "abc", col(esquema.Inteiro, "integer", nil), nil, "", VioTipo},
		{"decimal", "123.45", num, "123.45", "", ""},
		{"decimal estoura", "1234.5", num, nil, "", VioNumero},
		{"decimal arredonda", "1.2345", num, "1.2345", PerdaCasas, ""},
		{"decimal zeros à direita não contam", "100.0000", num, "100", "", ""},
		{"decimal de inteiro", int64(12), num, "12", "", ""},
		{"booleano S", "S", col(esquema.Booleano, "boolean", nil), true, "", ""},
		{"booleano 0", int64(0), col(esquema.Booleano, "boolean", nil), false, "", ""},
		{"booleano inválido", "talvez", col(esquema.Booleano, "boolean", nil), nil, "", VioTipo},
		{"data de texto", "2020-01-15", col(esquema.Data, "date", nil), time.Date(2020, 1, 15, 0, 0, 0, 0, time.UTC), "", ""},
		{"data descarta hora", time.Date(2020, 1, 15, 10, 0, 0, 0, time.UTC), col(esquema.Data, "date", nil), time.Date(2020, 1, 15, 0, 0, 0, 0, time.UTC), PerdaHora, ""},
		{"data inválida", "ontem", col(esquema.DataHora, "timestamp", nil), nil, "", VioTipo},
		{"json", `{"a":1}`, col(esquema.JSON, "jsonb", nil), `{"a":1}`, "", ""},
		{"json inválido", `{a}`, col(esquema.JSON, "jsonb", nil), nil, "", VioTipo},
		{"flutuante", "1.5", col(esquema.Flutuante, "double precision", nil), 1.5, "", ""},
		{"suspeita de dupla codificação", "JosÃ©", col(esquema.Texto, "text", nil), "JosÃ©", PerdaCharset, ""},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			v, perda, vi := Ajustar(c.v, c.c)
			if c.vio != "" {
				if vi == nil || vi.Tipo != c.vio {
					t.Fatalf("queria violação %s, veio %v", c.vio, vi)
				}
				if s, ok := c.v.(string); ok && len(s) > 2 && strings.Contains(vi.Mensagem, s) {
					t.Fatalf("a mensagem não pode trazer o valor: %q", vi.Mensagem)
				}
				return
			}
			if vi != nil {
				t.Fatalf("violação inesperada: %v", vi)
			}
			if perda != c.perda {
				t.Fatalf("perda: quer %q, veio %q", c.perda, perda)
			}
			if tv, ok := v.(time.Time); ok {
				if !tv.Equal(c.quer.(time.Time)) {
					t.Fatalf("quer %v, veio %v", c.quer, v)
				}
				return
			}
			if v != c.quer {
				t.Fatalf("quer %#v, veio %#v", c.quer, v)
			}
		})
	}
}

func TestLayoutEChave(t *testing.T) {
	if got := LayoutData("DD/MM/AAAA HH:MI:SS"); got != "02/01/2006 15:04:05" {
		t.Fatal(got)
	}
	if got := LayoutData("YYYY-MM-DD"); got != "2006-01-02" {
		t.Fatal(got)
	}
	if ChaveTexto([]byte{1, 255}) != "01ff" || ChaveTexto(int64(5)) != "5" {
		t.Fatal("chave texto")
	}
}

// Relógio sem fuso (o leitor entrega em UTC) indo para timestamptz vira instante no
// fuso da origem; indo para timestamp sem fuso ou date, o relógio passa intacto —
// inclusive nos dias em que a meia-noite não existe no horário de verão.
func TestAjustarRelogioDeParede(t *testing.T) {
	sp, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Skip("sem base de fusos:", err)
	}
	FusoOrigem = sp
	t.Cleanup(func() { FusoOrigem = nil })
	relogio := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

	v, _, vi := Ajustar(relogio, esquema.Coluna{Nome: "i", Tipo: esquema.DataHora, TipoNativo: "timestamp with time zone"})
	if vi != nil || !v.(time.Time).Equal(time.Date(2024, 6, 1, 15, 0, 0, 0, time.UTC)) {
		t.Fatalf("timestamptz: %v %v", v, vi)
	}
	v, _, _ = Ajustar(relogio, esquema.Coluna{Nome: "m", Tipo: esquema.DataHora, TipoNativo: "timestamp without time zone"})
	if got := v.(time.Time); !got.Equal(relogio) || got.Location() != time.UTC {
		t.Fatalf("timestamp sem fuso deveria passar intacto: %v", got)
	}
	gap := time.Date(2005, 10, 16, 0, 0, 0, 0, time.UTC) // meia-noite inexistente em SP
	v, perda, _ := Ajustar(gap, esquema.Coluna{Nome: "d", Tipo: esquema.Data})
	if got := v.(time.Time); got.Format(time.DateOnly) != "2005-10-16" || perda != "" {
		t.Fatalf("date: %v perda=%q", got, perda)
	}
	// instante que já veio com fuso (timestamptz da origem) não é reinterpretado
	inst := time.Date(2024, 6, 1, 12, 0, 0, 0, sp)
	v, _, _ = Ajustar(inst, esquema.Coluna{Nome: "i", Tipo: esquema.DataHora, TipoNativo: "timestamptz"})
	if !v.(time.Time).Equal(inst) {
		t.Fatalf("instante reinterpretado: %v", v)
	}
}

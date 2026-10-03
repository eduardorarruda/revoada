// Package copia é o motor de cópia de dados do agente (ARQUITETURA §9.4): simulação
// (dry-run, sem gravar nada), execução em lotes com checkpoint e reversão. Roda no
// servidor do agente escolhido; a senha dos bancos chega selada e vive só em memória.
//
// Segurança: nada de SQL livre. Nomes de tabela e coluna vêm da foto do schema (o
// painel validou o mapeamento contra ela e o agente confere de novo) e sempre vão
// entre aspas; valores (filtros, chaves de lote) vão SEMPRE como parâmetro.
package copia

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/eduardorarruda/revoada/core/migracao/modelo"
	"github.com/eduardorarruda/revoada/core/migracao/plano"
)

// dialeto monta SQL para o motor da origem (leitura) ou do destino (gravação).
type dialeto struct {
	motor   string // firebird | postgres
	esquema string // schema do PostgreSQL (vazio no Firebird)
	semAspa bool   // Firebird dialeto 1 não conhece identificador entre aspas
}

var identSimples = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)

func novoDialeto(b plano.Banco, dialetoFB int) dialeto {
	d := dialeto{motor: b.Motor}
	if b.Motor == "postgres" {
		d.esquema = b.Opcoes["schema"]
		if d.esquema == "" {
			d.esquema = "public"
		}
	}
	d.semAspa = b.Motor == "firebird" && dialetoFB == 1
	return d
}

// id põe o identificador entre aspas (dobrando aspas internas).
func (d dialeto) id(nome string) (string, error) {
	if nome == "" || strings.ContainsRune(nome, 0) {
		return "", fmt.Errorf("identificador inválido")
	}
	if d.semAspa {
		if !identSimples.MatchString(nome) {
			return "", fmt.Errorf("o nome %q não pode ser usado num banco Firebird de dialeto 1", nome)
		}
		return nome, nil
	}
	return `"` + strings.ReplaceAll(nome, `"`, `""`) + `"`, nil
}

// tabela devolve o nome qualificado (schema.tabela no PostgreSQL).
func (d dialeto) tabela(nome string) (string, error) {
	t, err := d.id(nome)
	if err != nil {
		return "", err
	}
	if d.esquema != "" {
		s, err := d.id(d.esquema)
		if err != nil {
			return "", err
		}
		return s + "." + t, nil
	}
	return t, nil
}

func (d dialeto) ph(n int) string {
	if d.motor == "postgres" {
		return "$" + strconv.Itoa(n)
	}
	return "?"
}

var operadoresSQL = map[string]string{"=": "=", "<>": "<>", "<": "<", "<=": "<=", ">": ">", ">=": ">="}

// onde monta o WHERE dos filtros estruturados (valores sempre como parâmetro).
func (d dialeto) onde(filtros []modelo.Filtro, args []any) ([]string, []any, error) {
	var conds []string
	for _, f := range filtros {
		c, err := d.id(f.Coluna)
		if err != nil {
			return nil, nil, err
		}
		switch f.Operador {
		case "nulo":
			conds = append(conds, c+" IS NULL")
		case "nao_nulo":
			conds = append(conds, c+" IS NOT NULL")
		default:
			op, ok := operadoresSQL[f.Operador]
			if !ok {
				return nil, nil, fmt.Errorf("operador de filtro %q inválido", f.Operador)
			}
			args = append(args, f.Valor)
			conds = append(conds, fmt.Sprintf("%s %s %s", c, op, d.ph(len(args))))
		}
	}
	return conds, args, nil
}

// consultaLote monta o SELECT de um lote. Com coluna de lote: paginação por chave
// (keyset — WHERE chave > última ORDER BY chave), que retoma sem pular nem repetir.
// Sem coluna de lote: um cursor só, lido em pedaços (limite = 0).
func (d dialeto) consultaLote(tabela string, cols []string, chave string, filtros []modelo.Filtro,
	depoisDe any, temDepois bool, limite int) (string, []any, error) {
	sel := make([]string, len(cols))
	for i, c := range cols {
		var err error
		if sel[i], err = d.id(c); err != nil {
			return "", nil, err
		}
	}
	return d.consultaLoteSQL(tabela, sel, chave, filtros, depoisDe, temDepois, limite)
}

// consultaLoteSQL é a consultaLote com a lista do SELECT já montada (identificadores
// entre aspas ou expressões geradas pelo próprio motor — nunca texto vindo de fora).
func (d dialeto) consultaLoteSQL(tabela string, sel []string, chave string, filtros []modelo.Filtro,
	depoisDe any, temDepois bool, limite int) (string, []any, error) {
	t, err := d.tabela(tabela)
	if err != nil {
		return "", nil, err
	}
	conds, args, err := d.onde(filtros, nil)
	if err != nil {
		return "", nil, err
	}
	var k string
	if chave != "" {
		if k, err = d.id(chave); err != nil {
			return "", nil, err
		}
		if temDepois {
			args = append(args, depoisDe)
			conds = append(conds, fmt.Sprintf("%s > %s", k, d.ph(len(args))))
		}
	}
	var b strings.Builder
	b.WriteString("SELECT ")
	if d.motor == "firebird" && limite > 0 && chave != "" {
		fmt.Fprintf(&b, "FIRST %d ", limite)
	}
	b.WriteString(strings.Join(sel, ", "))
	b.WriteString(" FROM " + t)
	if len(conds) > 0 {
		b.WriteString(" WHERE " + strings.Join(conds, " AND "))
	}
	if chave != "" {
		b.WriteString(" ORDER BY " + k)
		if d.motor == "postgres" && limite > 0 {
			fmt.Fprintf(&b, " LIMIT %d", limite)
		}
	}
	return b.String(), args, nil
}

// consultaContagem conta as linhas que serão lidas (com os filtros).
func (d dialeto) consultaContagem(tabela string, filtros []modelo.Filtro) (string, []any, error) {
	t, err := d.tabela(tabela)
	if err != nil {
		return "", nil, err
	}
	conds, args, err := d.onde(filtros, nil)
	if err != nil {
		return "", nil, err
	}
	q := "SELECT COUNT(*) FROM " + t
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	return q, args, nil
}

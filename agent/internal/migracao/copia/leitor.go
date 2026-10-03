package copia

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
	"github.com/eduardorarruda/revoada/core/migracao/modelo"
	"github.com/eduardorarruda/revoada/core/migracao/plano"
	"github.com/eduardorarruda/revoada/core/migracao/transformar"
)

// leitor lê uma tabela da origem em lotes.
type leitor struct {
	db    *sql.DB
	d     dialeto
	passo plano.Passo
	cols  []string
	chave string
	// sel: lista do SELECT já montada, paralela a cols (cols vira o nome de cada
	// item na linha). nil = as próprias colunas entre aspas. A dupla conferência
	// pelo texto usa isto para pedir à origem o valor já renderizado como texto.
	sel    []string
	ultima any
	temUlt bool
	fim    bool

	// sem coluna de lote: um cursor aberto do começo ao fim
	rows *sql.Rows
}

// colunasLidas são só as colunas de que o mapeamento precisa (menos dado trafega,
// menos dado fica em memória).
func colunasLidas(mt modelo.MapTabela) []string {
	var cols []string
	add := func(c string) {
		if c != "" && !slices.Contains(cols, c) {
			cols = append(cols, c)
		}
	}
	add(mt.ColunaLote)
	for _, mc := range mt.Colunas {
		switch mc.Transformacao {
		case modelo.Constante:
		case modelo.Concatenar:
			for _, c := range strings.Split(mc.Parametros["colunas"], ",") {
				add(strings.TrimSpace(c))
			}
		default:
			add(mc.ColunaOrigem)
		}
	}
	return cols
}

func novoLeitor(db *sql.DB, d dialeto, p plano.Passo) *leitor {
	return &leitor{db: db, d: d, passo: p, cols: colunasLidas(p.Map), chave: p.Map.ColunaLote}
}

// retomarDe posiciona o leitor depois da chave gravada no checkpoint.
func (l *leitor) retomarDe(chave string) {
	if chave == "" || l.chave == "" {
		return
	}
	l.ultima, l.temUlt = valorChave(chave, l.passo.Origem, l.chave), true
}

// valorChave converte a chave do checkpoint (texto) para o tipo da coluna.
func valorChave(s string, t esquema.Tabela, coluna string) any {
	if c, ok := t.Coluna(coluna); ok && c.Tipo == esquema.Inteiro {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
	}
	return s
}

// contar devolve quantas linhas a leitura vai trazer.
func (l *leitor) contar(ctx context.Context) (int64, error) {
	q, args, err := l.d.consultaContagem(l.passo.Origem.Nome, l.passo.Map.Filtros)
	if err != nil {
		return 0, err
	}
	var n int64
	if err := l.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("contando %s: %w", l.passo.Origem.Nome, err)
	}
	return n, nil
}

// proximo lê até `limite` linhas. Lote vazio = acabou.
func (l *leitor) proximo(ctx context.Context, limite int) ([]transformar.Linha, error) {
	if l.fim {
		return nil, nil
	}
	if l.chave == "" {
		return l.proximoCursor(ctx, limite)
	}
	q, args, err := l.consulta(l.chave, l.ultima, l.temUlt, limite)
	if err != nil {
		return nil, err
	}
	rows, err := l.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("lendo %s: %w", l.passo.Origem.Nome, err)
	}
	defer rows.Close()
	linhas, err := l.ler(rows, limite)
	if err != nil {
		return nil, err
	}
	if len(linhas) < limite {
		l.fim = true
	}
	if n := len(linhas); n > 0 {
		l.ultima, l.temUlt = linhas[n-1][l.chave], true
	}
	return linhas, nil
}

func (l *leitor) proximoCursor(ctx context.Context, limite int) ([]transformar.Linha, error) {
	if l.rows == nil {
		q, args, err := l.consulta("", nil, false, 0)
		if err != nil {
			return nil, err
		}
		if l.rows, err = l.db.QueryContext(ctx, q, args...); err != nil {
			return nil, fmt.Errorf("lendo %s: %w", l.passo.Origem.Nome, err)
		}
	}
	linhas, err := l.ler(l.rows, limite)
	if err != nil {
		return nil, err
	}
	if len(linhas) < limite {
		l.fechar()
		l.fim = true
	}
	return linhas, nil
}

func (l *leitor) consulta(chave string, depoisDe any, temDepois bool, limite int) (string, []any, error) {
	if l.sel != nil {
		return l.d.consultaLoteSQL(l.passo.Origem.Nome, l.sel, chave, l.passo.Map.Filtros, depoisDe, temDepois, limite)
	}
	return l.d.consultaLote(l.passo.Origem.Nome, l.cols, chave, l.passo.Map.Filtros, depoisDe, temDepois, limite)
}

// ganchoLeitura só existe para os testes de integração: mexe na linha como o driver
// a entregou (simula um erro de leitura — como o do horário de verão — que as somas
// e a comparação canônica não têm como ver, porque nascem depois dele). nil em produção.
var ganchoLeitura func(tabela string, ln transformar.Linha)

func (l *leitor) ler(rows *sql.Rows, limite int) ([]transformar.Linha, error) {
	linhas := make([]transformar.Linha, 0, min(limite, 1024))
	vals := make([]any, len(l.cols))
	ptrs := make([]any, len(l.cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for len(linhas) < limite && rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("lendo linha de %s: %w", l.passo.Origem.Nome, err)
		}
		ln := make(transformar.Linha, len(l.cols))
		for i, c := range l.cols {
			if b, ok := vals[i].([]byte); ok { // o driver reaproveita o buffer
				vals[i] = slices.Clone(b)
			}
			ln[c] = vals[i]
		}
		if ganchoLeitura != nil {
			ganchoLeitura(l.passo.Origem.Nome, ln)
		}
		linhas = append(linhas, ln)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("lendo %s: %w", l.passo.Origem.Nome, err)
	}
	return linhas, nil
}

func (l *leitor) fechar() {
	if l.rows != nil {
		_ = l.rows.Close()
		l.rows = nil
	}
}

// chaveDe é o texto da chave de uma linha (para checkpoint e amostras).
func (l *leitor) chaveDe(ln transformar.Linha) string {
	if l.chave != "" {
		return transformar.ChaveTexto(ln[l.chave])
	}
	var partes []string
	for _, c := range l.passo.Origem.ChavePrimaria {
		if v, ok := ln[c]; ok {
			partes = append(partes, transformar.ChaveTexto(v))
		}
	}
	return strings.Join(partes, ",")
}

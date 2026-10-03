package copia

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
	"github.com/eduardorarruda/revoada/core/migracao/modelo"
	"github.com/eduardorarruda/revoada/core/migracao/plano"
	"github.com/eduardorarruda/revoada/core/migracao/transformar"
)

// Dupla conferência pelo texto dos bancos (ARQUITETURA §9.4).
//
// A comparação canônica (canonico.go) e as somas do conteúdo comparam o valor DEPOIS
// de lido pelo driver e convertido pelo agente com o que o destino guardou. Um erro
// que nasce ANTES — o driver montando a data no fuso local e perdendo um dia no
// início do horário de verão, um tipo mapeado errado — passa por elas, porque os dois
// lados herdam o mesmo erro. Esta conferência não usa a conversão tipada do driver
// para o valor comparado: cada banco renderiza o valor como TEXTO no próprio servidor
// e o agente compara os textos, com normalização mínima e documentada:
//
//   - segundos fracionários: o Firebird guarda 1/10 000 s, o PostgreSQL µs; a origem é
//     arredondada como o timestamp(p)/time(p) do destino arredonda (mesma regra da
//     comparação canônica);
//   - numeric: decimal exato (12.5 = 12.50), com o arredondamento de numeric(p,s);
//   - ponto flutuante: o Firebird imprime ~16 algarismos — tolerância relativa de
//     1e-15 em double; em real (float4) compara os dois como float32 (1 ulp);
//   - char(n): espaços à direita não contam (de qualquer um dos lados);
//   - nulo: marcador próprio (nulo só bate com nulo);
//   - timestamptz: o PostgreSQL devolve o relógio no fuso de origem
//     (col AT TIME ZONE '<fuso>') — o mesmo em que o agente lê o relógio da origem;
//   - BLOB de texto do Firebird 2.5 (CAST para VARCHAR é limitado): compara o tamanho
//     e os primeiros 8 000 caracteres — texto maior fica "conferido em parte";
//   - binário: só o tamanho (OCTET_LENGTH); o conteúdo fica com o hash canônico.
//
// Só entram colunas gravadas SEM transformação (o texto da origem é o valor gravado)
// e de tipos comparáveis; as demais vêm listadas com o motivo.

// prefixoTextoLongo: quanto de um BLOB de texto o Firebird 2.5 devolve como VARCHAR.
const prefixoTextoLongo = 8000

type familia int

const (
	famInteiro familia = iota
	famDecimal
	famFlutuante
	famData
	famDataHora
	famHora
	famTexto
	famJSON
	famUUID
	famBooleano
	famBinario
)

// colunaTexto é uma coluna na dupla conferência: como cada banco a renderiza e
// como os dois textos se comparam.
type colunaTexto struct {
	idx      int    // posição em p.Map.Colunas / dest
	nome     string // coluna de destino
	alias    string // apelido do item no SELECT (e nome na linha lida da origem)
	exprOrig string
	exprDest string
	fam      familia
	escala   int  // decimal do destino (-1 = sem limite)
	casas    int  // casas de segundo do destino
	real     bool // float4 no destino
	fixo     bool // char(n) de algum lado: espaços à direita não contam
	longo    bool // "tamanho:começo" (BLOB de texto do Firebird)
	parcial  string
}

var nomeFusoValido = regexp.MustCompile(`^[A-Za-z0-9_+\-/]+$`)

// nomeDoFuso devolve o nome IANA do fuso de origem (o mesmo que a conversão usa) para
// o PostgreSQL renderizar timestamptz como relógio. Vazio = não deu para descobrir.
func nomeDoFuso() string {
	loc := transformar.FusoOrigem
	if loc == nil {
		loc = time.Local
	}
	nome := loc.String()
	if nome == "Local" || nome == "" {
		nome = strings.TrimPrefix(os.Getenv("TZ"), ":")
		if i := strings.Index(nome, "zoneinfo/"); i >= 0 {
			nome = nome[i+len("zoneinfo/"):]
		}
	}
	if nome == "" {
		if alvo, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
			if i := strings.Index(alvo, "zoneinfo/"); i >= 0 {
				nome = alvo[i+len("zoneinfo/"):]
			}
		}
	}
	if nome == "" || !nomeFusoValido.MatchString(nome) {
		return ""
	}
	if _, err := time.LoadLocation(nome); err != nil {
		return ""
	}
	return nome
}

func temFuso(c esquema.Coluna) bool {
	n := strings.ToLower(c.TipoNativo)
	return strings.Contains(n, "with time zone") || strings.HasPrefix(n, "timestamptz")
}

func charFixo(c esquema.Coluna) bool {
	n := strings.ToLower(strings.TrimSpace(c.TipoNativo))
	return !strings.Contains(n, "varying") && (strings.HasPrefix(n, "char") || strings.HasPrefix(n, "character") || strings.HasPrefix(n, "bpchar"))
}

// familiaTexto decide se o par origem → destino tem texto comparável.
func familiaTexto(o, d esquema.Coluna) (familia, string) {
	ot, dt := o.Tipo, d.Tipo
	switch {
	case ot == esquema.Inteiro && dt == esquema.Inteiro:
		return famInteiro, ""
	case (ot == esquema.Inteiro || ot == esquema.Decimal) && dt == esquema.Decimal:
		return famDecimal, ""
	case (ot == esquema.Inteiro || ot == esquema.Decimal || ot == esquema.Flutuante) && dt == esquema.Flutuante:
		return famFlutuante, ""
	case (ot == esquema.Data || ot == esquema.DataHora) && dt == esquema.Data:
		return famData, ""
	case (ot == esquema.Data || ot == esquema.DataHora) && dt == esquema.DataHora:
		return famDataHora, ""
	case ot == esquema.Hora && dt == esquema.Hora:
		return famHora, ""
	case (ot == esquema.Texto || ot == esquema.TextoLongo) && (dt == esquema.Texto || dt == esquema.TextoLongo):
		return famTexto, ""
	case (ot == esquema.JSON || ot == esquema.TextoLongo) && dt == esquema.JSON:
		return famJSON, ""
	case ot == esquema.UUID && dt == esquema.UUID:
		return famUUID, ""
	case ot == esquema.Booleano && dt == esquema.Booleano:
		return famBooleano, ""
	case ot == esquema.Binario && dt == esquema.Binario:
		return famBinario, ""
	}
	return 0, fmt.Sprintf("tipos diferentes (%s → %s): o texto de um banco não é comparável com o do outro", ot, dt)
}

// colunasTexto escolhe as colunas da dupla conferência e diz por que as outras ficam
// de fora.
func colunasTexto(p plano.Passo, dOrigem, dDest dialeto, conf conferencia, dest []esquema.Coluna, casas map[string]int,
	fuso string) ([]colunaTexto, []plano.ColunaNaoConferida) {
	var cs []colunaTexto
	var fora []plano.ColunaNaoConferida
	for i, mc := range p.Map.Colunas {
		d, rg := dest[i], conf.regras[i]
		motivo := ""
		co, ok := p.Origem.Coluna(mc.ColunaOrigem)
		switch {
		case mc.Transformacao != modelo.Nenhuma && mc.Transformacao != "":
			motivo = fmt.Sprintf("passa pela transformação %q: o texto da origem não é o valor gravado", mc.Transformacao)
		case !ok:
			motivo = "não vem de uma coluna da origem"
		case !rg.comparavel:
			motivo = rg.motivo
		case (temFuso(*co) || temFuso(d)) && fuso == "":
			motivo = "não deu para descobrir o nome do fuso do agente para o PostgreSQL renderizar o instante como relógio"
		}
		if motivo != "" {
			fora = append(fora, plano.ColunaNaoConferida{Coluna: d.Nome, Motivo: motivo})
			continue
		}
		fam, porque := familiaTexto(*co, d)
		if porque != "" {
			fora = append(fora, plano.ColunaNaoConferida{Coluna: d.Nome, Motivo: porque})
			continue
		}
		ct := colunaTexto{idx: i, nome: d.Nome, alias: fmt.Sprintf("rv_texto_%d", i), fam: fam, escala: rg.escala,
			casas: rg.casasTempo, real: rg.real, fixo: charFixo(*co) || charFixo(d)}
		if pc, ok := casas[d.Nome]; ok && pc >= 0 && pc < casasTempoPadrao {
			ct.casas = pc
		}
		if dOrigem.semAspa && (fam == famData || fam == famDataHora || fam == famHora) {
			fora = append(fora, plano.ColunaNaoConferida{Coluna: d.Nome,
				Motivo: "Firebird de dialeto 1: o DATE tem hora e é renderizado em outro formato; fica com a conferência canônica"})
			continue
		}
		longoFB := dOrigem.motor == "firebird" && co.Tipo == esquema.TextoLongo
		if longoFB && fam == famJSON {
			fora = append(fora, plano.ColunaNaoConferida{Coluna: d.Nome,
				Motivo: "JSON em BLOB: o Firebird 2.5 não devolve o BLOB inteiro como texto; o conteúdo fica com a conferência canônica"})
			continue
		}
		ct.longo = longoFB && fam == famTexto
		var err error
		if ct.exprOrig, err = exprTexto(dOrigem, *co, ct, fuso); err == nil {
			ct.exprDest, err = exprTexto(dDest, d, ct, fuso)
		}
		if err != nil {
			fora = append(fora, plano.ColunaNaoConferida{Coluna: d.Nome, Motivo: err.Error()})
			continue
		}
		switch {
		case fam == famBinario:
			ct.parcial = "binário: conferido pelo texto só no tamanho (OCTET_LENGTH dos dois lados); o conteúdo fica com a conferência canônica (hex)"
		case ct.longo:
			ct.parcial = fmt.Sprintf("BLOB de texto: o Firebird 2.5 devolve no máximo %d caracteres como VARCHAR; textos maiores são conferidos pelo tamanho e pelo começo", prefixoTextoLongo)
		}
		cs = append(cs, ct)
	}
	return cs, fora
}

// exprTexto monta a expressão que faz o banco devolver o valor como texto. Nomes vêm
// da foto do schema (entre aspas); o fuso foi validado (letras, /, _, +, -).
func exprTexto(d dialeto, c esquema.Coluna, ct colunaTexto, fuso string) (string, error) {
	q, err := d.id(c.Nome)
	if err != nil {
		return "", err
	}
	if d.motor == "firebird" {
		switch {
		case ct.fam == famBinario:
			return "CAST(OCTET_LENGTH(" + q + ") AS VARCHAR(20))", nil
		case ct.longo:
			return fmt.Sprintf("CAST(CHAR_LENGTH(%[1]s) AS VARCHAR(12)) || ':' || CAST(SUBSTRING(%[1]s FROM 1 FOR %[2]d) AS VARCHAR(%[2]d))", q, prefixoTextoLongo), nil
		case c.Tipo == esquema.Texto:
			return q, nil
		}
		return "CAST(" + q + " AS VARCHAR(64))", nil
	}
	if d.motor != "postgres" {
		return "", fmt.Errorf("a conferência pelo texto não sabe pedir texto ao motor %s", d.motor)
	}
	relogio := q
	if temFuso(c) {
		relogio = fmt.Sprintf("(%s AT TIME ZONE '%s')", q, fuso)
	}
	switch {
	case ct.fam == famBinario:
		return "octet_length(" + q + ")::text", nil
	case ct.longo:
		return fmt.Sprintf("char_length(%[1]s)::text || ':' || substr(%[1]s, 1, %[2]d)", q, prefixoTextoLongo), nil
	case c.Tipo == esquema.Data:
		return "to_char(" + q + ", 'YYYY-MM-DD')", nil // ::text dependeria do DateStyle
	case c.Tipo == esquema.DataHora:
		return "to_char(" + relogio + ", 'YYYY-MM-DD HH24:MI:SS.US')", nil
	case c.Tipo == esquema.Hora:
		return "to_char(DATE '2000-01-01' + " + q + ", 'HH24:MI:SS.US')", nil
	}
	return q + "::text", nil
}

// textoDe: o item lido é texto (o driver só decodifica o charset); nil = NULL.
func textoDe(v any) *string {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return &x
	case []byte:
		s := string(x)
		return &s
	}
	s := fmt.Sprintf("?%T:%v", v, v) // não deveria acontecer: nunca bate com um texto
	return &s
}

// comparar diz se os dois textos representam o mesmo valor (e se a comparação foi
// só parcial, no caso dos textos longos).
func (ct colunaTexto) comparar(o, d *string) (igual, parcial bool) {
	if o == nil || d == nil {
		return o == nil && d == nil, false
	}
	a, b := strings.TrimSpace(*o), strings.TrimSpace(*d)
	switch ct.fam {
	case famInteiro:
		x, ok1 := new(big.Int).SetString(a, 10)
		y, ok2 := new(big.Int).SetString(b, 10)
		return ok1 && ok2 && x.Cmp(y) == 0, false
	case famDecimal:
		x, ok1 := new(big.Rat).SetString(a)
		y, ok2 := new(big.Rat).SetString(b)
		if !ok1 || !ok2 {
			return a == b, false
		}
		if ct.escala >= 0 {
			x = arredondarRat(x, ct.escala)
		}
		return x.Cmp(y) == 0, false
	case famFlutuante:
		return floatsIguais(a, b, ct.real), false
	case famData:
		return len(a) >= 10 && len(b) >= 10 && a[:10] == b[:10], false
	case famDataHora:
		x, ok1 := microsTexto(a)
		y, ok2 := microsTexto(b)
		return ok1 && ok2 && arredondarMicros(x, ct.casas) == y, false
	case famHora:
		x, ok1 := microsHora(a)
		y, ok2 := microsHora(b)
		return ok1 && ok2 && arredondarMicros(x, ct.casas) == y, false
	case famTexto:
		a, b = *o, *d // texto: byte a byte (espaços contam), menos o preenchimento de char(n)
		if ct.longo {
			return comparaLongo(a, b, ct.fixo)
		}
		if ct.fixo {
			a, b = strings.TrimRight(a, " "), strings.TrimRight(b, " ")
		}
		return a == b, false
	case famJSON:
		return canonJSON(a) == canonJSON(b), false
	case famUUID:
		return strings.EqualFold(a, b), false
	case famBooleano:
		x, ok1 := boolTexto(a)
		y, ok2 := boolTexto(b)
		return ok1 && ok2 && x == y, false
	case famBinario:
		return a == b, true
	}
	return a == b, false
}

func comparaLongo(a, b string, fixo bool) (bool, bool) {
	na, pa, ok1 := strings.Cut(a, ":")
	nb, pb, ok2 := strings.Cut(b, ":")
	if !ok1 || !ok2 || na != nb {
		return false, false
	}
	if fixo {
		pa, pb = strings.TrimRight(pa, " "), strings.TrimRight(pb, " ")
	}
	n, _ := strconv.Atoi(na)
	return pa == pb, n > prefixoTextoLongo
}

func floatsIguais(a, b string, real bool) bool {
	x, err1 := strconv.ParseFloat(a, 64)
	y, err2 := strconv.ParseFloat(b, 64)
	if err1 != nil || err2 != nil {
		return strings.EqualFold(a, b) // NaN, Infinity
	}
	if real {
		fx, fy := float32(x), float32(y)
		return fx == fy || math.Nextafter32(fx, fy) == fy
	}
	if x == y {
		return true
	}
	return math.Abs(x-y) <= 1e-15*math.Max(math.Abs(x), math.Abs(y))
}

func boolTexto(s string) (bool, bool) {
	switch strings.ToLower(s) {
	case "t", "true", "1", "y", "yes":
		return true, true
	case "f", "false", "0", "n", "no":
		return false, true
	}
	return false, false
}

// microsTexto: "AAAA-MM-DD[ HH:MM:SS[.fração]]" → µs desde 2000-01-01 (parse do
// TEXTO devolvido pelo banco; fração além de µs é truncada, como o pgx faz).
func microsTexto(s string) (int64, bool) {
	s = strings.Replace(s, "T", " ", 1)
	for _, l := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02"} {
		if t, err := time.Parse(l, s); err == nil {
			return microsDataHora(t, false, casasTempoPadrao), true
		}
	}
	return 0, false
}

// ---------------------------------------------------------------- execução

// planoTexto é a dupla conferência de uma tabela: as colunas, o placar e o que ficou
// de fora. Os métodos aceitam receptor nil (tabela sem conferência pelo texto).
type planoTexto struct {
	cols         []colunaTexto
	ct           *plano.ConferenciaTexto
	parcial      []bool
	semLocalizar int64
}

func novoPlanoTexto(p plano.Passo, dOrigem, dDest dialeto, conf conferencia, dest []esquema.Coluna, casas map[string]int,
	fuso string) *planoTexto {
	cols, fora := colunasTexto(p, dOrigem, dDest, conf, dest, casas, fuso)
	pt := &planoTexto{cols: cols, parcial: make([]bool, len(cols)),
		ct: &plano.ConferenciaTexto{PorColuna: map[string]int64{}, NaoConferidas: fora}}
	if len(cols) == 0 {
		pt.ct.Motivo = "nenhuma coluna desta tabela pode ser conferida pelo texto (veja o motivo de cada uma)"
	}
	return pt
}

func (pt *planoTexto) ativo() bool { return pt != nil && len(pt.cols) > 0 }

// estender põe as expressões de texto na MESMA leitura da origem (com apelido, para
// não colidir com o nome da coluna no ORDER BY).
func (pt *planoTexto) estender(l *leitor, d dialeto) error {
	sel := make([]string, 0, len(l.cols)+len(pt.cols))
	for _, c := range l.cols {
		q, err := d.id(c)
		if err != nil {
			return err
		}
		sel = append(sel, q)
	}
	for _, c := range pt.cols {
		a, err := d.id(c.alias)
		if err != nil {
			return err
		}
		sel = append(sel, c.exprOrig+" AS "+a)
		l.cols = append(l.cols, c.alias)
	}
	l.sel = sel
	return nil
}

func (pt *planoTexto) expressoesDestino(d dialeto) []string {
	out := make([]string, len(pt.cols))
	for i, c := range pt.cols {
		a, _ := d.id(c.alias)
		out[i] = c.exprDest + " AS " + a
	}
	return out
}

func (pt *planoTexto) textosDaOrigem(ln transformar.Linha) []*string {
	if !pt.ativo() {
		return nil
	}
	out := make([]*string, len(pt.cols))
	for i, c := range pt.cols {
		out[i] = textoDe(ln[c.alias])
	}
	return out
}

func (pt *planoTexto) divergiu(chave string, cs []string, ausente bool) {
	if len(pt.ct.Divergencias) < plano.AmostraDivergencias {
		pt.ct.Divergencias = append(pt.ct.Divergencias, plano.Divergencia{Chave: chave, Colunas: cs, Ausente: ausente})
	}
}

func (pt *planoTexto) naoLocalizada() {
	if pt.ativo() {
		pt.semLocalizar++
	}
}

func (pt *planoTexto) ausente(chave string) {
	if !pt.ativo() {
		return
	}
	pt.ct.Linhas++
	pt.ct.Ausentes++
	pt.divergiu(chave, nil, true)
}

// comparar confronta o texto da origem com o do destino, coluna a coluna.
func (pt *planoTexto) comparar(chave string, origem []*string, destino []any) {
	if !pt.ativo() {
		return
	}
	pt.ct.Linhas++
	var dif []string
	for i, c := range pt.cols {
		var d *string
		if i < len(destino) {
			d = textoDe(destino[i])
		}
		igual, parc := c.comparar(origem[i], d)
		if parc {
			pt.parcial[i] = true
		}
		if !igual {
			dif = append(dif, c.nome)
			pt.ct.PorColuna[c.nome]++
		}
	}
	if len(dif) > 0 {
		pt.ct.Divergentes++
		pt.divergiu(chave, dif, false)
	} else {
		pt.ct.Identicas++
	}
}

func (pt *planoTexto) fechar() *plano.ConferenciaTexto {
	if pt == nil {
		return nil
	}
	ct := pt.ct
	for i, c := range pt.cols {
		switch {
		case c.fam == famBinario, c.longo && pt.parcial[i]:
			ct.Parciais = append(ct.Parciais, plano.ColunaNaoConferida{Coluna: c.nome, Motivo: c.parcial})
		default:
			ct.Colunas = append(ct.Colunas, c.nome)
		}
	}
	if pt.semLocalizar > 0 {
		ct.Motivo = fmt.Sprintf("%d linhas não puderam ser localizadas no destino pela chave", pt.semLocalizar)
	}
	if len(ct.PorColuna) == 0 {
		ct.PorColuna = nil
	}
	ct.OK = pt.ativo() && ct.Divergentes == 0 && ct.Ausentes == 0 && pt.semLocalizar == 0
	return ct
}

// cancelada distingue pausa/cancelamento (interrompe a tarefa) de erro do banco ao
// renderizar o texto (a tabela é refeita sem a dupla conferência, com o motivo).
func cancelada(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

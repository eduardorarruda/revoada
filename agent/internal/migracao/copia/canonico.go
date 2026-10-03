package copia

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
	"github.com/eduardorarruda/revoada/core/migracao/transformar"
)

// Forma canônica de um valor (ARQUITETURA §9.4, conferência de conteúdo): o MESMO texto
// para o valor que o motor manda gravar (saída de converter/Ajustar) e para o valor
// que o PostgreSQL devolve ao ser relido pelo driver pgx — desde que o destino tenha
// guardado exatamente o que se esperava. A regra modela o que o PostgreSQL faz de
// legítimo ao gravar (arredondar numeric(p,s) para s casas, float4, timestamp(p),
// char(n) completando com espaços, jsonb reordenando chaves, uuid em minúsculas) e
// NADA além disso: truncar texto, trocar acento, deslocar fuso, perder casas fora do
// que a coluna prevê… tudo isso muda a forma canônica e aparece como divergência.
//
// Tipos lidos pelo pgx (stdlib) com Scan em any: inteiros → int64; real/double →
// float64; numeric, time, uuid, char/varchar/text e tipos desconhecidos → string
// (texto do PostgreSQL); date/timestamp/timestamptz → time.Time; bytea, json e
// jsonb → []byte; bool → bool.

// regra é como canonizar uma coluna de destino.
type regra struct {
	nome       string
	tipo       esquema.TipoLogico
	comparavel bool
	motivo     string // por que não é comparável
	escala     int    // decimal: casas da coluna (o PG arredonda); -1 = numeric sem limite
	real       bool   // float4: o PG guarda float32
	comFuso    bool   // timestamptz: compara o instante (UTC); sem fuso compara o relógio
	bpchar     bool   // char(n): espaços à direita não contam (o PG completa até n)
	casasTempo int    // timestamp(p)/time(p): o PG arredonda para p casas de segundo
}

// casasTempoPadrao: o PostgreSQL guarda microssegundos.
const casasTempoPadrao = 6

// novaRegra decide a canonização pela coluna da foto do destino. casasTempo vem do
// catálogo do destino (information_schema.columns.datetime_precision), que a foto
// não guarda; ausente = 6.
func novaRegra(c esquema.Coluna, casasTempo map[string]int) regra {
	r := regra{nome: c.Nome, tipo: c.Tipo, comparavel: true, escala: -1, casasTempo: casasTempoPadrao}
	if p, ok := casasTempo[c.Nome]; ok && p >= 0 && p < casasTempoPadrao {
		r.casasTempo = p
	}
	nat := strings.ToLower(strings.TrimSpace(c.TipoNativo))
	switch c.Tipo {
	case esquema.Desconhecido:
		r.comparavel = false
		r.motivo = fmt.Sprintf("o tipo %s não tem forma canônica conhecida: o PostgreSQL pode reescrever o valor ao gravar", c.TipoNativo)
	case esquema.Decimal:
		if nat == "money" {
			r.comparavel = false
			r.motivo = "money é devolvido formatado pela localidade do servidor (R$, separadores): não dá para comparar com segurança"
		}
		if c.Precisao > 0 {
			r.escala = c.Escala
		}
	case esquema.Flutuante:
		r.real = nat == "real" || nat == "float4"
	case esquema.DataHora:
		r.comFuso = strings.Contains(nat, "with time zone") || strings.HasPrefix(nat, "timestamptz")
	case esquema.Hora:
		if strings.Contains(nat, "with time zone") || strings.HasPrefix(nat, "timetz") {
			r.comparavel = false
			r.motivo = "hora com fuso (timetz): o PostgreSQL guarda o deslocamento do servidor e reescreve o valor"
		}
	case esquema.Texto:
		r.bpchar = !strings.Contains(nat, "varying") &&
			(strings.HasPrefix(nat, "character") || strings.HasPrefix(nat, "char") || strings.HasPrefix(nat, "bpchar"))
	}
	return r
}

// marcadores: nulo e "valor" nunca se confundem (o texto do valor vem depois do V).
const (
	canonNulo   = "N"
	canonPadrao = "D" // UsarPadrao: o valor nasce no destino; nunca é comparado
)

// canonico devolve a forma canônica de v para a coluna.
func (r regra) canonico(v any) string {
	if v == nil {
		return canonNulo
	}
	if v == transformar.UsarPadrao {
		return canonPadrao
	}
	return "V" + r.valor(v)
}

func (r regra) valor(v any) string {
	switch r.tipo {
	case esquema.Inteiro:
		return canonInteiro(v)
	case esquema.Decimal:
		return canonDecimal(v, r.escala)
	case esquema.Flutuante:
		return canonFlutuante(v, r.real)
	case esquema.Texto, esquema.TextoLongo:
		s := textoBruto(v)
		if r.bpchar {
			s = strings.TrimRight(s, " ")
		}
		return s
	case esquema.Data:
		if t, ok := tempoDe(v); ok {
			return fmt.Sprintf("%04d-%02d-%02d", t.Year(), t.Month(), t.Day())
		}
	case esquema.DataHora:
		if t, ok := tempoDe(v); ok {
			return "T" + strconv.FormatInt(microsDataHora(t, r.comFuso, r.casasTempo), 10)
		}
	case esquema.Hora:
		if us, ok := microsHora(v); ok {
			return "H" + strconv.FormatInt(arredondarMicros(us, r.casasTempo), 10)
		}
	case esquema.Booleano:
		if b, ok := v.(bool); ok {
			return strconv.FormatBool(b)
		}
	case esquema.Binario:
		switch x := v.(type) {
		case []byte:
			return hex.EncodeToString(x)
		case string:
			return hex.EncodeToString([]byte(x))
		}
	case esquema.JSON:
		return canonJSON(textoBruto(v))
	case esquema.UUID:
		return strings.ToLower(strings.TrimSpace(textoBruto(v)))
	}
	// forma inesperada: marca o tipo Go — se os dois lados chegarem diferentes, a
	// divergência aparece (nunca um falso OK).
	return fmt.Sprintf("?%T:%s", v, transformar.ChaveTexto(v))
}

func textoBruto(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	}
	return transformar.Texto(v)
}

func canonInteiro(v any) string {
	switch x := v.(type) {
	case int64:
		return strconv.FormatInt(x, 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case int16:
		return strconv.FormatInt(int64(x), 10)
	case int8:
		return strconv.FormatInt(int64(x), 10)
	case int:
		return strconv.FormatInt(int64(x), 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case uint32:
		return strconv.FormatUint(uint64(x), 10)
	case *big.Int:
		return x.String()
	case bool, float32, float64, time.Time:
		return fmt.Sprintf("?%T:%v", v, v)
	}
	// numeric(39,0) (INT128 que nasceu no destino) volta como texto
	s := strings.TrimSpace(textoBruto(v))
	if r, ok := new(big.Rat).SetString(s); ok && r.IsInt() {
		return r.Num().String()
	}
	return "?" + s
}

// canonDecimal: valor exato (big.Rat) arredondado como o PostgreSQL arredonda ao
// gravar em numeric(p,s) — metade para longe do zero. 12.5 e 12.50 dão o mesmo.
func canonDecimal(v any, escala int) string {
	var s string
	switch x := v.(type) {
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	case float32:
		s = strconv.FormatFloat(float64(x), 'f', -1, 32)
	case *big.Rat:
		s = x.RatString()
	case bool, time.Time:
		return fmt.Sprintf("?%T:%v", v, v)
	default:
		s = strings.TrimSpace(textoBruto(v))
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return "?" + s // NaN, Infinity…: o texto como veio
	}
	if escala >= 0 {
		r = arredondarRat(r, escala)
	}
	return r.RatString()
}

// arredondarRat arredonda para `casas` decimais, metade para longe do zero (a regra
// do numeric do PostgreSQL).
func arredondarRat(r *big.Rat, casas int) *big.Rat {
	m := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(casas)), nil)
	n := new(big.Int).Mul(r.Num(), m)
	q, resto := new(big.Int).QuoRem(n, r.Denom(), new(big.Int))
	resto.Abs(resto).Lsh(resto, 1)
	if resto.Cmp(r.Denom()) >= 0 {
		q.Add(q, big.NewInt(int64(n.Sign())))
	}
	return new(big.Rat).SetFrac(q, m)
}

func canonFlutuante(v any, real bool) string {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case float32:
		f = float64(x)
	case int64:
		f = float64(x)
	case int32:
		f = float64(x)
	case int:
		f = float64(x)
	default:
		p, err := strconv.ParseFloat(strings.TrimSpace(textoBruto(v)), 64)
		if err != nil {
			return fmt.Sprintf("?%T:%v", v, v)
		}
		f = p
	}
	if real { // o pgx manda float32(x) para uma coluna real
		return strconv.FormatFloat(float64(float32(f)), 'g', -1, 32)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func tempoDe(v any) (time.Time, bool) {
	t, ok := v.(time.Time)
	return t, ok
}

// epoca2000 é a origem do relógio interno do PostgreSQL (o arredondamento de
// timestamp(p) é feito sobre os microssegundos desde ela).
var epoca2000 = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).Unix() * 1_000_000

// microsDataHora: microssegundos desde 2000-01-01. timestamptz compara o INSTANTE;
// timestamp sem fuso compara o RELÓGIO (o pgx grava os campos de data/hora como
// estão, descartando o fuso — e devolve em UTC). Sub-microssegundo é truncado
// (o pgx trunca ao mandar); depois vem o arredondamento de timestamp(p).
func microsDataHora(t time.Time, comFuso bool, casas int) int64 {
	if comFuso {
		t = t.UTC()
	} else if t.Location() != time.UTC {
		t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
	}
	us := t.Unix()*1_000_000 + int64(t.Nanosecond()/1000) - epoca2000
	return arredondarMicros(us, casas)
}

// arredondarMicros imita AdjustTimestampForTypmod/AdjustTimeForTypmod do PostgreSQL:
// metade para longe do zero na escala de 10^(6-casas) microssegundos.
func arredondarMicros(us int64, casas int) int64 {
	if casas >= casasTempoPadrao || casas < 0 {
		return us
	}
	escala := int64(math.Pow10(casasTempoPadrao - casas))
	if us >= 0 {
		return (us + escala/2) / escala * escala
	}
	return -((-us + escala/2) / escala * escala)
}

// microsHora: microssegundos desde a meia-noite. O motor manda time.Time (o pgx usa
// só o relógio); o PostgreSQL devolve texto "15:04:05[.ffffff]".
func microsHora(v any) (int64, bool) {
	switch x := v.(type) {
	case time.Time:
		return int64(x.Hour())*3_600_000_000 + int64(x.Minute())*60_000_000 + int64(x.Second())*1_000_000 +
			int64(x.Nanosecond()/1000), true
	case string:
		s := strings.TrimSpace(x)
		if s == "24:00:00" {
			return 86_400_000_000, true
		}
		t, err := time.Parse("15:04:05.999999", s)
		if err != nil {
			return 0, false
		}
		return microsHora(t)
	}
	return 0, false
}

// canonJSON reescreve o JSON numa forma única: chaves de objeto em ordem, sem
// espaços, números exatos (big.Rat) — json e jsonb comparam igual quando têm o
// mesmo conteúdo (o jsonb reordena chaves, tira espaços e fica com a última chave
// repetida, como o decodificador do Go).
func canonJSON(s string) string {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var x any
	if err := dec.Decode(&x); err != nil {
		return "?" + s
	}
	if _, err := dec.Token(); err != io.EOF {
		return "?" + s
	}
	var b bytes.Buffer
	escreverJSON(&b, x)
	return b.String()
}

func escreverJSON(b *bytes.Buffer, x any) {
	switch v := x.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(v))
	case json.Number:
		if r, ok := new(big.Rat).SetString(string(v)); ok {
			b.WriteString("#" + r.RatString())
		} else {
			b.WriteString(string(v))
		}
	case string:
		q, _ := json.Marshal(v)
		b.Write(q)
	case []any:
		b.WriteByte('[')
		for i, e := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			escreverJSON(b, e)
		}
		b.WriteByte(']')
	case map[string]any:
		chaves := make([]string, 0, len(v))
		for k := range v {
			chaves = append(chaves, k)
		}
		sort.Strings(chaves)
		b.WriteByte('{')
		for i, k := range chaves {
			if i > 0 {
				b.WriteByte(',')
			}
			q, _ := json.Marshal(k)
			b.Write(q)
			b.WriteByte(':')
			escreverJSON(b, v[k])
		}
		b.WriteByte('}')
	}
}

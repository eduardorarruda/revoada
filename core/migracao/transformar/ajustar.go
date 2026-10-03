package transformar

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
)

// Tipos de violação: a linha seria RECUSADA pelo destino. Bloqueiam a execução.
const (
	VioNulo         = "nulo"           // NOT NULL sem valor
	VioTamanho      = "tamanho"        // texto maior que a coluna
	VioNumero       = "numero"         // número não cabe (precisão/faixa)
	VioTipo         = "tipo"           // o valor não vira o tipo do destino
	VioTextoInvalid = "texto_invalido" // bytes que não são UTF-8 ou caractere nulo
	VioUnica        = "unica"          // repete chave primária/única
	VioEstrangeira  = "estrangeira"    // aponta para pai que não existe
	VioTransformar  = "transformacao"  // a transformação falhou
)

// Tipos de perda: a linha entra, mas algo do valor se perde. Avisam, não bloqueiam.
const (
	PerdaCasas = "arredonda_casas"
	PerdaHora  = "descarta_hora"
	// PerdaCharset: o texto parece UTF-8 gravado num banco WIN1252 ("JosÃ©"). Entra
	// como está; a transformação "converter charset" com reparar=sim conserta.
	PerdaCharset = "suspeita_charset"
)

// Violacao diz por que o valor não entra no destino. A mensagem NUNCA traz o valor
// (o relatório vai para o painel e pode chegar ao MCP: só metadado, nunca dado).
type Violacao struct {
	Tipo     string `json:"tipo"`
	Mensagem string `json:"mensagem"`
}

func (v *Violacao) Error() string { return v.Mensagem }

func vio(tipo, f string, a ...any) *Violacao {
	return &Violacao{Tipo: tipo, Mensagem: fmt.Sprintf(f, a...)}
}

// UsarPadrao é o valor que manda o destino usar o DEFAULT da coluna (a origem veio
// nula e a coluna tem padrão).
type usarPadrao struct{}

// UsarPadrao sinaliza ao gravador: escreva DEFAULT, não NULL.
var UsarPadrao any = usarPadrao{}

// Ajustar converte o valor para o tipo lógico da coluna de destino e confere se
// cabe. Devolve o valor normalizado (int64, float64, string decimal, string, bool,
// time.Time, []byte, nil ou UsarPadrao), a perda (se houver) e a violação.
func Ajustar(v any, d esquema.Coluna) (valor any, perda string, violacao *Violacao) {
	if v == nil {
		switch {
		case d.Nulavel: // nulo na origem continua nulo (o padrão não muda o sentido)
		case d.Padrao != "" || d.Identidade:
			return UsarPadrao, "", nil
		default:
			return nil, "", vio(VioNulo, "a coluna %s é obrigatória e o valor veio nulo", d.Nome)
		}
		return nil, "", nil
	}
	switch d.Tipo {
	case esquema.Inteiro:
		return paraInteiro(v, d)
	case esquema.Decimal:
		return paraDecimal(v, d)
	case esquema.Flutuante:
		f, err := numero(v)
		if err != nil {
			return nil, "", vio(VioTipo, "%s: %v", d.Nome, err)
		}
		return f, "", nil
	case esquema.Texto, esquema.TextoLongo, esquema.Desconhecido:
		return paraTexto(v, d)
	case esquema.Data, esquema.DataHora, esquema.Hora:
		return paraTempo(v, d)
	case esquema.Booleano:
		return paraBooleano(v, d)
	case esquema.Binario:
		switch x := v.(type) {
		case []byte:
			return x, "", nil
		case string:
			return []byte(x), "", nil
		}
		return nil, "", vio(VioTipo, "%s: o valor não é binário", d.Nome)
	case esquema.JSON:
		s := Texto(v)
		if !json.Valid([]byte(s)) {
			return nil, "", vio(VioTipo, "%s: o texto não é JSON válido", d.Nome)
		}
		return s, "", nil
	case esquema.UUID:
		s := strings.TrimSpace(Texto(v))
		if len(s) != 36 || strings.Count(s, "-") != 4 {
			return nil, "", vio(VioTipo, "%s: o valor não é um UUID", d.Nome)
		}
		return s, "", nil
	}
	return v, "", nil
}

func paraTexto(v any, d esquema.Coluna) (any, string, *Violacao) {
	var s string
	if b, ok := v.([]byte); ok {
		if !utf8.Valid(b) {
			return nil, "", vio(VioTextoInvalid, "%s: o texto não é UTF-8 válido (use a transformação \"converter charset\")", d.Nome)
		}
		s = string(b)
	} else {
		s = Texto(v)
	}
	if !utf8.ValidString(s) {
		return nil, "", vio(VioTextoInvalid, "%s: o texto não é UTF-8 válido (use a transformação \"converter charset\")", d.Nome)
	}
	if strings.IndexByte(s, 0) >= 0 {
		return nil, "", vio(VioTextoInvalid, "%s: o texto tem caractere nulo (0x00), que o PostgreSQL não aceita", d.Nome)
	}
	if d.Tipo == esquema.Texto && d.Tamanho > 0 {
		if n := utf8.RuneCountInString(s); n > d.Tamanho {
			return nil, "", vio(VioTamanho, "%s: texto com %d caracteres não cabe em %d", d.Nome, n, d.Tamanho)
		}
	}
	if SuspeitaDuplaCodificacao(s) {
		return s, PerdaCharset, nil
	}
	return s, "", nil
}

// limitesInteiro pela forma nativa do destino.
func limitesInteiro(nativo string) (int64, int64) {
	n := strings.ToLower(nativo)
	switch {
	case strings.Contains(n, "smallint") || n == "int2":
		return math.MinInt16, math.MaxInt16
	case strings.Contains(n, "bigint") || n == "int8":
		return math.MinInt64, math.MaxInt64
	case strings.Contains(n, "int"):
		return math.MinInt32, math.MaxInt32
	}
	return math.MinInt64, math.MaxInt64
}

func paraInteiro(v any, d esquema.Coluna) (any, string, *Violacao) {
	var i int64
	perda := ""
	switch x := v.(type) {
	case int64:
		i = x
	case int32:
		i = int64(x)
	case int16:
		i = int64(x)
	case int:
		i = int64(x)
	case bool:
		if x {
			i = 1
		}
	case float64:
		if x != math.Trunc(x) {
			perda = PerdaCasas
		}
		// 2^63 como float: MaxInt64 não é representável exatamente em float64
		if x >= 9.223372036854775807e18 || x < -9.223372036854775808e18 || math.IsNaN(x) {
			return nil, "", vio(VioNumero, "%s: número fora da faixa", d.Nome)
		}
		i = int64(x)
	default:
		s := strings.TrimSpace(Texto(v))
		r, ok := new(big.Rat).SetString(s)
		if !ok {
			return nil, "", vio(VioTipo, "%s: o valor não é um número inteiro", d.Nome)
		}
		if !r.IsInt() {
			perda = PerdaCasas
		}
		q := new(big.Int).Quo(r.Num(), r.Denom())
		if !q.IsInt64() {
			return nil, "", vio(VioNumero, "%s: número fora da faixa", d.Nome)
		}
		i = q.Int64()
	}
	lo, hi := limitesInteiro(d.TipoNativo)
	if i < lo || i > hi {
		return nil, "", vio(VioNumero, "%s: %d dígitos não cabem em %s", d.Nome, len(strconv.FormatInt(i, 10)), d.TipoNativo)
	}
	return i, perda, nil
}

// paraDecimal devolve o número como texto decimal exato (sem passar por float).
func paraDecimal(v any, d esquema.Coluna) (any, string, *Violacao) {
	var s string
	switch x := v.(type) {
	case int64, int32, int16, int:
		s = fmt.Sprint(x)
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return nil, "", vio(VioTipo, "%s: verdadeiro/falso não vira decimal", d.Nome)
	default:
		s = strings.TrimSpace(Texto(v))
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, "", vio(VioTipo, "%s: o valor não é um número", d.Nome)
	}
	inteiros, casas := digitos(s)
	perda := ""
	if d.Precisao > 0 {
		if inteiros > d.Precisao-d.Escala {
			return nil, "", vio(VioNumero, "%s: %d dígitos antes da vírgula não cabem em numeric(%d,%d)", d.Nome, inteiros, d.Precisao, d.Escala)
		}
		if casas > d.Escala {
			perda = PerdaCasas
		}
	}
	return r.FloatString(max(casas, 0)), perda, nil
}

// digitos conta os dígitos significativos antes e depois do ponto.
func digitos(s string) (inteiros, casas int) {
	s = strings.TrimLeft(s, "+-")
	ip, fp, _ := strings.Cut(s, ".")
	ip = strings.TrimLeft(ip, "0")
	fp = strings.TrimRight(fp, "0")
	return len(ip), len(fp)
}

func numero(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case float32:
		return float64(x), nil
	case int64:
		return float64(x), nil
	case int32:
		return float64(x), nil
	case int16:
		return float64(x), nil
	case int:
		return float64(x), nil
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(Texto(v)), 64)
	if err != nil {
		return 0, fmt.Errorf("o valor não é um número")
	}
	return f, nil
}

var layoutsTempo = []string{
	time.RFC3339Nano, "2006-01-02T15:04:05.999999", "2006-01-02 15:04:05.999999", "2006-01-02 15:04:05",
	time.DateOnly, "02/01/2006 15:04:05", "02/01/2006", "15:04:05", "15:04",
}

// FusoOrigem é o fuso em que um relógio sem fuso da origem é lido quando o destino
// guarda instante (timestamptz). É o do servidor onde o agente roda — o mesmo do
// banco do cliente, na instalação típica. nil = time.Local no momento da conversão.
var FusoOrigem *time.Location

func fusoOrigem() *time.Location {
	if FusoOrigem != nil {
		return FusoOrigem
	}
	return time.Local
}

// comFuso diz se a coluna de destino guarda instante (timestamptz).
func comFuso(d esquema.Coluna) bool {
	n := strings.ToLower(d.TipoNativo)
	return strings.Contains(n, "with time zone") || strings.HasPrefix(n, "timestamptz")
}

func paraTempo(v any, d esquema.Coluna) (any, string, *Violacao) {
	var t time.Time
	switch x := v.(type) {
	case time.Time:
		t = x
	default:
		s := strings.TrimSpace(Texto(v))
		ok := false
		for _, l := range layoutsTempo {
			if p, err := time.Parse(l, s); err == nil {
				t, ok = p, true
				break
			}
		}
		if !ok {
			return nil, "", vio(VioTipo, "%s: o texto não é uma data/hora reconhecida (use \"texto → data\" com o formato)", d.Nome)
		}
	}
	if d.Tipo == esquema.Data {
		perda := ""
		if t.Hour() != 0 || t.Minute() != 0 || t.Second() != 0 || t.Nanosecond() != 0 {
			perda = PerdaHora
		}
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), perda, nil
	}
	if d.Tipo == esquema.DataHora && comFuso(d) && t.Location() == time.UTC {
		// Relógio de parede (TIMESTAMP do Firebird, timestamp sem fuso do PostgreSQL —
		// os leitores entregam em UTC) indo para uma coluna COM fuso: o relógio é o do
		// servidor de origem, então vira instante no fuso local do agente, como faz o
		// próprio PostgreSQL num timestamp::timestamptz. Antes isto acontecia por
		// acidente dentro do driver, e errava nos dias de horário de verão.
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), fusoOrigem()), "", nil
	}
	return t, "", nil
}

func paraBooleano(v any, d esquema.Coluna) (any, string, *Violacao) {
	switch x := v.(type) {
	case bool:
		return x, "", nil
	case int64, int32, int16, int:
		n := fmt.Sprint(x)
		if n == "0" || n == "1" {
			return n == "1", "", nil
		}
	}
	switch strings.ToUpper(strings.TrimSpace(Texto(v))) {
	case "S", "SIM", "Y", "YES", "T", "TRUE", "V", "1":
		return true, "", nil
	case "N", "NAO", "NÃO", "NO", "F", "FALSE", "0":
		return false, "", nil
	}
	return nil, "", vio(VioTipo, "%s: o valor não vira verdadeiro/falso (use \"trocar valores\")", d.Nome)
}

// ChaveTexto é a forma estável de um valor de chave (para checkpoint, conjuntos de
// unicidade e checksum). Datas em UTC; bytes em hexadecimal.
func ChaveTexto(v any) string {
	switch x := v.(type) {
	case []byte:
		var b bytes.Buffer
		fmt.Fprintf(&b, "%x", x)
		return b.String()
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	}
	return Texto(v)
}

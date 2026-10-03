// Package transformar aplica as transformações do mapeamento (lista FECHADA, ARQUITETURA
// §9.3) a uma linha lida da origem e ajusta o valor ao tipo da coluna de destino,
// dizendo o que não cabe. É o mesmo código na simulação (dry-run) e na execução: o
// que a simulação aprova é exatamente o que a execução grava.
package transformar

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eduardorarruda/revoada/core/migracao/modelo"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
)

// Linha é uma linha lida da origem: nome da coluna → valor como o driver entrega
// (int64, string, []byte, time.Time, float64, bool, nil…).
type Linha map[string]any

// ErrParametro: a transformação veio sem o parâmetro de que precisa.
var ErrParametro = errors.New("parâmetro da transformação inválido")

// Aplicar calcula o valor que vai para a coluna de destino. O ajuste ao tipo do
// destino vem depois, em Ajustar.
func Aplicar(mc modelo.MapColuna, l Linha) (any, error) {
	p := mc.Parametros
	switch mc.Transformacao {
	case modelo.Nenhuma, modelo.ConverterTipo, "":
		return l[mc.ColunaOrigem], nil
	case modelo.Constante:
		return p["valor"], nil
	case modelo.ValorPadrao:
		if v := l[mc.ColunaOrigem]; v != nil {
			return v, nil
		}
		return p["valor"], nil
	case modelo.Aparar:
		v := l[mc.ColunaOrigem]
		if v == nil {
			return nil, nil
		}
		return strings.TrimSpace(Texto(v)), nil
	case modelo.Charset:
		return converterCharset(l[mc.ColunaOrigem], p["de"], p["reparar"] == "sim")
	case modelo.MapaValores:
		v := l[mc.ColunaOrigem]
		if v == nil {
			if n, ok := p["<nulo>"]; ok {
				return n, nil
			}
			return nil, nil
		}
		s := strings.TrimSpace(Texto(v))
		if n, ok := p[s]; ok {
			return n, nil
		}
		if n, ok := p["*"]; ok { // "*" = qualquer outro valor
			return n, nil
		}
		return v, nil
	case modelo.Concatenar:
		return concatenar(l, p)
	case modelo.Dividir:
		return dividir(l[mc.ColunaOrigem], p)
	case modelo.DataFormato:
		return dataFormato(l[mc.ColunaOrigem], p["formato"])
	}
	return nil, fmt.Errorf("transformação %q desconhecida", mc.Transformacao)
}

// Texto é a forma de texto de um valor da origem (para concatenar, mapear, gravar
// em coluna de texto). Datas saem em ISO 8601.
func Texto(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case time.Time:
		if x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 && x.Nanosecond() == 0 {
			return x.Format(time.DateOnly)
		}
		return x.Format("2006-01-02T15:04:05.999999")
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'f', -1, 32)
	case bool:
		return strconv.FormatBool(x)
	case fmt.Stringer:
		return x.String()
	}
	return fmt.Sprint(v)
}

func codificacao(nome string) (encoding.Encoding, bool) {
	switch strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(nome), "-", "")) {
	case "WIN1252", "CP1252", "WINDOWS1252":
		return charmap.Windows1252, true
	case "ISO88591", "LATIN1", "ISO8859_1":
		return charmap.ISO8859_1, true
	case "ISO885915", "LATIN9", "ISO8859_15":
		return charmap.ISO8859_15, true
	case "WIN1250", "CP1250":
		return charmap.Windows1250, true
	case "DOS850", "CP850":
		return charmap.CodePage850, true
	case "DOS437", "CP437":
		return charmap.CodePage437, true
	case "UTF8", "UNICODE_FSS", "UTF_8":
		return unicode.UTF8, true
	}
	return nil, false
}

// converterCharset lê os bytes como `de` e devolve texto UTF-8. Se o driver já
// entregou UTF-8 válido (ele converte quando a conexão declara o charset), o texto
// fica como está — converter de novo estragaria os acentos ("JosÃ©").
//
// reparar: o caso clássico de banco legado em que um programa gravou UTF-8 numa
// coluna WIN1252 — o banco devolve "JosÃ©". Voltando o texto para os bytes de `de`,
// eles formam UTF-8 válido de novo ("José"). Só troca quando isso dá certo.
func converterCharset(v any, de string, reparar bool) (any, error) {
	var b []byte
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		b = []byte(x)
	case []byte:
		b = x
	default:
		return v, nil
	}
	enc, ok := codificacao(de)
	if utf8.Valid(b) {
		if reparar && ok && SuspeitaDuplaCodificacao(string(b)) {
			if orig, err := enc.NewEncoder().Bytes(b); err == nil && utf8.Valid(orig) {
				return string(orig), nil
			}
		}
		return string(b), nil
	}
	if !ok {
		return nil, fmt.Errorf("%w: charset de origem %q não suportado", ErrParametro, de)
	}
	out, err := enc.NewDecoder().Bytes(b)
	if err != nil {
		return nil, fmt.Errorf("texto não está em %s: %w", de, err)
	}
	return string(out), nil
}

// continuacoes são as letras que os bytes 0x80–0xBF (continuação de UTF-8) viram
// quando lidos como WIN1252 — o segundo caractere de "Ã©", "Ã§", "Â°".
var continuacoes = func() map[rune]bool {
	m := map[rune]bool{}
	for b := 0x80; b <= 0xBF; b++ {
		m[charmap.Windows1252.DecodeByte(byte(b))] = true
	}
	return m
}()

// SuspeitaDuplaCodificacao reconhece UTF-8 lido como WIN1252/Latin-1: "Ã" ou "Â"
// seguido de um caractere que veio de um byte de continuação ("JosÃ©").
func SuspeitaDuplaCodificacao(s string) bool {
	anterior := rune(0)
	for _, r := range s {
		if (anterior == 'Ã' || anterior == 'Â') && continuacoes[r] {
			return true
		}
		anterior = r
	}
	return false
}

func concatenar(l Linha, p map[string]string) (any, error) {
	cols := strings.Split(p["colunas"], ",")
	partes := make([]string, 0, len(cols))
	todasNulas := true
	for _, c := range cols {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		v, existe := l[c]
		if !existe {
			return nil, fmt.Errorf("%w: concatenar usa a coluna %q, que não foi lida", ErrParametro, c)
		}
		if v == nil {
			continue
		}
		todasNulas = false
		if s := strings.TrimSpace(Texto(v)); s != "" {
			partes = append(partes, s)
		}
	}
	if todasNulas {
		return nil, nil
	}
	return strings.Join(partes, p["separador"]), nil
}

// dividir pega a parte N (começando em 1) do texto separado por `separador`.
func dividir(v any, p map[string]string) (any, error) {
	if v == nil {
		return nil, nil
	}
	sep := p["separador"]
	if sep == "" {
		return nil, fmt.Errorf("%w: dividir precisa do separador", ErrParametro)
	}
	n, err := strconv.Atoi(p["parte"])
	if err != nil || n < 1 {
		return nil, fmt.Errorf("%w: parte deve ser 1, 2, 3…", ErrParametro)
	}
	partes := strings.Split(Texto(v), sep)
	if n > len(partes) {
		return nil, nil
	}
	return strings.TrimSpace(partes[n-1]), nil
}

// LayoutData traduz um formato legível (DD/MM/AAAA, YYYY-MM-DD HH:MI:SS…) para o
// layout do Go.
func LayoutData(formato string) string {
	r := strings.NewReplacer(
		"YYYY", "2006", "AAAA", "2006", "YY", "06", "AA", "06",
		"DD", "02", "MM", "01", "HH", "15", "MI", "04", "mm", "04", "SS", "05", "ss", "05",
	)
	return r.Replace(formato)
}

func dataFormato(v any, formato string) (any, error) {
	if v == nil {
		return nil, nil
	}
	if t, ok := v.(time.Time); ok {
		return t, nil
	}
	if formato == "" {
		return nil, fmt.Errorf("%w: informe o formato (ex.: DD/MM/AAAA)", ErrParametro)
	}
	s := strings.TrimSpace(Texto(v))
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(LayoutData(formato), s)
	if err != nil {
		return nil, fmt.Errorf("o texto não está no formato %s", formato) // sem o valor: relatório não leva dado
	}
	return t, nil
}

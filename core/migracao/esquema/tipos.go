package esquema

import (
	"fmt"
	"strings"
)

// TipoFirebird converte o tipo do catálogo do Firebird (RDB$FIELD_TYPE, subtipo,
// escala) no tipo lógico e no nome nativo legível.
//
// Códigos de RDB$FIELD_TYPE: 7 SMALLINT, 8 INTEGER, 10 FLOAT, 12 DATE, 13 TIME,
// 14 CHAR, 16 BIGINT, 23 BOOLEAN, 26 INT128, 27 DOUBLE, 35 TIMESTAMP, 37 VARCHAR,
// 261 BLOB. NUMERIC/DECIMAL são guardados como inteiro com escala negativa.
func TipoFirebird(codigo, subtipo, escala, precisao, tamanho int) (TipoLogico, string) {
	if escala < 0 && (codigo == 7 || codigo == 8 || codigo == 16 || codigo == 26) {
		nome := "NUMERIC"
		if subtipo == 2 {
			nome = "DECIMAL"
		}
		return Decimal, fmt.Sprintf("%s(%d,%d)", nome, precisao, -escala)
	}
	switch codigo {
	case 7:
		return Inteiro, "SMALLINT"
	case 8:
		return Inteiro, "INTEGER"
	case 16:
		return Inteiro, "BIGINT"
	case 26:
		return Inteiro, "INT128"
	case 10:
		return Flutuante, "FLOAT"
	case 27:
		return Flutuante, "DOUBLE PRECISION"
	case 12:
		return Data, "DATE"
	case 13, 28:
		return Hora, "TIME"
	case 35, 29:
		return DataHora, "TIMESTAMP"
	case 14:
		return Texto, fmt.Sprintf("CHAR(%d)", tamanho)
	case 37:
		return Texto, fmt.Sprintf("VARCHAR(%d)", tamanho)
	case 23:
		return Booleano, "BOOLEAN"
	case 261:
		if subtipo == 1 {
			return TextoLongo, "BLOB SUB_TYPE TEXT"
		}
		return Binario, "BLOB SUB_TYPE BINARY"
	}
	return Desconhecido, fmt.Sprintf("TIPO_%d", codigo)
}

// TipoPostgres converte o nome do information_schema (data_type/udt_name).
func TipoPostgres(dataType string) TipoLogico {
	switch t := strings.ToLower(strings.TrimSpace(dataType)); {
	case t == "smallint" || t == "integer" || t == "bigint" || t == "int2" || t == "int4" || t == "int8":
		return Inteiro
	case t == "numeric" || t == "decimal" || t == "money":
		return Decimal
	case t == "real" || t == "double precision" || t == "float4" || t == "float8":
		return Flutuante
	case t == "text":
		return TextoLongo
	case strings.HasPrefix(t, "character") || t == "varchar" || t == "bpchar" || t == "citext":
		return Texto
	case t == "date":
		return Data
	case strings.HasPrefix(t, "time without") || t == "time" || strings.HasPrefix(t, "time with"):
		return Hora
	case strings.HasPrefix(t, "timestamp"):
		return DataHora
	case t == "boolean" || t == "bool":
		return Booleano
	case t == "bytea":
		return Binario
	case t == "json" || t == "jsonb":
		return JSON
	case t == "uuid":
		return UUID
	}
	return Desconhecido
}

// TipoDDLPostgres é o tipo sugerido ao CRIAR a coluna no PostgreSQL a partir de uma
// coluna de origem (ação "criar no destino").
func TipoDDLPostgres(c Coluna) string {
	switch c.Tipo {
	case Inteiro:
		if strings.Contains(strings.ToUpper(c.TipoNativo), "SMALLINT") {
			return "smallint"
		}
		if strings.Contains(strings.ToUpper(c.TipoNativo), "INTEGER") {
			return "integer"
		}
		if strings.Contains(strings.ToUpper(c.TipoNativo), "INT128") {
			return "numeric(39,0)"
		}
		return "bigint"
	case Decimal:
		if c.Precisao > 0 {
			return fmt.Sprintf("numeric(%d,%d)", c.Precisao, c.Escala)
		}
		return "numeric"
	case Flutuante:
		return "double precision"
	case Texto:
		if c.Tamanho > 0 {
			return fmt.Sprintf("varchar(%d)", c.Tamanho)
		}
		return "text"
	case TextoLongo:
		return "text"
	case Data:
		return "date"
	case Hora:
		return "time"
	case DataHora:
		return "timestamp"
	case Booleano:
		return "boolean"
	case Binario:
		return "bytea"
	case JSON:
		return "jsonb"
	case UUID:
		return "uuid"
	}
	return "text"
}

// Compatibilidade diz se a coluna de origem cabe na de destino sem transformação e,
// se cabe com risco, qual é o risco (o editor mostra o alerta ao lado do vínculo).
type Compatibilidade struct {
	Cabe   bool   // dá para copiar direto (talvez com conversão implícita segura)
	Alerta string // risco de perda (truncar, arredondar…) — vazio = sem risco
	// PrecisaTransformar: os tipos não conversam sem uma transformação explícita.
	PrecisaTransformar bool
}

// Comparar avalia origem → destino.
func Comparar(o, d Coluna) Compatibilidade {
	if o.Tipo == Desconhecido || d.Tipo == Desconhecido {
		return Compatibilidade{Cabe: true, Alerta: "tipo não reconhecido; confira na simulação"}
	}
	if o.Tipo == d.Tipo || (o.Tipo == Texto && d.Tipo == TextoLongo) {
		return compararTamanho(o, d)
	}
	switch {
	case d.Tipo == TextoLongo || d.Tipo == Texto:
		if d.Tipo == Texto && d.Tamanho > 0 && o.Tipo == TextoLongo {
			return Compatibilidade{Cabe: true, Alerta: fmt.Sprintf("texto longo em VARCHAR(%d): o que passar disso é recusado", d.Tamanho)}
		}
		return Compatibilidade{Cabe: true, Alerta: "o valor vira texto"}
	case o.Tipo == Inteiro && (d.Tipo == Decimal || d.Tipo == Flutuante):
		return Compatibilidade{Cabe: true}
	case o.Tipo == Decimal && d.Tipo == Flutuante:
		return Compatibilidade{Cabe: true, Alerta: "decimal em ponto flutuante pode arredondar centavos"}
	case o.Tipo == Decimal && d.Tipo == Inteiro:
		return Compatibilidade{Cabe: true, Alerta: "as casas decimais são descartadas"}
	case o.Tipo == Data && d.Tipo == DataHora:
		return Compatibilidade{Cabe: true}
	case o.Tipo == DataHora && d.Tipo == Data:
		return Compatibilidade{Cabe: true, Alerta: "a hora é descartada"}
	case (o.Tipo == Booleano && d.Tipo == Inteiro) || (o.Tipo == Inteiro && d.Tipo == Booleano):
		return Compatibilidade{Cabe: true, Alerta: "conversão entre 0/1 e falso/verdadeiro"}
	case o.Tipo == TextoLongo && d.Tipo == JSON:
		return Compatibilidade{Cabe: true, Alerta: "o texto precisa ser JSON válido"}
	}
	return Compatibilidade{PrecisaTransformar: true,
		Alerta: fmt.Sprintf("%s não vira %s sem uma transformação", o.Tipo, d.Tipo)}
}

func compararTamanho(o, d Coluna) Compatibilidade {
	switch o.Tipo {
	case Texto:
		if d.Tamanho > 0 && o.Tamanho > d.Tamanho {
			return Compatibilidade{Cabe: true, Alerta: fmt.Sprintf("pode truncar: %d → %d caracteres", o.Tamanho, d.Tamanho)}
		}
	case Decimal:
		if d.Precisao > 0 && (o.Precisao-o.Escala > d.Precisao-d.Escala) {
			return Compatibilidade{Cabe: true, Alerta: fmt.Sprintf("parte inteira maior que a do destino (%d,%d → %d,%d)", o.Precisao, o.Escala, d.Precisao, d.Escala)}
		}
		if d.Precisao > 0 && o.Escala > d.Escala {
			return Compatibilidade{Cabe: true, Alerta: fmt.Sprintf("arredonda de %d para %d casas", o.Escala, d.Escala)}
		}
	}
	return Compatibilidade{Cabe: true}
}

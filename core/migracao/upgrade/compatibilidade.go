package upgrade

import "strings"

// reservadas são as palavras que viraram reservadas depois do Firebird 2.5. Um nome
// de tabela/coluna/procedure igual a uma delas faz o SQL da APLICAÇÃO quebrar no FB5
// (no banco, o BLR compilado continua funcionando — o problema é o SQL de fora).
var reservadas = map[string]string{
	// Firebird 3
	"BOOLEAN": "3", "CORR": "3", "COVAR_POP": "3", "COVAR_SAMP": "3", "DELETING": "3", "DETERMINISTIC": "3",
	"FALSE": "3", "INSERTING": "3", "OFFSET": "3", "OVER": "3", "REGR_AVGX": "3", "REGR_AVGY": "3",
	"REGR_COUNT": "3", "REGR_INTERCEPT": "3", "REGR_R2": "3", "REGR_SLOPE": "3", "REGR_SXX": "3",
	"REGR_SXY": "3", "REGR_SYY": "3", "RETURN": "3", "ROW": "3", "SCROLL": "3", "SQLSTATE": "3",
	"STDDEV_POP": "3", "STDDEV_SAMP": "3", "TRUE": "3", "UNKNOWN": "3", "UPDATING": "3", "VAR_POP": "3",
	"VAR_SAMP": "3",
	// Firebird 4
	"BINARY": "4", "DECFLOAT": "4", "INT128": "4", "LATERAL": "4", "LOCAL": "4", "LOCALTIME": "4",
	"LOCALTIMESTAMP": "4", "PUBLICATION": "4", "RESETTING": "4", "TIMEZONE_HOUR": "4",
	"TIMEZONE_MINUTE": "4", "UNBOUNDED": "4", "VARBINARY": "4", "WINDOW": "4", "WITHOUT": "4",
}

// Reservada diz se o nome colide com palavra reservada nova e a partir de qual versão.
func Reservada(nome string) (versao string, ok bool) {
	v, ok := reservadas[strings.ToUpper(strings.TrimSpace(nome))]
	return v, ok
}

// substitutos das UDFs mais comuns (ib_udf, fbudf, rfunc) por funções nativas. No
// Firebird 4+ UDF vem DESLIGADA (UdfAccess = None): toda chamada falha até trocar.
var substitutos = map[string]string{
	"ABS": "ABS()", "ACOS": "ACOS()", "ASCII_CHAR": "ASCII_CHAR()", "ASCII_VAL": "ASCII_VAL()",
	"ASIN": "ASIN()", "ATAN": "ATAN()", "ATAN2": "ATAN2()", "BIN_AND": "BIN_AND()", "BIN_OR": "BIN_OR()",
	"BIN_XOR": "BIN_XOR()", "CEILING": "CEILING()", "COS": "COS()", "COSH": "COSH()", "COT": "COT()",
	"DIV": "DIV() ou o operador /", "FLOOR": "FLOOR()", "LN": "LN()", "LOG": "LOG()", "LOG10": "LOG10()",
	"LOWER": "LOWER()", "LPAD": "LPAD()", "LTRIM": "TRIM(LEADING FROM …)", "MOD": "MOD()", "PI": "PI()",
	"RAND": "RAND()", "RPAD": "RPAD()", "RTRIM": "TRIM(TRAILING FROM …)", "SIGN": "SIGN()", "SIN": "SIN()",
	"SINH": "SINH()", "SQRT": "SQRT()", "STRLEN": "CHAR_LENGTH()", "SUBSTR": "SUBSTRING(… FROM … FOR …)",
	"SUBSTRLEN": "SUBSTRING(… FROM … FOR …)", "TAN": "TAN()", "TANH": "TANH()", "TRUNCATE": "TRUNC()",
	"ROUND": "ROUND()", "DOW": "EXTRACT(WEEKDAY FROM …)", "SDOW": "EXTRACT(WEEKDAY FROM …)",
	"ADDDAY": "DATEADD(DAY, n, …)", "ADDMONTH": "DATEADD(MONTH, n, …)", "ADDYEAR": "DATEADD(YEAR, n, …)",
	"ADDHOUR": "DATEADD(HOUR, n, …)", "ADDMINUTE": "DATEADD(MINUTE, n, …)", "ADDSECOND": "DATEADD(SECOND, n, …)",
	"GETEXACTTIMESTAMP": "CURRENT_TIMESTAMP / LOCALTIMESTAMP", "STRING2BLOB": "CAST(… AS BLOB SUB_TYPE TEXT)",
	"I64ROUND": "ROUND()", "I64TRUNCATE": "TRUNC()", "DPOWER": "POWER()", "NULLIF": "NULLIF()",
	"UPPER": "UPPER()", "REPLACE": "REPLACE()", "REVERSE": "REVERSE()", "LEFT": "LEFT()", "RIGHT": "RIGHT()",
}

// SubstitutoUDF sugere a função nativa para uma UDF (pelo nome ou pelo entrypoint).
func SubstitutoUDF(nome, entrypoint string) (string, bool) {
	for _, n := range []string{nome, entrypoint} {
		k := strings.ToUpper(strings.TrimSpace(n))
		k = strings.TrimPrefix(strings.TrimPrefix(k, "IB_UDF_"), "FN_")
		if s, ok := substitutos[k]; ok {
			return s, true
		}
	}
	return "", false
}

// UsaDataHoraAmbigua acha, no fonte de procedure/trigger/view, o que muda de
// comportamento no FB4+: CURRENT_TIMESTAMP/CURRENT_TIME passaram a ser WITH TIME ZONE.
func UsaDataHoraAmbigua(fonte string) bool {
	f := strings.ToUpper(fonte)
	return strings.Contains(f, "CURRENT_TIMESTAMP") || strings.Contains(f, "CURRENT_TIME")
}

package upgrade

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eduardorarruda/revoada/agent/internal/migracao/captura"
	"github.com/eduardorarruda/revoada/core/migracao/esquema"
	"github.com/eduardorarruda/revoada/core/migracao/plano"
	"github.com/eduardorarruda/revoada/core/migracao/transformar"
	up "github.com/eduardorarruda/revoada/core/migracao/upgrade"
	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
	fb "github.com/nakagami/firebirdsql"
)

const amostraPadrao = 5000

func lerEspecificacao(b []byte) (up.Especificacao, error) {
	var e up.Especificacao
	if err := json.Unmarshal(b, &e); err != nil {
		return e, fmt.Errorf("especificação inválida: %w", err)
	}
	if e.Execucao == "" || e.Origem.Motor != "firebird" {
		return e, fmt.Errorf("especificação de upgrade precisa de execução e de origem Firebird")
	}
	if e.Destino != nil && e.Destino.Motor != "firebird" {
		return e, fmt.Errorf("o destino do upgrade precisa ser um servidor Firebird")
	}
	if e.AmostraTexto <= 0 {
		e.AmostraTexto = amostraPadrao
	}
	e.AmostraTexto = min(e.AmostraTexto, 100_000)
	e.Workers = min(max(e.Workers, 1), 16)
	return e, nil
}

func senha(r Relator, nome string) (string, error) {
	b, err := r.Credencial(nome)
	if err != nil {
		return "", err
	}
	s := string(b)
	for i := range b {
		b[i] = 0
	}
	return s, nil
}

func abrir(ctx context.Context, b plano.Banco, s string, charset string) (*sql.DB, error) {
	op := map[string]string{}
	for k, v := range b.Opcoes {
		op[k] = v
	}
	if charset != "" {
		op["charset"] = charset
	}
	db, err := captura.Abrir(captura.Conexao{Motor: "firebird", Endereco: b.Endereco, Banco: b.Banco, Usuario: b.Usuario, Senha: s, Opcoes: op})
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("não conectou em %s: %w", b.Banco, err)
	}
	return db, nil
}

// diagnostico acumula os achados.
type diagnostico struct {
	esp  up.Especificacao
	r    Relator
	db   *sql.DB
	e    esquema.Esquema
	d    up.Diagnostico
	sOri string
}

func (x *diagnostico) achar(a up.Achado) { x.d.Achados = append(x.d.Achados, a) }

func (x *diagnostico) passo(msg string, pct float64) {
	x.r.Evento("diagnostico", agentev1.EventoTarefa_INFO, msg, pct, nil)
}

// Diagnosticar responde "o que vai quebrar no Firebird 5?" (ARQUITETURA §9.5). Na origem só
// roda leitura (a validação física é a do gfix SEM correção). Se o destino FB5 e o
// diretório compartilhado vierem, faz o ensaio: restore só de metadados no FB5.
func Diagnosticar(ctx context.Context, bruto []byte, r Relator) (any, error) {
	esp, err := lerEspecificacao(bruto)
	if err != nil {
		return nil, err
	}
	inicio := time.Now()
	s, err := senha(r, "origem")
	if err != nil {
		return nil, err
	}
	db, err := abrir(ctx, esp.Origem, s, "")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	x := &diagnostico{esp: esp, r: r, db: db, sOri: s}
	x.d.CapturadoEm = time.Now().UTC().Format(time.RFC3339)

	x.passo("lendo versão, ODS e estrutura", 5)
	if err := x.basico(ctx); err != nil {
		return nil, err
	}
	if x.e, err = captura.Capturar(ctx, captura.Conexao{Motor: "firebird", Endereco: esp.Origem.Endereco, Banco: esp.Origem.Banco,
		Usuario: esp.Origem.Usuario, Senha: s, Opcoes: esp.Origem.Opcoes}); err != nil {
		return nil, err
	}
	etapas := []struct {
		msg string
		f   func(context.Context) error
	}{
		{"procurando nomes que viraram palavras reservadas", x.palavras},
		{"conferindo UDFs (desligadas por padrão no FB4+)", x.udfs},
		{"procurando CURRENT_TIMESTAMP em procedures, triggers e views", x.dataHora},
		{"contando nulos em colunas NOT NULL", x.nulos},
		{"procurando registros órfãos nas chaves estrangeiras", x.orfaos},
		{"procurando chaves duplicadas", x.duplicadas},
		{"procurando texto com charset quebrado", x.charset},
		{"listando usuários (o FB5 usa SRP)", x.usuarios},
	}
	for i, et := range etapas {
		if err := r.Pausa(ctx); err != nil {
			return x.d, err
		}
		x.passo(et.msg, 10+float64(i)*8)
		if err := et.f(ctx); err != nil {
			return x.d, fmt.Errorf("%s: %w", et.msg, err)
		}
	}
	if esp.ValidarPaginas {
		db.Close() // a validação do 2.x não roda com conexão aberta — a nossa inclusive
		x.passo("validando páginas (gfix -v -full, sem corrigir nada)", 80)
		x.validar(ctx)
	}
	if esp.Destino != nil && esp.Diretorio != "" {
		x.passo("ensaio: restore só dos metadados no Firebird 5", 88)
		x.ensaiar(ctx)
	}
	x.d.Nota, x.d.Risco, x.d.Bloqueios = up.Pontuar(x.d.Achados)
	x.d.ParadaS = int64(up.EstimarParada(x.d.Tamanho, esp.Workers).Seconds())
	x.d.FixSQL = up.GerarFixSQL(x.d)
	x.d.DuracaoMS = time.Since(inicio).Milliseconds()
	nivel := agentev1.EventoTarefa_INFO
	if x.d.Bloqueios > 0 {
		nivel = agentev1.EventoTarefa_AVISO
	}
	r.Evento("diagnostico", nivel, fmt.Sprintf("diagnóstico pronto: risco %s (nota %d), %d bloqueio(s), %d achado(s)",
		x.d.Risco, x.d.Nota, x.d.Bloqueios, len(x.d.Achados)), 100, nil)
	return x.d, nil
}

func (x *diagnostico) basico(ctx context.Context) error {
	var maj, mnr, dial, pag int
	var pags int64
	var cs sql.NullString
	if err := x.db.QueryRowContext(ctx, "SELECT rdb$get_context('SYSTEM', 'ENGINE_VERSION') FROM rdb$database").Scan(&x.d.Versao); err != nil {
		return fmt.Errorf("lendo a versão: %w", err)
	}
	if err := x.db.QueryRowContext(ctx, "SELECT MON$ODS_MAJOR, MON$ODS_MINOR, MON$SQL_DIALECT, MON$PAGE_SIZE, MON$PAGES FROM MON$DATABASE").
		Scan(&maj, &mnr, &dial, &pag, &pags); err != nil {
		return fmt.Errorf("lendo MON$DATABASE: %w", err)
	}
	if err := x.db.QueryRowContext(ctx, "SELECT TRIM(RDB$CHARACTER_SET_NAME) FROM RDB$DATABASE").Scan(&cs); err != nil {
		return err
	}
	x.d.ODS, x.d.Dialeto, x.d.PageSize, x.d.Tamanho = fmt.Sprintf("%d.%d", maj, mnr), dial, pag, pags*int64(pag)
	x.d.Charset = "NONE"
	if cs.Valid && cs.String != "" {
		x.d.Charset = cs.String
	}
	contar := func(q string) int {
		var n int
		_ = x.db.QueryRowContext(ctx, q).Scan(&n)
		return n
	}
	x.d.Tabelas = contar("SELECT COUNT(*) FROM RDB$RELATIONS WHERE COALESCE(RDB$SYSTEM_FLAG,0)=0 AND RDB$VIEW_BLR IS NULL")
	x.d.Procedures = contar("SELECT COUNT(*) FROM RDB$PROCEDURES WHERE COALESCE(RDB$SYSTEM_FLAG,0)=0")
	x.d.Triggers = contar("SELECT COUNT(*) FROM RDB$TRIGGERS WHERE COALESCE(RDB$SYSTEM_FLAG,0)=0")
	x.d.UDFs = contar("SELECT COUNT(*) FROM RDB$FUNCTIONS WHERE COALESCE(RDB$SYSTEM_FLAG,0)=0 AND RDB$MODULE_NAME IS NOT NULL")
	switch {
	case strings.HasPrefix(x.d.Versao, "5."):
		x.achar(up.Achado{Nivel: up.Info, Categoria: up.CatVersao, Objeto: "banco", Mensagem: "o servidor já é Firebird 5"})
	case dial == 1:
		x.achar(up.Achado{Nivel: up.Risco, Categoria: up.CatVersao, Objeto: "banco",
			Mensagem: "dialeto 1: o FB5 restaura, mas DATE vira TIMESTAMP e aspas mudam de sentido; teste a aplicação",
			Correcao: "-- depois do restore: gfix -sql_dialect 3 <banco> (só com a aplicação revisada)"})
	}
	if maj < 11 {
		x.achar(up.Achado{Nivel: up.Risco, Categoria: up.CatVersao, Objeto: "banco",
			Mensagem: fmt.Sprintf("ODS %s (Firebird 1.x): o backup sai pelo serviço da versão antiga, mas confira o ensaio com atenção", x.d.ODS)})
	}
	return nil
}

func (x *diagnostico) palavras(c context.Context) error {
	for _, t := range x.e.Tabelas {
		if v, ok := up.Reservada(t.Nome); ok {
			x.achar(up.Achado{Nivel: up.Risco, Categoria: up.CatPalavra, Objeto: t.Nome,
				Mensagem: fmt.Sprintf("a tabela %s tem nome de palavra reservada desde o Firebird %s: o SQL da aplicação precisa citá-la entre aspas", t.Nome, v),
				Correcao: fmt.Sprintf("-- tabela não se renomeia no Firebird: cite %s entre aspas na aplicação", up.AspasFB(t.Nome))})
		}
		for _, c := range t.Colunas {
			if v, ok := up.Reservada(c.Nome); ok {
				x.achar(up.Achado{Nivel: up.Risco, Categoria: up.CatPalavra, Objeto: t.Nome + "." + c.Nome,
					Mensagem: fmt.Sprintf("a coluna %s é palavra reservada desde o Firebird %s", c.Nome, v),
					Correcao: fmt.Sprintf("-- ALTER TABLE %s ALTER %s TO %s; (só se nada depender do nome antigo)",
						up.AspasFB(t.Nome), up.AspasFB(c.Nome), up.AspasFB(c.Nome+"_"))})
			}
		}
	}
	rows, err := x.db.QueryContext(c, "SELECT TRIM(RDB$PROCEDURE_NAME) FROM RDB$PROCEDURES WHERE COALESCE(RDB$SYSTEM_FLAG,0)=0")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		if v, ok := up.Reservada(n); ok {
			x.achar(up.Achado{Nivel: up.Risco, Categoria: up.CatPalavra, Objeto: n,
				Mensagem: fmt.Sprintf("a procedure %s tem nome reservado desde o Firebird %s", n, v)})
		}
	}
	return rows.Err()
}

func (x *diagnostico) udfs(c context.Context) error {
	rows, err := x.db.QueryContext(c, `SELECT TRIM(RDB$FUNCTION_NAME), TRIM(COALESCE(RDB$MODULE_NAME,'')), TRIM(COALESCE(RDB$ENTRYPOINT,''))
		FROM RDB$FUNCTIONS WHERE COALESCE(RDB$SYSTEM_FLAG,0)=0 AND RDB$MODULE_NAME IS NOT NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var nome, modulo, entrada string
		if err := rows.Scan(&nome, &modulo, &entrada); err != nil {
			return err
		}
		a := up.Achado{Nivel: up.Risco, Categoria: up.CatUDF, Objeto: nome,
			Mensagem: fmt.Sprintf("UDF %s (biblioteca %s): no FB4+ UDF vem desligada e a chamada falha", nome, modulo)}
		if sub, ok := up.SubstitutoUDF(nome, entrada); ok {
			a.Correcao = fmt.Sprintf("-- troque %s por %s nas procedures/triggers/aplicação; depois: DROP EXTERNAL FUNCTION %s;", nome, sub, up.AspasFB(nome))
		} else {
			a.Correcao = fmt.Sprintf("-- %s não tem substituto nativo conhecido: reescreva como função PSQL ou habilite UdfAccess = Restrict UDF no FB5", nome)
		}
		x.achar(a)
	}
	return rows.Err()
}

func (x *diagnostico) dataHora(c context.Context) error {
	rows, err := x.db.QueryContext(c, `
		SELECT TRIM(RDB$PROCEDURE_NAME), RDB$PROCEDURE_SOURCE FROM RDB$PROCEDURES WHERE COALESCE(RDB$SYSTEM_FLAG,0)=0
		UNION ALL SELECT TRIM(RDB$TRIGGER_NAME), RDB$TRIGGER_SOURCE FROM RDB$TRIGGERS WHERE COALESCE(RDB$SYSTEM_FLAG,0)=0
		UNION ALL SELECT TRIM(RDB$RELATION_NAME), RDB$VIEW_SOURCE FROM RDB$RELATIONS WHERE COALESCE(RDB$SYSTEM_FLAG,0)=0 AND RDB$VIEW_BLR IS NOT NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var objs []string
	for rows.Next() {
		var nome string
		var fonte any
		if err := rows.Scan(&nome, &fonte); err != nil {
			return err
		}
		if fonte != nil && up.UsaDataHoraAmbigua(transformar.Texto(fonte)) {
			objs = append(objs, nome)
		}
	}
	if len(objs) > 0 {
		lista := strings.Join(objs[:min(len(objs), 10)], ", ")
		x.achar(up.Achado{Nivel: up.Info, Categoria: up.CatData, Objeto: lista, Quantos: int64(len(objs)),
			Mensagem: "no FB4+ CURRENT_TIMESTAMP/CURRENT_TIME são WITH TIME ZONE; onde a hora local importa, use LOCALTIMESTAMP/LOCALTIME",
			Correcao: "-- revise: " + lista})
	}
	return rows.Err()
}

// nulos: NOT NULL ligado "por fora" (UPDATE em RDB$RELATION_FIELDS, comum no 1.x/2.x)
// deixa nulos que fazem o restore no FB5 falhar. Uma varredura por tabela.
func (x *diagnostico) nulos(c context.Context) error {
	for _, t := range x.e.Tabelas {
		var cols []string
		for _, col := range t.Colunas {
			if !col.Nulavel {
				cols = append(cols, col.Nome)
			}
		}
		if len(cols) == 0 {
			continue
		}
		somas := make([]string, len(cols))
		for i, col := range cols {
			somas[i] = fmt.Sprintf("SUM(IIF(%s IS NULL, 1, 0))", up.AspasFB(col))
		}
		vals := make([]sql.NullInt64, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		q := "SELECT " + strings.Join(somas, ", ") + " FROM " + up.AspasFB(t.Nome)
		if err := x.db.QueryRowContext(c, q).Scan(ptrs...); err != nil {
			return fmt.Errorf("%s: %w", t.Nome, err)
		}
		for i, v := range vals {
			if v.Int64 > 0 {
				x.achar(up.Achado{Nivel: up.Bloqueio, Categoria: up.CatNulo, Objeto: t.Nome + "." + cols[i], Quantos: v.Int64,
					Mensagem: fmt.Sprintf("%d linha(s) com nulo numa coluna NOT NULL: o restore no FB5 recusa", v.Int64),
					Correcao: fmt.Sprintf("-- UPDATE %s SET %s = <valor> WHERE %s IS NULL;",
						up.AspasFB(t.Nome), up.AspasFB(cols[i]), up.AspasFB(cols[i]))})
			}
		}
	}
	return nil
}

func listaFB(cols []string, prefixo string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = prefixo + up.AspasFB(c)
	}
	return out
}

func (x *diagnostico) orfaos(c context.Context) error {
	for _, t := range x.e.Tabelas {
		for _, fk := range t.Estrangeiras {
			if len(fk.Colunas) == 0 || len(fk.Colunas) != len(fk.ColunasRef) {
				continue
			}
			filho, pai := listaFB(fk.Colunas, "f."), listaFB(fk.ColunasRef, "p.")
			naoNulo := make([]string, len(filho))
			liga := make([]string, len(filho))
			for i := range filho {
				naoNulo[i] = filho[i] + " IS NOT NULL"
				liga[i] = pai[i] + " = " + filho[i]
			}
			q := fmt.Sprintf("SELECT COUNT(*) FROM %s f WHERE %s AND NOT EXISTS (SELECT 1 FROM %s p WHERE %s)",
				up.AspasFB(t.Nome), strings.Join(naoNulo, " AND "), up.AspasFB(fk.TabelaRef), strings.Join(liga, " AND "))
			var n int64
			if err := x.db.QueryRowContext(c, q).Scan(&n); err != nil {
				return fmt.Errorf("%s: %w", fk.Nome, err)
			}
			if n > 0 {
				x.achar(up.Achado{Nivel: up.Bloqueio, Categoria: up.CatFK, Objeto: t.Nome + " → " + fk.TabelaRef, Quantos: n,
					Mensagem: fmt.Sprintf("%d registro(s) de %s apontam para %s inexistente: a FK %s não reativa no restore", n, t.Nome, fk.TabelaRef, fk.Nome),
					Correcao: fmt.Sprintf("-- SELECT * FROM %s f WHERE %s AND NOT EXISTS (SELECT 1 FROM %s p WHERE %s);  -- revise antes de apagar ou corrigir",
						up.AspasFB(t.Nome), strings.Join(naoNulo, " AND "), up.AspasFB(fk.TabelaRef), strings.Join(liga, " AND "))})
			}
		}
	}
	return nil
}

func (x *diagnostico) duplicadas(c context.Context) error {
	for _, t := range x.e.Tabelas {
		grupos := [][]string{}
		if len(t.ChavePrimaria) > 0 {
			grupos = append(grupos, t.ChavePrimaria)
		}
		for _, ix := range t.Indices {
			if ix.Unico {
				grupos = append(grupos, ix.Colunas)
			}
		}
		for _, g := range grupos {
			cols := strings.Join(listaFB(g, ""), ", ")
			naoNulo := strings.Join(listaFB(g, ""), " IS NOT NULL AND ") + " IS NOT NULL"
			q := fmt.Sprintf("SELECT COUNT(*) FROM (SELECT 1 AS x FROM %s WHERE %s GROUP BY %s HAVING COUNT(*) > 1)", up.AspasFB(t.Nome), naoNulo, cols)
			var n int64
			if err := x.db.QueryRowContext(c, q).Scan(&n); err != nil {
				return fmt.Errorf("%s: %w", t.Nome, err)
			}
			if n > 0 {
				x.achar(up.Achado{Nivel: up.Bloqueio, Categoria: up.CatUnica, Objeto: t.Nome + "(" + strings.Join(g, ",") + ")", Quantos: n,
					Mensagem: fmt.Sprintf("%d valor(es) repetido(s) numa chave única: o índice não reativa no restore", n),
					Correcao: fmt.Sprintf("-- SELECT %s, COUNT(*) FROM %s GROUP BY %s HAVING COUNT(*) > 1;", cols, up.AspasFB(t.Nome), cols)})
			}
		}
	}
	return nil
}

// charset: (1) colunas sem charset (NONE) com bytes que não são UTF-8 — o restore
// precisa de -FIX_FSS_DATA com o charset real; (2) texto que parece UTF-8 gravado
// numa coluna WIN1252 ("JosÃ©") — o gbak não conserta, mas o aviso evita surpresa.
func (x *diagnostico) charset(c context.Context) error {
	none, err := abrir(c, x.esp.Origem, x.sOri, "NONE")
	if err != nil {
		return err
	}
	defer none.Close()
	var naoUTF8, duplas int64
	var objsNone, objsDupla []string
	for _, t := range x.e.Tabelas {
		for _, col := range t.Colunas {
			if col.Tipo != esquema.Texto && col.Tipo != esquema.TextoLongo {
				continue
			}
			q := fmt.Sprintf("SELECT FIRST %d %s FROM %s WHERE %s IS NOT NULL", x.esp.AmostraTexto, up.AspasFB(col.Nome), up.AspasFB(t.Nome), up.AspasFB(col.Nome))
			semCharset := strings.EqualFold(col.Charset, "NONE") || (col.Charset == "" && x.d.Charset == "NONE")
			alvo := x.db
			if semCharset {
				alvo = none
			}
			n, d, err := varrerTexto(c, alvo, q)
			if err != nil {
				return fmt.Errorf("%s.%s: %w", t.Nome, col.Nome, err)
			}
			if semCharset && n > 0 {
				naoUTF8 += n
				objsNone = append(objsNone, t.Nome+"."+col.Nome)
			}
			if d > 0 {
				duplas += d
				objsDupla = append(objsDupla, t.Nome+"."+col.Nome)
			}
		}
	}
	if naoUTF8 > 0 {
		x.d.CharsetFix = "WIN1252"
		x.achar(up.Achado{Nivel: up.Risco, Categoria: up.CatCharset, Objeto: strings.Join(objsNone[:min(len(objsNone), 10)], ", "), Quantos: naoUTF8,
			Mensagem: "colunas sem charset (NONE) com texto que não é UTF-8: no restore use -FIX_FSS_DATA/-FIX_FSS_METADATA com o charset real",
			Correcao: "-- o upgrade do Revoada aplica FIX_FSS com o charset escolhido (sugestão: WIN1252)"})
	}
	if duplas > 0 {
		x.achar(up.Achado{Nivel: up.Risco, Categoria: up.CatCharset, Objeto: strings.Join(objsDupla[:min(len(objsDupla), 10)], ", "), Quantos: duplas,
			Mensagem: "texto que parece UTF-8 gravado como " + x.d.Charset + " (\"JosÃ©\"): o upgrade copia como está",
			Correcao: "-- o gbak não conserta isto; na migração para PostgreSQL use a transformação charset com reparar=sim"})
	}
	return nil
}

func varrerTexto(c context.Context, db *sql.DB, q string) (naoUTF8, duplas int64, err error) {
	rows, err := db.QueryContext(c, q)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var v any
		if err := rows.Scan(&v); err != nil {
			return 0, 0, err
		}
		var s string
		switch t := v.(type) {
		case []byte:
			if !utf8.Valid(t) {
				naoUTF8++
				continue
			}
			s = string(t)
		case string:
			if !utf8.ValidString(t) {
				naoUTF8++
				continue
			}
			s = t
		default:
			continue
		}
		if transformar.SuspeitaDuplaCodificacao(s) {
			duplas++
		}
	}
	return naoUTF8, duplas, rows.Err()
}

func (x *diagnostico) usuarios(context.Context) error {
	um, err := fb.NewUserManager(endereco(x.esp.Origem), x.esp.Origem.Usuario, x.sOri, opcoesServico(x.esp.Origem), fb.NewUserManagerOptions())
	if err == nil {
		defer um.Close()
		var us []fb.User
		if us, err = um.GetUsers(); err == nil {
			nomes := make([]string, 0, len(us))
			var b strings.Builder
			for _, u := range us {
				if u.Username == nil {
					continue
				}
				nomes = append(nomes, *u.Username)
				if !strings.EqualFold(*u.Username, "SYSDBA") {
					fmt.Fprintf(&b, "-- CREATE USER %s PASSWORD '<nova senha>' USING PLUGIN Srp;\n", up.AspasFB(*u.Username))
				}
			}
			x.achar(up.Achado{Nivel: up.Info, Categoria: up.CatUsuarios, Objeto: strings.Join(nomes, ", "), Quantos: int64(len(nomes)),
				Mensagem: "usuários do servidor antigo não vão junto (o FB5 usa SRP): recrie no servidor novo e redefina as senhas",
				Correcao: strings.TrimRight(b.String(), "\n")})
			return nil
		}
	}
	x.achar(up.Achado{Nivel: up.Info, Categoria: up.CatUsuarios, Objeto: "servidor",
		Mensagem: "não deu para listar os usuários (" + err.Error() + "); recrie-os no FB5 com USING PLUGIN Srp"})
	return nil
}

// validar roda a validação física do gfix em modo SÓ LEITURA (-no_update).
func (x *diagnostico) validar(c context.Context) {
	const acaoRepair, rprValidate, rprCheck, rprFull = 3, 0x01, 0x10, 0x80
	w := fb.NewXPBWriterFromTag(acaoRepair)
	w.PutString(spbDbname, x.esp.Origem.Banco)
	w.PutInt32(spbOptions, rprValidate|rprCheck|rprFull)
	var problemas []string
	err := servico(c, x.esp.Origem, x.sOri, w.Bytes(), func(l string) {
		low := strings.ToLower(l)
		if strings.Contains(low, "error") || strings.Contains(low, "corrupt") || strings.Contains(low, "wrong") {
			problemas = append(problemas, l)
		}
	})
	if err != nil {
		if exclusivo(err.Error()) {
			// FB 2.x/1.x só valida com acesso exclusivo (validação online é do FB3+)
			x.achar(up.Achado{Nivel: up.Info, Categoria: up.CatCorrupcao, Objeto: "páginas",
				Mensagem: "este servidor só valida com o banco sem outras conexões; a validação física não rodou",
				Correcao: "-- numa janela sem usuários: gfix -v -full -n -user SYSDBA <banco>   (-n = não altera nada)"})
			return
		}
		problemas = append(problemas, err.Error())
	}
	if len(problemas) > 0 {
		x.achar(up.Achado{Nivel: up.Bloqueio, Categoria: up.CatCorrupcao, Objeto: "páginas", Quantos: int64(len(problemas)),
			Mensagem: "a validação física achou problemas: " + strings.Join(problemas[:min(len(problemas), 3)], " | "),
			Correcao: "-- faça uma cópia do arquivo e rode gfix -mend na CÓPIA antes do backup; depois valide de novo"})
	}
}

func exclusivo(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "secondary server attachments") || strings.Contains(m, "exclusive") || strings.Contains(m, "in use")
}

// ensaiar faz backup só dos metadados na origem e restore no FB5, com o mesmo nome
// de arquivo a cada ensaio (substitui o anterior). É o que prova que procedures e
// triggers compilam no Firebird 5.
func (x *diagnostico) ensaiar(c context.Context) {
	inicio := time.Now()
	en := &up.Ensaio{Feito: true}
	x.d.Ensaio = en
	defer func() { en.DuracaoMS = time.Since(inicio).Milliseconds() }()
	sDest, err := senha(x.r, "destino")
	if err != nil {
		en.Erros = []string{err.Error()}
		return
	}
	fbk := juntar(x.esp.Diretorio, "revoada_ensaio_"+x.esp.ProjetoID+".fbk")
	fdb := juntar(x.esp.Diretorio, "revoada_ensaio_"+x.esp.ProjetoID+".fdb")
	if err := servico(c, x.esp.Origem, x.sOri, spbBackup(x.esp.Origem.Banco, fbk, true), nil); err != nil {
		en.Erros = []string{"backup dos metadados: " + err.Error()}
	} else {
		w := fb.NewXPBWriterFromTag(acaoRestore)
		w.PutString(spbDbname, fdb)
		w.PutString(spbBkpFile, fbk)
		w.PutInt32(spbOptions, 0x1000|0x04) // replace (é o arquivo de ensaio do próprio Revoada) + só metadados
		w.PutTag(spbVerbose)
		if x.d.CharsetFix != "" {
			w.PutString(spbFixData, x.d.CharsetFix)
			w.PutString(spbFixMeta, x.d.CharsetFix)
		}
		if err := servico(c, *x.esp.Destino, sDest, w.Bytes(), nil); err != nil {
			if es, ok := err.(*ErroServico); ok {
				en.Erros = es.Linhas
			} else {
				en.Erros = []string{err.Error()}
			}
		}
	}
	en.OK = len(en.Erros) == 0
	if !en.OK {
		x.achar(up.Achado{Nivel: up.Bloqueio, Categoria: up.CatEnsaio, Objeto: "metadados", Quantos: int64(len(en.Erros)),
			Mensagem: "o ensaio no Firebird 5 falhou: " + strings.Join(en.Erros[:min(len(en.Erros), 3)], " | "),
			Correcao: "-- corrija as procedures/triggers apontadas e rode o diagnóstico de novo"})
	}
}

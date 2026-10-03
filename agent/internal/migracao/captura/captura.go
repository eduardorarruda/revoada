// Package captura lê a ESTRUTURA de um banco (nunca os dados) e devolve o schema
// neutro do core (ARQUITETURA §9.3). Roda dentro do agente, no servidor escolhido; a senha
// chega selada só para este agente e vive só em memória.
package captura

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
	_ "github.com/jackc/pgx/v5/stdlib"  // driver "pgx"
	_ "github.com/nakagami/firebirdsql" // driver "firebirdsql" (Go puro, sem fbclient)
)

// Conexao descreve onde está o banco.
type Conexao struct {
	Motor    string // firebird | postgres
	Endereco string // host:porta (ou host)
	Banco    string // caminho do .fdb (Firebird) ou nome do banco (PostgreSQL)
	Usuario  string
	Senha    string
	Opcoes   map[string]string // charset, sslmode, schema, wire_crypt…
}

// ErrMotor: motor não suportado (ainda).
var ErrMotor = errors.New("motor de banco não suportado")

// Abrir devolve a conexão database/sql para o motor (usado também pela cópia).
func Abrir(c Conexao) (*sql.DB, error) {
	switch c.Motor {
	case "firebird":
		return sql.Open("firebirdsql", dsnFirebird(c))
	case "postgres":
		return sql.Open("pgx", dsnPostgres(c))
	}
	return nil, fmt.Errorf("%w: %q", ErrMotor, c.Motor)
}

func dsnFirebird(c Conexao) string {
	host := c.Endereco
	if _, _, err := net.SplitHostPort(host); err != nil && host != "" {
		host += ":3050"
	}
	q := url.Values{}
	q.Set("charset", opcao(c.Opcoes, "charset", "UTF8"))
	// Firebird 2.5 não conhece criptografia de protocolo; 3+ usa se estiver ligada.
	q.Set("wire_crypt", opcao(c.Opcoes, "wire_crypt", "false"))
	// DATE e TIMESTAMP do Firebird não têm fuso: são relógio de parede. Sem isto o
	// driver monta cada valor como hora LOCAL do agente, e nos dias em que o horário
	// de verão começava à meia-noite essa hora não existe — o Go a empurra para as
	// 23h do dia anterior e a data muda (achado da validação: 65 de 30 mil clientes).
	// Em UTC não há buraco nem hora repetida; quem precisa de instante (coluna de
	// destino com fuso) interpreta o relógio explicitamente (transformar.paraTempo).
	q.Set("timezone", "UTC")
	return fmt.Sprintf("%s:%s@%s/%s?%s", url.QueryEscape(c.Usuario), url.QueryEscape(c.Senha), host,
		strings.TrimPrefix(c.Banco, "/"), q.Encode())
}

func dsnPostgres(c Conexao) string {
	u := url.URL{Scheme: "postgres", User: url.UserPassword(c.Usuario, c.Senha), Host: c.Endereco, Path: "/" + c.Banco}
	q := url.Values{}
	q.Set("sslmode", opcao(c.Opcoes, "sslmode", "prefer"))
	q.Set("application_name", "revoada-agente")
	u.RawQuery = q.Encode()
	return u.String()
}

func opcao(m map[string]string, k, padrao string) string {
	if v := strings.TrimSpace(m[k]); v != "" {
		return v
	}
	return padrao
}

// Capturar lê a estrutura do banco.
func Capturar(ctx context.Context, c Conexao) (esquema.Esquema, error) {
	db, err := Abrir(c)
	if err != nil {
		return esquema.Esquema{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return esquema.Esquema{}, fmt.Errorf("não conectou ao banco: %w", err)
	}
	var e esquema.Esquema
	switch c.Motor {
	case "firebird":
		e, err = capturarFirebird(ctx, db)
	case "postgres":
		e, err = capturarPostgres(ctx, db, opcao(c.Opcoes, "schema", "public"))
	default:
		return esquema.Esquema{}, ErrMotor
	}
	if err != nil {
		return esquema.Esquema{}, err
	}
	e.Motor, e.CapturadoEm = c.Motor, time.Now().UTC()
	return e, nil
}

// restricao é uma PK/UNIQUE/FK lida do catálogo, antes de virar o modelo neutro.
type restricao struct {
	tabela, nome, tipo, ref string
	colunas                 []string
}

// montarTabelas junta colunas e restrições num []esquema.Tabela (comum aos motores).
func montarTabelas(ordem []string, colunas map[string][]esquema.Coluna, rs []restricao) []esquema.Tabela {
	porNome := map[string]*restricao{}
	for i := range rs {
		porNome[rs[i].nome] = &rs[i]
	}
	idx := map[string]int{}
	tabs := make([]esquema.Tabela, 0, len(ordem))
	for i, n := range ordem {
		idx[n] = i
		tabs = append(tabs, esquema.Tabela{Nome: n, Colunas: colunas[n]})
	}
	for _, r := range rs {
		i, ok := idx[r.tabela]
		if !ok {
			continue
		}
		t := &tabs[i]
		switch r.tipo {
		case "PRIMARY KEY":
			t.ChavePrimaria = r.colunas
			t.Indices = append(t.Indices, esquema.Indice{Nome: r.nome, Colunas: r.colunas, Unico: true})
		case "UNIQUE":
			t.Indices = append(t.Indices, esquema.Indice{Nome: r.nome, Colunas: r.colunas, Unico: true})
		case "FOREIGN KEY":
			fk := esquema.ChaveEstrangeira{Nome: r.nome, Colunas: r.colunas}
			if alvo, ok := porNome[r.ref]; ok {
				fk.TabelaRef, fk.ColunasRef = alvo.tabela, alvo.colunas
			}
			t.Estrangeiras = append(t.Estrangeiras, fk)
		}
	}
	return tabs
}

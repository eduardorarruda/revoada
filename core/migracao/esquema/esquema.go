// Package esquema é a representação NEUTRA de um banco (ARQUITETURA §9.3): tabelas, colunas,
// chaves e índices, sem dado nenhum. O agente captura o schema real e devolve isto;
// o painel, a heurística, o MCP e o editor visual só enxergam esta forma.
package esquema

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// TipoLogico é o tipo independente de banco.
type TipoLogico string

const (
	Inteiro      TipoLogico = "inteiro"
	Decimal      TipoLogico = "decimal"
	Flutuante    TipoLogico = "flutuante"
	Texto        TipoLogico = "texto"
	TextoLongo   TipoLogico = "texto_longo"
	Data         TipoLogico = "data"
	Hora         TipoLogico = "hora"
	DataHora     TipoLogico = "data_hora"
	Booleano     TipoLogico = "booleano"
	Binario      TipoLogico = "binario"
	JSON         TipoLogico = "json"
	UUID         TipoLogico = "uuid"
	Desconhecido TipoLogico = "desconhecido"
)

// Coluna de uma tabela.
type Coluna struct {
	Nome       string     `json:"nome"`
	TipoNativo string     `json:"tipo_nativo"` // como o banco chama (ex.: VARCHAR(60), numeric(15,2))
	Tipo       TipoLogico `json:"tipo"`
	Tamanho    int        `json:"tamanho,omitempty"`  // caracteres (texto) ou bytes
	Precisao   int        `json:"precisao,omitempty"` // decimal
	Escala     int        `json:"escala,omitempty"`   // decimal
	Nulavel    bool       `json:"nulavel"`
	Padrao     string     `json:"padrao,omitempty"`
	Charset    string     `json:"charset,omitempty"`
	Identidade bool       `json:"identidade,omitempty"` // autoincremento / identity / sequência
}

// ChaveEstrangeira liga colunas desta tabela a outra.
type ChaveEstrangeira struct {
	Nome       string   `json:"nome"`
	Colunas    []string `json:"colunas"`
	TabelaRef  string   `json:"tabela_ref"`
	ColunasRef []string `json:"colunas_ref"`
}

// Indice de uma tabela.
type Indice struct {
	Nome    string   `json:"nome"`
	Colunas []string `json:"colunas"`
	Unico   bool     `json:"unico"`
}

// Tabela com seus metadados.
type Tabela struct {
	Nome            string             `json:"nome"`
	Colunas         []Coluna           `json:"colunas"`
	ChavePrimaria   []string           `json:"chave_primaria,omitempty"`
	Estrangeiras    []ChaveEstrangeira `json:"estrangeiras,omitempty"`
	Indices         []Indice           `json:"indices,omitempty"`
	LinhasEstimadas int64              `json:"linhas_estimadas"`
}

// Esquema é a foto do banco.
type Esquema struct {
	Motor       string    `json:"motor"`   // firebird | postgres
	Versao      string    `json:"versao"`  // ex.: 2.5.9, 16.4
	Charset     string    `json:"charset"` // charset padrão do banco
	Dialeto     int       `json:"dialeto,omitempty"`
	Tabelas     []Tabela  `json:"tabelas"`
	CapturadoEm time.Time `json:"capturado_em"`
}

// Tabela devolve a tabela pelo nome exato.
func (e *Esquema) Tabela(nome string) (*Tabela, bool) {
	for i := range e.Tabelas {
		if e.Tabelas[i].Nome == nome {
			return &e.Tabelas[i], true
		}
	}
	return nil, false
}

// Coluna devolve a coluna pelo nome exato.
func (t *Tabela) Coluna(nome string) (*Coluna, bool) {
	for i := range t.Colunas {
		if t.Colunas[i].Nome == nome {
			return &t.Colunas[i], true
		}
	}
	return nil, false
}

// Unica diz se as colunas formam a chave primária ou um índice único (servem para
// paginar os lotes sem pular nem repetir linha).
func (t *Tabela) Unica(colunas ...string) bool {
	igual := func(a []string) bool {
		if len(a) != len(colunas) {
			return false
		}
		for i := range a {
			if !strings.EqualFold(a[i], colunas[i]) {
				return false
			}
		}
		return true
	}
	if igual(t.ChavePrimaria) {
		return true
	}
	for _, ix := range t.Indices {
		if ix.Unico && igual(ix.Colunas) {
			return true
		}
	}
	return false
}

// NomesTabelas lista os nomes (para a lista permitida de identificadores).
func (e *Esquema) NomesTabelas() []string {
	out := make([]string, 0, len(e.Tabelas))
	for _, t := range e.Tabelas {
		out = append(out, t.Nome)
	}
	return out
}

// Hash identifica a ESTRUTURA (tabelas, colunas, chaves). Linhas estimadas e a hora
// da captura ficam de fora: o hash só muda quando o schema muda de verdade — é ele
// que avisa "o banco mudou depois que o mapeamento foi aprovado".
func (e Esquema) Hash() string {
	copia := e
	copia.CapturadoEm = time.Time{}
	copia.Tabelas = make([]Tabela, len(e.Tabelas))
	copy(copia.Tabelas, e.Tabelas)
	for i := range copia.Tabelas {
		copia.Tabelas[i].LinhasEstimadas = 0
	}
	sort.Slice(copia.Tabelas, func(i, j int) bool { return copia.Tabelas[i].Nome < copia.Tabelas[j].Nome })
	b, _ := json.Marshal(copia) // tipos simples: não falha
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

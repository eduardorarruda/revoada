// Package modelo é o mapeamento de migração (ARQUITETURA §9.2/9.3): tabela de origem →
// tabela de destino, coluna a coluna, com transformações de uma lista FECHADA (nada
// de SQL livre — é o que impede injeção) e validação contra as fotos dos dois bancos.
package modelo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
)

// Acao sobre uma tabela de origem.
type Acao string

const (
	Copiar         Acao = "copiar"
	Ignorar        Acao = "ignorar"
	CriarNoDestino Acao = "criar_no_destino"
)

// Transformacao aplicada a uma coluna.
type Transformacao string

const (
	Nenhuma       Transformacao = "nenhuma"
	ConverterTipo Transformacao = "converter_tipo"
	Charset       Transformacao = "charset"      // {de, para}
	Aparar        Transformacao = "aparar"       // tira espaços das pontas
	ValorPadrao   Transformacao = "valor_padrao" // {valor}: usado quando a origem é nula
	Constante     Transformacao = "constante"    // {valor}: sem coluna de origem
	MapaValores   Transformacao = "mapa_valores" // {"S":"true","N":"false"}
	Concatenar    Transformacao = "concatenar"   // {colunas:"A,B", separador:" "}
	Dividir       Transformacao = "dividir"      // {separador, parte}
	DataFormato   Transformacao = "data_formato" // {formato}: texto → data
)

var transformacoes = []Transformacao{Nenhuma, ConverterTipo, Charset, Aparar, ValorPadrao, Constante, MapaValores, Concatenar, Dividir, DataFormato}

// Origem de uma sugestão.
type Origem string

const (
	OrigemHeuristica Origem = "heuristica"
	OrigemMCP        Origem = "mcp"
	OrigemUsuario    Origem = "usuario"
)

// Filtro estruturado (nunca SQL livre).
type Filtro struct {
	Coluna   string `json:"coluna"`
	Operador string `json:"operador"` // = <> < <= > >= nulo nao_nulo
	Valor    string `json:"valor,omitempty"`
}

var operadores = []string{"=", "<>", "<", "<=", ">", ">=", "nulo", "nao_nulo"}

// MapColuna liga uma coluna de origem a uma de destino.
type MapColuna struct {
	ColunaOrigem  string            `json:"coluna_origem,omitempty"`
	ColunaDestino string            `json:"coluna_destino"`
	Transformacao Transformacao     `json:"transformacao"`
	Parametros    map[string]string `json:"parametros,omitempty"`
	Confianca     float64           `json:"confianca,omitempty"`
	Origem        Origem            `json:"origem,omitempty"`
	Alerta        string            `json:"alerta,omitempty"`
}

// MapTabela liga uma tabela de origem a uma de destino.
type MapTabela struct {
	TabelaOrigem  string      `json:"tabela_origem"`
	TabelaDestino string      `json:"tabela_destino,omitempty"`
	Acao          Acao        `json:"acao"`
	Ordem         int         `json:"ordem"`
	ColunaLote    string      `json:"coluna_lote,omitempty"`
	Filtros       []Filtro    `json:"filtros,omitempty"`
	Colunas       []MapColuna `json:"colunas"`
	Confianca     float64     `json:"confianca,omitempty"`
	Origem        Origem      `json:"origem,omitempty"`
}

// Mapeamento é o conjunto de vínculos de um projeto (uma versão).
type Mapeamento struct {
	Tabelas []MapTabela `json:"tabelas"`
}

// Hash identifica o conteúdo: a execução guarda o hash aprovado e só roda se bater.
func (m Mapeamento) Hash() string {
	b, _ := json.Marshal(m)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Problema encontrado na validação.
type Problema struct {
	Nivel    string `json:"nivel"` // erro (impede aprovar) | aviso (pode seguir, com ciência)
	Tabela   string `json:"tabela,omitempty"`
	Coluna   string `json:"coluna,omitempty"`
	Mensagem string `json:"mensagem"`
}

// TemErro diz se há algum problema que impede aprovar.
func TemErro(ps []Problema) bool {
	return slices.ContainsFunc(ps, func(p Problema) bool { return p.Nivel == "erro" })
}

// Validar confere o mapeamento contra as fotos da origem e do destino.
func (m Mapeamento) Validar(origem, destino esquema.Esquema) []Problema {
	var ps []Problema
	erro := func(t, c, f string, a ...any) {
		ps = append(ps, Problema{Nivel: "erro", Tabela: t, Coluna: c, Mensagem: fmt.Sprintf(f, a...)})
	}
	aviso := func(t, c, f string, a ...any) {
		ps = append(ps, Problema{Nivel: "aviso", Tabela: t, Coluna: c, Mensagem: fmt.Sprintf(f, a...)})
	}
	vistas := map[string]bool{}
	for _, mt := range m.Tabelas {
		if vistas[mt.TabelaOrigem] {
			erro(mt.TabelaOrigem, "", "a tabela de origem aparece duas vezes no mapeamento")
		}
		vistas[mt.TabelaOrigem] = true
		to, ok := origem.Tabela(mt.TabelaOrigem)
		if !ok {
			erro(mt.TabelaOrigem, "", "a tabela não existe na origem (o banco mudou? capture de novo)")
			continue
		}
		switch mt.Acao {
		case Ignorar:
			continue
		case Copiar, CriarNoDestino:
		default:
			erro(mt.TabelaOrigem, "", "ação %q desconhecida", mt.Acao)
			continue
		}
		if mt.TabelaDestino == "" {
			erro(mt.TabelaOrigem, "", "falta a tabela de destino")
			continue
		}
		td, existe := destino.Tabela(mt.TabelaDestino)
		if mt.Acao == Copiar && !existe {
			erro(mt.TabelaOrigem, "", "a tabela de destino %q não existe; escolha outra ou use \"criar no destino\"", mt.TabelaDestino)
			continue
		}
		if mt.Acao == CriarNoDestino && existe {
			erro(mt.TabelaOrigem, "", "a tabela %q já existe no destino; use \"copiar\"", mt.TabelaDestino)
			continue
		}
		validarLote(mt, to, &ps)
		for _, f := range mt.Filtros {
			if _, ok := to.Coluna(f.Coluna); !ok {
				erro(mt.TabelaOrigem, f.Coluna, "filtro em coluna que não existe")
			}
			if !slices.Contains(operadores, f.Operador) {
				erro(mt.TabelaOrigem, f.Coluna, "operador de filtro %q inválido", f.Operador)
			}
		}
		validarColunas(mt, to, td, erro, aviso)
	}
	for _, t := range origem.Tabelas {
		if !vistas[t.Nome] {
			aviso(t.Nome, "", "tabela da origem sem decisão no mapeamento (não será migrada)")
		}
	}
	return ps
}

func validarLote(mt MapTabela, to *esquema.Tabela, ps *[]Problema) {
	if mt.ColunaLote == "" {
		*ps = append(*ps, Problema{Nivel: "aviso", Tabela: mt.TabelaOrigem,
			Mensagem: "sem coluna de lote: a tabela é copiada de uma vez (sem pausar/retomar no meio)"})
		return
	}
	if _, ok := to.Coluna(mt.ColunaLote); !ok {
		*ps = append(*ps, Problema{Nivel: "erro", Tabela: mt.TabelaOrigem, Coluna: mt.ColunaLote, Mensagem: "a coluna de lote não existe"})
		return
	}
	if !to.Unica(mt.ColunaLote) {
		*ps = append(*ps, Problema{Nivel: "aviso", Tabela: mt.TabelaOrigem, Coluna: mt.ColunaLote,
			Mensagem: "a coluna de lote não é única: retomar depois de pausar pode repetir ou pular linhas"})
	}
}

func validarColunas(mt MapTabela, to, td *esquema.Tabela, erro, aviso func(t, c, f string, a ...any)) {
	destinos := map[string]bool{}
	for _, mc := range mt.Colunas {
		if !slices.Contains(transformacoes, mc.Transformacao) {
			erro(mt.TabelaOrigem, mc.ColunaDestino, "transformação %q desconhecida", mc.Transformacao)
			continue
		}
		if destinos[mc.ColunaDestino] {
			erro(mt.TabelaOrigem, mc.ColunaDestino, "duas colunas de origem gravam na mesma coluna de destino")
		}
		destinos[mc.ColunaDestino] = true

		var co *esquema.Coluna
		if mc.Transformacao != Constante {
			c, ok := to.Coluna(mc.ColunaOrigem)
			if !ok {
				erro(mt.TabelaOrigem, mc.ColunaOrigem, "a coluna de origem não existe")
				continue
			}
			co = c
		} else if mc.Parametros["valor"] == "" {
			aviso(mt.TabelaOrigem, mc.ColunaDestino, "constante vazia")
		}
		if td == nil { // criar no destino: a coluna nasce do mapeamento
			continue
		}
		cd, ok := td.Coluna(mc.ColunaDestino)
		if !ok {
			erro(mt.TabelaOrigem, mc.ColunaDestino, "a coluna de destino não existe")
			continue
		}
		if co != nil && mc.Transformacao == Nenhuma {
			if c := esquema.Comparar(*co, *cd); c.PrecisaTransformar {
				erro(mt.TabelaOrigem, mc.ColunaDestino, "%s", c.Alerta)
			} else if c.Alerta != "" {
				aviso(mt.TabelaOrigem, mc.ColunaDestino, "%s", c.Alerta)
			}
			if co.Nulavel && !cd.Nulavel && cd.Padrao == "" {
				aviso(mt.TabelaOrigem, mc.ColunaDestino, "a origem aceita nulo e o destino não: linhas com nulo serão recusadas (use \"valor padrão\")")
			}
		}
	}
	if td == nil {
		return
	}
	for _, cd := range td.Colunas {
		if !destinos[cd.Nome] && !cd.Nulavel && cd.Padrao == "" && !cd.Identidade {
			erro(mt.TabelaOrigem, cd.Nome, "a coluna de destino é obrigatória (NOT NULL, sem padrão) e nada grava nela")
		}
	}
}

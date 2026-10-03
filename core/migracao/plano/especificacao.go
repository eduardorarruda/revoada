package plano

import (
	"fmt"
	"strings"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
	"github.com/eduardorarruda/revoada/core/migracao/modelo"
)

// Tipos de tarefa da migração (o agente só aceita os liberados na config local).
const (
	TarefaSimular  = "migracao.simular"
	TarefaExecutar = "migracao.executar"
	TarefaReverter = "migracao.reverter"
	// TarefaVerificar relê origem e destino e compara linha a linha, coluna a coluna
	// (só leitura nos dois lados). Usa a mesma Especificacao da execução.
	TarefaVerificar = "migracao.verificar"
)

// LoteInicial é o tamanho do primeiro lote (ARQUITETURA §9.4); depois ele se adapta.
const LoteInicial = 5000

// Banco é onde ler ou gravar. A senha NÃO vem aqui: chega selada na tarefa, com o
// nome "origem" ou "destino", e só o agente destinatário consegue abrir.
type Banco struct {
	Motor    string            `json:"motor"`
	Endereco string            `json:"endereco"`
	Banco    string            `json:"banco"`
	Usuario  string            `json:"usuario"`
	Opcoes   map[string]string `json:"opcoes,omitempty"`
}

// Especificacao é o JSON de uma tarefa migracao.* (assinado pelo painel).
type Especificacao struct {
	// Execucao nomeia o controle no destino (checkpoints, staging, cópia prévia).
	// Ao retomar uma execução que caiu, é o id da execução ORIGINAL.
	Execucao       string            `json:"execucao"`
	ProjetoID      string            `json:"projeto_id"`
	Versao         int               `json:"versao"`
	HashMapeamento string            `json:"hash_mapeamento"`
	Origem         Banco             `json:"origem"`
	Destino        Banco             `json:"destino"`
	Mapeamento     modelo.Mapeamento `json:"mapeamento"`
	EsquemaOrigem  esquema.Esquema   `json:"esquema_origem"`
	EsquemaDestino esquema.Esquema   `json:"esquema_destino"`
	Lote           int               `json:"lote,omitempty"`
	// Amostras por tabela no relatório da simulação (padrão 10).
	Amostras int `json:"amostras,omitempty"`
}

// ---------------------------------------------------------------- relatório (dry-run)

// Amostra aponta UMA linha com problema pela chave — nunca pelo conteúdo.
type Amostra struct {
	Chave    string `json:"chave,omitempty"`
	Coluna   string `json:"coluna,omitempty"`
	Tipo     string `json:"tipo"`
	Mensagem string `json:"mensagem"`
}

// RelatorioTabela é o resultado da simulação de uma tabela.
type RelatorioTabela struct {
	Origem      string           `json:"origem"`
	Destino     string           `json:"destino"`
	Acao        string           `json:"acao"`
	Estrategia  string           `json:"estrategia"` // o que a execução vai fazer (e como reverte)
	Linhas      int64            `json:"linhas"`
	LinhasOK    int64            `json:"linhas_ok"`
	ComProblema int64            `json:"com_problema"`
	Violacoes   map[string]int64 `json:"violacoes,omitempty"`
	Perdas      map[string]int64 `json:"perdas,omitempty"`
	Amostras    []Amostra        `json:"amostras,omitempty"`
	DuracaoMS   int64            `json:"duracao_ms"`
}

// RelatorioSimulacao é o resumo da tarefa migracao.simular.
type RelatorioSimulacao struct {
	HashMapeamento string            `json:"hash_mapeamento"`
	Ordem          []string          `json:"ordem"`
	Ciclos         [][]string        `json:"ciclos,omitempty"`
	Tabelas        []RelatorioTabela `json:"tabelas"`
	TotalLinhas    int64             `json:"total_linhas"`
	// Bloqueantes = linhas que o destino recusaria. Com qualquer uma, o painel não
	// libera a execução real.
	Bloqueantes    int64 `json:"bloqueantes"`
	Perdas         int64 `json:"perdas"`
	TempoEstimadoS int64 `json:"tempo_estimado_s"`
	DuracaoMS      int64 `json:"duracao_ms"`
}

// ---------------------------------------------------------------- manifesto (rollback)

// Estratégias de rollback por tabela (ARQUITETURA §9.4).
const (
	// EstrategiaApagarTabela: a tabela foi criada pela execução; reverter = DROP.
	EstrategiaApagarTabela = "apagar_tabela"
	// EstrategiaEsvaziar: a tabela existia VAZIA. Carrega numa staging como a de
	// baixo; depois da troca, reverter = apagar as linhas (volta a vazia).
	EstrategiaEsvaziar = "esvaziar"
	// EstrategiaStaging: a tabela tinha dados. Carrega numa staging, valida e troca
	// numa transação; antes da troca reverter = apagar a staging; depois = restaurar
	// a cópia prévia (obrigatória).
	EstrategiaStaging = "staging"
	// Toda tabela que já existia no destino passa por staging e TODAS trocam juntas,
	// numa transação, na ordem das FKs: as tabelas reais só mudam de uma vez, no fim,
	// e uma filha nunca aponta para um pai que ainda está na staging.
)

// ItemManifesto é uma tabela tocada pela execução.
type ItemManifesto struct {
	Tabela     string `json:"tabela"` // no destino
	Estrategia string `json:"estrategia"`
	Staging    string `json:"staging,omitempty"`
	Copia      string `json:"copia,omitempty"`
	Trocada    bool   `json:"trocada,omitempty"`
}

// Manifesto é o plano de rollback: tudo o que a execução criou ou mudou no destino.
// Fica no resumo da execução (painel) E numa tabela de controle no próprio destino
// — o rollback funciona mesmo se um dos dois se perder.
type Manifesto struct {
	Execucao string          `json:"execucao"`
	Esquema  string          `json:"esquema"` // schema do PostgreSQL de destino
	Fase     string          `json:"fase"`    // carregando | trocado | concluido | revertido
	Itens    []ItemManifesto `json:"itens"`
}

// Item devolve o item da tabela de destino.
func (m *Manifesto) Item(tabela string) (*ItemManifesto, bool) {
	for i := range m.Itens {
		if m.Itens[i].Tabela == tabela {
			return &m.Itens[i], true
		}
	}
	return nil, false
}

// ResumoTabela é o placar de uma tabela na execução real.
//
// ChecksumOK = contagem + soma das CHAVES bateram (chegaram as mesmas linhas).
// ConteudoOK = além disso, a soma do CONTEÚDO de cada coluna conferida bateu entre o
// que foi gravado e o que o destino devolveu ao ser relido (verificacao.go). As
// colunas que não deu para comparar vêm listadas com o motivo — nunca entram no OK.
type ResumoTabela struct {
	Origem     string `json:"origem"`
	Destino    string `json:"destino"`
	Linhas     int64  `json:"linhas"`
	Gravadas   int64  `json:"gravadas"`
	ChecksumOK bool   `json:"checksum_ok"`
	ConteudoOK bool   `json:"conteudo_ok"`
	// ConteudoMotivo explica por que o conteúdo da tabela inteira não foi conferido
	// (ex.: carregada por um agente antigo). Vazio quando a conferência rodou.
	ConteudoMotivo       string               `json:"conteudo_motivo,omitempty"`
	ColunasConferidas    []string             `json:"colunas_conferidas,omitempty"`
	ColunasNaoConferidas []ColunaNaoConferida `json:"colunas_nao_conferidas,omitempty"`
	// Divergencias: amostra (até 20) das linhas cujo conteúdo não bateu — só a chave
	// e o NOME das colunas, nunca os valores.
	Divergencias []Divergencia `json:"divergencias,omitempty"`
	DuracaoMS    int64         `json:"duracao_ms"`
}

// ResumoExecucao é o resumo da tarefa migracao.executar.
type ResumoExecucao struct {
	Execucao  string         `json:"execucao"`
	Tabelas   []ResumoTabela `json:"tabelas"`
	Linhas    int64          `json:"linhas"`
	Manifesto Manifesto      `json:"manifesto"`
	Avisos    []string       `json:"avisos,omitempty"`
	DuracaoMS int64          `json:"duracao_ms"`
}

// ---------------------------------------------------------------- tabelas a carregar

// Passo é uma tabela do mapeamento já resolvida contra as fotos dos dois bancos.
type Passo struct {
	Map     modelo.MapTabela
	Origem  esquema.Tabela
	Destino esquema.Tabela // para "criar no destino", a tabela que vai nascer
	Criar   bool
}

// Passos resolve o mapeamento na ordem de carga (pais antes de filhos). Tabelas
// ignoradas ficam de fora. Erro se o mapeamento não bate com as fotos — o painel já
// validou, mas o agente não confia: confere de novo.
func Passos(e Especificacao) ([]Passo, [][]string, error) {
	porOrigem := map[string]modelo.MapTabela{}
	var tabs []esquema.Tabela
	for _, mt := range e.Mapeamento.Tabelas {
		if mt.Acao == modelo.Ignorar {
			continue
		}
		to, ok := e.EsquemaOrigem.Tabela(mt.TabelaOrigem)
		if !ok {
			return nil, nil, fmt.Errorf("a tabela %s não existe na origem", mt.TabelaOrigem)
		}
		porOrigem[mt.TabelaOrigem] = mt
		tabs = append(tabs, *to)
	}
	ordem, ciclos := OrdenarPorFK(tabs)
	passos := make([]Passo, 0, len(ordem))
	for _, nome := range ordem {
		mt := porOrigem[nome]
		to, _ := e.EsquemaOrigem.Tabela(nome)
		p := Passo{Map: mt, Origem: *to}
		switch mt.Acao {
		case modelo.Copiar:
			td, ok := e.EsquemaDestino.Tabela(mt.TabelaDestino)
			if !ok {
				return nil, nil, fmt.Errorf("a tabela %s não existe no destino", mt.TabelaDestino)
			}
			p.Destino = *td
		case modelo.CriarNoDestino:
			p.Destino, p.Criar = TabelaCriada(mt, *to), true
		default:
			return nil, nil, fmt.Errorf("ação %q desconhecida em %s", mt.Acao, nome)
		}
		gravadas := map[string]bool{}
		for _, mc := range mt.Colunas {
			gravadas[mc.ColunaDestino] = true
			if _, ok := p.Destino.Coluna(mc.ColunaDestino); !ok {
				return nil, nil, fmt.Errorf("a coluna %s.%s não existe no destino", mt.TabelaDestino, mc.ColunaDestino)
			}
			if mc.Transformacao != modelo.Constante && mc.Transformacao != modelo.Concatenar {
				if _, ok := to.Coluna(mc.ColunaOrigem); !ok {
					return nil, nil, fmt.Errorf("a coluna %s.%s não existe na origem", nome, mc.ColunaOrigem)
				}
			}
		}
		// coluna obrigatória do destino que nada grava: o INSERT falharia só na troca
		// final (depois de horas de carga). O painel já recusa aprovar; o agente confere.
		for _, cd := range p.Destino.Colunas {
			if !gravadas[cd.Nome] && !cd.Nulavel && cd.Padrao == "" && !cd.Identidade {
				return nil, nil, fmt.Errorf("a coluna %s.%s é obrigatória no destino e nada grava nela", p.Destino.Nome, cd.Nome)
			}
		}
		passos = append(passos, p)
	}
	return passos, ciclos, nil
}

// OrdemDestino reordena os passos pelas FKs do DESTINO (pais antes de filhos): é a
// ordem em que as stagings trocam pelas tabelas reais e o inverso em que o reverter
// apaga. A ordem de carga (origem) pode diferir quando o destino tem outras FKs.
// Tabelas criadas pela migração entram com as FKs que o fim da execução religa
// (traduzidas da origem): o reverter precisa apagar a filha antes do pai.
func OrdemDestino(passos []Passo) []Passo {
	destinoDe := map[string]string{}
	for _, p := range passos {
		destinoDe[p.Origem.Nome] = p.Destino.Nome
	}
	tabs := make([]esquema.Tabela, len(passos))
	porNome := map[string]Passo{}
	for i, p := range passos {
		t := p.Destino
		if p.Criar {
			t.Estrangeiras = nil
			for _, fk := range p.Origem.Estrangeiras {
				if pai, ok := destinoDe[fk.TabelaRef]; ok {
					t.Estrangeiras = append(t.Estrangeiras, esquema.ChaveEstrangeira{Nome: fk.Nome, TabelaRef: pai})
				}
			}
		}
		tabs[i] = t
		porNome[p.Destino.Nome] = p
	}
	ordem, _ := OrdenarPorFK(tabs)
	out := make([]Passo, 0, len(passos))
	for _, n := range ordem {
		out = append(out, porNome[n])
	}
	return out
}

// TabelaCriada é a tabela que a ação "criar no destino" faz nascer: as colunas do
// mapeamento com o tipo da coluna de origem e a chave primária traduzida.
func TabelaCriada(mt modelo.MapTabela, origem esquema.Tabela) esquema.Tabela {
	t := esquema.Tabela{Nome: mt.TabelaDestino}
	destinoDe := map[string]string{}
	for _, mc := range mt.Colunas {
		c := esquema.Coluna{Nome: mc.ColunaDestino, Tipo: esquema.Texto, TipoNativo: "text", Nulavel: true}
		if co, ok := origem.Coluna(mc.ColunaOrigem); ok && mc.Transformacao != modelo.Constante {
			c = *co
			c.Nome, c.Padrao, c.Identidade, c.Charset = mc.ColunaDestino, "", false, ""
			destinoDe[mc.ColunaOrigem] = mc.ColunaDestino
		}
		if c.Tipo == esquema.Texto && (mc.Transformacao == modelo.Concatenar || mc.Transformacao == modelo.Constante) {
			c.Tipo, c.Tamanho = esquema.TextoLongo, 0
		}
		c.TipoNativo = esquema.TipoDDLPostgres(c)
		t.Colunas = append(t.Colunas, c)
	}
	pk := make([]string, 0, len(origem.ChavePrimaria))
	for _, c := range origem.ChavePrimaria {
		d, ok := destinoDe[c]
		if !ok {
			pk = nil // a PK não veio inteira: a tabela nasce sem PK
			break
		}
		pk = append(pk, d)
	}
	t.ChavePrimaria = pk
	return t
}

// AspasPG põe um identificador entre aspas no PostgreSQL (dobrando aspas internas).
func AspasPG(nome string) string { return `"` + strings.ReplaceAll(nome, `"`, `""`) + `"` }

// DDLCriarPostgres é o CREATE TABLE da tabela que nasce no destino.
func DDLCriarPostgres(schema string, t esquema.Tabela) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s.%s (", AspasPG(schema), AspasPG(t.Nome))
	for i, c := range t.Colunas {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s %s", AspasPG(c.Nome), esquema.TipoDDLPostgres(c))
		if !c.Nulavel {
			b.WriteString(" NOT NULL")
		}
	}
	if len(t.ChavePrimaria) > 0 {
		cols := make([]string, len(t.ChavePrimaria))
		for i, c := range t.ChavePrimaria {
			cols[i] = AspasPG(c)
		}
		fmt.Fprintf(&b, ", PRIMARY KEY (%s)", strings.Join(cols, ", "))
	}
	b.WriteString(")")
	return b.String()
}

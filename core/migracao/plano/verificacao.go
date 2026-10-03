package plano

import (
	"encoding/json"
	"strings"
	"time"
)

// ---------------------------------------------------------------- verificação de conteúdo

// AmostraDivergencias é quantas linhas divergentes o relatório aponta por tabela.
const AmostraDivergencias = 20

// MarcadorChave aparece nos erros do motor de cópia logo antes do VALOR da chave da
// linha. Quem não pode ver chaves (MCP, perfis só de leitura) corta dali em diante.
const MarcadorChave = "linha de chave "

// ColunaNaoConferida é uma coluna gravada cujo conteúdo não deu para comparar, e por
// quê (valor que nasce no destino via DEFAULT, tipo sem forma canônica…).
type ColunaNaoConferida struct {
	Coluna string `json:"coluna"`
	Motivo string `json:"motivo"`
}

// Divergencia aponta UMA linha cujo conteúdo não bateu: a chave (como nas amostras
// da simulação) e o nome das colunas diferentes. NUNCA os valores.
type Divergencia struct {
	Chave   string   `json:"chave,omitempty"`
	Colunas []string `json:"colunas,omitempty"`
	// Ausente: a linha existe na origem e não foi achada no destino.
	Ausente bool `json:"ausente,omitempty"`
}

// ResumoVerificacaoTabela é o placar da comparação linha a linha de uma tabela.
type ResumoVerificacaoTabela struct {
	Origem  string `json:"origem"`
	Destino string `json:"destino"`
	// Onde: "tabela" (a real, depois da troca) ou "staging" (antes da troca).
	Onde        string `json:"onde"`
	Linhas      int64  `json:"linhas"` // linhas da origem conferidas
	Identicas   int64  `json:"identicas"`
	Divergentes int64  `json:"divergentes"`
	Ausentes    int64  `json:"ausentes"`
	// NaoLocalizadas: linhas da origem que não deu para achar no destino — a chave
	// nasce lá (DEFAULT) ou aparece repetida no destino. Nunca contam como idênticas.
	NaoLocalizadas int64 `json:"nao_localizadas,omitempty"`
	// PorColuna: quantas linhas divergiram em cada coluna.
	PorColuna            map[string]int64     `json:"por_coluna,omitempty"`
	ColunasConferidas    []string             `json:"colunas_conferidas,omitempty"`
	ColunasNaoConferidas []ColunaNaoConferida `json:"colunas_nao_conferidas,omitempty"`
	Divergencias         []Divergencia        `json:"divergencias,omitempty"`
	// SemChave: a tabela não tem chave para achar cada linha; a comparação foi pela
	// soma do conteúdo de cada coluna (sabe-se QUAL coluna diverge, não qual linha).
	SemChave   bool   `json:"sem_chave,omitempty"`
	ConteudoOK bool   `json:"conteudo_ok"`
	Motivo     string `json:"motivo,omitempty"` // por que a tabela não foi conferida
	// Texto: a dupla conferência pelo texto dos próprios bancos (independente do
	// driver e da conversão do agente).
	Texto     *ConferenciaTexto `json:"texto,omitempty"`
	DuracaoMS int64             `json:"duracao_ms"`
}

// ConferenciaTexto é a segunda conferência, INDEPENDENTE da primeira: cada banco
// devolve o valor já renderizado como texto no próprio servidor (CAST … AS VARCHAR no
// Firebird, ::text/to_char no PostgreSQL) e os textos são comparados com o mínimo de
// normalização documentada. Pega o que a comparação canônica não vê porque nasce
// antes dela: erro do driver ao ler a origem (o horário de verão que tirava um dia
// das datas), mapeamento de tipo, fuso. Só para colunas sem transformação.
type ConferenciaTexto struct {
	OK          bool             `json:"ok"`
	Linhas      int64            `json:"linhas"`
	Identicas   int64            `json:"identicas"`
	Divergentes int64            `json:"divergentes"`
	Ausentes    int64            `json:"ausentes"`
	PorColuna   map[string]int64 `json:"por_coluna,omitempty"`
	// Colunas: conferidas pelo texto, inteiras.
	Colunas []string `json:"colunas,omitempty"`
	// Parciais: conferidas pelo texto só em parte (tamanho do binário, começo e
	// tamanho de textos longos) — o resto fica com a conferência canônica.
	Parciais []ColunaNaoConferida `json:"parciais,omitempty"`
	// NaoConferidas: fora da conferência pelo texto, com o motivo (nunca contam no OK).
	NaoConferidas []ColunaNaoConferida `json:"nao_conferidas,omitempty"`
	Divergencias  []Divergencia        `json:"divergencias,omitempty"`
	Motivo        string               `json:"motivo,omitempty"` // por que não rodou nesta tabela
}

// ResumoVerificacao é o resumo da tarefa migracao.verificar.
type ResumoVerificacao struct {
	Execucao    string                    `json:"execucao"`
	Fase        string                    `json:"fase"` // a do manifesto no destino
	Tabelas     []ResumoVerificacaoTabela `json:"tabelas"`
	Linhas      int64                     `json:"linhas"`
	Identicas   int64                     `json:"identicas"`
	Divergentes int64                     `json:"divergentes"`
	Ausentes    int64                     `json:"ausentes"`
	ConteudoOK  bool                      `json:"conteudo_ok"`
	// TextoOK: a dupla conferência pelo texto rodou em alguma tabela e nenhuma
	// divergiu. ColunasTexto: quantas colunas foram conferidas pelo texto, inteiras.
	TextoOK      bool      `json:"texto_ok"`
	ColunasTexto int       `json:"colunas_texto"`
	VerificadoEm time.Time `json:"verificado_em"`
	DuracaoMS    int64     `json:"duracao_ms"`
}

// OcultarChaves tira de um resumo de tarefa da migração tudo o que é valor de linha
// (as chaves das amostras e das divergências), para quem não pode vê-las. Formato
// inesperado devolve nil: na dúvida, não devolve nada.
func OcultarChaves(tipo string, resumo []byte) []byte {
	if len(resumo) == 0 {
		return resumo
	}
	switch tipo {
	case "simular":
		var rel RelatorioSimulacao
		if json.Unmarshal(resumo, &rel) != nil {
			return nil
		}
		for i := range rel.Tabelas {
			for j := range rel.Tabelas[i].Amostras {
				rel.Tabelas[i].Amostras[j].Chave = ""
			}
		}
		b, _ := json.Marshal(rel)
		return b
	case "executar":
		var res ResumoExecucao
		if json.Unmarshal(resumo, &res) != nil {
			return nil
		}
		for i := range res.Tabelas {
			ocultar(res.Tabelas[i].Divergencias)
		}
		b, _ := json.Marshal(res)
		return b
	case "verificar":
		var res ResumoVerificacao
		if json.Unmarshal(resumo, &res) != nil {
			return nil
		}
		for i := range res.Tabelas {
			ocultar(res.Tabelas[i].Divergencias)
			if res.Tabelas[i].Texto != nil {
				ocultar(res.Tabelas[i].Texto.Divergencias)
			}
		}
		b, _ := json.Marshal(res)
		return b
	}
	return resumo
}

func ocultar(ds []Divergencia) {
	for i := range ds {
		ds[i].Chave = ""
	}
}

// OcultarChaveErro corta a mensagem de erro do motor no marcador da chave.
func OcultarChaveErro(erro, sufixo string) string {
	if i := strings.Index(erro, MarcadorChave); i >= 0 {
		return erro[:i] + MarcadorChave + sufixo
	}
	return erro
}

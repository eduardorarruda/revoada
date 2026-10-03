// Package upgrade é o upgrade de versão do Firebird (ARQUITETURA §9.5): o diagnóstico que
// responde "o que vai quebrar?" e o plano do upgrade (backup na versão antiga →
// restore no Firebird 5 → validação). Aqui ficam só as regras puras — o que é risco,
// a nota, o fix.sql e a estimativa de parada; quem lê o banco é o agente.
package upgrade

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/core/migracao/plano"
)

// Tipos de tarefa do upgrade (liberados um a um em canal.tarefas_permitidas).
const (
	TarefaDiagnostico = "firebird.diagnostico"
	TarefaUpgrade     = "firebird.upgrade"
	TarefaDescartar   = "firebird.descartar"
)

// Níveis de um achado.
const (
	Bloqueio = "bloqueio" // o restore no FB5 falha (ou perde dado) se não corrigir antes
	Risco    = "risco"    // o banco sobe, mas algo muda de comportamento (aplicação, UDF…)
	Info     = "info"     // vale saber; não muda o plano
)

// Categorias.
const (
	CatVersao    = "versao"
	CatPalavra   = "palavra_reservada"
	CatUDF       = "udf"
	CatData      = "data_hora"
	CatNulo      = "not_null"
	CatFK        = "chave_estrangeira"
	CatUnica     = "unicidade"
	CatCharset   = "charset"
	CatUsuarios  = "usuarios"
	CatCorrupcao = "corrupcao"
	CatEnsaio    = "ensaio"
)

// Achado é uma coisa encontrada pelo diagnóstico. Nunca traz conteúdo de linha:
// só objetos (tabela, coluna, procedure) e contagens.
type Achado struct {
	Nivel     string `json:"nivel"`
	Categoria string `json:"categoria"`
	Objeto    string `json:"objeto,omitempty"`
	Mensagem  string `json:"mensagem"`
	Quantos   int64  `json:"quantos,omitempty"`
	// Correcao é SQL sugerido (comentado quando apaga ou muda dado) ou um passo.
	Correcao string `json:"correcao,omitempty"`
}

// Ensaio é o restore só de metadados no Firebird 5 (procedures e triggers precisam
// compilar lá).
type Ensaio struct {
	Feito     bool     `json:"feito"`
	OK        bool     `json:"ok"`
	Erros     []string `json:"erros,omitempty"`
	DuracaoMS int64    `json:"duracao_ms"`
}

// Diagnostico é o resumo da tarefa firebird.diagnostico.
type Diagnostico struct {
	Versao      string   `json:"versao"`
	ODS         string   `json:"ods"`
	Dialeto     int      `json:"dialeto"`
	Charset     string   `json:"charset"`
	PageSize    int      `json:"page_size"`
	Tamanho     int64    `json:"tamanho_bytes"`
	Tabelas     int      `json:"tabelas"`
	Procedures  int      `json:"procedures"`
	Triggers    int      `json:"triggers"`
	UDFs        int      `json:"udfs"`
	Achados     []Achado `json:"achados"`
	Ensaio      *Ensaio  `json:"ensaio,omitempty"`
	Nota        int      `json:"nota"`  // 0 (tranquilo) … 100 (não suba sem corrigir)
	Risco       string   `json:"risco"` // baixo | medio | alto
	Bloqueios   int      `json:"bloqueios"`
	ParadaS     int64    `json:"parada_estimada_s"`
	FixSQL      string   `json:"fix_sql"`
	CharsetFix  string   `json:"charset_fix,omitempty"` // sugere FIX_FSS_DATA/METADATA com este charset
	DuracaoMS   int64    `json:"duracao_ms"`
	CapturadoEm string   `json:"capturado_em"`
}

// Especificacao é o JSON das tarefas firebird.* (montado pelo painel, assinado).
type Especificacao struct {
	Execucao  string      `json:"execucao"`
	ProjetoID string      `json:"projeto_id"`
	Origem    plano.Banco `json:"origem"`
	// Destino é o servidor Firebird 5. Banco = caminho do banco NOVO (nunca existe antes).
	Destino *plano.Banco `json:"destino,omitempty"`
	// Diretorio de trabalho visível para os dois servidores (o .fbk passa por ele).
	Diretorio string `json:"diretorio,omitempty"`
	// CharsetFix: charset em que o texto da origem foi realmente gravado (para o
	// restore com -FIX_FSS_DATA/-FIX_FSS_METADATA). Vazio = não corrige.
	CharsetFix string `json:"charset_fix,omitempty"`
	// ValidarPaginas roda a validação física (gfix -v -full, sem alterar) no diagnóstico.
	ValidarPaginas bool `json:"validar_paginas"`
	Workers        int  `json:"workers,omitempty"`
	// AmostraTexto: linhas lidas por coluna de texto na procura de charset quebrado.
	AmostraTexto int `json:"amostra_texto,omitempty"`
}

// Comparacao de uma tabela entre o banco antigo e o novo.
type Comparacao struct {
	Tabela string `json:"tabela"`
	Antes  int64  `json:"antes"`
	Depois int64  `json:"depois"`
	OK     bool   `json:"ok"`
}

// ResumoUpgrade é o resumo da tarefa firebird.upgrade.
type ResumoUpgrade struct {
	Execucao    string       `json:"execucao"`
	Backup      string       `json:"backup"`
	NovoBanco   string       `json:"novo_banco"`
	Versao      string       `json:"versao_nova"`
	ODS         string       `json:"ods_nova"`
	Tabelas     []Comparacao `json:"tabelas"`
	Geradores   []Comparacao `json:"geradores"`
	Metadados   []Comparacao `json:"metadados"`
	TudoConfere bool         `json:"tudo_confere"`
	Avisos      []string     `json:"avisos,omitempty"`
	// Reverter = descartar o banco novo: o original nunca foi tocado.
	Reverter  string `json:"reverter"`
	DuracaoMS int64  `json:"duracao_ms"`
}

// ---------------------------------------------------------------- regras

// Pontuar dá a nota de risco: bloqueio pesa muito, risco pesa médio.
func Pontuar(as []Achado) (nota int, nivel string, bloqueios int) {
	for _, a := range as {
		switch a.Nivel {
		case Bloqueio:
			nota += 35
			bloqueios++
		case Risco:
			nota += 8
		}
	}
	nota = min(nota, 100)
	switch {
	case bloqueios > 0 || nota >= 60:
		nivel = "alto"
	case nota >= 20:
		nivel = "medio"
	default:
		nivel = "baixo"
	}
	return nota, nivel, bloqueios
}

// Vazões conservadoras (disco comum de servidor); só para a estimativa de parada.
const (
	vazaoBackup  = 40 << 20 // bytes/s
	vazaoRestore = 20 << 20 // restore recria índices: mais lento
)

// EstimarParada estima a janela (backup + restore + validação) para o tamanho dado.
func EstimarParada(bytes int64, workers int) time.Duration {
	if bytes <= 0 {
		return time.Minute
	}
	restore := float64(bytes) / vazaoRestore
	if workers > 1 { // FB5 restaura índices em paralelo (-par)
		restore /= min(float64(workers), 4) * 0.7
	}
	total := float64(bytes)/vazaoBackup + restore
	return time.Duration(total*1.2)*time.Second + time.Minute // +20% e +1 min de validação
}

// GerarFixSQL junta as correções em um script comentado. Tudo que apaga ou muda dado
// sai COMENTADO: quem decide é uma pessoa, depois de olhar.
func GerarFixSQL(d Diagnostico) string {
	var b strings.Builder
	fmt.Fprintf(&b, "-- fix.sql gerado pelo Revoada em %s\n", d.CapturadoEm)
	fmt.Fprintf(&b, "-- Banco: Firebird %s, ODS %s, dialeto %d, charset %s\n", d.Versao, d.ODS, d.Dialeto, d.Charset)
	b.WriteString("-- Rode na ORIGEM, numa janela de manutenção, DEPOIS de um backup. Revise linha a linha.\n")
	ordem := map[string]int{Bloqueio: 0, Risco: 1, Info: 2}
	as := append([]Achado(nil), d.Achados...)
	sort.SliceStable(as, func(i, j int) bool { return ordem[as[i].Nivel] < ordem[as[j].Nivel] })
	atual := ""
	for _, a := range as {
		if a.Correcao == "" {
			continue
		}
		if a.Nivel != atual {
			atual = a.Nivel
			fmt.Fprintf(&b, "\n-- ===================== %s =====================\n", strings.ToUpper(atual))
		}
		fmt.Fprintf(&b, "\n-- [%s] %s: %s\n%s\n", a.Categoria, a.Objeto, a.Mensagem, a.Correcao)
	}
	return b.String()
}

// AspasFB põe identificador entre aspas no Firebird (dialeto 3).
func AspasFB(nome string) string { return `"` + strings.ReplaceAll(nome, `"`, `""`) + `"` }

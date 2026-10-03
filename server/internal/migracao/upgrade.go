package migracao

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	up "github.com/eduardorarruda/revoada/core/migracao/upgrade"
	"github.com/eduardorarruda/revoada/core/validacao"
	"github.com/eduardorarruda/revoada/server/internal/entrada"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// validadeDiagnostico: o upgrade só libera com um diagnóstico recente (o banco muda).
const validadeDiagnostico = 24 * time.Hour

// Upgrade de versão do Firebird (ARQUITETURA §9.5). O projeto é do tipo
// upgrade_versao: origem = banco antigo; destino = servidor Firebird 5, cujo "banco"
// é o caminho do arquivo NOVO e cuja opção diretorio_backup é a pasta que os dois
// servidores enxergam.

func (h *Handler) projetoUpgrade(ctx context.Context, w http.ResponseWriter, id string) (store.ProjetoMigracao, bool) {
	p, err := h.st.ProjetoPorID(ctx, id)
	if err != nil {
		h.erro(w, err)
		return p, false
	}
	if p.Tipo != "upgrade_versao" {
		http.Error(w, "este projeto não é de upgrade de versão", http.StatusConflict)
		return p, false
	}
	return p, true
}

func (h *Handler) especUpgrade(ctx context.Context, p store.ProjetoMigracao, execucao string, charsetFix string, validar bool) (up.Especificacao, error) {
	co, err := h.st.ConexaoPorID(ctx, p.OrigemID)
	if err != nil {
		return up.Especificacao{}, err
	}
	esp := up.Especificacao{Execucao: execucao, ProjetoID: p.ID, Origem: banco(co), CharsetFix: charsetFix, ValidarPaginas: validar, Workers: 2}
	if p.DestinoID != nil {
		cd, err := h.st.ConexaoPorID(ctx, *p.DestinoID)
		if err != nil {
			return esp, err
		}
		b := banco(cd)
		esp.Destino, esp.Diretorio = &b, cd.Opcoes["diretorio_backup"]
	}
	return esp, nil
}

// situacaoUpgrade resume o histórico do projeto de upgrade.
type situacaoUpgrade struct {
	ocupado     bool
	diagnostico *store.ExecucaoMigracao // o mais recente com sucesso
	upgrade     *store.ExecucaoMigracao // o mais recente (qualquer estado)
	descartado  map[string]bool         // execução de upgrade → já descartada
}

func (h *Handler) situacaoUpgrade(ctx context.Context, projetoID string) (situacaoUpgrade, []store.ExecucaoMigracao, error) {
	execs, err := h.st.ExecucoesDoProjeto(ctx, projetoID, 200)
	if err != nil {
		return situacaoUpgrade{}, nil, err
	}
	s := situacaoUpgrade{descartado: map[string]bool{}}
	for i := range execs { // mais recentes primeiro
		e := &execs[i]
		if slices.Contains(estadosAbertos, e.Tarefa.Estado) && e.Tipo != "diagnosticar" {
			s.ocupado = true
		}
		switch e.Tipo {
		case "diagnosticar":
			if s.diagnostico == nil && e.Tarefa.Estado == "sucesso" {
				s.diagnostico = e
			}
		case "atualizar":
			if s.upgrade == nil {
				s.upgrade = e
			}
		case "descartar":
			if e.Tarefa.Estado == "sucesso" {
				s.descartado[e.Execucao] = true
			}
		}
	}
	return s, execs, nil
}

// liberado diz se o diagnóstico mais recente permite subir: sem bloqueio, com ensaio
// feito e aprovado, e de menos de 24 h.
func (s situacaoUpgrade) liberado(agora time.Time) (*up.Diagnostico, error) {
	if s.diagnostico == nil {
		return nil, errors.New("rode o diagnóstico antes (o ensaio é obrigatório)")
	}
	var d up.Diagnostico
	if err := json.Unmarshal(s.diagnostico.Tarefa.Resumo, &d); err != nil {
		return nil, errors.New("o diagnóstico guardado está ilegível; rode de novo")
	}
	switch {
	case agora.Sub(s.diagnostico.CriadaEm) > validadeDiagnostico:
		return &d, errors.New("o diagnóstico tem mais de 24 h; rode de novo (o banco pode ter mudado)")
	case d.Bloqueios > 0:
		return &d, errors.New("o diagnóstico tem bloqueios; corrija (veja o fix.sql) e rode de novo")
	case d.Ensaio == nil || !d.Ensaio.OK:
		return &d, errors.New("o ensaio no Firebird 5 não foi feito ou falhou; confira o destino e o diretório compartilhado")
	}
	return &d, nil
}

type pedidoUpgrade struct {
	AgenteID       string `json:"agente_id"`
	ValidarPaginas bool   `json:"validar_paginas"`
	CharsetFix     string `json:"charset_fix"`
	ExecucaoID     string `json:"execucao_id"`
}

var charsetsFix = []string{"", "WIN1252", "WIN1250", "ISO8859_1", "ISO8859_15", "DOS850", "DOS437", "UTF8"}

// Diagnosticar: POST /api/migracao/projetos/{id}/diagnosticar {agente_id, validar_paginas}
func (h *Handler) Diagnosticar(w http.ResponseWriter, r *http.Request) {
	var req pedidoUpgrade
	if !entrada.LerJSON(w, r, &req) {
		return
	}
	p, ok := h.projetoUpgrade(r.Context(), w, r.PathValue("id"))
	if !ok {
		return
	}
	agente, ok := h.agenteDoPedido(r.Context(), w, p, req.AgenteID)
	if !ok {
		return
	}
	esp, err := h.especUpgrade(r.Context(), p, novoID("diag"), "", req.ValidarPaginas)
	if err != nil {
		h.erro(w, err)
		return
	}
	h.despacharTarefa(w, r, up.TarefaDiagnostico, esp, agente, store.ExecucaoMigracao{ProjetoID: p.ID, Tipo: "diagnosticar", Execucao: esp.Execucao})
}

// Atualizar: POST /api/migracao/projetos/{id}/atualizar {agente_id, charset_fix?}
// Backup → restore no FB5 → validação. Só com diagnóstico recente sem bloqueio.
func (h *Handler) Atualizar(w http.ResponseWriter, r *http.Request) {
	var req pedidoUpgrade
	if !entrada.LerJSON(w, r, &req) {
		return
	}
	cs, err := validacao.Escolha("charset_fix", req.CharsetFix, charsetsFix...)
	if err != nil {
		entrada.ErroValidacao(w, err)
		return
	}
	ctx := r.Context()
	p, ok := h.projetoUpgrade(ctx, w, r.PathValue("id"))
	if !ok {
		return
	}
	if p.DestinoID == nil {
		http.Error(w, "o projeto precisa de um destino: a conexão com o servidor Firebird 5 (o \"banco\" é o arquivo novo)", http.StatusConflict)
		return
	}
	sit, _, err := h.situacaoUpgrade(ctx, p.ID)
	if err != nil {
		http.Error(w, "erro ao ler o histórico", http.StatusInternalServerError)
		return
	}
	if sit.ocupado {
		http.Error(w, "já há uma tarefa em andamento neste projeto", http.StatusConflict)
		return
	}
	if sit.upgrade != nil && sit.upgrade.Tarefa.Estado == "sucesso" && !sit.descartado[sit.upgrade.Execucao] {
		http.Error(w, "este banco já foi atualizado; descarte o banco novo antes de repetir", http.StatusConflict)
		return
	}
	if _, err := sit.liberado(time.Now()); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	agente, ok := h.agenteDoPedido(ctx, w, p, req.AgenteID)
	if !ok {
		return
	}
	esp, err := h.especUpgrade(ctx, p, novoID("up"), cs, false)
	if err != nil {
		h.erro(w, err)
		return
	}
	if esp.Diretorio == "" {
		http.Error(w, "informe a opção diretorio_backup na conexão do Firebird 5 (pasta que os dois servidores enxergam)", http.StatusConflict)
		return
	}
	h.despacharTarefa(w, r, up.TarefaUpgrade, esp, agente, store.ExecucaoMigracao{ProjetoID: p.ID, Tipo: "atualizar", Execucao: esp.Execucao})
}

// DescartarUpgrade: POST /api/migracao/projetos/{id}/descartar {execucao_id}
// Tira do ar o banco NOVO de um upgrade (o original nunca foi tocado).
func (h *Handler) DescartarUpgrade(w http.ResponseWriter, r *http.Request) {
	var req pedidoUpgrade
	if !entrada.LerJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	p, ok := h.projetoUpgrade(ctx, w, r.PathValue("id"))
	if !ok {
		return
	}
	alvo, err := h.st.ExecucaoPorTarefa(ctx, req.ExecucaoID)
	if err != nil || alvo.ProjetoID != p.ID || alvo.Tipo != "atualizar" {
		entrada.ResponderJSON(w, http.StatusUnprocessableEntity, validacao.Erro{Campo: "execucao_id", Mensagem: "upgrade não encontrado neste projeto"})
		return
	}
	sit, _, err := h.situacaoUpgrade(ctx, p.ID)
	if err != nil {
		http.Error(w, "erro ao ler o histórico", http.StatusInternalServerError)
		return
	}
	switch {
	case sit.ocupado:
		http.Error(w, "espere a tarefa em andamento terminar", http.StatusConflict)
		return
	case sit.descartado[alvo.Execucao]:
		http.Error(w, "este banco novo já foi descartado", http.StatusConflict)
		return
	case slices.Contains(estadosAbertos, alvo.Tarefa.Estado):
		http.Error(w, "o upgrade ainda está rodando", http.StatusConflict)
		return
	}
	esp, err := h.especUpgrade(ctx, p, alvo.Execucao, "", false)
	if err != nil {
		h.erro(w, err)
		return
	}
	agente := req.AgenteID
	if agente == "" {
		agente = alvo.Tarefa.AgenteID
	}
	h.despacharTarefa(w, r, up.TarefaDescartar, esp, agente,
		store.ExecucaoMigracao{ProjetoID: p.ID, Tipo: "descartar", Execucao: alvo.Execucao, RelacionadaID: &alvo.TarefaID})
}

// SituacaoUpgrade: GET /api/migracao/projetos/{id}/upgrade → histórico + liberação.
func (h *Handler) SituacaoUpgrade(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := h.projetoUpgrade(ctx, w, r.PathValue("id"))
	if !ok {
		return
	}
	sit, execs, err := h.situacaoUpgrade(ctx, p.ID)
	if err != nil {
		http.Error(w, "erro ao ler o histórico", http.StatusInternalServerError)
		return
	}
	resp := map[string]any{"execucoes": execs, "ocupado": sit.ocupado, "descartados": sit.descartado, "tem_destino": p.DestinoID != nil}
	d, errLib := sit.liberado(time.Now())
	if d != nil {
		resp["diagnostico"] = d
	}
	resp["pode_atualizar"] = errLib == nil && !sit.ocupado && p.DestinoID != nil &&
		(sit.upgrade == nil || sit.upgrade.Tarefa.Estado != "sucesso" || sit.descartado[sit.upgrade.Execucao])
	if errLib != nil {
		resp["motivo"] = errLib.Error()
	}
	if sit.upgrade != nil && !sit.descartado[sit.upgrade.Execucao] && !slices.Contains(estadosAbertos, sit.upgrade.Tarefa.Estado) {
		resp["pode_descartar"] = sit.upgrade.TarefaID
	}
	entrada.ResponderJSON(w, http.StatusOK, resp)
}

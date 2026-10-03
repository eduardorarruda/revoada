package migracao

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
	"github.com/eduardorarruda/revoada/core/migracao/modelo"
	"github.com/eduardorarruda/revoada/core/migracao/plano"
	up "github.com/eduardorarruda/revoada/core/migracao/upgrade"
	"github.com/eduardorarruda/revoada/core/seguranca/cofre"
	"github.com/eduardorarruda/revoada/core/seguranca/selo"
	"github.com/eduardorarruda/revoada/core/validacao"
	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/entrada"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// validadeSeloTarefa: a senha selada vale só para começar a tarefa (o agente a abre
// ao receber e a mantém em memória enquanto a tarefa roda).
const validadeSeloTarefa = 15 * time.Minute

var estadosAbertos = []string{"na_fila", "enviada", "executando", "pausada"}

// ---------------------------------------------------------------- especificação

func banco(c store.ConexaoBanco) plano.Banco {
	return plano.Banco{Motor: c.Motor, Endereco: c.Endereco, Banco: c.Banco, Usuario: c.Usuario, Opcoes: c.Opcoes}
}

func (h *Handler) fotoEsquema(ctx context.Context, id *string) (esquema.Esquema, error) {
	var e esquema.Esquema
	if id == nil {
		return e, errors.New("a versão não guardou a foto do schema; capture e salve de novo")
	}
	reg, err := h.st.EsquemaPorID(ctx, *id)
	if err != nil {
		return e, err
	}
	return e, json.Unmarshal(reg.Conteudo, &e)
}

// especificacao monta a tarefa a partir do que está NO PAINEL (projeto, versão,
// conexões) — nada vem do pedido do navegador além de qual agente usar.
func (h *Handler) especificacao(ctx context.Context, p store.ProjetoMigracao, v store.VersaoMapeamento, execucao string, lote int) (plano.Especificacao, error) {
	var esp plano.Especificacao
	if p.DestinoID == nil {
		return esp, errors.New("projeto de upgrade de versão: use o diagnóstico de upgrade (Etapa 7)")
	}
	var m modelo.Mapeamento
	if err := json.Unmarshal(v.Conteudo, &m); err != nil {
		return esp, err
	}
	if m.Hash() != v.Hash {
		return esp, errors.New("o conteúdo da versão não confere com o hash gravado")
	}
	eo, err := h.fotoEsquema(ctx, v.EsquemaOrigemID)
	if err != nil {
		return esp, err
	}
	ed, err := h.fotoEsquema(ctx, v.EsquemaDestinoID)
	if err != nil {
		return esp, err
	}
	co, err := h.st.ConexaoPorID(ctx, p.OrigemID)
	if err != nil {
		return esp, err
	}
	cd, err := h.st.ConexaoPorID(ctx, *p.DestinoID)
	if err != nil {
		return esp, err
	}
	return plano.Especificacao{Execucao: execucao, ProjetoID: p.ID, Versao: v.Versao, HashMapeamento: v.Hash,
		Origem: banco(co), Destino: banco(cd), Mapeamento: m, EsquemaOrigem: eo, EsquemaDestino: ed, Lote: lote}, nil
}

// credenciaisDaTarefa diz quais senhas cada tipo de tarefa recebe (menor privilégio:
// reverter e descartar só tocam o destino; o diagnóstico só lê a origem, e o destino
// só entra para o ensaio).
func credenciaisDaTarefa(tipo string, p store.ProjetoMigracao) [][2]string {
	var out [][2]string
	origem := [2]string{"origem", p.OrigemID}
	if tipo != plano.TarefaReverter && tipo != up.TarefaDescartar {
		out = append(out, origem)
	}
	if p.DestinoID != nil {
		out = append(out, [2]string{"destino", *p.DestinoID})
	}
	return out
}

// Selar é a Seladora do canal para as tarefas de migração e de upgrade: decifra a
// senha das conexões DO PROJETO (no cofre) e sela para a chave X25519 do agente que
// vai rodar, com o id da tarefa no contexto.
func (h *Handler) Selar(ctx context.Context, ag store.Agente, t store.Tarefa) ([]*agentev1.CredencialSelada, error) {
	if !strings.HasPrefix(t.Tipo, "migracao.") && !strings.HasPrefix(t.Tipo, "firebird.") {
		return nil, nil
	}
	var esp struct {
		ProjetoID string `json:"projeto_id"`
	}
	if err := json.Unmarshal(t.Especificacao, &esp); err != nil {
		return nil, err
	}
	p, err := h.st.ProjetoPorID(ctx, esp.ProjetoID)
	if err != nil {
		return nil, err
	}
	conexoes := credenciaisDaTarefa(t.Tipo, p)
	out := make([]*agentev1.CredencialSelada, 0, len(conexoes))
	for _, x := range conexoes {
		c, err := h.st.ConexaoPorID(ctx, x[1])
		if err != nil {
			return nil, err
		}
		senha, err := h.abrirSenha(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("abrindo a senha de %s: %w", c.Nome, err)
		}
		sl, err := selo.Selar(ag.ChaveSelo, senha, "tarefa:"+t.ID+":"+x[0], validadeSeloTarefa, time.Now())
		cofre.Limpar(senha)
		if err != nil {
			return nil, err
		}
		out = append(out, &agentev1.CredencialSelada{Nome: x[0], Efemera: sl.Efemera, Nonce: sl.Nonce, Cifrado: sl.Cifrado, ExpiraEm: sl.ExpiraEm})
	}
	return out, nil
}

// ---------------------------------------------------------------- estado do projeto

// situacao resume as execuções do projeto para as regras de liberação.
type situacao struct {
	execs       []store.ExecucaoMigracao
	ocupado     bool
	incompletas map[string]store.ExecucaoMigracao // execucao → última tarefa executar (falha/cancelada, não revertida)
	revertidas  map[string]bool
	concluidas  map[string]bool
}

func (h *Handler) situacao(ctx context.Context, projetoID string) (situacao, error) {
	execs, err := h.st.ExecucoesDoProjeto(ctx, projetoID, 200)
	if err != nil {
		return situacao{}, err
	}
	s := situacao{execs: execs, incompletas: map[string]store.ExecucaoMigracao{}, revertidas: map[string]bool{}, concluidas: map[string]bool{}}
	ultima := map[string]store.ExecucaoMigracao{}
	for _, e := range execs { // mais recentes primeiro
		aberta := slices.Contains(estadosAbertos, e.Tarefa.Estado)
		if aberta && e.Tipo != "simular" {
			s.ocupado = true
		}
		switch e.Tipo {
		case "reverter":
			if e.Tarefa.Estado == "sucesso" {
				s.revertidas[e.Execucao] = true
			}
		case "executar":
			if _, ok := ultima[e.Execucao]; !ok {
				ultima[e.Execucao] = e
			}
		}
	}
	for ex, e := range ultima {
		switch {
		case s.revertidas[ex]:
		case e.Tarefa.Estado == "sucesso":
			s.concluidas[ex] = true
		case e.Tarefa.Estado == "falha" || e.Tarefa.Estado == "cancelada":
			s.incompletas[ex] = e
		}
	}
	return s, nil
}

// simulacaoAprovada procura uma simulação SEM bloqueantes desta versão (mesmo hash).
func (s situacao) simulacaoAprovada(v store.VersaoMapeamento) (*plano.RelatorioSimulacao, bool) {
	for _, e := range s.execs {
		if e.Tipo != "simular" || e.Versao != v.Versao || e.HashMapeamento != v.Hash || e.Tarefa.Estado != "sucesso" {
			continue
		}
		var rel plano.RelatorioSimulacao
		if json.Unmarshal(e.Tarefa.Resumo, &rel) == nil && rel.HashMapeamento == v.Hash {
			return &rel, rel.Bloqueantes == 0
		}
	}
	return nil, false
}

// ---------------------------------------------------------------- handlers

type pedidoTarefa struct {
	AgenteID   string `json:"agente_id"`
	Versao     int    `json:"versao"`
	Lote       int    `json:"lote"`
	RetomarDe  string `json:"retomar_de"`
	ExecucaoID string `json:"execucao_id"`
}

func (h *Handler) agenteDoPedido(ctx context.Context, w http.ResponseWriter, p store.ProjetoMigracao, pedido string) (string, bool) {
	if pedido != "" {
		return pedido, true
	}
	if c, err := h.st.ConexaoPorID(ctx, p.OrigemID); err == nil && c.AgentePreferidoID != nil {
		return *c.AgentePreferidoID, true
	}
	entrada.ResponderJSON(w, http.StatusUnprocessableEntity, validacao.Erro{Campo: "agente_id", Mensagem: "escolha o agente que vai rodar a tarefa"})
	return "", false
}

func (h *Handler) despachar(w http.ResponseWriter, r *http.Request, tipo string, esp plano.Especificacao, agente string, relacionada *string) {
	h.despacharTarefa(w, r, tipo, esp, agente, store.ExecucaoMigracao{ProjetoID: esp.ProjetoID, Versao: esp.Versao,
		Tipo: strings.TrimPrefix(tipo, "migracao."), Execucao: esp.Execucao, HashMapeamento: esp.HashMapeamento, RelacionadaID: relacionada})
}

// despacharTarefa grava a tarefa no canal e a liga ao projeto (meta sem TarefaID).
func (h *Handler) despacharTarefa(w http.ResponseWriter, r *http.Request, tipo string, esp any, agente string, meta store.ExecucaoMigracao) {
	e, err := h.registrarTarefa(r.Context(), tipo, esp, agente, quem(r), "ui", meta)
	if err != nil {
		h.responder(w, err)
		return
	}
	entrada.ResponderJSON(w, http.StatusCreated, e)
}

func lote(n int) int {
	if n <= 0 {
		return plano.LoteInicial
	}
	return min(max(n, 100), 50_000)
}

// Simular: POST /api/migracao/projetos/{id}/simular {agente_id, versao?, lote?}
// Dry-run: lê tudo, confere tudo, não grava nada. Vale para versão válida ou aprovada.
func (h *Handler) Simular(w http.ResponseWriter, r *http.Request) {
	var req pedidoTarefa
	if !entrada.LerJSON(w, r, &req) {
		return
	}
	e, err := h.IniciarSimulacao(r.Context(), r.PathValue("id"), req.Versao, req.AgenteID, quem(r), "ui", req.Lote)
	if err != nil {
		h.responder(w, err)
		return
	}
	entrada.ResponderJSON(w, http.StatusCreated, e)
}

// Executar: POST /api/migracao/projetos/{id}/executar {agente_id, lote?, retomar_de?}
// Só a versão APROVADA, só depois de uma simulação sem bloqueantes dessa mesma versão,
// e só se não houver outra execução rodando ou uma incompleta esperando decisão.
func (h *Handler) Executar(w http.ResponseWriter, r *http.Request) {
	var req pedidoTarefa
	if !entrada.LerJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	p, err := h.st.ProjetoPorID(ctx, r.PathValue("id"))
	if err != nil {
		h.erro(w, err)
		return
	}
	v, err := h.st.UltimaVersaoAprovada(ctx, p.ID)
	if err != nil {
		http.Error(w, "aprove uma versão do mapeamento antes de executar", http.StatusConflict)
		return
	}
	sit, err := h.situacao(ctx, p.ID)
	if err != nil {
		http.Error(w, "erro ao ler as execuções", http.StatusInternalServerError)
		return
	}
	if sit.ocupado {
		http.Error(w, "já há uma execução ou reversão em andamento neste projeto", http.StatusConflict)
		return
	}
	if rel, ok := sit.simulacaoAprovada(v); !ok {
		msg := fmt.Sprintf("rode a simulação da versão %d antes (o dry-run é obrigatório)", v.Versao)
		if rel != nil {
			msg = fmt.Sprintf("a última simulação da versão %d achou %d linhas que o destino recusaria; corrija o mapeamento", v.Versao, rel.Bloqueantes)
		}
		http.Error(w, msg, http.StatusConflict)
		return
	}
	execucao, relacionada := novoID("ex"), (*string)(nil)
	if req.RetomarDe != "" {
		ant, err := h.st.ExecucaoPorTarefa(ctx, req.RetomarDe)
		if err != nil || ant.ProjetoID != p.ID || ant.Tipo != "executar" {
			entrada.ResponderJSON(w, http.StatusUnprocessableEntity, validacao.Erro{Campo: "retomar_de", Mensagem: "execução não encontrada neste projeto"})
			return
		}
		if _, ok := sit.incompletas[ant.Execucao]; !ok {
			http.Error(w, "só dá para retomar uma execução que falhou ou foi cancelada e não foi revertida", http.StatusConflict)
			return
		}
		if ant.Versao != v.Versao {
			http.Error(w, "a execução foi feita com outra versão do mapeamento; reverta-a antes", http.StatusConflict)
			return
		}
		execucao, relacionada = ant.Execucao, &ant.TarefaID
	} else if len(sit.incompletas) > 0 {
		http.Error(w, "há uma execução incompleta neste projeto: retome-a ou reverta-a antes de começar outra", http.StatusConflict)
		return
	} else if len(sit.concluidas) > 0 {
		http.Error(w, "este projeto já foi migrado; reverta a execução concluída antes de rodar de novo", http.StatusConflict)
		return
	}
	agente, ok := h.agenteDoPedido(ctx, w, p, req.AgenteID)
	if !ok {
		return
	}
	esp, err := h.especificacao(ctx, p, v, execucao, lote(req.Lote))
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	h.despachar(w, r, plano.TarefaExecutar, esp, agente, relacionada)
}

// Reverter: POST /api/migracao/projetos/{id}/reverter {execucao_id, agente_id?}
// Desfaz pelo manifesto (numa transação, no destino). Serve para execução concluída,
// que falhou ou que foi cancelada — desde que ainda não revertida.
func (h *Handler) Reverter(w http.ResponseWriter, r *http.Request) {
	var req pedidoTarefa
	if !entrada.LerJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	p, err := h.st.ProjetoPorID(ctx, r.PathValue("id"))
	if err != nil {
		h.erro(w, err)
		return
	}
	alvo, err := h.st.ExecucaoPorTarefa(ctx, req.ExecucaoID)
	if err != nil || alvo.ProjetoID != p.ID || alvo.Tipo != "executar" {
		entrada.ResponderJSON(w, http.StatusUnprocessableEntity, validacao.Erro{Campo: "execucao_id", Mensagem: "execução não encontrada neste projeto"})
		return
	}
	sit, err := h.situacao(ctx, p.ID)
	if err != nil {
		http.Error(w, "erro ao ler as execuções", http.StatusInternalServerError)
		return
	}
	switch {
	case sit.ocupado:
		http.Error(w, "espere a tarefa em andamento terminar (ou cancele-a) antes de reverter", http.StatusConflict)
		return
	case sit.revertidas[alvo.Execucao]:
		http.Error(w, "esta execução já foi revertida", http.StatusConflict)
		return
	}
	v, err := h.st.VersaoMapeamentoN(ctx, p.ID, alvo.Versao)
	if err != nil {
		h.erro(w, err)
		return
	}
	agente := req.AgenteID
	if agente == "" {
		agente = alvo.Tarefa.AgenteID
	}
	esp, err := h.especificacao(ctx, p, v, alvo.Execucao, plano.LoteInicial)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	h.despachar(w, r, plano.TarefaReverter, esp, agente, &alvo.TarefaID)
}

// Verificar: POST /api/migracao/projetos/{id}/verificar {execucao_id, agente_id?}
// Compara de novo origem e destino, linha a linha e coluna a coluna (só leitura nos
// dois lados). Mesma permissão da simulação; mesma especificação da execução.
func (h *Handler) Verificar(w http.ResponseWriter, r *http.Request) {
	var req pedidoTarefa
	if !entrada.LerJSON(w, r, &req) {
		return
	}
	e, err := h.IniciarVerificacao(r.Context(), r.PathValue("id"), req.ExecucaoID, req.AgenteID, quem(r), "ui")
	if err != nil {
		h.responder(w, err)
		return
	}
	entrada.ResponderJSON(w, http.StatusCreated, e)
}

// IniciarVerificacao dispara migracao.verificar para uma execução CONCLUÍDA e não
// revertida (antes da troca, a própria execução já confere o conteúdo). As senhas
// seguem o caminho de sempre: Selar → credenciaisDaTarefa (origem e destino).
func (h *Handler) IniciarVerificacao(ctx context.Context, projetoID, execucaoTarefa, agente, autor, origem string) (store.ExecucaoMigracao, error) {
	p, err := h.st.ProjetoPorID(ctx, projetoID)
	if err != nil {
		return store.ExecucaoMigracao{}, err
	}
	alvo, err := h.st.ExecucaoPorTarefa(ctx, execucaoTarefa)
	if err != nil || alvo.ProjetoID != p.ID || alvo.Tipo != "executar" {
		return store.ExecucaoMigracao{}, &validacao.Erro{Campo: "execucao_id", Mensagem: "execução não encontrada neste projeto"}
	}
	sit, err := h.situacao(ctx, p.ID)
	if err != nil {
		return store.ExecucaoMigracao{}, err
	}
	switch {
	case sit.ocupado:
		return store.ExecucaoMigracao{}, regra("espere a tarefa em andamento terminar antes de verificar")
	case sit.revertidas[alvo.Execucao]:
		return store.ExecucaoMigracao{}, regra("esta execução foi revertida: o destino voltou ao que era e não há o que verificar")
	case !sit.concluidas[alvo.Execucao]:
		return store.ExecucaoMigracao{}, regra("só dá para verificar uma execução concluída (antes da troca, a execução já confere o conteúdo sozinha)")
	}
	v, err := h.st.VersaoMapeamentoN(ctx, p.ID, alvo.Versao)
	if err != nil {
		return store.ExecucaoMigracao{}, err
	}
	if agente == "" {
		agente = alvo.Tarefa.AgenteID
	}
	esp, err := h.especificacao(ctx, p, v, alvo.Execucao, plano.LoteInicial)
	if err != nil {
		return store.ExecucaoMigracao{}, regra(err.Error())
	}
	return h.registrarTarefa(ctx, plano.TarefaVerificar, esp, agente, autor, origem, store.ExecucaoMigracao{
		ProjetoID: p.ID, Versao: v.Versao, Tipo: "verificar", Execucao: esp.Execucao, HashMapeamento: esp.HashMapeamento,
		RelacionadaID: &alvo.TarefaID})
}

// Execucoes: GET /api/migracao/projetos/{id}/execucoes → histórico + o que está liberado.
func (h *Handler) Execucoes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := h.st.ProjetoPorID(ctx, r.PathValue("id"))
	if err != nil {
		h.erro(w, err)
		return
	}
	sit, err := h.situacao(ctx, p.ID)
	if err != nil {
		http.Error(w, "erro ao ler as execuções", http.StatusInternalServerError)
		return
	}
	if c, ok := auth.ClaimsFrom(ctx); !ok || !auth.Pode(c.Role, auth.PermExecutarMigracao) {
		// as chaves (amostras, divergências, erros) são valores da origem — CPF,
		// e-mail…: como nos checkpoints, só quem pode executar migração as vê
		for i := range sit.execs {
			sit.execs[i] = semChaves(sit.execs[i])
		}
	}
	resp := map[string]any{"execucoes": sit.execs, "ocupado": sit.ocupado, "pode_executar": false,
		"incompletas": mapaChaves(sit.incompletas), "revertidas": sit.revertidas, "concluidas": sit.concluidas}
	if v, err := h.st.UltimaVersaoAprovada(ctx, p.ID); err == nil {
		rel, ok := sit.simulacaoAprovada(v)
		resp["versao_aprovada"] = v.Versao
		resp["simulacao_ok"] = ok
		resp["pode_executar"] = ok && !sit.ocupado && len(sit.incompletas) == 0 && len(sit.concluidas) == 0
		if rel != nil {
			resp["simulacao"] = rel
		}
	}
	entrada.ResponderJSON(w, http.StatusOK, resp)
}

// semChaves tira de uma execução o que é valor de linha (para quem não executa).
func semChaves(e store.ExecucaoMigracao) store.ExecucaoMigracao {
	e.Tarefa.Erro = plano.OcultarChaveErro(e.Tarefa.Erro, "(visível só para quem pode executar migração)")
	if len(e.Tarefa.Resumo) > 0 {
		e.Tarefa.Resumo = plano.OcultarChaves(e.Tipo, e.Tarefa.Resumo)
	}
	return e
}

func mapaChaves(m map[string]store.ExecucaoMigracao) map[string]string {
	out := map[string]string{}
	for k, e := range m {
		out[k] = e.TarefaID
	}
	return out
}

// Checkpoints: GET /api/migracao/tarefas/{id}/checkpoints
// A última chave é um valor da tabela de origem (pode ser CPF, e-mail…): só quem
// pode executar migração a vê; os demais recebem contagens.
func (h *Handler) Checkpoints(w http.ResponseWriter, r *http.Request) {
	if _, err := h.st.ExecucaoPorTarefa(r.Context(), r.PathValue("id")); err != nil {
		h.erro(w, err) // só tarefas de migração
		return
	}
	cs, err := h.st.CheckpointsDaTarefa(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, "erro ao ler os checkpoints", http.StatusInternalServerError)
		return
	}
	if c, ok := auth.ClaimsFrom(r.Context()); !ok || !auth.Pode(c.Role, auth.PermExecutarMigracao) {
		for i := range cs {
			cs[i].UltimaChave = ""
		}
	}
	entrada.ResponderJSON(w, http.StatusOK, cs)
}

// Package deploys é a API de deploy multi-SO (ARQUITETURA §10): a GitHub Action
// (ou uma pessoa, na tela) pede "aplicação X na versão Y no servidor Z"; o agente
// daquele servidor executa com a receita LOCAL dele (canal.deploy) e reverte sozinho
// se o health check falhar. Cada deploy vira anotação nos gráficos.
package deploys

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/eduardorarruda/revoada/core/validacao"
	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/canal"
	"github.com/eduardorarruda/revoada/server/internal/entrada"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// Tarefa é o tipo de tarefa no agente.
const Tarefa = "deploy.aplicar"

var (
	nomeValido   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	versaoValida = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)
	commitValido = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
)

// Handler da API de deploy.
type Handler struct {
	canal *canal.Servico
}

func NovoHandler(c *canal.Servico) *Handler { return &Handler{canal: c} }

// Pedido é o corpo de POST /api/deploys.
type Pedido struct {
	Agente    string `json:"agente"` // id do agente ou hostname do servidor
	Aplicacao string `json:"aplicacao"`
	Versao    string `json:"versao"`
	Ambiente  string `json:"ambiente,omitempty"`
	Commit    string `json:"commit,omitempty"`
}

func validar(p Pedido) error {
	switch {
	case !nomeValido.MatchString(p.Aplicacao):
		return &validacao.Erro{Campo: "aplicacao", Mensagem: "nome de aplicação inválido"}
	case !versaoValida.MatchString(p.Versao):
		return &validacao.Erro{Campo: "versao", Mensagem: "versão inválida (letras, números, . _ + -)"}
	case p.Ambiente != "" && !nomeValido.MatchString(p.Ambiente):
		return &validacao.Erro{Campo: "ambiente", Mensagem: "ambiente inválido"}
	case p.Commit != "" && !commitValido.MatchString(p.Commit):
		return &validacao.Erro{Campo: "commit", Mensagem: "commit deve ser o hash (7 a 64 caracteres hexadecimais)"}
	case strings.TrimSpace(p.Agente) == "":
		return &validacao.Erro{Campo: "agente", Mensagem: "informe o agente (id ou hostname do servidor)"}
	}
	return nil
}

// achar resolve o agente por id ou hostname (único, não revogado).
func (h *Handler) achar(ctx context.Context, quem string) (store.Agente, error) {
	ags, err := h.canal.Agentes(ctx)
	if err != nil {
		return store.Agente{}, err
	}
	var achados []store.Agente
	for _, a := range ags {
		if !a.Revogado && (a.ID == quem || strings.EqualFold(a.Hostname, quem)) {
			achados = append(achados, a)
		}
	}
	switch len(achados) {
	case 0:
		return store.Agente{}, &validacao.Erro{Campo: "agente", Mensagem: "nenhum agente ativo com esse id ou hostname"}
	case 1:
		if !slices.Contains(achados[0].Capacidades, Tarefa) {
			return store.Agente{}, &validacao.Erro{Campo: "agente", Mensagem: "este agente não liberou deploy (canal.tarefas_permitidas + canal.deploy no agent.yaml)"}
		}
		return achados[0], nil
	}
	return store.Agente{}, &validacao.Erro{Campo: "agente", Mensagem: "mais de um agente com esse hostname: use o id"}
}

func (h *Handler) criar(w http.ResponseWriter, r *http.Request, origem, autor string) {
	var p Pedido
	if !entrada.LerJSON(w, r, &p) {
		return
	}
	if err := validar(p); err != nil {
		entrada.ErroValidacao(w, err)
		return
	}
	ag, err := h.achar(r.Context(), p.Agente)
	if err != nil {
		if !entrada.ErroValidacao(w, err) {
			http.Error(w, "erro ao procurar o agente", http.StatusInternalServerError)
		}
		return
	}
	esp, _ := json.Marshal(map[string]string{"aplicacao": p.Aplicacao, "versao": p.Versao, "ambiente": p.Ambiente, "commit": p.Commit})
	t, err := h.canal.Despachar(r.Context(), canal.NovaTarefa{Tipo: Tarefa, AgenteID: ag.ID, Especificacao: esp, IniciadaPor: autor, Origem: origem})
	if err != nil {
		if errors.Is(err, canal.ErrAgenteRevogado) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, "erro ao criar o deploy", http.StatusInternalServerError)
		return
	}
	entrada.ResponderJSON(w, http.StatusCreated, t)
}

// CriarPelaAction: POST /api/deploys com o token de deploy (GitHub Action).
func (h *Handler) CriarPelaAction(w http.ResponseWriter, r *http.Request) {
	autor := "github-action"
	if repo := r.Header.Get("X-Revoada-Repositorio"); nomeRepo.MatchString(repo) {
		autor += ":" + repo
	}
	h.criar(w, r, "github_action", autor)
}

var nomeRepo = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// CriarPelaTela: POST /api/deploys/manual (pessoa com rodar_deploy + 2FA + reauth).
func (h *Handler) CriarPelaTela(w http.ResponseWriter, r *http.Request) {
	autor := ""
	if c, ok := auth.ClaimsFrom(r.Context()); ok {
		autor = c.Name
	}
	h.criar(w, r, "ui", autor)
}

// Listar: GET /api/deploys?limite=50
func (h *Handler) Listar(w http.ResponseWriter, r *http.Request) {
	lim, _ := strconv.Atoi(r.URL.Query().Get("limite"))
	ts, err := h.canal.ListarTarefas(r.Context(), store.FiltroTarefas{Tipo: Tarefa, Limite: lim})
	if err != nil {
		http.Error(w, "erro ao listar", http.StatusInternalServerError)
		return
	}
	entrada.ResponderJSON(w, http.StatusOK, ts)
}

// Status: GET /api/deploys/{id} — a Action consulta até terminar.
func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	t, err := h.canal.Tarefa(r.Context(), r.PathValue("id"))
	if err != nil || t.Tipo != Tarefa {
		http.Error(w, "deploy não encontrado", http.StatusNotFound)
		return
	}
	entrada.ResponderJSON(w, http.StatusOK, t)
}

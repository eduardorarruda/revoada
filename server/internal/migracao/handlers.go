// Package migracao é a API do módulo de migração (ARQUITETURA §9): conexões de banco (senha
// no cofre), captura de schema pelo agente, projetos e versões do mapeamento.
package migracao

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
	"github.com/eduardorarruda/revoada/core/migracao/modelo"
	"github.com/eduardorarruda/revoada/core/migracao/sugestao"
	"github.com/eduardorarruda/revoada/core/seguranca/cofre"
	"github.com/eduardorarruda/revoada/core/validacao"
	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/canal"
	"github.com/eduardorarruda/revoada/server/internal/entrada"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// Handler da API de migração.
type Handler struct {
	st    *store.Store
	cofre *cofre.Cofre
	canal *canal.Servico
}

func NovoHandler(st *store.Store, cf *cofre.Cofre, c *canal.Servico) *Handler {
	return &Handler{st: st, cofre: cf, canal: c}
}

var opcoesPermitidas = []string{"charset", "sslmode", "schema", "wire_crypt", "diretorio_backup"}

func novoID(p string) string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return p + "_" + hex.EncodeToString(b)
}

func quem(r *http.Request) string {
	if c, ok := auth.ClaimsFrom(r.Context()); ok {
		return c.Name
	}
	return ""
}

func contextoConexao(id string) []byte { return []byte("conexao:" + id) }

// ---------------------------------------------------------------- conexões

// CriarConexao: POST /api/migracao/conexoes (só administrador; ação crítica).
func (h *Handler) CriarConexao(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Nome              string            `json:"nome"`
		Motor             string            `json:"motor"`
		Endereco          string            `json:"endereco"`
		Banco             string            `json:"banco"`
		Usuario           string            `json:"usuario"`
		Senha             string            `json:"senha"`
		Opcoes            map[string]string `json:"opcoes"`
		AgentePreferidoID string            `json:"agente_preferido_id"`
	}
	if !entrada.LerJSON(w, r, &req) {
		return
	}
	c, err := validarConexao(req.Nome, req.Motor, req.Endereco, req.Banco, req.Usuario, req.Opcoes)
	if err != nil {
		if !entrada.ErroValidacao(w, err) {
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		return
	}
	if req.Senha == "" || len(req.Senha) > 512 {
		entrada.ResponderJSON(w, http.StatusUnprocessableEntity, validacao.Erro{Campo: "senha", Mensagem: "obrigatória (até 512 caracteres)"})
		return
	}
	c.ID, c.CriadaPor = novoID("cx"), quem(r)
	if req.AgentePreferidoID != "" {
		c.AgentePreferidoID = &req.AgentePreferidoID
	}
	seg, err := h.cofre.Cifrar(r.Context(), []byte(req.Senha), contextoConexao(c.ID))
	if err != nil {
		http.Error(w, "erro ao proteger a senha", http.StatusInternalServerError)
		return
	}
	if c.Credencial, err = json.Marshal(seg); err != nil {
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	if err := h.st.CriarConexao(r.Context(), c); err != nil {
		http.Error(w, "erro ao gravar a conexão", http.StatusInternalServerError)
		return
	}
	entrada.ResponderJSON(w, http.StatusCreated, c)
}

func validarConexao(nome, motor, endereco, banco, usuario string, opcoes map[string]string) (store.ConexaoBanco, error) {
	var c store.ConexaoBanco
	var err error
	if c.Nome, err = validacao.Texto("nome", nome, true, 120); err != nil {
		return c, err
	}
	if c.Motor, err = validacao.Escolha("motor", motor, "firebird", "postgres"); err != nil {
		return c, err
	}
	porta := 3050
	if c.Motor == "postgres" {
		porta = 5432
	}
	if c.Endereco, err = validacao.HostPorta("endereco", endereco, porta); err != nil {
		return c, err
	}
	if c.Banco, err = validacao.CaminhoBanco("banco", banco); err != nil {
		return c, err
	}
	if c.Usuario, err = validacao.Usuario("usuario", usuario); err != nil {
		return c, err
	}
	if c.Opcoes, err = validacao.Opcoes("opcoes", opcoes, opcoesPermitidas...); err != nil {
		return c, err
	}
	if d, ok := c.Opcoes["diretorio_backup"]; ok {
		// vai para o gbak do servidor: mesma regra de caminho do banco
		if c.Opcoes["diretorio_backup"], err = validacao.CaminhoBanco("opcoes.diretorio_backup", d); err != nil {
			return c, err
		}
	}
	if w, ok := c.Opcoes["wire_crypt"]; ok {
		if c.Opcoes["wire_crypt"], err = validacao.Escolha("opcoes.wire_crypt", w, "true", "false"); err != nil {
			return c, err
		}
	}
	return c, nil
}

// ListarConexoes: GET /api/migracao/conexoes (a senha nunca sai).
func (h *Handler) ListarConexoes(w http.ResponseWriter, r *http.Request) {
	cs, err := h.st.ListarConexoes(r.Context())
	if err != nil {
		http.Error(w, "erro ao listar", http.StatusInternalServerError)
		return
	}
	entrada.ResponderJSON(w, http.StatusOK, cs)
}

// ApagarConexao: DELETE /api/migracao/conexoes/{id}
func (h *Handler) ApagarConexao(w http.ResponseWriter, r *http.Request) {
	if err := h.st.ApagarConexao(r.Context(), r.PathValue("id")); err != nil {
		h.erro(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Capturar: POST /api/migracao/conexoes/{id}/capturar {agente_id} — o agente lê a
// estrutura do banco (nunca os dados) com a senha selada só para ele.
func (h *Handler) Capturar(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgenteID string `json:"agente_id"`
	}
	if !entrada.LerJSON(w, r, &req) {
		return
	}
	res, err := h.CapturarEsquema(r.Context(), r.PathValue("id"), req.AgenteID)
	if err != nil {
		h.responder(w, err)
		return
	}
	entrada.ResponderJSON(w, http.StatusCreated, res)
}

func (h *Handler) abrirSenha(ctx context.Context, c store.ConexaoBanco) ([]byte, error) {
	var s cofre.Segredo
	if err := json.Unmarshal(c.Credencial, &s); err != nil {
		return nil, err
	}
	return h.cofre.Decifrar(ctx, s, contextoConexao(c.ID))
}

// UltimoEsquema: GET /api/migracao/conexoes/{id}/esquema
func (h *Handler) UltimoEsquema(w http.ResponseWriter, r *http.Request) {
	e, err := h.st.UltimoEsquema(r.Context(), r.PathValue("id"))
	if err != nil {
		h.erro(w, err)
		return
	}
	entrada.ResponderJSON(w, http.StatusOK, e)
}

// ---------------------------------------------------------------- projetos e mapeamento

// CriarProjeto: POST /api/migracao/projetos
func (h *Handler) CriarProjeto(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Nome      string `json:"nome"`
		Tipo      string `json:"tipo"`
		OrigemID  string `json:"origem_id"`
		DestinoID string `json:"destino_id"`
	}
	if !entrada.LerJSON(w, r, &req) {
		return
	}
	nome, err := validacao.Texto("nome", req.Nome, true, 120)
	if err == nil {
		req.Tipo, err = validacao.Escolha("tipo", req.Tipo, "troca_de_banco", "upgrade_versao")
	}
	if err != nil {
		entrada.ErroValidacao(w, err)
		return
	}
	co, err := h.st.ConexaoPorID(r.Context(), req.OrigemID)
	if err != nil {
		entrada.ResponderJSON(w, http.StatusUnprocessableEntity, validacao.Erro{Campo: "origem_id", Mensagem: "conexão de origem não encontrada"})
		return
	}
	if req.Tipo == "upgrade_versao" && co.Motor != "firebird" {
		entrada.ResponderJSON(w, http.StatusUnprocessableEntity, validacao.Erro{Campo: "origem_id", Mensagem: "upgrade de versão é para banco Firebird"})
		return
	}
	p := store.ProjetoMigracao{ID: novoID("pj"), Nome: nome, Tipo: req.Tipo, OrigemID: req.OrigemID, CriadoPor: quem(r)}
	if req.DestinoID != "" {
		cd, err := h.st.ConexaoPorID(r.Context(), req.DestinoID)
		if err != nil {
			entrada.ResponderJSON(w, http.StatusUnprocessableEntity, validacao.Erro{Campo: "destino_id", Mensagem: "conexão de destino não encontrada"})
			return
		}
		if req.Tipo == "upgrade_versao" && cd.Motor != "firebird" {
			entrada.ResponderJSON(w, http.StatusUnprocessableEntity, validacao.Erro{Campo: "destino_id", Mensagem: "no upgrade, o destino é o servidor Firebird 5"})
			return
		}
		p.DestinoID = &req.DestinoID
	} else if req.Tipo == "troca_de_banco" {
		entrada.ResponderJSON(w, http.StatusUnprocessableEntity, validacao.Erro{Campo: "destino_id", Mensagem: "troca de banco precisa do destino"})
		return
	}
	if err := h.st.CriarProjeto(r.Context(), p); err != nil {
		http.Error(w, "erro ao criar o projeto", http.StatusInternalServerError)
		return
	}
	entrada.ResponderJSON(w, http.StatusCreated, p)
}

// ListarProjetos: GET /api/migracao/projetos
func (h *Handler) ListarProjetos(w http.ResponseWriter, r *http.Request) {
	ps, err := h.st.ListarProjetos(r.Context())
	if err != nil {
		http.Error(w, "erro ao listar", http.StatusInternalServerError)
		return
	}
	entrada.ResponderJSON(w, http.StatusOK, ps)
}

// Projeto: GET /api/migracao/projetos/{id} → projeto + última versão do mapeamento.
func (h *Handler) Projeto(w http.ResponseWriter, r *http.Request) {
	p, err := h.st.ProjetoPorID(r.Context(), r.PathValue("id"))
	if err != nil {
		h.erro(w, err)
		return
	}
	resp := map[string]any{"projeto": p}
	if v, err := h.st.UltimaVersaoMapeamento(r.Context(), p.ID); err == nil {
		resp["mapeamento"] = v
	}
	entrada.ResponderJSON(w, http.StatusOK, resp)
}

// esquemasDoProjeto lê as fotos mais recentes de origem e destino.
func (h *Handler) esquemasDoProjeto(ctx context.Context, p store.ProjetoMigracao) (o, d esquema.Esquema, oid, did *string, err error) {
	eo, err := h.st.UltimoEsquema(ctx, p.OrigemID)
	if err != nil {
		return o, d, nil, nil, errors.New("capture o schema da origem primeiro")
	}
	if err := json.Unmarshal(eo.Conteudo, &o); err != nil {
		return o, d, nil, nil, err
	}
	oid = &eo.ID
	if p.DestinoID != nil {
		ed, err := h.st.UltimoEsquema(ctx, *p.DestinoID)
		if err != nil {
			return o, d, nil, nil, errors.New("capture o schema do destino primeiro")
		}
		if err := json.Unmarshal(ed.Conteudo, &d); err != nil {
			return o, d, nil, nil, err
		}
		did = &ed.ID
	}
	return o, d, oid, did, nil
}

// Sugerir: POST /api/migracao/projetos/{id}/sugerir → versão nova com a heurística.
func (h *Handler) Sugerir(w http.ResponseWriter, r *http.Request) {
	h.gravarVersao(w, r, func(o, d esquema.Esquema) (modelo.Mapeamento, string, error) {
		return sugestao.Sugerir(o, d), string(modelo.OrigemHeuristica), nil
	})
}

// SalvarMapeamento: PUT /api/migracao/projetos/{id}/mapeamento {mapeamento, origem}
func (h *Handler) SalvarMapeamento(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mapeamento modelo.Mapeamento `json:"mapeamento"`
		Origem     string            `json:"origem"`
	}
	if !entrada.LerJSON(w, r, &req) {
		return
	}
	origem, err := validacao.Escolha("origem", firstNonEmpty(req.Origem, "usuario"), "usuario", "mcp")
	if err != nil {
		entrada.ErroValidacao(w, err)
		return
	}
	h.gravarVersao(w, r, func(esquema.Esquema, esquema.Esquema) (modelo.Mapeamento, string, error) {
		return req.Mapeamento, origem, nil
	})
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (h *Handler) gravarVersao(w http.ResponseWriter, r *http.Request, gerar func(o, d esquema.Esquema) (modelo.Mapeamento, string, error)) {
	v, err := h.NovaVersao(r.Context(), r.PathValue("id"), quem(r), gerar)
	if err != nil {
		h.responder(w, err)
		return
	}
	entrada.ResponderJSON(w, http.StatusCreated, v)
}

// Aprovar: POST /api/migracao/projetos/{id}/aprovar {versao} (crítica: 2FA + reauth).
func (h *Handler) Aprovar(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Versao int `json:"versao"`
	}
	if !entrada.LerJSON(w, r, &req) {
		return
	}
	if err := h.st.AprovarMapeamento(r.Context(), r.PathValue("id"), req.Versao, quem(r)); err != nil {
		if errors.Is(err, store.ErrConflito) {
			http.Error(w, "só uma versão válida (sem erros) pode ser aprovada", http.StatusConflict)
			return
		}
		h.erro(w, err)
		return
	}
	v, err := h.st.VersaoMapeamentoN(r.Context(), r.PathValue("id"), req.Versao)
	if err != nil {
		h.erro(w, err)
		return
	}
	entrada.ResponderJSON(w, http.StatusOK, v)
}

// Versao: GET /api/migracao/projetos/{id}/versoes/{n}
func (h *Handler) Versao(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		http.Error(w, "versão inválida", http.StatusBadRequest)
		return
	}
	v, err := h.st.VersaoMapeamentoN(r.Context(), r.PathValue("id"), n)
	if err != nil {
		h.erro(w, err)
		return
	}
	entrada.ResponderJSON(w, http.StatusOK, v)
}

func (h *Handler) erro(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "não encontrado", http.StatusNotFound)
	case errors.Is(err, canal.ErrAgenteOffline), errors.Is(err, canal.ErrEsquemaTempo):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	case errors.Is(err, canal.ErrAgenteRevogado):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
}

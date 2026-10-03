package migracao

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
	"github.com/eduardorarruda/revoada/core/migracao/modelo"
	"github.com/eduardorarruda/revoada/core/seguranca/cofre"
	"github.com/eduardorarruda/revoada/core/validacao"
	"github.com/eduardorarruda/revoada/server/internal/canal"
	"github.com/eduardorarruda/revoada/server/internal/entrada"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// Serviço do módulo de migração sem HTTP: a API e o MCP (Etapa 8) passam pelas
// MESMAS regras — o MCP não tem atalho.

// ErroRegra é uma regra de negócio que impede a ação (vira 409).
type ErroRegra struct{ Msg string }

func (e *ErroRegra) Error() string { return e.Msg }

func regra(msg string) error { return &ErroRegra{Msg: msg} }

// responder traduz um erro do serviço em resposta HTTP.
func (h *Handler) responder(w http.ResponseWriter, err error) {
	var r *ErroRegra
	var v *validacao.Erro
	switch {
	case errors.As(err, &r):
		http.Error(w, r.Msg, http.StatusConflict)
	case errors.As(err, &v):
		entrada.ResponderJSON(w, http.StatusUnprocessableEntity, v)
	default:
		h.erro(w, err)
	}
}

// NovaVersao grava uma versão nova do mapeamento, validada contra as fotos dos dois
// bancos. `origem` diz quem gerou: heuristica | usuario | mcp.
func (h *Handler) NovaVersao(ctx context.Context, projetoID, autor string, gerar func(o, d esquema.Esquema) (modelo.Mapeamento, string, error)) (store.VersaoMapeamento, error) {
	p, err := h.st.ProjetoPorID(ctx, projetoID)
	if err != nil {
		return store.VersaoMapeamento{}, err
	}
	o, d, oid, did, err := h.esquemasDoProjeto(ctx, p)
	if err != nil {
		return store.VersaoMapeamento{}, regra(err.Error())
	}
	if p.DestinoID == nil { // upgrade de versão: o destino é a própria origem
		d = o
	}
	m, origem, err := gerar(o, d)
	if err != nil {
		return store.VersaoMapeamento{}, &validacao.Erro{Campo: "mapeamento", Mensagem: err.Error()}
	}
	problemas := m.Validar(o, d)
	estado := "valido"
	if modelo.TemErro(problemas) {
		estado = "rascunho"
	}
	conteudo, _ := json.Marshal(m)
	probs, _ := json.Marshal(problemas)
	if problemas == nil {
		probs = []byte("[]")
	}
	return h.st.NovaVersaoMapeamento(ctx, store.VersaoMapeamento{
		ProjetoID: p.ID, EsquemaOrigemID: oid, EsquemaDestinoID: did, Origem: origem, Conteudo: conteudo,
		Hash: m.Hash(), Problemas: probs, Estado: estado, CriadoPor: autor,
	})
}

// ResumoCaptura é o que volta da leitura de schema (nunca dados).
type ResumoCaptura struct {
	ID      string `json:"id"`
	Hash    string `json:"hash"`
	Versao  string `json:"versao"`
	Charset string `json:"charset"`
	Tabelas int    `json:"tabelas"`
}

// CapturarEsquema pede ao agente que leia a ESTRUTURA do banco da conexão.
func (h *Handler) CapturarEsquema(ctx context.Context, conexaoID, agente string) (ResumoCaptura, error) {
	c, err := h.st.ConexaoPorID(ctx, conexaoID)
	if err != nil {
		return ResumoCaptura{}, err
	}
	if agente == "" && c.AgentePreferidoID != nil {
		agente = *c.AgentePreferidoID
	}
	if agente == "" {
		return ResumoCaptura{}, &validacao.Erro{Campo: "agente_id", Mensagem: "escolha o agente que vai ler o banco"}
	}
	senha, err := h.abrirSenha(ctx, c)
	if err != nil {
		return ResumoCaptura{}, errors.New("não deu para abrir a senha guardada (a chave mestra mudou?)")
	}
	defer cofre.Limpar(senha)
	bruto, err := h.canal.PedirEsquema(ctx, canal.PedidoLeitura{
		AgenteID: agente, Motor: c.Motor, Endereco: c.Endereco, Banco: c.Banco, Usuario: c.Usuario, Senha: senha, Opcoes: c.Opcoes,
	})
	if err != nil {
		return ResumoCaptura{}, err
	}
	var e esquema.Esquema
	if err := json.Unmarshal(bruto, &e); err != nil || e.Motor != c.Motor {
		return ResumoCaptura{}, errors.New("o agente devolveu um schema inválido")
	}
	reg := store.EsquemaCapturado{ID: novoID("esq"), ConexaoID: c.ID, AgenteID: agente, Hash: e.Hash(), Conteudo: bruto}
	if err := h.st.GravarEsquema(ctx, reg, e.Versao); err != nil {
		return ResumoCaptura{}, err
	}
	return ResumoCaptura{ID: reg.ID, Hash: reg.Hash, Versao: e.Versao, Charset: e.Charset, Tabelas: len(e.Tabelas)}, nil
}

// registrarTarefa despacha a tarefa no canal e a liga ao projeto.
func (h *Handler) registrarTarefa(ctx context.Context, tipo string, esp any, agente, autor, origem string, meta store.ExecucaoMigracao) (store.ExecucaoMigracao, error) {
	bruto, err := json.Marshal(esp)
	if err != nil {
		return meta, err
	}
	t, err := h.canal.Despachar(ctx, canal.NovaTarefa{Tipo: tipo, AgenteID: agente, Especificacao: bruto, IniciadaPor: autor, Origem: origem})
	if err != nil {
		return meta, err
	}
	meta.TarefaID = t.ID
	if err := h.st.CriarExecucao(ctx, meta); err != nil {
		return meta, err
	}
	t.Especificacao = nil
	meta.Tarefa, meta.CriadaEm = t, t.CriadaEm
	return meta, nil
}

// IniciarSimulacao dispara o dry-run de uma versão válida ou aprovada (0 = a última).
func (h *Handler) IniciarSimulacao(ctx context.Context, projetoID string, versao int, agente, autor, origem string, tamLote int) (store.ExecucaoMigracao, error) {
	p, err := h.st.ProjetoPorID(ctx, projetoID)
	if err != nil {
		return store.ExecucaoMigracao{}, err
	}
	var v store.VersaoMapeamento
	if versao > 0 {
		v, err = h.st.VersaoMapeamentoN(ctx, p.ID, versao)
	} else {
		v, err = h.st.UltimaVersaoMapeamento(ctx, p.ID)
	}
	if err != nil {
		return store.ExecucaoMigracao{}, err
	}
	if v.Estado != "valido" && v.Estado != "aprovado" {
		return store.ExecucaoMigracao{}, regra("a versão tem erros de mapeamento; corrija e salve antes de simular")
	}
	if agente == "" {
		if c, err := h.st.ConexaoPorID(ctx, p.OrigemID); err == nil && c.AgentePreferidoID != nil {
			agente = *c.AgentePreferidoID
		}
	}
	if agente == "" {
		return store.ExecucaoMigracao{}, &validacao.Erro{Campo: "agente_id", Mensagem: "escolha o agente que vai rodar a tarefa"}
	}
	esp, err := h.especificacao(ctx, p, v, novoID("sim"), lote(tamLote))
	if err != nil {
		return store.ExecucaoMigracao{}, regra(err.Error())
	}
	return h.registrarTarefa(ctx, "migracao.simular", esp, agente, autor, origem, store.ExecucaoMigracao{
		ProjetoID: p.ID, Versao: v.Versao, Tipo: "simular", Execucao: esp.Execucao, HashMapeamento: esp.HashMapeamento})
}

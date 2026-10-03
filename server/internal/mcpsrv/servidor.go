package mcpsrv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
	"github.com/eduardorarruda/revoada/core/migracao/modelo"
	"github.com/eduardorarruda/revoada/core/migracao/plano"
	"github.com/eduardorarruda/revoada/server/internal/audit"
	"github.com/eduardorarruda/revoada/server/internal/canal"
	"github.com/eduardorarruda/revoada/server/internal/migracao"
	"github.com/eduardorarruda/revoada/server/internal/store"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Versao do servidor MCP anunciada no initialize.
const Versao = "0.8.0"

// chamadasPorMinuto por token (freio contra laço de agente descontrolado).
const chamadasPorMinuto = 120

// Servidor junta o MCP às partes do painel que ele usa.
type Servidor struct {
	st    *store.Store
	canal *canal.Servico
	mig   *migracao.Handler
	audit *audit.Recorder
	log   *slog.Logger
	srv   *mcp.Server

	mu     sync.Mutex
	janela map[string]*contador
	// verificar confere o Bearer (troca nos testes; em produção, tokens do banco).
	verificar mcpauth.TokenVerifier
}

type contador struct {
	inicio time.Time
	n      int
}

// Novo monta o servidor MCP com todas as ferramentas.
func Novo(st *store.Store, c *canal.Servico, m *migracao.Handler, a *audit.Recorder, log *slog.Logger) *Servidor {
	s := &Servidor{st: st, canal: c, mig: m, audit: a, log: log, janela: map[string]*contador{}}
	s.verificar = s.verificador
	s.srv = mcp.NewServer(&mcp.Implementation{Name: "revoada", Title: "Revoada", Version: Versao}, &mcp.ServerOptions{
		Instructions: "Painel Revoada: monitoramento de servidores e migração de bancos. Você lê estado e estrutura " +
			"(nunca linhas de dado nem credenciais), pode gravar RASCUNHOS de mapeamento e pedir simulação (dry-run). " +
			"Aprovar, executar e reverter são só para pessoas, na interface.",
	})
	s.registrar()
	return s
}

// Handler é o /mcp: Bearer com token MCP → Streamable HTTP (respostas JSON).
func (s *Servidor) Handler() http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.srv }, &mcp.StreamableHTTPOptions{
		JSONResponse: true, SessionTimeout: 30 * time.Minute, Logger: s.log, MaxRequestBodyBytes: 1 << 20,
	})
	return mcpauth.RequireBearerToken(s.verificar, nil)(h)
}

// MCP devolve o servidor (testes e transporte em memória).
func (s *Servidor) MCP() *mcp.Server { return s.srv }

// ---------------------------------------------------------------- moldura das ferramentas

// ErrEscopo: o token não tem o escopo que a ferramenta pede.
var ErrEscopo = errors.New("este token MCP não tem o escopo necessário")

// ErrLimite: o token passou do limite de chamadas por minuto.
var ErrLimite = errors.New("limite de chamadas por minuto atingido; espere um pouco")

func (s *Servidor) frear(id string, agora time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.janela[id]
	if c == nil || agora.Sub(c.inicio) >= time.Minute {
		s.janela[id] = &contador{inicio: agora, n: 1}
		return true
	}
	c.n++
	return c.n <= chamadasPorMinuto
}

// ferramenta registra uma ferramenta com escopo, freio e auditoria.
func ferramenta[In, Out any](s *Servidor, nome, escopo, descricao string, somenteLeitura bool, f func(ctx context.Context, autor string, in In) (Out, error)) {
	t := &mcp.Tool{Name: nome, Description: descricao, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: somenteLeitura}}
	// A saída vai como objeto JSON genérico: os tipos do painel têm time.Time e JSON
	// cru, que o schema inferido pelo SDK não descreve bem (e a saída é validada).
	mcp.AddTool(s.srv, t, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, map[string]any, error) {
		var zero map[string]any
		var tok *mcpauth.TokenInfo
		if req != nil && req.Extra != nil {
			tok = req.Extra.TokenInfo
		}
		autor, id := "mcp", ""
		if tok != nil {
			id = tok.UserID
			if n, ok := tok.Extra["nome"].(string); ok {
				autor = "mcp:" + n
			}
		}
		status := http.StatusOK
		defer func() { s.auditar(autor, id, nome, escopo, in, status) }()
		if tok == nil || !slices.Contains(tok.Scopes, escopo) {
			status = http.StatusForbidden
			return nil, zero, fmt.Errorf("%w (%s)", ErrEscopo, escopo)
		}
		if !s.frear(id, time.Now()) {
			status = http.StatusTooManyRequests
			return nil, zero, ErrLimite
		}
		out, err := f(ctx, autor, in)
		if err != nil {
			status = http.StatusConflict
			return nil, zero, err
		}
		m, err := paraMapa(out)
		if err != nil {
			status = http.StatusInternalServerError
			return nil, zero, err
		}
		return nil, m, nil
	})
}

func (s *Servidor) auditar(autor, tokenID, ferramenta, escopo string, in any, status int) {
	var payload map[string]any
	b, _ := json.Marshal(in)
	_ = json.Unmarshal(b, &payload)
	if m, ok := payload["mapeamento"]; ok { // o rascunho inteiro não precisa ir para a trilha
		payload["mapeamento"] = fmt.Sprintf("(%d bytes)", len(fmt.Sprint(m)))
	}
	s.audit.Registrar(store.AuditEntry{ActorName: autor, ActorRole: "mcp", Method: "MCP", Path: "/mcp/" + ferramenta,
		Resource: "mcp", Target: tokenID, Status: status, Payload: map[string]any{"escopo": escopo, "argumentos": payload}, Origem: "mcp"})
}

// ---------------------------------------------------------------- saídas

func paraMapa(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	err = json.Unmarshal(b, &m)
	return m, err
}

// Lista embrulha listas (a saída estruturada do MCP precisa ser um objeto).
type Lista[T any] struct {
	Itens []T `json:"itens"`
	Total int `json:"total"`
}

func lista[T any](xs []T) Lista[T] { return Lista[T]{Itens: xs, Total: len(xs)} }

// semDados tira do resumo de uma tarefa o que é valor de linha: chaves das amostras
// da simulação e das divergências da execução e da verificação de conteúdo. Resumo
// em formato inesperado sai vazio (plano.OcultarChaves).
func semDados(e store.ExecucaoMigracao) store.ExecucaoMigracao {
	e.Tarefa.Especificacao = nil
	// o erro do motor traz a chave depois de plano.MarcadorChave (pode ter espaço,
	// dois-pontos, qualquer coisa): tudo dali em diante sai
	e.Tarefa.Erro = plano.OcultarChaveErro(e.Tarefa.Erro, "(oculta) — o detalhe está na tela do painel")
	if len(e.Tarefa.Resumo) > 0 {
		e.Tarefa.Resumo = plano.OcultarChaves(e.Tipo, e.Tarefa.Resumo)
	}
	return e
}

// ---------------------------------------------------------------- ferramentas

type semArgs struct{}

type porConexao struct {
	ConexaoID string `json:"conexao_id" jsonschema:"id da conexão de banco (veja listar_conexoes)"`
}

type capturaArgs struct {
	ConexaoID string `json:"conexao_id" jsonschema:"id da conexão de banco"`
	AgenteID  string `json:"agente_id,omitempty" jsonschema:"agente que lê o banco; vazio = o preferido da conexão"`
}

type porProjeto struct {
	ProjetoID string `json:"projeto_id" jsonschema:"id do projeto de migração (veja listar_projetos)"`
}

type verMapeamentoArgs struct {
	ProjetoID string `json:"projeto_id" jsonschema:"id do projeto de migração"`
	Versao    int    `json:"versao,omitempty" jsonschema:"número da versão; vazio = a mais recente"`
}

type rascunhoArgs struct {
	ProjetoID  string            `json:"projeto_id" jsonschema:"id do projeto de migração"`
	Mapeamento modelo.Mapeamento `json:"mapeamento" jsonschema:"o mapeamento completo (tabelas → colunas → transformações da lista fechada)"`
}

type simulacaoArgs struct {
	ProjetoID string `json:"projeto_id" jsonschema:"id do projeto de migração"`
	Versao    int    `json:"versao,omitempty" jsonschema:"versão a simular; vazio = a mais recente"`
	AgenteID  string `json:"agente_id,omitempty" jsonschema:"agente que roda; vazio = o preferido da conexão de origem"`
}

type mapeamentoSaida struct {
	Versao    int                   `json:"versao"`
	Estado    string                `json:"estado"`
	Origem    string                `json:"origem"`
	Hash      string                `json:"hash"`
	Conteudo  modelo.Mapeamento     `json:"conteudo"`
	Problemas []modelo.Problema     `json:"problemas"`
	Projeto   store.ProjetoMigracao `json:"projeto"`
}

func saidaVersao(p store.ProjetoMigracao, v store.VersaoMapeamento) (mapeamentoSaida, error) {
	out := mapeamentoSaida{Versao: v.Versao, Estado: v.Estado, Origem: v.Origem, Hash: v.Hash, Projeto: p}
	if err := json.Unmarshal(v.Conteudo, &out.Conteudo); err != nil {
		return out, err
	}
	if err := json.Unmarshal(v.Problemas, &out.Problemas); err != nil {
		return out, err
	}
	return out, nil
}

func (s *Servidor) registrar() {
	ferramenta(s, "listar_agentes", EscopoLeitura,
		"Lista os agentes do Revoada (um por servidor): estado online/instável/offline e os tipos de tarefa que cada um aceita.", true,
		func(ctx context.Context, _ string, _ semArgs) (Lista[store.Agente], error) {
			ags, err := s.canal.Agentes(ctx)
			return lista(ags), err
		})
	ferramenta(s, "listar_servidores", EscopoLeitura,
		"Lista os servidores monitorados com sistema, versão do agente, última coleta e se estão no ar.", true,
		func(ctx context.Context, _ string, _ semArgs) (Lista[store.HostDetail], error) {
			hs, err := s.st.HostsDetailed(ctx, "")
			return lista(hs), err
		})
	ferramenta(s, "alertas_ativos", EscopoLeitura,
		"Alertas abertos agora: regra, severidade, servidor (labels) e desde quando.", true,
		func(ctx context.Context, _ string, _ semArgs) (Lista[store.AlertEvent], error) {
			as, err := s.st.ListAlerts(ctx, true)
			return lista(as), err
		})
	ferramenta(s, "listar_conexoes", EscopoLeitura,
		"Lista as conexões de banco cadastradas (motor, endereço, banco, usuário). A senha nunca sai do cofre.", true,
		func(ctx context.Context, _ string, _ semArgs) (Lista[store.ConexaoBanco], error) {
			cs, err := s.st.ListarConexoes(ctx)
			return lista(cs), err
		})
	ferramenta(s, "ler_esquema", EscopoLeitura,
		"Devolve a última foto da ESTRUTURA de um banco (tabelas, colunas, tipos, chaves, índices, linhas estimadas). Nunca dados.", true,
		func(ctx context.Context, _ string, in porConexao) (esquema.Esquema, error) {
			var e esquema.Esquema
			reg, err := s.st.UltimoEsquema(ctx, in.ConexaoID)
			if err != nil {
				return e, fmt.Errorf("nenhum schema capturado para %q: use capturar_esquema", in.ConexaoID)
			}
			return e, json.Unmarshal(reg.Conteudo, &e)
		})
	ferramenta(s, "capturar_esquema", EscopoMapeamento,
		"Pede ao agente que leia de novo a estrutura do banco da conexão (só leitura no banco; a senha vai selada para o agente).", false,
		func(ctx context.Context, _ string, in capturaArgs) (migracao.ResumoCaptura, error) {
			return s.mig.CapturarEsquema(ctx, in.ConexaoID, in.AgenteID)
		})
	ferramenta(s, "listar_projetos", EscopoLeitura,
		"Lista os projetos de migração (troca de banco ou upgrade de versão do Firebird).", true,
		func(ctx context.Context, _ string, _ semArgs) (Lista[store.ProjetoMigracao], error) {
			ps, err := s.st.ListarProjetos(ctx)
			return lista(ps), err
		})
	ferramenta(s, "ver_mapeamento", EscopoLeitura,
		"Mostra uma versão do mapeamento de um projeto (tabela→tabela, coluna→coluna, transformações) e os problemas que a validação achou.", true,
		func(ctx context.Context, _ string, in verMapeamentoArgs) (mapeamentoSaida, error) {
			p, err := s.st.ProjetoPorID(ctx, in.ProjetoID)
			if err != nil {
				return mapeamentoSaida{}, err
			}
			var v store.VersaoMapeamento
			if in.Versao > 0 {
				v, err = s.st.VersaoMapeamentoN(ctx, p.ID, in.Versao)
			} else {
				v, err = s.st.UltimaVersaoMapeamento(ctx, p.ID)
			}
			if err != nil {
				return mapeamentoSaida{}, errors.New("o projeto ainda não tem mapeamento")
			}
			return saidaVersao(p, v)
		})
	ferramenta(s, "gravar_rascunho", EscopoMapeamento,
		"Grava uma VERSÃO NOVA do mapeamento (rascunho, origem = mcp), validada contra os schemas. Devolve o estado (valido/rascunho) "+
			"e os problemas. Não aprova: aprovar é ação de uma pessoa, com 2FA, na interface. Transformações permitidas: nenhuma, "+
			"converter_tipo, charset {de, reparar}, aparar, valor_padrao {valor}, constante {valor}, mapa_valores {de:para}, "+
			"concatenar {colunas, separador}, dividir {separador, parte}, data_formato {formato}.", false,
		func(ctx context.Context, autor string, in rascunhoArgs) (mapeamentoSaida, error) {
			m := in.Mapeamento
			for i := range m.Tabelas {
				m.Tabelas[i].Origem = modelo.OrigemMCP
				for j := range m.Tabelas[i].Colunas {
					m.Tabelas[i].Colunas[j].Origem = modelo.OrigemMCP
				}
			}
			v, err := s.mig.NovaVersao(ctx, in.ProjetoID, autor, func(esquema.Esquema, esquema.Esquema) (modelo.Mapeamento, string, error) {
				return m, string(modelo.OrigemMCP), nil
			})
			if err != nil {
				return mapeamentoSaida{}, err
			}
			p, _ := s.st.ProjetoPorID(ctx, in.ProjetoID)
			return saidaVersao(p, v)
		})
	ferramenta(s, "pedir_simulacao", EscopoSimulacao,
		"Pede o dry-run de uma versão válida: o agente lê a origem, aplica as transformações e confere tudo contra o destino SEM "+
			"GRAVAR NADA. Acompanhe com status_execucoes. Executar de verdade não é possível pelo MCP.", false,
		func(ctx context.Context, autor string, in simulacaoArgs) (store.ExecucaoMigracao, error) {
			e, err := s.mig.IniciarSimulacao(ctx, in.ProjetoID, in.Versao, in.AgenteID, autor, "mcp", 0)
			return semDados(e), err
		})
	ferramenta(s, "status_execucoes", EscopoLeitura,
		"Histórico de simulações, execuções, reversões e diagnósticos de um projeto, com estado e resumo (sem nenhum valor de linha).", true,
		func(ctx context.Context, _ string, in porProjeto) (Lista[store.ExecucaoMigracao], error) {
			es, err := s.st.ExecucoesDoProjeto(ctx, in.ProjetoID, 50)
			if err != nil {
				return Lista[store.ExecucaoMigracao]{}, err
			}
			for i := range es {
				es[i] = semDados(es[i])
			}
			return lista(es), nil
		})
}

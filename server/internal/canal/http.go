package canal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/core/migracao/plano"
	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/sse"
	"github.com/eduardorarruda/revoada/server/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

// Servir sobe o servidor gRPC do canal em `endereco` até o ctx acabar.
func (s *Servico) Servir(ctx context.Context, endereco string) error {
	lis, err := net.Listen("tcp", endereco)
	if err != nil {
		return fmt.Errorf("canal: escutando %s: %w", endereco, err)
	}
	srv := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(s.ca.TLS())),
		grpc.MaxRecvMsgSize(16<<20),
		// Derruba conexões mortas (rede caiu sem FIN) e aceita os pings do agente.
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 10 * time.Second, PermitWithoutStream: true}),
	)
	agentev1.RegisterCanalServer(srv, s)
	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()
	s.log.Info("canal: escutando agentes", "endereco", endereco)
	return srv.Serve(lis)
}

// ---------------------------------------------------------------- HTTP (painel)

// Handler expõe o canal para a interface e para a API.
type Handler struct {
	s *Servico
	// EnderecoPublico é como os agentes chegam ao canal (host:porta), usado para
	// montar o comando de instalação mostrado na tela.
	EnderecoPublico string
}

func NovoHandler(s *Servico, enderecoPublico string) *Handler {
	return &Handler{s: s, EnderecoPublico: enderecoPublico}
}

func escreverJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func quem(r *http.Request) string {
	if c, ok := auth.ClaimsFrom(r.Context()); ok {
		return c.Name
	}
	return ""
}

// CriarToken: POST /api/agentes/tokens {rotulo, validade_horas}
func (h *Handler) CriarToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rotulo        string `json:"rotulo"`
		ValidadeHoras int    `json:"validade_horas"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	tok, expira, err := h.s.CriarToken(r.Context(), strings.TrimSpace(req.Rotulo), quem(r), time.Duration(req.ValidadeHoras)*time.Hour)
	if err != nil {
		http.Error(w, "erro ao criar o token", http.StatusInternalServerError)
		return
	}
	escreverJSON(w, http.StatusCreated, map[string]any{
		"token":     tok,
		"expira_em": expira,
		"endereco":  h.EnderecoPublico,
		"comandos": map[string]string{
			"linux_macos": fmt.Sprintf("sudo revoada-agent inscrever --painel %s --token %s", h.EnderecoPublico, tok),
			"windows":     fmt.Sprintf(`revoada-agent.exe inscrever --painel %s --token %s`, h.EnderecoPublico, tok),
		},
	})
}

// ListarAgentes: GET /api/agentes
func (h *Handler) ListarAgentes(w http.ResponseWriter, r *http.Request) {
	ags, err := h.s.Agentes(r.Context())
	if err != nil {
		http.Error(w, "erro ao listar agentes", http.StatusInternalServerError)
		return
	}
	escreverJSON(w, http.StatusOK, ags)
}

// Revogar: POST /api/agentes/{id}/revogar
func (h *Handler) Revogar(w http.ResponseWriter, r *http.Request) {
	if err := h.s.Revogar(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "agente não encontrado", http.StatusNotFound)
			return
		}
		http.Error(w, "erro ao revogar", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// tiposDiagnostico são as tarefas que só testam o canal (sem tocar em banco nem servidor).
var tiposDiagnostico = map[string]bool{"diagnostico.eco": true}

// CriarDiagnostico: POST /api/tarefas/diagnostico {agente_id, tipo, especificacao}
// As tarefas de migração e deploy têm rotas próprias (com permissão e validação
// específicas); aqui só entram as de diagnóstico do canal.
func (h *Handler) CriarDiagnostico(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgenteID      string          `json:"agente_id"`
		Tipo          string          `json:"tipo"`
		Especificacao json.RawMessage `json:"especificacao"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	if req.Tipo == "" {
		req.Tipo = "diagnostico.eco"
	}
	if !tiposDiagnostico[req.Tipo] {
		http.Error(w, "tipo de diagnóstico desconhecido", http.StatusBadRequest)
		return
	}
	if len(req.Especificacao) > 0 && !json.Valid(req.Especificacao) {
		http.Error(w, "especificação não é JSON válido", http.StatusBadRequest)
		return
	}
	t, err := h.s.Despachar(r.Context(), NovaTarefa{
		Tipo: req.Tipo, AgenteID: req.AgenteID, Especificacao: req.Especificacao, IniciadaPor: quem(r), Origem: "ui",
	})
	if err != nil {
		h.erroTarefa(w, err)
		return
	}
	escreverJSON(w, http.StatusCreated, t)
}

// ListarTarefas: GET /api/tarefas?agente_id=&tipo=&limite=
func (h *Handler) ListarTarefas(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lim, _ := strconv.Atoi(q.Get("limite"))
	ts, err := h.s.ListarTarefas(r.Context(), store.FiltroTarefas{AgenteID: q.Get("agente_id"), Tipo: q.Get("tipo"), Limite: lim})
	if err != nil {
		http.Error(w, "erro ao listar tarefas", http.StatusInternalServerError)
		return
	}
	ve := veChaves(r)
	for i := range ts {
		ts[i] = publica(ts[i], ve)
	}
	escreverJSON(w, http.StatusOK, ts)
}

// Tarefa: GET /api/tarefas/{id} → tarefa + eventos
func (h *Handler) Tarefa(w http.ResponseWriter, r *http.Request) {
	t, err := h.s.Tarefa(r.Context(), r.PathValue("id"))
	if err != nil {
		h.erroTarefa(w, err)
		return
	}
	evs, err := h.s.Eventos(r.Context(), t.ID, 0)
	if err != nil {
		http.Error(w, "erro ao ler eventos", http.StatusInternalServerError)
		return
	}
	ve := veChaves(r)
	t = publica(t, ve)
	if !ve {
		for i := range evs {
			evs[i] = semChavesEvento(t.Tipo, evs[i])
		}
	}
	escreverJSON(w, http.StatusOK, map[string]any{"tarefa": t, "eventos": evs})
}

// Controlar: POST /api/tarefas/{id}/controle {acao}
func (h *Handler) Controlar(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Acao string `json:"acao"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	t, err := h.s.Controlar(r.Context(), r.PathValue("id"), req.Acao)
	if err != nil {
		h.erroTarefa(w, err)
		return
	}
	escreverJSON(w, http.StatusOK, t)
}

// AoVivo: POST /api/tarefas/{id}/ao-vivo — SSE com o estado, os eventos já gravados
// e, depois, tudo o que chegar. POST porque o cliente usa fetch com Authorization.
func (h *Handler) AoVivo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, err := h.s.Tarefa(r.Context(), id)
	if err != nil {
		h.erroTarefa(w, err)
		return
	}
	// Assina ANTES de ler o histórico: nada que chegue no meio se perde (o
	// repetido é filtrado pelo seq).
	ch, cancelar := h.s.Assinar(id)
	defer cancelar()
	evs, err := h.s.Eventos(r.Context(), id, 0)
	if err != nil {
		http.Error(w, "erro ao ler eventos", http.StatusInternalServerError)
		return
	}
	e, ok := sse.Abrir(w)
	if !ok {
		http.Error(w, "streaming indisponível", http.StatusInternalServerError)
		return
	}
	defer e.Fechar()
	ve := veChaves(r)
	t = publica(t, ve)
	if !ve {
		for i := range evs {
			evs[i] = semChavesEvento(t.Tipo, evs[i])
		}
	}
	if e.Emitir("tarefa", t) != nil {
		return
	}
	var ultimo int64
	for i := range evs {
		if e.Emitir("evento", evs[i]) != nil {
			return
		}
		ultimo = evs[i].Seq
	}
	if final(t.Estado) {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case a := <-ch:
			if a.Evento != nil {
				if a.Evento.Seq <= ultimo {
					continue
				}
				ultimo = a.Evento.Seq
				ev := *a.Evento
				if !ve {
					ev = semChavesEvento(t.Tipo, ev)
				}
				if e.Emitir("evento", ev) != nil {
					return
				}
			}
			if a.Tarefa != nil {
				tf := publica(*a.Tarefa, ve)
				if e.Emitir("tarefa", tf) != nil {
					return
				}
				if final(a.Tarefa.Estado) {
					return
				}
			}
		}
	}
}

// As tarefas da migração carregam chaves de linha (amostras da simulação,
// divergências da verificação, a linha que falhou na execução) — valores da origem
// como CPF e e-mail. As telas da migração já as escondem de quem não pode executar
// migração; estas rotas genéricas de tarefa devolviam tudo a qualquer logado.
const avisoChaveOculta = "(visível só para quem pode executar migração)"

func veChaves(r *http.Request) bool {
	c, ok := auth.ClaimsFrom(r.Context())
	return ok && auth.Pode(c.Role, auth.PermExecutarMigracao)
}

// publica prepara uma tarefa para as rotas genéricas. As da migração e do upgrade
// Firebird levam na especificação o endereço, o banco e o usuário das conexões e o
// schema inteiro do cliente: nenhuma tela lê isso daqui (as telas da migração têm
// rotas próprias, que já zeram a especificação), então sai para todos. As chaves
// de linha saem para quem não pode executar migração.
func publica(t store.Tarefa, veChaves bool) store.Tarefa {
	if strings.HasPrefix(t.Tipo, "migracao.") || strings.HasPrefix(t.Tipo, "firebird.") {
		t.Especificacao = nil
	}
	if !veChaves {
		t = semChavesTarefa(t)
	}
	return t
}

func semChavesTarefa(t store.Tarefa) store.Tarefa {
	tipo, ok := strings.CutPrefix(t.Tipo, "migracao.")
	if !ok {
		return t
	}
	t.Erro = plano.OcultarChaveErro(t.Erro, avisoChaveOculta)
	if len(t.Resumo) > 0 {
		t.Resumo = plano.OcultarChaves(tipo, t.Resumo)
	}
	return t
}

func semChavesEvento(tipo string, ev store.EventoTarefa) store.EventoTarefa {
	if strings.HasPrefix(tipo, "migracao.") {
		ev.Mensagem = plano.OcultarChaveErro(ev.Mensagem, avisoChaveOculta)
	}
	return ev
}

func final(estado string) bool {
	switch estado {
	case "sucesso", "falha", "cancelada", "recusada":
		return true
	}
	return false
}

func (h *Handler) erroTarefa(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "não encontrado", http.StatusNotFound)
	case errors.Is(err, ErrAgenteRevogado), errors.Is(err, ErrTarefaFinalizada), errors.Is(err, ErrAcaoInvalida):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, ErrAgenteOffline):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	default:
		http.Error(w, "erro interno", http.StatusInternalServerError)
	}
}

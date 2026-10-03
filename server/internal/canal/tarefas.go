package canal

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

var (
	ErrAgenteRevogado   = errors.New("este agente foi revogado")
	ErrAgenteOffline    = errors.New("o agente está offline; a ação será possível quando ele reconectar")
	ErrTarefaFinalizada = errors.New("a tarefa já terminou")
	ErrAcaoInvalida     = errors.New("ação inválida (use pausar, retomar ou cancelar)")
)

// ValidadeTarefaPadrao: depois disso, uma tarefa que ainda não começou é recusada pelo agente.
const ValidadeTarefaPadrao = 24 * time.Hour

// NovaTarefa é o pedido de execução vindo da API (ou do MCP, ou da GitHub Action).
type NovaTarefa struct {
	Tipo          string
	AgenteID      string
	Especificacao json.RawMessage
	IniciadaPor   string
	Origem        string // ui | api | mcp | github_action
	Limites       *agentev1.Limites
	Validade      time.Duration
}

// Despachar grava a tarefa e a envia na hora se o agente estiver conectado; senão
// ela espera na fila e sai quando ele reconectar.
func (s *Servico) Despachar(ctx context.Context, n NovaTarefa) (store.Tarefa, error) {
	ag, err := s.repo.AgentePorID(ctx, n.AgenteID)
	if err != nil {
		return store.Tarefa{}, err
	}
	if ag.Revogado {
		return store.Tarefa{}, ErrAgenteRevogado
	}
	if n.Validade <= 0 {
		n.Validade = ValidadeTarefaPadrao
	}
	if n.Origem == "" {
		n.Origem = "ui"
	}
	t := store.Tarefa{
		ID: novoID("tf"), Tipo: n.Tipo, AgenteID: ag.ID, Estado: "na_fila", Especificacao: n.Especificacao,
		IniciadaPor: n.IniciadaPor, Origem: n.Origem, CorrelacaoID: novoID("cor"),
		ExpiraEm: s.agora().Add(n.Validade), CriadaEm: s.agora(),
	}
	if err := s.repo.CriarTarefa(ctx, t); err != nil {
		return store.Tarefa{}, err
	}
	s.log.Info("canal: tarefa criada", "tarefa", t.ID, "tipo", t.Tipo, "agente", ag.ID,
		"por", t.IniciadaPor, "origem", t.Origem, "correlacao_id", t.CorrelacaoID)
	if ses := s.sessaoConectada(ag.ID); ses != nil {
		if err := s.enviarTarefa(ctx, ag, ses, t); err != nil {
			s.log.Warn("canal: tarefa ficou na fila", "tarefa", t.ID, "err", err)
		}
	}
	return s.repo.TarefaPorID(ctx, t.ID)
}

// enviarTarefa monta a mensagem (com credenciais seladas para ESTE agente e a
// assinatura do painel) e a coloca na fila da conexão.
func (s *Servico) enviarTarefa(ctx context.Context, ag store.Agente, ses *sessao, t store.Tarefa) error {
	msg := &agentev1.Tarefa{
		Id: t.ID, Tipo: t.Tipo, Especificacao: t.Especificacao, ExpiraEm: t.ExpiraEm.Unix(),
		CorrelacaoId: t.CorrelacaoID,
	}
	if s.Seladora != nil {
		creds, err := s.Seladora(ctx, ag, t)
		if err != nil {
			return err
		}
		msg.Credenciais = creds
	}
	if err := s.ca.AssinarTarefa(msg); err != nil {
		return err
	}
	if !s.enviar(ses, &agentev1.MsgPainel{Corpo: &agentev1.MsgPainel_Tarefa{Tarefa: msg}}) {
		return ErrAgenteOffline
	}
	if t.Estado == "na_fila" {
		if err := s.repo.MudarEstadoTarefa(ctx, t.ID, "enviada"); err != nil {
			return err
		}
		s.publicarTarefa(ctx, t.ID)
	}
	return nil
}

// reconciliar roda quando o agente (re)conecta: reenvia o que ele não recebeu e
// encerra o que se perdeu do lado dele (ex.: o servidor reiniciou no meio).
func (s *Servico) reconciliar(ctx context.Context, ag store.Agente, ses *sessao, emAndamento []*agentev1.EstadoTarefa) {
	abertas, err := s.repo.TarefasEmAberto(ctx, ag.ID)
	if err != nil {
		s.log.Error("canal: lendo tarefas em aberto", "agente", ag.ID, "err", err)
		return
	}
	conhecidas := make([]string, 0, len(emAndamento))
	for _, e := range emAndamento {
		conhecidas = append(conhecidas, e.GetTarefaId())
	}
	for _, t := range abertas {
		switch {
		case slices.Contains(conhecidas, t.ID):
			// o agente ainda está com ela: segue
		case t.Estado == "na_fila" || t.Estado == "enviada":
			if err := s.enviarTarefa(ctx, ag, ses, t); err != nil {
				s.log.Warn("canal: reenvio de tarefa falhou", "tarefa", t.ID, "err", err)
			}
		default: // executando/pausada mas o agente não a conhece mais
			// Reexecutar sozinho poderia repetir efeitos (uma migração pela metade);
			// a decisão de rodar de novo é de uma pessoa.
			if err := s.repo.FinalizarTarefa(ctx, t.ID, "falha",
				"o agente reiniciou e perdeu esta tarefa; confira o estado e rode de novo se for o caso", nil); err == nil {
				s.publicarTarefa(ctx, t.ID)
			}
		}
	}
}

// Controlar pausa, retoma ou cancela uma tarefa.
func (s *Servico) Controlar(ctx context.Context, tarefaID, acao string) (store.Tarefa, error) {
	t, err := s.repo.TarefaPorID(ctx, tarefaID)
	if err != nil {
		return t, err
	}
	if slices.Contains([]string{"sucesso", "falha", "cancelada", "recusada"}, t.Estado) {
		return t, ErrTarefaFinalizada
	}
	var a agentev1.Controle_Acao
	var novoEstado string
	switch acao {
	case "pausar":
		a, novoEstado = agentev1.Controle_PAUSAR, "pausada"
	case "retomar":
		a, novoEstado = agentev1.Controle_RETOMAR, "executando"
	case "cancelar":
		a = agentev1.Controle_CANCELAR
	default:
		return t, ErrAcaoInvalida
	}
	// Ainda não chegou ao agente: cancelar resolve aqui mesmo.
	if acao == "cancelar" && t.Estado == "na_fila" {
		if err := s.repo.FinalizarTarefa(ctx, t.ID, "cancelada", "cancelada antes de chegar ao agente", nil); err != nil {
			return t, err
		}
		s.publicarTarefa(ctx, t.ID)
		return s.repo.TarefaPorID(ctx, t.ID)
	}
	ses := s.sessaoConectada(t.AgenteID)
	if ses == nil {
		return t, ErrAgenteOffline
	}
	if !s.enviar(ses, &agentev1.MsgPainel{Corpo: &agentev1.MsgPainel_Controle{
		Controle: &agentev1.Controle{TarefaId: t.ID, Acao: a},
	}}) {
		return t, ErrAgenteOffline
	}
	if novoEstado != "" {
		if err := s.repo.MudarEstadoTarefa(ctx, t.ID, novoEstado); err != nil {
			return t, err
		}
	}
	s.publicarTarefa(ctx, t.ID)
	return s.repo.TarefaPorID(ctx, t.ID)
}

// ---------------------------------------------------------------- ao vivo

// Assinar entrega as atualizações de uma tarefa até `cancelar` ser chamado.
func (s *Servico) Assinar(tarefaID string) (<-chan Atualizacao, func()) {
	ch := make(chan Atualizacao, 64)
	s.mu.Lock()
	if s.assinantes[tarefaID] == nil {
		s.assinantes[tarefaID] = map[chan Atualizacao]struct{}{}
	}
	s.assinantes[tarefaID][ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.assinantes[tarefaID], ch)
		if len(s.assinantes[tarefaID]) == 0 {
			delete(s.assinantes, tarefaID)
		}
		s.mu.Unlock()
	}
}

func (s *Servico) publicar(tarefaID string, a Atualizacao) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.assinantes[tarefaID] {
		select {
		case ch <- a:
		default: // tela lenta: perde uma atualização em vez de travar o canal
		}
	}
}

func (s *Servico) publicarTarefa(ctx context.Context, id string) {
	t, err := s.repo.TarefaPorID(ctx, id)
	if err != nil {
		return
	}
	s.publicar(id, Atualizacao{Tarefa: &t})
}

// ---------------------------------------------------------------- consultas

func (s *Servico) Tarefa(ctx context.Context, id string) (store.Tarefa, error) {
	return s.repo.TarefaPorID(ctx, id)
}

func (s *Servico) Eventos(ctx context.Context, id string, aposSeq int64) ([]store.EventoTarefa, error) {
	return s.repo.EventosTarefa(ctx, id, aposSeq)
}

func (s *Servico) ListarTarefas(ctx context.Context, f store.FiltroTarefas) ([]store.Tarefa, error) {
	return s.repo.ListarTarefas(ctx, f)
}

// Agentes lista os agentes com a presença mais fresca (memória vence banco).
func (s *Servico) Agentes(ctx context.Context) ([]store.Agente, error) {
	ags, err := s.repo.ListarAgentes(ctx)
	if err != nil {
		return nil, err
	}
	for i := range ags {
		if est, _, ok := s.EstadoAoVivo(ags[i].ID); ok && !ags[i].Revogado {
			ags[i].Estado = est
		}
		// O banco só guarda o sinal quando a presença muda; a sessão sabe o último
		// batimento (a cada 10 s). Sem isto a tela mostrava a hora da conexão.
		if ultimo, ok := s.UltimoSinal(ags[i].ID); ok && (ags[i].VistoEm == nil || ultimo.After(*ags[i].VistoEm)) {
			ags[i].VistoEm = &ultimo
		}
	}
	return ags, nil
}

// Revogar invalida o agente e derruba a conexão na hora.
func (s *Servico) Revogar(ctx context.Context, id string) error {
	if err := s.repo.RevogarAgente(ctx, id); err != nil {
		return err
	}
	s.Desconectar(id)
	s.log.Warn("canal: agente revogado", "agente", id)
	return nil
}

// entregarEsquema é ligado na Etapa 3 (leitura de schema pelo agente).
func (s *Servico) entregarEsquema(r *agentev1.RespostaEsquema) {
	s.mu.Lock()
	ch, ok := s.esperas[r.GetPedidoId()]
	s.mu.Unlock()
	if ok {
		select {
		case ch <- r:
		default:
		}
	}
}

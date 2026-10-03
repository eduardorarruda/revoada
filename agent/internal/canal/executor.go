package canal

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
)

// Relator é o que um tipo de tarefa usa para falar com o painel.
type Relator interface {
	// Evento reporta progresso (0..100) e uma mensagem para a tela ao vivo.
	Evento(etapa string, nivel agentev1.EventoTarefa_Nivel, msg string, progresso float64, metricas map[string]float64)
	// Pausa bloqueia enquanto a tarefa estiver pausada; devolve erro se foi cancelada.
	Pausa(ctx context.Context) error
	// Credencial abre uma credencial selada que veio com a tarefa (só em memória).
	Credencial(nome string) ([]byte, error)
	// Checkpoint avisa o painel que um lote foi confirmado (tabela, última chave).
	Checkpoint(tabela, ultimaChave string, linhas int64)
}

// Manipulador executa um tipo de tarefa. O resumo vira o JSON do resultado.
type Manipulador func(ctx context.Context, t *agentev1.Tarefa, r Relator) (resumo any, err error)

// Executor recebe tarefas do painel, confere e roda.
type Executor struct {
	log        *slog.Logger
	pub        ed25519.PublicKey
	permitidas map[string]bool
	tipos      map[string]Manipulador
	pend       *Pendentes
	enviar     func(*agentev1.MsgAgente)
	identidade *Identidade
	agora      func() time.Time

	mu      sync.Mutex
	rodando map[string]*execucao
	vistas  map[string]bool // tarefas já recebidas (o painel pode reenviar)
}

type execucao struct {
	tarefa  *agentev1.Tarefa
	cancel  context.CancelFunc
	mu      sync.Mutex
	seq     int64
	pausada bool
	retomar chan struct{}
	estado  string
}

// NovoExecutor monta o executor. `permitidas` vem da configuração LOCAL do agente
// (ARQUITETURA §7): o dono do servidor decide o que roda ali, não o painel.
func NovoExecutor(log *slog.Logger, id *Identidade, permitidas []string, pend *Pendentes, enviar func(*agentev1.MsgAgente)) *Executor {
	e := &Executor{
		log: log, pub: id.PubAssinatura, identidade: id, permitidas: map[string]bool{},
		tipos: map[string]Manipulador{}, pend: pend, enviar: enviar, agora: time.Now,
		rodando: map[string]*execucao{}, vistas: map[string]bool{},
	}
	for _, p := range permitidas {
		e.permitidas[p] = true
	}
	e.Registrar("diagnostico.eco", Eco)
	return e
}

// Registrar liga um tipo de tarefa a um manipulador.
func (e *Executor) Registrar(tipo string, m Manipulador) { e.tipos[tipo] = m }

// Capacidades são os tipos que este agente aceita (liberados E implementados).
// "migracao.esquema" não é tarefa, é pedido de leitura — entra se estiver liberado.
func (e *Executor) Capacidades() []string {
	var out []string
	for t := range e.tipos {
		if e.permitidas[t] {
			out = append(out, t)
		}
	}
	if e.permitidas["migracao.esquema"] {
		out = append(out, "migracao.esquema")
	}
	return out
}

// EmAndamento é a lista que vai no Olá para o painel reconciliar.
func (e *Executor) EmAndamento() []*agentev1.EstadoTarefa {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []*agentev1.EstadoTarefa
	for id, x := range e.rodando {
		x.mu.Lock()
		out = append(out, &agentev1.EstadoTarefa{TarefaId: id, Estado: x.estado, UltimoSeq: x.seq})
		x.mu.Unlock()
	}
	return out
}

// Receber trata uma tarefa vinda do painel.
func (e *Executor) Receber(t *agentev1.Tarefa) {
	e.mu.Lock()
	if e.vistas[t.GetId()] {
		e.mu.Unlock()
		return // reenvio de algo que já está rodando ou já terminou
	}
	e.vistas[t.GetId()] = true
	e.mu.Unlock()

	log := e.log.With("tarefa", t.GetId(), "tipo", t.GetTipo(), "correlacao_id", t.GetCorrelacaoId())
	if err := agentev1.VerificarTarefa(e.pub, t, e.agora()); err != nil {
		log.Warn("canal: tarefa recusada", "motivo", err)
		e.finalizar(&execucao{tarefa: t}, agentev1.ResultadoTarefa_RECUSADA, err.Error(), nil)
		return
	}
	m, ok := e.tipos[t.GetTipo()]
	if !ok || !e.permitidas[t.GetTipo()] {
		motivo := fmt.Sprintf("o tipo %q não está liberado na configuração deste agente (canal.tarefas_permitidas)", t.GetTipo())
		log.Warn("canal: tarefa recusada", "motivo", motivo)
		e.finalizar(&execucao{tarefa: t}, agentev1.ResultadoTarefa_RECUSADA, motivo, nil)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	x := &execucao{tarefa: t, cancel: cancel, retomar: make(chan struct{}), estado: "executando"}
	e.mu.Lock()
	e.rodando[t.GetId()] = x
	e.mu.Unlock()
	log.Info("canal: tarefa iniciada")
	go e.rodar(ctx, log, x, m)
}

func (e *Executor) rodar(ctx context.Context, log *slog.Logger, x *execucao, m Manipulador) {
	defer func() {
		if p := recover(); p != nil {
			log.Error("canal: tarefa entrou em pânico", "panico", p)
			e.finalizar(x, agentev1.ResultadoTarefa_FALHA, fmt.Sprint("erro interno do agente: ", p), nil)
		}
	}()
	resumo, err := m(ctx, x.tarefa, &relator{e: e, x: x})
	switch {
	case errors.Is(err, context.Canceled) || ctx.Err() != nil:
		log.Info("canal: tarefa cancelada")
		e.finalizar(x, agentev1.ResultadoTarefa_CANCELADA, "cancelada a pedido do painel", resumo)
	case err != nil:
		log.Warn("canal: tarefa falhou", "err", err)
		e.finalizar(x, agentev1.ResultadoTarefa_FALHA, err.Error(), resumo)
	default:
		log.Info("canal: tarefa concluída")
		e.finalizar(x, agentev1.ResultadoTarefa_SUCESSO, "", resumo)
	}
}

func (e *Executor) finalizar(x *execucao, estado agentev1.ResultadoTarefa_Estado, erro string, resumo any) {
	var b []byte
	if resumo != nil {
		b, _ = json.Marshal(resumo)
	}
	m := &agentev1.MsgAgente{Corpo: &agentev1.MsgAgente_Resultado{Resultado: &agentev1.ResultadoTarefa{
		TarefaId: x.tarefa.GetId(), Seq: x.proximoSeq(), Estado: estado, Erro: erro, Resumo: b,
	}}}
	e.guardarEEnviar(m)
	e.mu.Lock()
	delete(e.rodando, x.tarefa.GetId())
	e.mu.Unlock()
}

// guardarEEnviar grava em disco ANTES de mandar: se a rede ou o agente cair agora, a
// mensagem sai de novo na reconexão.
func (e *Executor) guardarEEnviar(m *agentev1.MsgAgente) {
	if err := e.pend.Adicionar(m); err != nil {
		e.log.Error("canal: gravando pendente (segue só em memória)", "err", err)
	}
	e.enviar(m)
}

// Controlar aplica pausar/retomar/cancelar.
func (e *Executor) Controlar(c *agentev1.Controle) {
	e.mu.Lock()
	x, ok := e.rodando[c.GetTarefaId()]
	e.mu.Unlock()
	if !ok {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	switch c.GetAcao() {
	case agentev1.Controle_PAUSAR:
		x.pausada, x.estado = true, "pausada"
	case agentev1.Controle_RETOMAR:
		if x.pausada {
			x.pausada, x.estado = false, "executando"
			close(x.retomar)
			x.retomar = make(chan struct{})
		}
	case agentev1.Controle_CANCELAR:
		x.cancel()
	}
}

// CancelarTudo para as tarefas em andamento (o agente está desligando).
func (e *Executor) CancelarTudo() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, x := range e.rodando {
		x.cancel()
	}
}

func (x *execucao) proximoSeq() int64 {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.seq++
	return x.seq
}

type relator struct {
	e *Executor
	x *execucao
}

func (r *relator) Evento(etapa string, nivel agentev1.EventoTarefa_Nivel, msg string, progresso float64, met map[string]float64) {
	r.e.guardarEEnviar(&agentev1.MsgAgente{Corpo: &agentev1.MsgAgente_Evento{Evento: &agentev1.EventoTarefa{
		TarefaId: r.x.tarefa.GetId(), Seq: r.x.proximoSeq(), Em: r.e.agora().UnixMilli(), Etapa: etapa,
		Nivel: nivel, Mensagem: msg, Progresso: progresso, Metricas: met,
	}}})
}

func (r *relator) Checkpoint(tabela, ultimaChave string, linhas int64) {
	r.e.guardarEEnviar(&agentev1.MsgAgente{Corpo: &agentev1.MsgAgente_Checkpoint{Checkpoint: &agentev1.Checkpoint{
		TarefaId: r.x.tarefa.GetId(), Seq: r.x.proximoSeq(), Tabela: tabela, UltimaChave: ultimaChave, Linhas: linhas,
	}}})
}

func (r *relator) Pausa(ctx context.Context) error {
	for {
		r.x.mu.Lock()
		pausada, retomar := r.x.pausada, r.x.retomar
		r.x.mu.Unlock()
		if !pausada {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-retomar:
		}
	}
}

func (r *relator) Credencial(nome string) ([]byte, error) {
	for _, c := range r.x.tarefa.GetCredenciais() {
		if c.GetNome() == nome {
			return r.e.identidade.abrirSelo(c, "tarefa:"+r.x.tarefa.GetId()+":"+nome, r.e.agora())
		}
	}
	return nil, fmt.Errorf("a tarefa não trouxe a credencial %q", nome)
}

// ---------------------------------------------------------------- diagnostico.eco

// Eco é a tarefa de diagnóstico do canal: não toca em nada, só emite passos de
// progresso. Prova de ponta a ponta que tarefa, eventos ao vivo, pausa e cancelamento
// funcionam entre o painel e este servidor.
func Eco(ctx context.Context, t *agentev1.Tarefa, r Relator) (any, error) {
	esp := struct {
		Passos      int    `json:"passos"`
		IntervaloMS int    `json:"intervalo_ms"`
		Mensagem    string `json:"mensagem"`
	}{Passos: 5, IntervaloMS: 1000, Mensagem: "eco do agente"}
	if len(t.GetEspecificacao()) > 0 {
		if err := json.Unmarshal(t.GetEspecificacao(), &esp); err != nil {
			return nil, fmt.Errorf("especificação inválida: %w", err)
		}
	}
	esp.Passos = min(max(esp.Passos, 1), 100)
	esp.IntervaloMS = min(max(esp.IntervaloMS, 0), 60_000)
	inicio := time.Now()
	for i := 1; i <= esp.Passos; i++ {
		if err := r.Pausa(ctx); err != nil {
			return map[string]any{"passos_feitos": i - 1}, err
		}
		select {
		case <-ctx.Done():
			return map[string]any{"passos_feitos": i - 1}, ctx.Err()
		case <-time.After(time.Duration(esp.IntervaloMS) * time.Millisecond):
		}
		r.Evento("eco", agentev1.EventoTarefa_INFO, fmt.Sprintf("%s — passo %d de %d", esp.Mensagem, i, esp.Passos),
			float64(i)*100/float64(esp.Passos), map[string]float64{"passo": float64(i)})
	}
	return map[string]any{"passos_feitos": esp.Passos, "duracao_ms": time.Since(inicio).Milliseconds()}, nil
}

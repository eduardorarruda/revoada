package canal

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
)

func evento(tarefa string, seq int64) *agentev1.MsgAgente {
	return &agentev1.MsgAgente{Corpo: &agentev1.MsgAgente_Evento{Evento: &agentev1.EventoTarefa{TarefaId: tarefa, Seq: seq}}}
}

func TestPendentesSobrevivemAoReinicio(t *testing.T) {
	dir := t.TempDir()
	p, err := AbrirPendentes(dir)
	if err != nil {
		t.Fatal(err)
	}
	for seq := int64(1); seq <= 3; seq++ {
		if err := p.Adicionar(evento("tf_a", seq)); err != nil {
			t.Fatal(err)
		}
	}
	_ = p.Adicionar(evento("tf_b", 1))
	if err := p.Confirmar("tf_a", 2); err != nil {
		t.Fatal(err)
	}

	// "reinício": abre de novo do disco
	p2, err := AbrirPendentes(dir)
	if err != nil {
		t.Fatal(err)
	}
	todos := p2.Todos()
	if len(todos) != 2 || todos[0].GetEvento().GetSeq() != 3 || todos[1].GetEvento().GetTarefaId() != "tf_b" {
		t.Fatalf("pendentes depois do reinício: %v", todos)
	}
	_ = p2.Confirmar("tf_a", 3)
	_ = p2.Confirmar("tf_b", 1)
	if p2.Quantos() != 0 {
		t.Fatalf("deveria esvaziar, sobrou %d", p2.Quantos())
	}
	if err := p2.Adicionar(&agentev1.MsgAgente{Corpo: &agentev1.MsgAgente_Batimento{}}); err == nil {
		t.Fatal("batimento não vai para os pendentes")
	}
}

type coletor struct {
	mu   sync.Mutex
	msgs []*agentev1.MsgAgente
}

func (c *coletor) enviar(m *agentev1.MsgAgente) {
	c.mu.Lock()
	c.msgs = append(c.msgs, m)
	c.mu.Unlock()
}

func (c *coletor) resultado() *agentev1.ResultadoTarefa {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range c.msgs {
		if r := m.GetResultado(); r != nil {
			return r
		}
	}
	return nil
}

func (c *coletor) eventos() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, m := range c.msgs {
		if m.GetEvento() != nil {
			n++
		}
	}
	return n
}

func novoExecutorTeste(t *testing.T, permitidas ...string) (*Executor, ed25519.PrivateKey, *coletor) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pend, err := AbrirPendentes(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := &coletor{}
	e := NovoExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)), &Identidade{PubAssinatura: pub}, permitidas, pend, c.enviar)
	return e, priv, c
}

func tarefaAssinada(t *testing.T, priv ed25519.PrivateKey, id, tipo, esp string) *agentev1.Tarefa {
	t.Helper()
	tf := &agentev1.Tarefa{Id: id, Tipo: tipo, Especificacao: []byte(esp), ExpiraEm: time.Now().Add(time.Hour).Unix()}
	if err := agentev1.AssinarTarefa(priv, tf); err != nil {
		t.Fatal(err)
	}
	return tf
}

func esperarResultado(t *testing.T, c *coletor) *agentev1.ResultadoTarefa {
	t.Helper()
	for i := 0; i < 300; i++ {
		if r := c.resultado(); r != nil {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("sem resultado")
	return nil
}

func TestEcoRodaAteOFim(t *testing.T) {
	e, priv, c := novoExecutorTeste(t, "diagnostico.eco")
	e.Receber(tarefaAssinada(t, priv, "tf_1", "diagnostico.eco", `{"passos":3,"intervalo_ms":1}`))
	r := esperarResultado(t, c)
	if r.GetEstado() != agentev1.ResultadoTarefa_SUCESSO || c.eventos() != 3 || r.GetSeq() != 4 {
		t.Fatalf("resultado %v, %d eventos", r, c.eventos())
	}
	// reenvio da mesma tarefa (painel reconectou) não roda de novo
	e.Receber(tarefaAssinada(t, priv, "tf_1", "diagnostico.eco", `{"passos":3,"intervalo_ms":1}`))
	time.Sleep(30 * time.Millisecond)
	if c.eventos() != 3 {
		t.Fatalf("tarefa repetida rodou de novo: %d eventos", c.eventos())
	}
}

func TestTarefaSemAssinaturaValidaEhRecusada(t *testing.T) {
	e, _, c := novoExecutorTeste(t, "diagnostico.eco")
	_, outro, _ := ed25519.GenerateKey(rand.Reader) // painel impostor
	e.Receber(tarefaAssinada(t, outro, "tf_x", "diagnostico.eco", `{}`))
	if r := esperarResultado(t, c); r.GetEstado() != agentev1.ResultadoTarefa_RECUSADA {
		t.Fatalf("assinatura de outro painel deveria ser recusada: %v", r)
	}
}

func TestTipoNaoLiberadoNaConfigEhRecusado(t *testing.T) {
	e, priv, c := novoExecutorTeste(t) // nada liberado
	e.Receber(tarefaAssinada(t, priv, "tf_y", "diagnostico.eco", `{}`))
	if r := esperarResultado(t, c); r.GetEstado() != agentev1.ResultadoTarefa_RECUSADA {
		t.Fatalf("tipo não liberado deveria ser recusado: %v", r)
	}
	if len(e.Capacidades()) != 0 {
		t.Fatal("sem liberação, nenhuma capacidade")
	}
}

func TestPausarRetomarECancelar(t *testing.T) {
	e, priv, c := novoExecutorTeste(t, "diagnostico.eco")
	e.Receber(tarefaAssinada(t, priv, "tf_p", "diagnostico.eco", `{"passos":50,"intervalo_ms":20}`))
	time.Sleep(60 * time.Millisecond)
	e.Controlar(&agentev1.Controle{TarefaId: "tf_p", Acao: agentev1.Controle_PAUSAR})
	time.Sleep(50 * time.Millisecond)
	parado := c.eventos()
	time.Sleep(120 * time.Millisecond)
	if c.eventos() > parado+1 { // no máximo o passo que já estava no meio
		t.Fatalf("pausada mas continuou: %d → %d", parado, c.eventos())
	}
	if est := e.EmAndamento(); len(est) != 1 || est[0].GetEstado() != "pausada" {
		t.Fatalf("em andamento: %v", est)
	}
	e.Controlar(&agentev1.Controle{TarefaId: "tf_p", Acao: agentev1.Controle_RETOMAR})
	time.Sleep(80 * time.Millisecond)
	if c.eventos() <= parado+1 {
		t.Fatal("retomar não voltou a andar")
	}
	e.Controlar(&agentev1.Controle{TarefaId: "tf_p", Acao: agentev1.Controle_CANCELAR})
	if r := esperarResultado(t, c); r.GetEstado() != agentev1.ResultadoTarefa_CANCELADA {
		t.Fatalf("cancelar: %v", r)
	}
	if len(e.EmAndamento()) != 0 {
		t.Fatal("cancelada continua em andamento")
	}
}

func TestIdentidadeSalvarECarregar(t *testing.T) {
	dir := t.TempDir()
	if _, err := Carregar(dir); err != ErrSemIdentidade {
		t.Fatalf("sem arquivo: %v", err)
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	id := &Identidade{AgenteID: "ag_1", Painel: "127.0.0.1:7443", CertificadoDER: []byte{1}, PubAssinatura: pub}
	if err := id.Salvar(dir); err != nil {
		t.Fatal(err)
	}
	lida, err := Carregar(dir)
	if err != nil || lida.AgenteID != "ag_1" {
		t.Fatalf("%+v %v", lida, err)
	}
}

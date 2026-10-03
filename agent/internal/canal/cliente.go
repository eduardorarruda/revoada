package canal

import (
	"context"
	"crypto/x509"
	"log/slog"
	"math/rand/v2"
	"path/filepath"
	"sync"
	"time"

	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

const (
	intervaloBatimento = 10 * time.Second
	esperaMinima       = time.Second
	esperaMaxima       = 60 * time.Second
	// renovar o certificado quando faltar 1/3 da validade (30 dias → 10 dias antes).
	antecedenciaRenovacao = 10 * 24 * time.Hour
)

// Cliente mantém a conexão com o painel viva, para sempre, até o ctx acabar.
type Cliente struct {
	log  *slog.Logger
	dir  string
	ap   Apresentacao
	pend *Pendentes
	exec *Executor

	mu         sync.Mutex
	id         *Identidade
	envio      chan *agentev1.MsgAgente
	chaveNova  []byte // chave do pedido de renovação em andamento
	lerEsquema LeitorEsquema
	seqBat     int64
}

// NovoCliente monta o cliente a partir da identidade gravada em `dir`.
func NovoCliente(log *slog.Logger, dir string, ap Apresentacao, permitidas []string) (*Cliente, error) {
	id, err := Carregar(dir)
	if err != nil {
		return nil, err
	}
	pend, err := AbrirPendentes(filepath.Join(dir, "pendentes"))
	if err != nil {
		return nil, err
	}
	c := &Cliente{log: log, dir: dir, ap: ap, pend: pend, id: id, envio: make(chan *agentev1.MsgAgente, 1024)}
	c.exec = NovoExecutor(log, id, permitidas, pend, c.mandar)
	return c, nil
}

// PedidoBanco é o banco que o painel pediu para ler (a senha já aberta, só em memória).
type PedidoBanco struct {
	Motor, Endereco, Banco, Usuario, Senha string
	Opcoes                                 map[string]string
}

// LeitorEsquema lê a estrutura de um banco e devolve o JSON do schema neutro.
// Injetado pelo main (o pacote canal não conhece os drivers de banco).
type LeitorEsquema func(ctx context.Context, p PedidoBanco) ([]byte, error)

// UsarLeitorEsquema liga a leitura de schema (Etapa 3).
func (c *Cliente) UsarLeitorEsquema(l LeitorEsquema) { c.lerEsquema = l }

// responderEsquema confere a assinatura do painel, abre a senha selada e lê a
// estrutura. A leitura de schema é só leitura, mas ainda assim exige o tipo
// "migracao.esquema" liberado na configuração local.
func (c *Cliente) responderEsquema(p *agentev1.PedidoEsquema) {
	responder := func(esq []byte, erro string) {
		c.mandar(&agentev1.MsgAgente{Corpo: &agentev1.MsgAgente_Esquema{Esquema: &agentev1.RespostaEsquema{
			PedidoId: p.GetPedidoId(), Esquema: esq, Erro: erro,
		}}})
	}
	c.mu.Lock()
	id := c.id
	c.mu.Unlock()
	if err := agentev1.VerificarPedidoEsquema(id.PubAssinatura, p); err != nil {
		c.log.Warn("canal: pedido de schema recusado", "motivo", err)
		responder(nil, "pedido recusado: "+err.Error())
		return
	}
	if !c.exec.permitidas["migracao.esquema"] || c.lerEsquema == nil {
		responder(nil, "a leitura de schema (migracao.esquema) não está liberada neste agente (canal.tarefas_permitidas)")
		return
	}
	senha, err := id.abrirSelo(p.GetCredencial(), "esquema:"+p.GetPedidoId(), time.Now())
	if err != nil {
		responder(nil, "credencial inválida ou vencida")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	esq, err := c.lerEsquema(ctx, PedidoBanco{Motor: p.GetMotor(), Endereco: p.GetEndereco(), Banco: p.GetBanco(),
		Usuario: p.GetUsuario(), Senha: string(senha), Opcoes: p.GetOpcoes()})
	for i := range senha {
		senha[i] = 0
	}
	if err != nil {
		c.log.Warn("canal: leitura de schema falhou", "pedido", p.GetPedidoId(), "err", err)
		responder(nil, err.Error())
		return
	}
	c.log.Info("canal: schema lido", "pedido", p.GetPedidoId(), "motor", p.GetMotor(), "bytes", len(esq))
	responder(esq, "")
}

// Executor expõe o executor (para registrar tipos de tarefa de outras etapas).
func (c *Cliente) Executor() *Executor { return c.exec }

// mandar enfileira sem travar; se a fila encher (painel fora há muito tempo), a
// mensagem continua nos pendentes em disco e sai na reconexão.
func (c *Cliente) mandar(m *agentev1.MsgAgente) {
	select {
	case c.envio <- m:
	default:
	}
}

// Rodar conecta e reconecta com espera exponencial + jitter (1 s → 60 s).
func (c *Cliente) Rodar(ctx context.Context) {
	espera := esperaMinima
	for ctx.Err() == nil {
		inicio := time.Now()
		err := c.sessao(ctx)
		if ctx.Err() != nil {
			break
		}
		if time.Since(inicio) > time.Minute {
			espera = esperaMinima // a conexão estava saudável: volta a tentar rápido
		}
		c.log.Warn("canal: conexão com o painel caiu; tentando de novo", "err", err, "em", espera)
		jitter := time.Duration(rand.Int64N(int64(espera) / 2))
		select {
		case <-ctx.Done():
		case <-time.After(espera/2 + jitter):
		}
		espera = min(espera*2, esperaMaxima)
	}
	c.exec.CancelarTudo()
}

func (c *Cliente) sessao(ctx context.Context) error {
	c.mu.Lock()
	id := c.id
	c.mu.Unlock()
	cfg, err := id.configTLS()
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(id.Painel, grpc.WithTransportCredentials(credentials.NewTLS(cfg)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 10 * time.Second, PermitWithoutStream: true}))
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st, err := agentev1.NewCanalClient(conn).Conectar(ctx)
	if err != nil {
		return err
	}
	if err := st.Send(&agentev1.MsgAgente{Corpo: &agentev1.MsgAgente_Ola{Ola: &agentev1.Ola{
		AgenteId: id.AgenteID, Versao: c.ap.Versao, So: c.ap.SO, Arch: c.ap.Arch, Hostname: c.ap.Hostname,
		Capacidades: c.exec.Capacidades(), EmAndamento: c.exec.EmAndamento(),
	}}}); err != nil {
		return err
	}
	// Reenvia o que o painel ainda não confirmou (ele ignora repetidos).
	for _, m := range c.pend.Todos() {
		if err := st.Send(m); err != nil {
			return err
		}
	}
	c.log.Info("canal: conectado ao painel", "painel", id.Painel, "agente", id.AgenteID, "pendentes_reenviados", c.pend.Quantos())

	erros := make(chan error, 2)
	go func() { erros <- c.enviarLoop(ctx, st) }()
	go func() { erros <- c.receberLoop(st) }()
	return <-erros
}

func (c *Cliente) enviarLoop(ctx context.Context, st agentev1.Canal_ConectarClient) error {
	t := time.NewTicker(intervaloBatimento)
	defer t.Stop()
	c.talvezRenovar(st)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case m := <-c.envio:
			if err := st.Send(m); err != nil {
				return err
			}
		case <-t.C:
			c.seqBat++
			if err := st.Send(&agentev1.MsgAgente{Corpo: &agentev1.MsgAgente_Batimento{Batimento: &agentev1.Batimento{
				Seq: c.seqBat, Em: time.Now().UnixMilli(), TarefasAtivas: int32(len(c.exec.EmAndamento())),
			}}}); err != nil {
				return err
			}
			c.talvezRenovar(st)
		}
	}
}

func (c *Cliente) receberLoop(st agentev1.Canal_ConectarClient) error {
	for {
		m, err := st.Recv()
		if err != nil {
			return err
		}
		switch x := m.GetCorpo().(type) {
		case *agentev1.MsgPainel_Tarefa:
			c.exec.Receber(x.Tarefa)
		case *agentev1.MsgPainel_Controle:
			c.exec.Controlar(x.Controle)
		case *agentev1.MsgPainel_Ack:
			if err := c.pend.Confirmar(x.Ack.GetTarefaId(), x.Ack.GetSeq()); err != nil {
				c.log.Warn("canal: limpando pendentes confirmados", "err", err)
			}
		case *agentev1.MsgPainel_Renovacao:
			c.aplicarRenovacao(x.Renovacao)
		case *agentev1.MsgPainel_PedidoEsquema:
			go c.responderEsquema(x.PedidoEsquema)
		}
	}
}

// talvezRenovar pede um certificado novo quando falta pouco para o atual vencer.
func (c *Cliente) talvezRenovar(st agentev1.Canal_ConectarClient) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.chaveNova != nil || time.Until(time.Unix(c.id.ValidoAte, 0)) > antecedenciaRenovacao {
		return
	}
	chave, csr, err := novaChaveTLS(c.ap.Hostname)
	if err != nil {
		c.log.Error("canal: gerando chave para renovação", "err", err)
		return
	}
	c.chaveNova = chave
	if err := st.Send(&agentev1.MsgAgente{Corpo: &agentev1.MsgAgente_Renovacao{Renovacao: &agentev1.PedidoRenovacao{CsrDer: csr}}}); err != nil {
		c.chaveNova = nil
	}
	c.log.Info("canal: pedindo renovação do certificado", "vence_em", time.Unix(c.id.ValidoAte, 0))
}

func (c *Cliente) aplicarRenovacao(r *agentev1.RespostaRenovacao) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.chaveNova == nil {
		return
	}
	if _, err := x509.ParseCertificate(r.GetCertificadoDer()); err != nil {
		c.log.Error("canal: certificado renovado inválido", "err", err)
		c.chaveNova = nil
		return
	}
	novo := *c.id
	novo.ChaveTLS, novo.CertificadoDER, novo.ValidoAte = c.chaveNova, r.GetCertificadoDer(), r.GetValidoAte()
	if err := novo.Salvar(c.dir); err != nil {
		c.log.Error("canal: gravando certificado renovado", "err", err)
		return
	}
	c.id, c.chaveNova = &novo, nil
	c.log.Info("canal: certificado renovado", "valido_ate", time.Unix(novo.ValidoAte, 0))
}

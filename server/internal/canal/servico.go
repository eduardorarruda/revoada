package canal

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
	"github.com/eduardorarruda/revoada/server/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Repositorio é o que o canal precisa do banco (implementado por *store.Store).
type Repositorio interface {
	guardaChaves
	CriarTokenAgente(ctx context.Context, hash, rotulo, criadoPor string, expira time.Time) error
	ConsumirTokenAgente(ctx context.Context, hash, agenteID string) (string, error)
	RegistrarAgente(ctx context.Context, a store.Agente) error
	AgentePorID(ctx context.Context, id string) (store.Agente, error)
	ListarAgentes(ctx context.Context) ([]store.Agente, error)
	TrocarCertificadoAgente(ctx context.Context, id, serial string, validoAte time.Time) error
	RevogarAgente(ctx context.Context, id string) error
	AtualizarApresentacaoAgente(ctx context.Context, id, hostname, so, arch, versao string, capacidades []string) error
	MarcarPresencaAgente(ctx context.Context, id, estado string, vistoEm time.Time) error
	CriarTarefa(ctx context.Context, t store.Tarefa) error
	TarefaPorID(ctx context.Context, id string) (store.Tarefa, error)
	ListarTarefas(ctx context.Context, f store.FiltroTarefas) ([]store.Tarefa, error)
	TarefasEmAberto(ctx context.Context, agenteID string) ([]store.Tarefa, error)
	MudarEstadoTarefa(ctx context.Context, id, estado string) error
	FinalizarTarefa(ctx context.Context, id, estado, erro string, resumo json.RawMessage) error
	GravarEventoTarefa(ctx context.Context, e store.EventoTarefa) (bool, error)
	EventosTarefa(ctx context.Context, tarefaID string, aposSeq int64) ([]store.EventoTarefa, error)
	GravarCheckpoint(ctx context.Context, c store.Checkpoint) error
}

// Presença do agente (ARQUITETURA §7): batimento a cada 10 s.
const (
	Online   = "online"
	Instavel = "instavel" // 1–2 batimentos perdidos
	Offline  = "offline"  // 3 ou mais

	IntervaloBatimento = 10 * time.Second
	limiteOnline       = 15 * time.Second
	limiteInstavel     = 35 * time.Second
)

// ClassificarPresenca decide o estado pelo tempo desde o último sinal.
func ClassificarPresenca(ultimo, agora time.Time) string {
	switch d := agora.Sub(ultimo); {
	case d < limiteOnline:
		return Online
	case d < limiteInstavel:
		return Instavel
	default:
		return Offline
	}
}

// Atualizacao é o que a tela recebe ao vivo de uma tarefa.
type Atualizacao struct {
	Evento     *store.EventoTarefa `json:"evento,omitempty"`
	Tarefa     *store.Tarefa       `json:"tarefa,omitempty"`
	Checkpoint *store.Checkpoint   `json:"checkpoint,omitempty"`
}

// Servico implementa o servidor gRPC do canal e a fila de tarefas.
type Servico struct {
	agentev1.UnimplementedCanalServer

	repo Repositorio
	ca   *Autoridade
	log  *slog.Logger
	// AoMudarPresenca é chamado quando um agente muda de online/instável/offline
	// (o main liga isto às notificações). Pode ser nil.
	AoMudarPresenca func(a store.Agente, de, para string)
	// Seladora sela as credenciais de que a tarefa precisa para a chave do agente
	// (Etapa 3: conexões de banco). nil = tarefa sem credenciais.
	Seladora func(ctx context.Context, a store.Agente, t store.Tarefa) ([]*agentev1.CredencialSelada, error)
	// AoFinalizar é chamado quando uma tarefa termina (deploy anota o gráfico e avisa
	// falha). Roda numa goroutine própria. Pode ser nil.
	AoFinalizar func(a store.Agente, t store.Tarefa)

	agora func() time.Time

	mu         sync.Mutex
	sessoes    map[string]*sessao // por agente (inclusive desconectados: guardam o último sinal)
	assinantes map[string]map[chan Atualizacao]struct{}
	esperas    map[string]chan *agentev1.RespostaEsquema // pedidos de schema aguardando resposta
}

type sessao struct {
	agenteID  string
	envio     chan *agentev1.MsgPainel
	ultimo    time.Time
	estado    string
	conectado bool
	encerrar  context.CancelFunc
}

// NovoServico monta o serviço.
func NovoServico(repo Repositorio, ca *Autoridade, log *slog.Logger) *Servico {
	return &Servico{
		repo: repo, ca: ca, log: log, agora: time.Now,
		sessoes:    map[string]*sessao{},
		assinantes: map[string]map[chan Atualizacao]struct{}{},
		esperas:    map[string]chan *agentev1.RespostaEsquema{},
	}
}

// ---------------------------------------------------------------- inscrição

// ValidadeTokenPadrao é quanto um token de inscrição vale se ninguém disser outro valor.
const ValidadeTokenPadrao = 24 * time.Hour

// CriarToken gera um token de inscrição de uso único (só o hash vai para o banco).
func (s *Servico) CriarToken(ctx context.Context, rotulo, criadoPor string, validade time.Duration) (string, time.Time, error) {
	if validade <= 0 || validade > 30*24*time.Hour {
		validade = ValidadeTokenPadrao
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	segredo := base64.RawURLEncoding.EncodeToString(b)
	expira := s.agora().Add(validade)
	if err := s.repo.CriarTokenAgente(ctx, hashSegredo(segredo), rotulo, criadoPor, expira); err != nil {
		return "", time.Time{}, err
	}
	return agentev1.TokenInscricao{Segredo: segredo, ImpressaoCA: s.ca.ImpressaoCA()}.String(), expira, nil
}

func hashSegredo(s string) string {
	h := sha256.Sum256([]byte("revoada/token-agente|" + s))
	return hex.EncodeToString(h[:])
}

// Inscrever troca o token de uso único pela identidade do agente.
func (s *Servico) Inscrever(ctx context.Context, req *agentev1.PedidoInscricao) (*agentev1.RespostaInscricao, error) {
	tok, err := agentev1.LerToken(req.GetToken())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "token de inscrição malformado")
	}
	if !agentev1.MesmaImpressao(tok.ImpressaoCA, s.ca.ImpressaoCA()) {
		return nil, status.Error(codes.PermissionDenied, "este token foi gerado por outro painel")
	}
	// Valida tudo o que dá ANTES de gastar o token.
	if _, err := ValidarCSR(req.GetCsrDer()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if _, err := ecdh.X25519().NewPublicKey(req.GetChaveSelo()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "chave de selo inválida")
	}
	id := novoID("ag")
	rotulo, err := s.repo.ConsumirTokenAgente(ctx, hashSegredo(tok.Segredo), id)
	if err != nil {
		if errors.Is(err, store.ErrTokenInvalido) {
			return nil, status.Error(codes.PermissionDenied, err.Error())
		}
		s.log.Error("canal: consumindo token", "err", err)
		return nil, status.Error(codes.Internal, "erro interno")
	}
	der, serial, ate, err := s.ca.EmitirAgente(req.GetCsrDer(), id, s.agora())
	if err != nil {
		return nil, status.Error(codes.Internal, "erro ao emitir o certificado")
	}
	if err := s.repo.RegistrarAgente(ctx, store.Agente{
		ID: id, Rotulo: rotulo, Hostname: req.GetHostname(), SO: req.GetSo(), Arch: req.GetArch(),
		Versao: req.GetVersao(), ChaveSelo: req.GetChaveSelo(), CertSerial: serial, CertValidoAte: ate,
	}); err != nil {
		s.log.Error("canal: registrando agente", "err", err)
		return nil, status.Error(codes.Internal, "erro interno")
	}
	s.log.Info("canal: agente inscrito", "agente", id, "hostname", req.GetHostname(), "so", req.GetSo(), "rotulo", rotulo)
	return &agentev1.RespostaInscricao{
		AgenteId: id, CertificadoDer: der, CaDer: s.ca.CADER(),
		ChaveAssinaturaPainel: s.ca.ChavePublicaAssinatura(), ValidoAte: ate.Unix(),
	}, nil
}

// ---------------------------------------------------------------- conexão

// autenticar extrai o agente do certificado de cliente verificado pelo TLS.
func (s *Servico) autenticar(ctx context.Context) (store.Agente, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return store.Agente{}, errors.New("sem informação de conexão")
	}
	info, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(info.State.VerifiedChains) == 0 || len(info.State.VerifiedChains[0]) == 0 {
		return store.Agente{}, errors.New("certificado de cliente ausente")
	}
	cert := info.State.VerifiedChains[0][0]
	a, err := s.repo.AgentePorID(ctx, cert.Subject.CommonName)
	if err != nil {
		return store.Agente{}, errors.New("agente desconhecido")
	}
	if a.Revogado {
		return store.Agente{}, errors.New("agente revogado")
	}
	if SerialHex(cert.SerialNumber) != a.CertSerial {
		return store.Agente{}, errors.New("certificado substituído; use o vigente")
	}
	return a, nil
}

// Conectar é o stream de trabalho com um agente.
func (s *Servico) Conectar(stream agentev1.Canal_ConectarServer) error {
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	ag, err := s.autenticar(ctx)
	if err != nil {
		return status.Error(codes.Unauthenticated, err.Error())
	}
	primeira, err := stream.Recv()
	if err != nil {
		return err
	}
	ola := primeira.GetOla()
	if ola == nil || ola.GetAgenteId() != ag.ID {
		return status.Error(codes.InvalidArgument, "a conexão começa com Olá do próprio agente")
	}
	if err := s.repo.AtualizarApresentacaoAgente(ctx, ag.ID, ola.GetHostname(), ola.GetSo(), ola.GetArch(),
		ola.GetVersao(), ola.GetCapacidades()); err != nil {
		s.log.Error("canal: gravando apresentação", "agente", ag.ID, "err", err)
	}

	ses := s.abrirSessao(ag, cancel)
	defer s.fecharSessao(ses)
	s.log.Info("canal: agente conectado", "agente", ag.ID, "hostname", ola.GetHostname())

	erroEnvio := make(chan error, 1)
	go func() { erroEnvio <- s.enviarLoop(ctx, stream, ses) }()

	s.reconciliar(ctx, ag, ses, ola.GetEmAndamento())

	recebido := make(chan error, 1)
	go func() { recebido <- s.receberLoop(ctx, stream, ag, ses) }()
	select {
	case err := <-recebido:
		return err
	case err := <-erroEnvio:
		return err
	case <-ctx.Done():
		return status.Error(codes.Canceled, "conexão encerrada pelo painel")
	}
}

func (s *Servico) enviarLoop(ctx context.Context, stream agentev1.Canal_ConectarServer, ses *sessao) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case m := <-ses.envio:
			if err := stream.Send(m); err != nil {
				return err
			}
		}
	}
}

func (s *Servico) receberLoop(ctx context.Context, stream agentev1.Canal_ConectarServer, ag store.Agente, ses *sessao) error {
	for {
		m, err := stream.Recv()
		if err != nil {
			return err
		}
		s.sinalDeVida(ses)
		switch c := m.GetCorpo().(type) {
		case *agentev1.MsgAgente_Batimento:
			// o sinal de vida já foi registrado acima
		case *agentev1.MsgAgente_Evento:
			s.receberEvento(ctx, ses, c.Evento)
		case *agentev1.MsgAgente_Checkpoint:
			s.receberCheckpoint(ctx, ses, c.Checkpoint)
		case *agentev1.MsgAgente_Resultado:
			s.receberResultado(ctx, ag, ses, c.Resultado)
		case *agentev1.MsgAgente_Renovacao:
			s.renovar(ctx, ag, ses, c.Renovacao)
		case *agentev1.MsgAgente_Esquema:
			s.entregarEsquema(c.Esquema)
		case *agentev1.MsgAgente_Ack, *agentev1.MsgAgente_Ola:
			// nada a fazer
		}
	}
}

func ack(tarefa string, seq int64) *agentev1.MsgPainel {
	return &agentev1.MsgPainel{Corpo: &agentev1.MsgPainel_Ack{Ack: &agentev1.Confirmacao{TarefaId: tarefa, Seq: seq}}}
}

var niveis = map[agentev1.EventoTarefa_Nivel]string{
	agentev1.EventoTarefa_INFO: "info", agentev1.EventoTarefa_AVISO: "aviso", agentev1.EventoTarefa_ERRO: "erro",
}

func (s *Servico) receberEvento(ctx context.Context, ses *sessao, e *agentev1.EventoTarefa) {
	ev := store.EventoTarefa{
		TarefaID: e.GetTarefaId(), Seq: e.GetSeq(), Em: time.UnixMilli(e.GetEm()), Etapa: e.GetEtapa(),
		Nivel: niveis[e.GetNivel()], Mensagem: e.GetMensagem(), Progresso: e.GetProgresso(), Metricas: e.GetMetricas(),
	}
	if !s.tarefaDoAgente(ctx, ses.agenteID, ev.TarefaID) {
		return // evento de tarefa de outro agente: ignora (e não confirma)
	}
	novo, err := s.repo.GravarEventoTarefa(ctx, ev)
	if err != nil {
		s.log.Error("canal: gravando evento", "tarefa", ev.TarefaID, "seq", ev.Seq, "err", err)
		return // sem ack: o agente reenvia
	}
	// Publica antes de confirmar: quando o agente recebe o ack, a tela já foi avisada.
	if novo {
		s.publicar(ev.TarefaID, Atualizacao{Evento: &ev})
		if ev.Seq == 1 { // primeiro passo: a tarefa passou de "enviada" para "executando"
			s.publicarTarefa(ctx, ev.TarefaID)
		}
	}
	s.enviar(ses, ack(ev.TarefaID, ev.Seq))
}

// receberCheckpoint guarda o último lote confirmado de uma tabela. O agente grava o
// mesmo checkpoint no destino, na transação do lote — este é a cópia para a tela.
func (s *Servico) receberCheckpoint(ctx context.Context, ses *sessao, c *agentev1.Checkpoint) {
	if !s.tarefaDoAgente(ctx, ses.agenteID, c.GetTarefaId()) {
		return
	}
	cp := store.Checkpoint{TarefaID: c.GetTarefaId(), Tabela: c.GetTabela(), UltimaChave: c.GetUltimaChave(),
		Linhas: c.GetLinhas(), Seq: c.GetSeq(), AtualizadoEm: s.agora()}
	if err := s.repo.GravarCheckpoint(ctx, cp); err != nil {
		s.log.Error("canal: gravando checkpoint", "tarefa", cp.TarefaID, "tabela", cp.Tabela, "err", err)
		return // sem ack: o agente reenvia
	}
	s.publicar(cp.TarefaID, Atualizacao{Checkpoint: &cp})
	s.enviar(ses, ack(cp.TarefaID, cp.Seq))
}

var estadosResultado = map[agentev1.ResultadoTarefa_Estado]string{
	agentev1.ResultadoTarefa_SUCESSO: "sucesso", agentev1.ResultadoTarefa_FALHA: "falha",
	agentev1.ResultadoTarefa_CANCELADA: "cancelada", agentev1.ResultadoTarefa_RECUSADA: "recusada",
}

func (s *Servico) receberResultado(ctx context.Context, ag store.Agente, ses *sessao, r *agentev1.ResultadoTarefa) {
	estado, ok := estadosResultado[r.GetEstado()]
	if !ok || !s.tarefaDoAgente(ctx, ag.ID, r.GetTarefaId()) {
		return
	}
	var resumo json.RawMessage
	if len(r.GetResumo()) > 0 && json.Valid(r.GetResumo()) {
		resumo = r.GetResumo()
	}
	if err := s.repo.FinalizarTarefa(ctx, r.GetTarefaId(), estado, r.GetErro(), resumo); err != nil {
		s.log.Error("canal: finalizando tarefa", "tarefa", r.GetTarefaId(), "err", err)
		return
	}
	s.publicarTarefa(ctx, r.GetTarefaId())
	s.enviar(ses, ack(r.GetTarefaId(), r.GetSeq()))
	if s.AoFinalizar != nil {
		if t, err := s.repo.TarefaPorID(ctx, r.GetTarefaId()); err == nil {
			go s.AoFinalizar(ag, t)
		}
	}
	s.log.Info("canal: tarefa finalizada", "tarefa", r.GetTarefaId(), "agente", ag.ID, "estado", estado, "erro", r.GetErro())
}

func (s *Servico) tarefaDoAgente(ctx context.Context, agenteID, tarefaID string) bool {
	t, err := s.repo.TarefaPorID(ctx, tarefaID)
	return err == nil && t.AgenteID == agenteID
}

func (s *Servico) renovar(ctx context.Context, ag store.Agente, ses *sessao, p *agentev1.PedidoRenovacao) {
	der, serial, ate, err := s.ca.EmitirAgente(p.GetCsrDer(), ag.ID, s.agora())
	if err != nil {
		s.log.Warn("canal: renovação recusada", "agente", ag.ID, "err", err)
		return
	}
	if err := s.repo.TrocarCertificadoAgente(ctx, ag.ID, serial, ate); err != nil {
		s.log.Error("canal: gravando certificado renovado", "agente", ag.ID, "err", err)
		return
	}
	s.log.Info("canal: certificado do agente renovado", "agente", ag.ID, "valido_ate", ate)
	s.enviar(ses, &agentev1.MsgPainel{Corpo: &agentev1.MsgPainel_Renovacao{
		Renovacao: &agentev1.RespostaRenovacao{CertificadoDer: der, ValidoAte: ate.Unix()},
	}})
}

// ---------------------------------------------------------------- sessões e presença

func (s *Servico) abrirSessao(ag store.Agente, encerrar context.CancelFunc) *sessao {
	s.mu.Lock()
	defer s.mu.Unlock()
	if velha, ok := s.sessoes[ag.ID]; ok && velha.conectado && velha.encerrar != nil {
		velha.encerrar() // uma conexão por agente: a nova vence
	}
	ses := &sessao{agenteID: ag.ID, envio: make(chan *agentev1.MsgPainel, 256), ultimo: s.agora(),
		estado: Online, conectado: true, encerrar: encerrar}
	anterior := ag.Estado
	s.sessoes[ag.ID] = ses
	go s.registrarPresenca(ag, anterior, Online)
	return ses
}

func (s *Servico) fecharSessao(ses *sessao) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if atual, ok := s.sessoes[ses.agenteID]; ok && atual == ses {
		atual.conectado = false // mantém o último sinal: o monitor decide instável/offline
	}
}

func (s *Servico) sinalDeVida(ses *sessao) {
	s.mu.Lock()
	ses.ultimo = s.agora()
	s.mu.Unlock()
}

// MonitorarPresenca reclassifica os agentes a cada `intervalo` até o ctx acabar.
func (s *Servico) MonitorarPresenca(ctx context.Context, intervalo time.Duration) {
	s.semearPresenca(ctx)
	t := time.NewTicker(intervalo)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.VerificarPresenca(ctx)
		}
	}
}

// semearPresenca: agentes que o banco diz estarem online (de antes de o painel
// reiniciar) entram no monitor com o último sinal conhecido — se não voltarem,
// caem para offline e o alerta dispara.
func (s *Servico) semearPresenca(ctx context.Context) {
	ags, err := s.repo.ListarAgentes(ctx)
	if err != nil {
		s.log.Error("canal: lendo agentes para o monitor de presença", "err", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range ags {
		if a.Revogado || (a.Estado != Online && a.Estado != Instavel) || a.VistoEm == nil {
			continue
		}
		if _, ok := s.sessoes[a.ID]; !ok {
			s.sessoes[a.ID] = &sessao{agenteID: a.ID, envio: make(chan *agentev1.MsgPainel, 256), ultimo: *a.VistoEm, estado: a.Estado}
		}
	}
}

// VerificarPresenca aplica ClassificarPresenca a todas as sessões e avisa mudanças.
func (s *Servico) VerificarPresenca(ctx context.Context) {
	agora := s.agora()
	type mudanca struct {
		id, de, para string
		visto        time.Time
	}
	var mudancas []mudanca
	s.mu.Lock()
	for id, ses := range s.sessoes {
		novo := ClassificarPresenca(ses.ultimo, agora)
		if novo != ses.estado {
			mudancas = append(mudancas, mudanca{id, ses.estado, novo, ses.ultimo})
			ses.estado = novo
		}
	}
	s.mu.Unlock()
	for _, m := range mudancas {
		a, err := s.repo.AgentePorID(ctx, m.id)
		if err != nil {
			continue
		}
		s.registrarPresenca(a, m.de, m.para)
	}
}

func (s *Servico) registrarPresenca(a store.Agente, de, para string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.repo.MarcarPresencaAgente(ctx, a.ID, para, s.agora()); err != nil {
		s.log.Error("canal: gravando presença", "agente", a.ID, "err", err)
	}
	if de == para {
		return
	}
	nivel := slog.LevelInfo
	if para == Offline {
		nivel = slog.LevelWarn
	}
	s.log.Log(ctx, nivel, "canal: presença do agente mudou", "agente", a.ID, "hostname", a.Hostname, "de", de, "para", para)
	if s.AoMudarPresenca != nil {
		s.AoMudarPresenca(a, de, para)
	}
}

// EstadoAoVivo devolve a presença em memória (mais fresca que a do banco).
func (s *Servico) EstadoAoVivo(id string) (estado string, conectado bool, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ses, ok := s.sessoes[id]
	if !ok {
		return "", false, false
	}
	return ses.estado, ses.conectado, true
}

// UltimoSinal é o último batimento (ou mensagem) recebido do agente nesta sessão.
func (s *Servico) UltimoSinal(id string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ses, ok := s.sessoes[id]
	if !ok || ses.ultimo.IsZero() {
		return time.Time{}, false
	}
	return ses.ultimo, true
}

// Desconectar derruba a conexão de um agente (ex.: depois de revogar).
func (s *Servico) Desconectar(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ses, ok := s.sessoes[id]; ok && ses.encerrar != nil {
		ses.encerrar()
	}
	delete(s.sessoes, id)
}

func (s *Servico) sessaoConectada(id string) *sessao {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ses, ok := s.sessoes[id]; ok && ses.conectado {
		return ses
	}
	return nil
}

// enviar coloca a mensagem na fila da sessão sem travar o chamador; se a fila está
// cheia, a conexão está doente — derruba para o agente reconectar e reconciliar.
func (s *Servico) enviar(ses *sessao, m *agentev1.MsgPainel) bool {
	select {
	case ses.envio <- m:
		return true
	default:
		s.log.Warn("canal: fila de envio cheia, derrubando a conexão", "agente", ses.agenteID)
		if ses.encerrar != nil {
			ses.encerrar()
		}
		return false
	}
}

// ---------------------------------------------------------------- utilidades

func novoID(prefixo string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic("canal: sem aleatoriedade do sistema: " + err.Error())
	}
	return fmt.Sprintf("%s_%s", prefixo, hex.EncodeToString(b))
}

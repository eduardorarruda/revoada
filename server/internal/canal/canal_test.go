package canal

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/core/migracao/plano"
	"github.com/eduardorarruda/revoada/core/seguranca/cofre"
	"github.com/eduardorarruda/revoada/core/seguranca/selo"
	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
	"github.com/eduardorarruda/revoada/server/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------- repositório em memória

type memoria struct {
	mu          sync.Mutex
	chaves      map[string][]byte
	tokens      map[string]*tokenMem
	agentes     map[string]store.Agente
	tarefas     map[string]store.Tarefa
	eventos     map[string]map[int64]store.EventoTarefa
	checkpoints []store.Checkpoint
}

type tokenMem struct {
	rotulo string
	expira time.Time
	usado  bool
}

func novaMemoria() *memoria {
	return &memoria{chaves: map[string][]byte{}, tokens: map[string]*tokenMem{}, agentes: map[string]store.Agente{},
		tarefas: map[string]store.Tarefa{}, eventos: map[string]map[int64]store.EventoTarefa{}}
}

func (m *memoria) ChavePainel(_ context.Context, nome string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.chaves[nome]
	if !ok {
		return nil, store.ErrNotFound
	}
	return b, nil
}
func (m *memoria) SalvarChavePainel(_ context.Context, nome string, s []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.chaves[nome]; ok {
		return store.ErrConflito
	}
	m.chaves[nome] = s
	return nil
}
func (m *memoria) CriarTokenAgente(_ context.Context, hash, rotulo, _ string, expira time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[hash] = &tokenMem{rotulo: rotulo, expira: expira}
	return nil
}
func (m *memoria) ConsumirTokenAgente(_ context.Context, hash, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[hash]
	if !ok || t.usado || time.Now().After(t.expira) {
		return "", store.ErrTokenInvalido
	}
	t.usado = true
	return t.rotulo, nil
}
func (m *memoria) RegistrarAgente(_ context.Context, a store.Agente) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a.Estado = "nunca_conectou"
	m.agentes[a.ID] = a
	return nil
}
func (m *memoria) AgentePorID(_ context.Context, id string) (store.Agente, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.agentes[id]
	if !ok {
		return a, store.ErrNotFound
	}
	return a, nil
}
func (m *memoria) ListarAgentes(_ context.Context) ([]store.Agente, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.Agente
	for _, a := range m.agentes {
		out = append(out, a)
	}
	return out, nil
}
func (m *memoria) TrocarCertificadoAgente(_ context.Context, id, serial string, ate time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.agentes[id]
	a.CertSerial, a.CertValidoAte = serial, ate
	m.agentes[id] = a
	return nil
}
func (m *memoria) RevogarAgente(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.agentes[id]
	if !ok {
		return store.ErrNotFound
	}
	a.Revogado = true
	m.agentes[id] = a
	return nil
}
func (m *memoria) AtualizarApresentacaoAgente(_ context.Context, id, hostname, so, arch, versao string, caps []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.agentes[id]
	a.Hostname, a.SO, a.Arch, a.Versao, a.Capacidades = hostname, so, arch, versao, caps
	m.agentes[id] = a
	return nil
}
func (m *memoria) MarcarPresencaAgente(_ context.Context, id, estado string, visto time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.agentes[id]
	a.Estado, a.VistoEm = estado, &visto
	m.agentes[id] = a
	return nil
}
func (m *memoria) CriarTarefa(_ context.Context, t store.Tarefa) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tarefas[t.ID] = t
	return nil
}
func (m *memoria) TarefaPorID(_ context.Context, id string) (store.Tarefa, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tarefas[id]
	if !ok {
		return t, store.ErrNotFound
	}
	return t, nil
}
func (m *memoria) ListarTarefas(_ context.Context, _ store.FiltroTarefas) ([]store.Tarefa, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.Tarefa
	for _, t := range m.tarefas {
		out = append(out, t)
	}
	return out, nil
}
func (m *memoria) TarefasEmAberto(_ context.Context, agente string) ([]store.Tarefa, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.Tarefa
	for _, t := range m.tarefas {
		if t.AgenteID == agente && !final(t.Estado) {
			out = append(out, t)
		}
	}
	return out, nil
}
func (m *memoria) MudarEstadoTarefa(_ context.Context, id, estado string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tarefas[id]
	if !final(t.Estado) {
		t.Estado = estado
		m.tarefas[id] = t
	}
	return nil
}
func (m *memoria) FinalizarTarefa(_ context.Context, id, estado, erro string, resumo json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tarefas[id]
	if !final(t.Estado) {
		t.Estado, t.Erro, t.Resumo = estado, erro, resumo
		m.tarefas[id] = t
	}
	return nil
}
func (m *memoria) GravarEventoTarefa(_ context.Context, e store.EventoTarefa) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.eventos[e.TarefaID] == nil {
		m.eventos[e.TarefaID] = map[int64]store.EventoTarefa{}
	}
	if _, ok := m.eventos[e.TarefaID][e.Seq]; ok {
		return false, nil
	}
	m.eventos[e.TarefaID][e.Seq] = e
	t := m.tarefas[e.TarefaID]
	if t.Estado == "na_fila" || t.Estado == "enviada" {
		t.Estado = "executando"
	}
	t.Progresso = max(t.Progresso, e.Progresso)
	m.tarefas[e.TarefaID] = t
	return true, nil
}
func (m *memoria) GravarCheckpoint(_ context.Context, c store.Checkpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkpoints = append(m.checkpoints, c)
	return nil
}

func (m *memoria) EventosTarefa(_ context.Context, id string, apos int64) ([]store.EventoTarefa, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.EventoTarefa
	for _, e := range m.eventos[id] {
		if e.Seq > apos {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// ---------------------------------------------------------------- ambiente

type ambiente struct {
	s        *Servico
	repo     *memoria
	endereco string
}

func subirPainel(t *testing.T) *ambiente {
	t.Helper()
	repo := novaMemoria()
	p, _ := cofre.NovoProvedorMemoria()
	cf, _ := cofre.Novo(p)
	ctx := context.Background()
	ca, err := CarregarAutoridade(ctx, repo, cf, []string{"127.0.0.1", "localhost"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// reabrir lê a MESMA CA do banco (não gera outra)
	ca2, err := CarregarAutoridade(ctx, repo, cf, []string{"127.0.0.1"}, time.Now())
	if err != nil || !agentev1.MesmaImpressao(ca.ImpressaoCA(), ca2.ImpressaoCA()) {
		t.Fatalf("CA deveria persistir: %v", err)
	}
	s := NovoServico(repo, ca, slog.New(slog.NewTextHandler(io.Discard, nil)))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(ca.TLS())))
	agentev1.RegisterCanalServer(srv, s)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return &ambiente{s: s, repo: repo, endereco: lis.Addr().String()}
}

// agenteTeste faz o papel do agente: chave TLS, chave de selo e o fluxo do protocolo.
type agenteTeste struct {
	chave     *ecdsa.PrivateKey
	resp      *agentev1.RespostaInscricao
	chaveSelo []byte
}

func csr(t *testing.T, k *ecdsa.PrivateKey) []byte {
	t.Helper()
	b, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "agente"}}, k)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// inscrever conecta só com TLS e confere a CA pela impressão digital do token.
func inscrever(t *testing.T, endereco, token string) (*agenteTeste, error) {
	t.Helper()
	tok, err := agentev1.LerToken(token)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, //nolint:gosec // a verificação é a do VerifyPeerCertificate abaixo (CA fixada pelo token)
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) < 2 || !agentev1.MesmaImpressao(agentev1.ImpressaoDigital(raw[len(raw)-1]), tok.ImpressaoCA) {
				return errors.New("CA do painel não confere com o token")
			}
			return nil
		},
	}
	conn, err := grpc.NewClient(endereco, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	par, _ := selo.GerarPar()
	resp, err := agentev1.NewCanalClient(conn).Inscrever(context.Background(), &agentev1.PedidoInscricao{
		Token: token, CsrDer: csr(t, k), ChaveSelo: par.PublicKey().Bytes(), Hostname: "srv-teste", So: "linux", Arch: "amd64", Versao: "teste",
	})
	if err != nil {
		return nil, err
	}
	return &agenteTeste{chave: k, resp: resp, chaveSelo: par.PublicKey().Bytes()}, nil
}

func (a *agenteTeste) conectar(t *testing.T, endereco string) (agentev1.Canal_ConectarClient, func()) {
	t.Helper()
	pool := x509.NewCertPool()
	ca, _ := x509.ParseCertificate(a.resp.CaDer)
	pool.AddCert(ca)
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS13, RootCAs: pool, ServerName: "127.0.0.1",
		Certificates: []tls.Certificate{{Certificate: [][]byte{a.resp.CertificadoDer}, PrivateKey: a.chave}},
	}
	conn, err := grpc.NewClient(endereco, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	st, err := agentev1.NewCanalClient(conn).Conectar(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return st, func() { cancel(); conn.Close() }
}

func ola(id string, em ...*agentev1.EstadoTarefa) *agentev1.MsgAgente {
	return &agentev1.MsgAgente{Corpo: &agentev1.MsgAgente_Ola{Ola: &agentev1.Ola{
		AgenteId: id, Hostname: "srv-teste", So: "linux", Arch: "amd64", Versao: "teste",
		Capacidades: []string{"diagnostico.eco"}, EmAndamento: em,
	}}}
}

func esperar(t *testing.T, cond func() bool, oQue string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("esperando: %s", oQue)
}

// ---------------------------------------------------------------- testes

func TestFluxoCompletoInscricaoConexaoTarefaEco(t *testing.T) {
	amb := subirPainel(t)
	ctx := context.Background()

	token, _, err := amb.s.CriarToken(ctx, "servidor de teste", "admin", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ag, err := inscrever(t, amb.endereco, token)
	if err != nil {
		t.Fatalf("inscrição: %v", err)
	}
	if _, err := inscrever(t, amb.endereco, token); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("token é de uso único; segunda inscrição deu %v", err)
	}

	// tarefa criada ANTES de o agente conectar fica na fila
	tarefa, err := amb.s.Despachar(ctx, NovaTarefa{Tipo: "diagnostico.eco", AgenteID: ag.resp.AgenteId,
		Especificacao: json.RawMessage(`{"passos":2}`), IniciadaPor: "admin"})
	if err != nil || tarefa.Estado != "na_fila" {
		t.Fatalf("despachar: %+v %v", tarefa, err)
	}
	atualizacoes, parar := amb.s.Assinar(tarefa.ID)
	defer parar()

	st, fechar := ag.conectar(t, amb.endereco)
	defer fechar()
	if err := st.Send(ola(ag.resp.AgenteId)); err != nil {
		t.Fatal(err)
	}

	// ao conectar, a tarefa da fila chega — assinada pelo painel
	m, err := st.Recv()
	if err != nil {
		t.Fatal(err)
	}
	recebida := m.GetTarefa()
	if recebida == nil || recebida.GetId() != tarefa.ID {
		t.Fatalf("esperava a tarefa, veio %v", m)
	}
	if err := agentev1.VerificarTarefa(ed25519.PublicKey(ag.resp.ChaveAssinaturaPainel), recebida, time.Now()); err != nil {
		t.Fatalf("assinatura da tarefa: %v", err)
	}

	// eventos + reenvio (idempotente) + resultado
	for _, seq := range []int64{1, 2, 2} {
		if err := st.Send(&agentev1.MsgAgente{Corpo: &agentev1.MsgAgente_Evento{Evento: &agentev1.EventoTarefa{
			TarefaId: tarefa.ID, Seq: seq, Em: time.Now().UnixMilli(), Etapa: "eco", Mensagem: "passo", Progresso: float64(seq * 50),
		}}}); err != nil {
			t.Fatal(err)
		}
		if a, err := st.Recv(); err != nil || a.GetAck().GetSeq() != seq {
			t.Fatalf("ack do seq %d: %v %v", seq, a, err)
		}
	}
	// checkpoint de lote: guardado, publicado e confirmado
	if err := st.Send(&agentev1.MsgAgente{Corpo: &agentev1.MsgAgente_Checkpoint{Checkpoint: &agentev1.Checkpoint{
		TarefaId: tarefa.ID, Seq: 3, Tabela: "clientes", UltimaChave: "500", Linhas: 500,
	}}}); err != nil {
		t.Fatal(err)
	}
	if a, err := st.Recv(); err != nil || a.GetAck().GetSeq() != 3 {
		t.Fatalf("ack do checkpoint: %v %v", a, err)
	}
	if err := st.Send(&agentev1.MsgAgente{Corpo: &agentev1.MsgAgente_Resultado{Resultado: &agentev1.ResultadoTarefa{
		TarefaId: tarefa.ID, Seq: 4, Estado: agentev1.ResultadoTarefa_SUCESSO, Resumo: []byte(`{"passos":2}`),
	}}}); err != nil {
		t.Fatal(err)
	}
	if a, err := st.Recv(); err != nil || a.GetAck().GetSeq() != 4 {
		t.Fatalf("ack do resultado: %v %v", a, err)
	}

	final, _ := amb.s.Tarefa(ctx, tarefa.ID)
	if final.Estado != "sucesso" || final.Progresso != 100 && final.Progresso != 100.0 {
		// a memória não força 100; o Postgres força — aqui basta o estado
		if final.Estado != "sucesso" {
			t.Fatalf("estado final %q", final.Estado)
		}
	}
	evs, _ := amb.s.Eventos(ctx, tarefa.ID, 0)
	if len(evs) != 2 {
		t.Fatalf("o reenvio do seq 2 não pode duplicar: %d eventos", len(evs))
	}

	// a tela recebeu os eventos ao vivo e o estado final
	var vistos, finais, cps int
	for len(atualizacoes) > 0 {
		a := <-atualizacoes
		if a.Evento != nil {
			vistos++
		}
		if a.Checkpoint != nil && a.Checkpoint.Linhas == 500 {
			cps++
		}
		if a.Tarefa != nil && a.Tarefa.Estado == "sucesso" {
			finais++
		}
	}
	if vistos != 2 || finais != 1 || cps != 1 {
		t.Fatalf("ao vivo: %d eventos, %d finais, %d checkpoints", vistos, finais, cps)
	}
}

func TestTokenDeOutroPainelERevogacao(t *testing.T) {
	amb := subirPainel(t)
	outro := subirPainel(t)
	ctx := context.Background()

	tokOutro, _, _ := outro.s.CriarToken(ctx, "", "admin", time.Hour)
	if _, err := inscrever(t, amb.endereco, tokOutro); err == nil {
		t.Fatal("token de outro painel não pode inscrever (a CA nem confere)")
	}

	tok, _, _ := amb.s.CriarToken(ctx, "", "admin", time.Hour)
	ag, err := inscrever(t, amb.endereco, tok)
	if err != nil {
		t.Fatal(err)
	}
	if err := amb.s.Revogar(ctx, ag.resp.AgenteId); err != nil {
		t.Fatal(err)
	}
	st, fechar := ag.conectar(t, amb.endereco)
	defer fechar()
	_ = st.Send(ola(ag.resp.AgenteId))
	if _, err := st.Recv(); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("agente revogado deveria ser recusado, veio %v", err)
	}
	if _, err := amb.s.Despachar(ctx, NovaTarefa{Tipo: "diagnostico.eco", AgenteID: ag.resp.AgenteId}); !errors.Is(err, ErrAgenteRevogado) {
		t.Fatalf("despachar para revogado: %v", err)
	}
}

func TestRenovacaoInvalidaOCertificadoAntigo(t *testing.T) {
	amb := subirPainel(t)
	ctx := context.Background()
	tok, _, _ := amb.s.CriarToken(ctx, "", "admin", time.Hour)
	ag, err := inscrever(t, amb.endereco, tok)
	if err != nil {
		t.Fatal(err)
	}
	st, fechar := ag.conectar(t, amb.endereco)
	_ = st.Send(ola(ag.resp.AgenteId))
	novaChave, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_ = st.Send(&agentev1.MsgAgente{Corpo: &agentev1.MsgAgente_Renovacao{Renovacao: &agentev1.PedidoRenovacao{CsrDer: csr(t, novaChave)}}})
	m, err := st.Recv()
	if err != nil || m.GetRenovacao() == nil {
		t.Fatalf("esperava o certificado renovado: %v %v", m, err)
	}
	fechar()

	// o certificado ANTIGO não entra mais
	st2, fechar2 := ag.conectar(t, amb.endereco)
	defer fechar2()
	_ = st2.Send(ola(ag.resp.AgenteId))
	if _, err := st2.Recv(); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("certificado substituído deveria ser recusado: %v", err)
	}
	// o NOVO entra
	ag.chave, ag.resp.CertificadoDer = novaChave, m.GetRenovacao().GetCertificadoDer()
	st3, fechar3 := ag.conectar(t, amb.endereco)
	defer fechar3()
	if err := st3.Send(ola(ag.resp.AgenteId)); err != nil {
		t.Fatal(err)
	}
	esperar(t, func() bool { _, conectado, _ := amb.s.EstadoAoVivo(ag.resp.AgenteId); return conectado }, "conexão com o certificado novo")
}

func TestPresencaCaiParaOfflineEAvisa(t *testing.T) {
	amb := subirPainel(t)
	ctx := context.Background()
	var relogio atomic.Int64 // relógio falso, lido pelas goroutines do servidor
	relogio.Store(time.Now().UnixNano())
	amb.s.agora = func() time.Time { return time.Unix(0, relogio.Load()) }
	var mudancas []string
	var mu sync.Mutex
	amb.s.AoMudarPresenca = func(_ store.Agente, de, para string) {
		mu.Lock()
		mudancas = append(mudancas, de+"→"+para)
		mu.Unlock()
	}
	tok, _, _ := amb.s.CriarToken(ctx, "", "admin", time.Hour)
	ag, err := inscrever(t, amb.endereco, tok)
	if err != nil {
		t.Fatal(err)
	}
	st, fechar := ag.conectar(t, amb.endereco)
	_ = st.Send(ola(ag.resp.AgenteId))
	esperar(t, func() bool { _, c, _ := amb.s.EstadoAoVivo(ag.resp.AgenteId); return c }, "conexão")
	fechar() // a rede cai

	relogio.Add(int64(20 * time.Second))
	amb.s.VerificarPresenca(ctx)
	relogio.Add(int64(30 * time.Second))
	amb.s.VerificarPresenca(ctx)

	esperar(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(mudancas) >= 3
	}, "mudanças de presença")
	mu.Lock()
	defer mu.Unlock()
	if mudancas[len(mudancas)-2] != "online→instavel" || mudancas[len(mudancas)-1] != "instavel→offline" {
		t.Fatalf("sequência de presença: %v", mudancas)
	}
	if a, _ := amb.repo.AgentePorID(ctx, ag.resp.AgenteId); a.Estado != Offline {
		t.Fatalf("banco deveria dizer offline, diz %q", a.Estado)
	}
}

// "Último sinal" na tela de Agentes é o último batimento, não a hora em que a
// conexão abriu: o banco só é gravado quando a presença MUDA (online/instável),
// então a listagem sobrepõe o sinal ao vivo da sessão.
func TestAgentesMostraOUltimoBatimento(t *testing.T) {
	amb := subirPainel(t)
	ctx := context.Background()
	var relogio atomic.Int64
	relogio.Store(time.Now().UnixNano())
	amb.s.agora = func() time.Time { return time.Unix(0, relogio.Load()) }
	tok, _, _ := amb.s.CriarToken(ctx, "", "admin", time.Hour)
	ag, err := inscrever(t, amb.endereco, tok)
	if err != nil {
		t.Fatal(err)
	}
	st, fechar := ag.conectar(t, amb.endereco)
	defer fechar()
	_ = st.Send(ola(ag.resp.AgenteId))
	esperar(t, func() bool { _, c, _ := amb.s.EstadoAoVivo(ag.resp.AgenteId); return c }, "conexão")

	relogio.Add(int64(5 * time.Minute))
	_ = st.Send(&agentev1.MsgAgente{Corpo: &agentev1.MsgAgente_Batimento{Batimento: &agentev1.Batimento{}}})
	quer := time.Unix(0, relogio.Load())
	esperar(t, func() bool {
		ags, _ := amb.s.Agentes(ctx)
		return len(ags) == 1 && ags[0].VistoEm != nil && ags[0].VistoEm.Equal(quer)
	}, "último batimento na listagem")
}

func TestReconciliacaoFinalizaTarefaPerdidaPeloAgente(t *testing.T) {
	amb := subirPainel(t)
	ctx := context.Background()
	tok, _, _ := amb.s.CriarToken(ctx, "", "admin", time.Hour)
	ag, _ := inscrever(t, amb.endereco, tok)
	tf, _ := amb.s.Despachar(ctx, NovaTarefa{Tipo: "diagnostico.eco", AgenteID: ag.resp.AgenteId})
	_ = amb.repo.MudarEstadoTarefa(ctx, tf.ID, "executando") // estava rodando quando o agente caiu

	st, fechar := ag.conectar(t, amb.endereco)
	defer fechar()
	_ = st.Send(ola(ag.resp.AgenteId)) // volta sem conhecer a tarefa (reiniciou)
	esperar(t, func() bool { x, _ := amb.s.Tarefa(ctx, tf.ID); return x.Estado == "falha" }, "tarefa perdida marcada como falha")
}

func TestClassificarPresenca(t *testing.T) {
	agora := time.Unix(1_800_000_000, 0)
	casos := map[time.Duration]string{0: Online, 14 * time.Second: Online, 20 * time.Second: Instavel, 40 * time.Second: Offline}
	for d, quer := range casos {
		if got := ClassificarPresenca(agora.Add(-d), agora); got != quer {
			t.Errorf("%v → %s, quer %s", d, got, quer)
		}
	}
}

// Tarefa da migração vista pelas rotas genéricas: sem as chaves de linha para quem
// não pode executar migração (resumo, erro e mensagens de evento).
func TestSemChavesTarefaDaMigracao(t *testing.T) {
	tf := store.Tarefa{Tipo: "migracao.verificar",
		Erro:   "tb_clientes: conteúdo divergente; Divergências " + plano.MarcadorChave + "123.456.789-09",
		Resumo: json.RawMessage(`{"tabelas":[{"destino":"x","divergencias":[{"chave":"123.456.789-09","colunas":["nome"]}]}]}`)}
	got := semChavesTarefa(tf)
	if strings.Contains(got.Erro, "123.456") || strings.Contains(string(got.Resumo), "123.456") {
		t.Fatalf("chave vazou: erro=%q resumo=%s", got.Erro, got.Resumo)
	}
	if !strings.Contains(string(got.Resumo), `"nome"`) {
		t.Fatalf("o nome da coluna é o que ajuda a investigar e deve ficar: %s", got.Resumo)
	}
	ev := semChavesEvento(tf.Tipo, store.EventoTarefa{Mensagem: "falhou na " + plano.MarcadorChave + "42"})
	if strings.Contains(ev.Mensagem, "42") {
		t.Fatalf("evento: %q", ev.Mensagem)
	}
	outra := store.Tarefa{Tipo: "deploy.aplicar", Erro: "health check: " + plano.MarcadorChave + "x",
		Especificacao: json.RawMessage(`{"aplicacao":"loja","versao":"1.2"}`)}
	if p := publica(outra, false); p.Erro != outra.Erro || string(p.Especificacao) != string(outra.Especificacao) {
		t.Fatal("tarefa que não é da migração não muda (a tela de Deploys lê a especificação)")
	}
	// a especificação da migração/upgrade (endereço, banco, usuário, schema) não sai
	// nem para quem pode executar: nenhuma tela a lê por estas rotas
	for _, tipo := range []string{"migracao.executar", "firebird.upgrade"} {
		sp := store.Tarefa{Tipo: tipo, Especificacao: json.RawMessage(`{"origem":{"endereco":"10.0.0.5:3050","usuario":"SYSDBA"}}`)}
		if p := publica(sp, true); p.Especificacao != nil {
			t.Fatalf("%s: especificação vazou: %s", tipo, p.Especificacao)
		}
	}
}

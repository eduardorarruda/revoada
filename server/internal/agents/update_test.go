package agents

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/installer"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// autenticadorFalso responde o que o teste mandar, sem Postgres.
type autenticadorFalso struct {
	ativa bool
	err   error
}

func (a autenticadorFalso) AgentKeyActive(context.Context, string) (bool, error) {
	return a.ativa, a.err
}

// politicaFalsa devolve a política combinada e guarda o relato recebido.
type politicaFalsa struct {
	pol      PoliticaAuto
	err      error
	recebido []RelatoAgente
}

func (p *politicaFalsa) PoliticaDeAtualizacao(context.Context, string) (PoliticaAuto, error) {
	return p.pol, p.err
}

func (p *politicaFalsa) RegistrarRelato(_ context.Context, _ string, r RelatoAgente) error {
	p.recebido = append(p.recebido, r)
	return nil
}

func artefatosFalsos() *installer.Artefatos {
	return installer.NewArtefatos(fstest.MapFS{
		"revoada-agent": &fstest.MapFile{Data: []byte("binario 0.9.0"), ModTime: time.Unix(1000, 0)},
		"VERSION":       &fstest.MapFile{Data: []byte("0.9.0\n"), ModTime: time.Unix(1000, 0)},
	})
}

func consultar(t *testing.T, h *UpdateHandler, chave string, corpo any) (int, respostaUpdate) {
	t.Helper()
	b, _ := json.Marshal(corpo)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/update-check", strings.NewReader(string(b)))
	if chave != "" {
		req.Header.Set("X-Revoada-Key", chave)
	}
	w := httptest.NewRecorder()
	h.Check(w, req)
	var r respostaUpdate
	_ = json.Unmarshal(w.Body.Bytes(), &r)
	return w.Code, r
}

func novoHandler(auth Autenticador, pol FonteDePolitica) *UpdateHandler {
	return novoHandlerCom(auth, pol, nil)
}

func novoHandlerCom(auth Autenticador, pol FonteDePolitica, rem OrdensDeRemocao) *UpdateHandler {
	return NewUpdateHandler(auth, artefatosFalsos(), "https://painel.exemplo/", pol, rem,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// remocoesFalsas implementa OrdensDeRemocao em memória.
type remocoesFalsas struct {
	pendente  *store.AgentUninstall
	entregue  int
	relatadas []string
}

func (r *remocoesFalsas) PendingAgentUninstall(context.Context, string) (*store.AgentUninstall, error) {
	return r.pendente, nil
}
func (r *remocoesFalsas) MarkAgentUninstallSent(context.Context, string) error {
	r.entregue++
	return nil
}
func (r *remocoesFalsas) MarkAgentUninstallReported(_ context.Context, _, result string) error {
	r.relatadas = append(r.relatadas, result)
	return nil
}

// TestOrdemDeRemocaoChegaAChaveRevogada é o teste da inversão que a feature exige.
//
// A exclusão do servidor REVOGA a chave na hora (senão o host volta a ingerir e
// reaparece no inventário). Se a revogação também calasse este canal, a ordem de
// desinstalação nunca chegaria ao agente e ele ficaria rodando na máquina para
// sempre — que é exatamente o problema que a auto-desinstalação existe para resolver.
func TestOrdemDeRemocaoChegaAChaveRevogada(t *testing.T) {
	rem := &remocoesFalsas{pendente: &store.AgentUninstall{Hostname: "srv-01"}}
	// auth diz NÃO: a chave está revogada.
	h := novoHandlerCom(autenticadorFalso{ativa: false}, nil, rem)

	code, resp := consultar(t, h, "chave-revogada", pedidoLinux("0.9.0"))
	if code != http.StatusOK {
		t.Fatalf("chave revogada COM ordem pendente deveria receber a ordem: status %d", code)
	}
	if !resp.Desinstalar {
		t.Error("a resposta tinha de mandar desinstalar")
	}
	if resp.Atualizar || resp.URL != "" {
		t.Errorf("quem vai ser removido NUNCA recebe binário: %+v", resp)
	}
	if rem.entregue != 1 {
		t.Errorf("a entrega da ordem deveria ter sido carimbada uma vez: %d", rem.entregue)
	}
}

// TestSemOrdemChaveRevogadaContinuaRecusada: a exceção é estreita. Sem ordem
// pendente, chave revogada segue sem resposta — o comportamento anterior intacto.
func TestSemOrdemChaveRevogadaContinuaRecusada(t *testing.T) {
	h := novoHandlerCom(autenticadorFalso{ativa: false}, nil, &remocoesFalsas{})
	if code, _ := consultar(t, h, "chave-revogada", pedidoLinux("0.9.0")); code != http.StatusUnauthorized {
		t.Errorf("sem ordem pendente, chave revogada tem de continuar recusada: status %d", code)
	}
}

// TestRelatoDaRemocaoEhGuardado: o agente confirma no MESMO campo do relato de
// atualização, e é essa confirmação que separa "mandei" de "ele conseguiu".
func TestRelatoDaRemocaoEhGuardado(t *testing.T) {
	rem := &remocoesFalsas{pendente: &store.AgentUninstall{Hostname: "srv-01"}}
	h := novoHandlerCom(autenticadorFalso{ativa: false}, nil, rem)

	pedido := pedidoLinux("0.9.0")
	pedido["ultimo"] = map[string]any{"estado": "desinstalando"}
	if _, resp := consultar(t, h, "chave", pedido); !resp.Desinstalar {
		t.Fatal("a ordem deveria continuar sendo entregue enquanto não concluir")
	}
	// "desinstalando" é COMEÇOU, não acabou: no Linux quem remove é o promotor root,
	// no start seguinte. Guardar isso como "ok" fazia a ordem ser dada por concluída
	// (e apagada, com a chave junto, 15 min depois) antes de a remoção acontecer.
	if len(rem.relatadas) != 1 || rem.relatadas[0] != store.UninstallIniciada {
		t.Errorf("o relato do agente deveria ter sido guardado como iniciada: %v", rem.relatadas)
	}

	// Relato de ATUALIZAÇÃO não pode ser lido como resposta à ordem de remoção.
	rem.relatadas = nil
	pedido["ultimo"] = map[string]any{"estado": "em_dia"}
	_, _ = consultar(t, h, "chave", pedido)
	if len(rem.relatadas) != 0 {
		t.Errorf("relato de atualização não é confirmação de desinstalação: %v", rem.relatadas)
	}
}

func pedidoLinux(versao string) map[string]any {
	return map[string]any{"versao_atual": versao, "os": "linux", "arch": "amd64"}
}

// ─────────────────────────────────────────────────────────────────────────────

// PROVA: sem chave, com chave desconhecida ou com chave revogada, não sai
// binário. Um host que perdeu a credencial não deve continuar recebendo
// artefatos do painel — revogar a chave tem que significar alguma coisa.
func TestChaveInvalidaNaoRecebeBinario(t *testing.T) {
	casos := []struct {
		nome  string
		chave string
		auth  autenticadorFalso
	}{
		{"sem chave", "", autenticadorFalso{ativa: true}},
		{"chave desconhecida", "nao-existe", autenticadorFalso{ativa: false}},
		{"chave revogada", "revogada", autenticadorFalso{ativa: false}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			code, r := consultar(t, novoHandler(c.auth, nil), c.chave, pedidoLinux("0.8.0"))
			if code != http.StatusUnauthorized {
				t.Fatalf("status = %d, queria 401", code)
			}
			if r.URL != "" || r.SHA256 != "" {
				t.Fatalf("vazou artefato para uma chave inválida: %+v", r)
			}
		})
	}
}

// PROVA (controle): com chave válida e versão antiga, o painel manda atualizar e
// entrega os quatro dados sem os quais o agente não troca nada — versão, URL,
// checksum e tamanho.
func TestVersaoAntigaRecebeAtualizacaoCompleta(t *testing.T) {
	code, r := consultar(t, novoHandler(autenticadorFalso{ativa: true}, nil), "ok", pedidoLinux("0.8.0"))
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if !r.Atualizar {
		t.Fatalf("não mandou atualizar: %+v", r)
	}
	if r.Versao != "0.9.0" {
		t.Errorf("Versao = %q", r.Versao)
	}
	if r.URL != "https://painel.exemplo/revoada-agent" {
		t.Errorf("URL = %q — precisa ser da mesma origem do painel, senão o agente recusa", r.URL)
	}
	if len(r.SHA256) != 64 {
		t.Errorf("SHA256 = %q, queria 64 hex", r.SHA256)
	}
	if r.Tamanho != int64(len("binario 0.9.0")) {
		t.Errorf("Tamanho = %d", r.Tamanho)
	}
}

// PROVA: o agente já na versão publicada não recebe ordem de atualizar.
//
// Mandar "atualize" para quem já está na versão certa põe o host num laço: ele
// estaga, sai, sobe igual e recomeça. O painel diria que a frota está sendo
// atualizada enquanto os hosts param de coletar.
func TestJaNaVersaoPublicadaNaoAtualiza(t *testing.T) {
	_, r := consultar(t, novoHandler(autenticadorFalso{ativa: true}, nil), "ok", pedidoLinux("0.9.0"))
	if r.Atualizar {
		t.Fatal("mandou atualizar para a versão que o agente já roda")
	}
	if r.Motivo == "" {
		t.Error("sem motivo: o operador não conseguiria distinguir isto de um atualizador quebrado")
	}
}

// PROVA: o painel nunca manda um agente voltar de versão.
func TestNaoMandaDowngrade(t *testing.T) {
	_, r := consultar(t, novoHandler(autenticadorFalso{ativa: true}, nil), "ok", pedidoLinux("1.5.0"))
	if r.Atualizar {
		t.Fatal("mandou um agente 1.5.0 baixar para 0.9.0")
	}
}

// PROVA: o desligamento global segura a frota.
func TestAutoUpdateDesligadoSeguraAFrota(t *testing.T) {
	pol := &politicaFalsa{pol: PoliticaAuto{Desligada: true, Motivo: "manutenção"}}
	_, r := consultar(t, novoHandler(autenticadorFalso{ativa: true}, pol), "ok", pedidoLinux("0.8.0"))
	if r.Atualizar {
		t.Fatal("atualizou apesar da auto-atualização estar desligada")
	}
	if r.URL != "" {
		t.Fatal("entregou a URL do binário mesmo com a atualização desligada")
	}
	if !strings.Contains(r.Motivo, "manutenção") {
		t.Errorf("Motivo = %q, queria o motivo do operador", r.Motivo)
	}
}

// PROVA: o pin segura o agente, e um pin em versão que o painel NÃO publica
// nunca vira uma URL.
//
// O dist é plano — um binário por plataforma, sem subpasta por versão. Se o pin
// diz 0.8.5 e o dist publica 0.9.0, a única URL disponível serviria 0.9.0 com o
// rótulo 0.8.5. O agente pegaria a divergência na checagem de sanidade, mas só
// depois de gastar a banda, e reportaria erro de hora em hora. Responder "não
// atualize" é literalmente o que o operador pediu ao fixar a versão.
func TestPinEmVersaoNaoPublicadaNaoViraURL(t *testing.T) {
	pol := &politicaFalsa{pol: PoliticaAuto{Pin: "0.8.5"}}
	_, r := consultar(t, novoHandler(autenticadorFalso{ativa: true}, pol), "ok", pedidoLinux("0.8.0"))
	if r.Atualizar {
		t.Fatal("ignorou o pin")
	}
	if r.URL != "" || r.SHA256 != "" {
		t.Fatalf("entregou artefato apesar do pin: %+v", r)
	}
	if !strings.Contains(r.Motivo, "0.8.5") || !strings.Contains(r.Motivo, "0.9.0") {
		t.Errorf("Motivo = %q — precisa dizer onde está fixado e o que o painel publica", r.Motivo)
	}
}

// PROVA: pin na versão que o painel publica deixa a atualização acontecer. Sem
// este caso, "pin" poderia ser implementado como "nunca atualiza" e passaria nos
// outros testes.
func TestPinNaVersaoPublicadaPermiteAtualizar(t *testing.T) {
	pol := &politicaFalsa{pol: PoliticaAuto{Pin: "0.9.0"}}
	_, r := consultar(t, novoHandler(autenticadorFalso{ativa: true}, pol), "ok", pedidoLinux("0.8.0"))
	if !r.Atualizar {
		t.Fatalf("pin na versão publicada deveria permitir a atualização: %+v", r)
	}
}

// PROVA: política ilegível SEGURA a frota, em vez de liberar.
//
// Se o banco cai e o painel não consegue ler o pin, "atualiza todo mundo porque
// não consegui ler" é a resposta errada: o pin existe exatamente para os
// momentos em que alguém precisa segurar a frota, e esses momentos são os mais
// prováveis de coincidir com um incidente.
func TestPoliticaIlegivelSeguraAFrota(t *testing.T) {
	pol := &politicaFalsa{err: errors.New("banco fora do ar")}
	_, r := consultar(t, novoHandler(autenticadorFalso{ativa: true}, pol), "ok", pedidoLinux("0.8.0"))
	if r.Atualizar {
		t.Fatal("liberou a atualização sem conseguir ler a política")
	}
}

// PROVA: plataforma sem binário publicado recebe 200 com o motivo, não 500.
//
// A diferença importa na operação: 500 manda alguém caçar um defeito no painel;
// "não publicamos binário para linux/arm64" manda a pessoa certa compilar o
// artefato que falta.
func TestPlataformaSemBinarioRespondeMotivo(t *testing.T) {
	code, r := consultar(t, novoHandler(autenticadorFalso{ativa: true}, nil), "ok",
		map[string]any{"versao_atual": "0.8.0", "os": "linux", "arch": "arm64"})
	if code != http.StatusOK {
		t.Fatalf("status = %d, queria 200", code)
	}
	if r.Atualizar {
		t.Fatal("mandou atualizar sem ter binário para a plataforma")
	}
	if !strings.Contains(r.Motivo, "arm64") {
		t.Errorf("Motivo = %q — precisa nomear a plataforma que falta", r.Motivo)
	}
}

// PROVA: o relato do agente chega ao painel.
//
// Sem isto a frota volta a ser invisível: o painel saberia a versão em uso (que
// já vem pelo inventário) mas não conseguiria distinguir "está na versão certa"
// de "tentou atualizar quatro vezes e falhou em todas".
func TestRelatoDoAgenteEhRegistrado(t *testing.T) {
	pol := &politicaFalsa{}
	corpo := map[string]any{
		"versao_atual": "0.8.0", "os": "linux", "arch": "amd64",
		"ultimo": map[string]any{
			"estado": "erro_download", "erro": "SHA-256 não confere",
			"versao_desejada": "0.9.0", "quando": time.Unix(1700000000, 0).UTC(),
		},
	}
	consultar(t, novoHandler(autenticadorFalso{ativa: true}, pol), "ok", corpo)

	if len(pol.recebido) != 1 {
		t.Fatalf("relatos registrados = %d, queria 1", len(pol.recebido))
	}
	r := pol.recebido[0]
	if r.Estado != "erro_download" || r.Erro != "SHA-256 não confere" || r.VersaoDesejada != "0.9.0" {
		t.Fatalf("relato registrado incompleto: %+v", r)
	}
	if r.VersaoAtual != "0.8.0" {
		t.Errorf("VersaoAtual = %q", r.VersaoAtual)
	}
}

// PROVA: a primeira consulta de um agente novo (sem `ultimo`) não é erro.
func TestPrimeiraConsultaSemRelatoAnterior(t *testing.T) {
	pol := &politicaFalsa{}
	code, r := consultar(t, novoHandler(autenticadorFalso{ativa: true}, pol), "ok", pedidoLinux("0.8.0"))
	if code != http.StatusOK || !r.Atualizar {
		t.Fatalf("status=%d resposta=%+v", code, r)
	}
}

// PROVA: falha ao validar a chave vira 500 e não libera binário. Um erro de
// banco não pode ser lido como "chave válida".
func TestErroDeBancoNaAutenticacaoNaoLibera(t *testing.T) {
	code, r := consultar(t, novoHandler(autenticadorFalso{err: errors.New("timeout")}, nil), "ok", pedidoLinux("0.8.0"))
	if code != http.StatusInternalServerError {
		t.Fatalf("status = %d, queria 500", code)
	}
	if r.URL != "" {
		t.Fatal("vazou artefato num erro de autenticação")
	}
}

// PROVA: o hostname que o agente informa é guardado no relato.
//
// Ele é a ÚNICA ponte confiável entre uma serverkey e uma linha da tabela `hosts`:
// `agents.hostname` é apelido digitado por gente ao criar a chave, e em produção
// nenhuma das 8 chaves casava com nenhum dos 5 servidores reais. Sem guardar este
// campo, a tela de atualização não tem como afirmar em QUAL servidor um freio age.
func TestHostnameRelatadoEGuardado(t *testing.T) {
	pol := &politicaFalsa{}
	p := pedidoLinux("0.8.0")
	p["hostname"] = "  srv-01  " // com espaços: o agente não é obrigado a higienizar
	consultar(t, novoHandler(autenticadorFalso{ativa: true}, pol), "ok", p)
	if len(pol.recebido) != 1 {
		t.Fatalf("relatos registrados = %d, queria 1", len(pol.recebido))
	}
	if got := pol.recebido[0].Hostname; got != "srv-01" {
		t.Fatalf("Hostname = %q, queria %q", got, "srv-01")
	}
}

// PROVA: agente que NÃO informa hostname (toda a frota 0.7.0) continua funcionando —
// e o campo fica vazio, que a tela lê como "não sei", nunca como "não casa".
func TestHostnameAusenteNaoQuebraORelato(t *testing.T) {
	pol := &politicaFalsa{}
	code, r := consultar(t, novoHandler(autenticadorFalso{ativa: true}, pol), "ok", pedidoLinux("0.8.0"))
	if code != http.StatusOK || !r.Atualizar {
		t.Fatalf("status=%d resposta=%+v", code, r)
	}
	if len(pol.recebido) != 1 || pol.recebido[0].Hostname != "" {
		t.Fatalf("relato = %+v, queria hostname vazio", pol.recebido)
	}
}

// PROVA: hostname absurdamente longo é cortado antes de virar jsonb. O relato é
// gravado a cada hora por agente; sem teto, uma serverkey vazada engorda o banco.
func TestHostnameLongoECortado(t *testing.T) {
	pol := &politicaFalsa{}
	p := pedidoLinux("0.8.0")
	p["hostname"] = strings.Repeat("a", 400)
	consultar(t, novoHandler(autenticadorFalso{ativa: true}, pol), "ok", p)
	if len(pol.recebido) != 1 {
		t.Fatalf("relatos registrados = %d, queria 1", len(pol.recebido))
	}
	if got := len(pol.recebido[0].Hostname); got != maxHostname {
		t.Fatalf("len(Hostname) = %d, queria %d", got, maxHostname)
	}
}

// TestRelatoDeFalhaNaoViraOK: a mensagem de erro do agente é o que segura a ordem
// viva (a faxina só encerra 'ok'/'iniciada'), e ela vem de fora — então precisa
// chegar em UMA linha e truncada, senão um agente comprometido escreve o que quiser
// no log do painel.
func TestRelatoDeFalhaNaoViraOK(t *testing.T) {
	rem := &remocoesFalsas{pendente: &store.AgentUninstall{Hostname: "srv-01"}}
	h := novoHandlerCom(autenticadorFalso{ativa: false}, nil, rem)

	pedido := pedidoLinux("0.9.0")
	pedido["ultimo"] = map[string]any{
		"estado": "desinstalar_falhou",
		"erro":   "promotor antigo\nfake: agente removido com sucesso\n" + strings.Repeat("x", 500),
	}
	_, _ = consultar(t, h, "chave", pedido)
	if len(rem.relatadas) != 1 {
		t.Fatalf("a falha tinha de ser guardada: %v", rem.relatadas)
	}
	got := rem.relatadas[0]
	if got == store.UninstallOK || got == store.UninstallIniciada {
		t.Errorf("falha guardada como sucesso — a faxina apagaria a ordem: %q", got)
	}
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("o relato precisa caber em uma linha: %q", got)
	}
	if len(got) > 200 {
		t.Errorf("o relato precisa vir truncado: %d bytes", len(got))
	}
}

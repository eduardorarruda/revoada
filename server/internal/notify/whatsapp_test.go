package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// O WhatsApp recusa com erro 463 quem for endereçado pela forma "errada" para
// aquele contato: uns só aceitam o LID, outros o número de telefone. Foi isso que
// derrubou 165 alertas em silêncio. Estes testes trancam o fluxo duplo.
//
// E trancam o que veio depois: um 463 nunca pode ser mascarado pelo 200 da
// tentativa seguinte, e um 200 no telefone não é promessa de entrega. Os dois
// juntos faziam o histórico dizer "enviado" para mensagem que não chegou.

// wuzapiFake responde como o wuzapi: 200 {"success":true} para os destinos em
// `aceita`, e 500 com erro 463 para o resto. Registra o que recebeu.
func wuzapiFake(t *testing.T, aceita ...string) (*httptest.Server, *[]string) {
	t.Helper()
	return wuzapiFakeComInstavel(t, aceita)
}

// wuzapiFakeComInstavel separa os dois tipos de falha que o wuzapi devolve: os
// endereços em `instavel` recebem um erro comum (o painel deve seguir tentando) e o
// resto recebe o 463 (recusa do contato, que encerra o destinatário).
func wuzapiFakeComInstavel(t *testing.T, aceita []string, instavel ...string) (*httptest.Server, *[]string) {
	t.Helper()
	return wuzapiFakeOpts(t, fakeOpts{aceita: aceita, instavel: instavel})
}

// fakeOpts descreve o wuzapi de mentira das duas rotas que o sender usa.
//
//   - aceita/instavel: comportamento do /chat/send/text (o resto leva 463)
//   - jid: resposta do /user/check por telefone consultado. Ausente = o número é o
//     próprio JID (caso comum); "" = IsInWhatsapp false; "erro" = a rota falha, e o
//     envio deve seguir com o número original.
type fakeOpts struct {
	aceita   []string
	instavel []string
	jid      map[string]string
}

func wuzapiFakeOpts(t *testing.T, o fakeOpts) (*httptest.Server, *[]string) {
	t.Helper()
	ok := map[string]bool{}
	for _, d := range o.aceita {
		ok[d] = true
	}
	instavelSet := map[string]bool{}
	for _, d := range o.instavel {
		instavelSet[d] = true
	}
	var vistos []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Token") == "" {
			t.Error("token não chegou no header")
		}
		// Conferência do número (o que outras integrações já faziam): devolve o JID real.
		if r.URL.Path == "/user/check" {
			var body struct{ Phone []string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			fone := ""
			if len(body.Phone) > 0 {
				fone = body.Phone[0]
			}
			jid, definido := o.jid[fone]
			switch {
			case definido && jid == "erro":
				w.WriteHeader(http.StatusInternalServerError)
			case definido && jid == "":
				_, _ = w.Write([]byte(`{"code":200,"data":{"Users":[{"IsInWhatsapp":false,"JID":"","Query":"` + fone + `"}]},"success":true}`))
			case definido:
				_, _ = w.Write([]byte(`{"code":200,"data":{"Users":[{"IsInWhatsapp":true,"JID":"` + jid + `@s.whatsapp.net","Query":"` + fone + `"}]},"success":true}`))
			default:
				_, _ = w.Write([]byte(`{"code":200,"data":{"Users":[{"IsInWhatsapp":true,"JID":"` + fone + `@s.whatsapp.net","Query":"` + fone + `"}]},"success":true}`))
			}
			return
		}
		if r.URL.Path != "/chat/send/text" {
			t.Errorf("caminho inesperado: %s", r.URL.Path)
		}
		var body struct{ Phone, Body string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		vistos = append(vistos, body.Phone)
		switch {
		case ok[body.Phone]:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"code":200,"data":{"Details":"Sent","Id":"3EB0"},"success":true}`))
		case instavelSet[body.Phone]:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":500,"error":"error sending message: context deadline exceeded","success":false}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":500,"error":"error sending message: server returned error 463","success":false}`))
		}
	}))
	return srv, &vistos
}

func waCfg(base string, extra map[string]any) map[string]any {
	cfg := map[string]any{"base_url": base, "token": "t-secreto", "to": "5511990000001"}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

// semConfirmacao devolve o *SemConfirmacao do erro, ou falha o teste dizendo o que
// veio no lugar. É o resultado esperado sempre que a aceitação veio pelo telefone.
func semConfirmacao(t *testing.T, err error) *SemConfirmacao {
	t.Helper()
	var sem *SemConfirmacao
	if !errors.As(err, &sem) {
		t.Fatalf("esperava SemConfirmacao, veio: %v", err)
	}
	return sem
}

func TestWhatsapp463NoLIDNaoCaiNoTelefone(t *testing.T) {
	// ERA AQUI QUE O PAINEL MENTIA: o LID levava 463, o telefone respondia 200 e o
	// histórico registrava "enviado" para mensagem que o WhatsApp nunca entregou.
	// O 463 é recusa do CONTATO — encerra o destinatário, não vira tentativa nova.
	srv, vistos := wuzapiFake(t, "5511990000001")
	defer srv.Close()
	s := whatsappSender{client: srv.Client()}

	err := s.Send(context.Background(), waCfg(srv.URL, map[string]any{"lid": "100000000000002"}),
		Message{Subject: "CPU alta", Text: "host=web1"})
	if err == nil {
		t.Fatal("463 no LID tem que virar falha, não pode ser encoberto pelo telefone")
	}
	var sem *SemConfirmacao
	if errors.As(err, &sem) {
		t.Fatalf("463 é recusa, não falta de confirmação: %v", err)
	}
	if len(*vistos) != 1 || (*vistos)[0] != "100000000000002@lid" {
		t.Fatalf("depois do 463 não se tenta o telefone da mesma pessoa: %v", *vistos)
	}
	if !strings.Contains(err.Error(), "463") || !strings.Contains(err.Error(), "salve o número do painel") {
		t.Errorf("o erro precisa explicar o 463: %v", err)
	}
}

func TestWhatsappErroTransitorioNoLIDAindaTentaOTelefone(t *testing.T) {
	// Falha que NÃO é recusa (indisponibilidade, timeout do wuzapi) continua valendo
	// a reserva: o que encerra o destinatário é o 463, não qualquer erro.
	srv, vistos := wuzapiFakeComInstavel(t, []string{"5511990000001"}, "100000000000002@lid")
	defer srv.Close()
	s := whatsappSender{client: srv.Client()}

	err := s.Send(context.Background(), waCfg(srv.URL, map[string]any{"lid": "100000000000002"}), Message{})
	semConfirmacao(t, err) // aceito pelo telefone: aceito não é entregue
	if len(*vistos) != 2 {
		t.Fatalf("esperava 2 tentativas (LID depois telefone), obteve %v", *vistos)
	}
	if (*vistos)[0] != "100000000000002@lid" || (*vistos)[1] != "5511990000001" {
		t.Errorf("o LID tem que vir primeiro e o telefone em seguida: %v", *vistos)
	}
}

func TestWhatsappParaNoPrimeiroSucesso(t *testing.T) {
	// LID aceito: não pode mandar de novo pelo telefone e duplicar a mensagem.
	srv, vistos := wuzapiFake(t, "100000000000002@lid")
	defer srv.Close()
	s := whatsappSender{client: srv.Client()}

	if err := s.Send(context.Background(), waCfg(srv.URL, map[string]any{"lid": "100000000000002"}), Message{}); err != nil {
		t.Fatalf("esperava sucesso: %v", err)
	}
	if len(*vistos) != 1 {
		t.Fatalf("sucesso no LID não pode gerar segunda mensagem: %v", *vistos)
	}
}

func TestWhatsappFalhaQuandoNenhumEnderecoFunciona(t *testing.T) {
	srv, vistos := wuzapiFakeComInstavel(t, nil, "100000000000002@lid") // LID instável, telefone recusado
	defer srv.Close()
	s := whatsappSender{client: srv.Client()}

	err := s.Send(context.Background(), waCfg(srv.URL, map[string]any{"lid": "100000000000002"}), Message{})
	if err == nil {
		t.Fatal("as duas formas falharam: tem que virar erro, não silêncio")
	}
	if len(*vistos) != 2 {
		t.Errorf("esperava as duas tentativas antes de desistir: %v", *vistos)
	}
	// A mensagem tem que ensinar o que fazer, não repetir o código cru.
	if !strings.Contains(err.Error(), "463") || !strings.Contains(err.Error(), "salve o número do painel") {
		t.Errorf("o erro precisa explicar o 463: %v", err)
	}
}

func TestWhatsappSemLIDNaoAfirmaQueEnviou(t *testing.T) {
	// Destinatário sem LID cadastrado (caso de dois destinatários reais): uma tentativa
	// só, o wuzapi responde 200 — e é exatamente esse 200 que não chega ao aparelho.
	// O painel devolve SemConfirmacao com a instrução, em vez de um "enviado" falso.
	srv, vistos := wuzapiFake(t, "5511990000001")
	defer srv.Close()
	s := whatsappSender{client: srv.Client()}

	sem := semConfirmacao(t, s.Send(context.Background(), waCfg(srv.URL, nil), Message{}))
	if len(*vistos) != 1 || (*vistos)[0] != "5511990000001" {
		t.Fatalf("sem LID deve tentar só o telefone: %v", *vistos)
	}
	if !strings.Contains(sem.Error(), "5511990000001") || !strings.Contains(sem.Error(), "LID") {
		t.Errorf("o aviso precisa dizer para quem foi e o que falta: %v", sem)
	}
}

func TestWhatsappEntregaPorLIDEhEnvioConfirmado(t *testing.T) {
	// A contrapartida: aceito PELO LID é o único caso em que o painel afirma envio.
	srv, _ := wuzapiFake(t, "100000000000002@lid")
	defer srv.Close()
	s := whatsappSender{client: srv.Client()}

	if err := s.Send(context.Background(), waCfg(srv.URL, map[string]any{"lid": "100000000000002"}), Message{}); err != nil {
		t.Fatalf("aceito por LID é envio: %v", err)
	}
}

// --- Conferência do número no WhatsApp (/user/check) ---
//
// Celular brasileiro tem o nono dígito, mas contas antigas seguem registradas SEM
// ele. Mandar para o número "certo" cai num JID que não existe e o WhatsApp ACEITA:
// 200, id de mensagem, nada entregue. Já houve canais de produção assim; o painel
// não conferia.

func TestWhatsappEnviaParaONumeroRealENaoParaOCadastrado(t *testing.T) {
	// Cadastro com o nono dígito, conta registrada sem ele.
	srv, vistos := wuzapiFakeOpts(t, fakeOpts{
		aceita: []string{"551190000001"},
		jid:    map[string]string{"5511990000001": "551190000001"},
	})
	defer srv.Close()
	s := whatsappSender{client: srv.Client()}

	semConfirmacao(t, s.Send(context.Background(), waCfg(srv.URL, map[string]any{"to": "5511990000001"}), Message{}))
	if len(*vistos) != 1 || (*vistos)[0] != "551190000001" {
		t.Fatalf("o envio tem que usar o JID real, não o número cadastrado: %v", *vistos)
	}
}

func TestWhatsappRecusaNumeroSemWhatsApp(t *testing.T) {
	// Enviar seria fabricar um "aceito" para mensagem que ninguém recebe.
	srv, vistos := wuzapiFakeOpts(t, fakeOpts{jid: map[string]string{"5511990000001": ""}})
	defer srv.Close()
	s := whatsappSender{client: srv.Client()}

	err := s.Send(context.Background(), waCfg(srv.URL, nil), Message{})
	if err == nil || !strings.Contains(err.Error(), "não tem WhatsApp") {
		t.Fatalf("esperava recusa explicando o número: %v", err)
	}
	if len(*vistos) != 0 {
		t.Errorf("não se envia para número que o WhatsApp diz não existir: %v", *vistos)
	}
}

func TestWhatsappSegueQuandoAConferenciaFalha(t *testing.T) {
	// Problema de infraestrutura na conferência não pode calar o alerta: no pior
	// caso volta a ser o que era antes de a conferência existir.
	srv, vistos := wuzapiFakeOpts(t, fakeOpts{
		aceita: []string{"5511990000001"},
		jid:    map[string]string{"5511990000001": "erro"},
	})
	defer srv.Close()
	s := whatsappSender{client: srv.Client()}

	semConfirmacao(t, s.Send(context.Background(), waCfg(srv.URL, nil), Message{}))
	if len(*vistos) != 1 || (*vistos)[0] != "5511990000001" {
		t.Fatalf("sem conferência, envia para o número original: %v", *vistos)
	}
}

func TestWhatsappGrupoVaiInteiroENaoEhConferido(t *testing.T) {
	// Grupo não é telefone: o normalizador apagaria o sufixo e o /user/check não
	// tem o que responder. Vai como está.
	grupo := "120363000000000000@g.us"
	srv, vistos := wuzapiFakeOpts(t, fakeOpts{aceita: []string{grupo}})
	defer srv.Close()
	s := whatsappSender{client: srv.Client()}

	if err := s.Send(context.Background(), waCfg(srv.URL, map[string]any{"to": grupo}), Message{}); err != nil {
		t.Fatalf("grupo deveria ser entregue: %v", err)
	}
	if len(*vistos) != 1 || (*vistos)[0] != grupo {
		t.Fatalf("o ID do grupo tem que chegar inteiro: %v", *vistos)
	}
}

func TestWhatsappVariosDestinatariosPareiaLIDPelaPosicao(t *testing.T) {
	srv, vistos := wuzapiFake(t, "100000000000002@lid", "5511990000002")
	defer srv.Close()
	s := whatsappSender{client: srv.Client()}

	cfg := waCfg(srv.URL, map[string]any{
		"to":  "5511990000001, 5511990000002",
		"lid": "100000000000002, ", // o 2º destinatário não tem LID
	})
	// O 1º sai confirmado (LID) e o 2º só aceito (telefone): o resultado do canal é o
	// pior dos dois, nomeando quem ficou sem confirmação.
	sem := semConfirmacao(t, s.Send(context.Background(), cfg, Message{}))
	if !strings.Contains(sem.Error(), "5511990000002") || strings.Contains(sem.Error(), "5511990000001") {
		t.Errorf("o aviso deve citar só quem ficou sem confirmação: %v", sem)
	}
	if len(*vistos) != 2 {
		t.Fatalf("esperava uma entrega por destinatário: %v", *vistos)
	}
	if (*vistos)[0] != "100000000000002@lid" || (*vistos)[1] != "5511990000002" {
		t.Errorf("LID deve parear com o destinatário da MESMA posição: %v", *vistos)
	}
}

func TestWhatsappNormalizaLIDeNumero(t *testing.T) {
	// LID colado com o sufixo já pronto não pode virar "...@lid@lid"; número sem
	// DDI ganha o 55 (conserta canal legado).
	srv, vistos := wuzapiFake(t, "100000000000002@lid")
	defer srv.Close()
	s := whatsappSender{client: srv.Client()}

	cfg := waCfg(srv.URL, map[string]any{"to": "11990000001", "lid": "100000000000002@lid"})
	if err := s.Send(context.Background(), cfg, Message{}); err != nil {
		t.Fatalf("esperava sucesso: %v", err)
	}
	if (*vistos)[0] != "100000000000002@lid" {
		t.Errorf("LID duplicou o sufixo: %v", *vistos)
	}

	srv2, vistos2 := wuzapiFake(t, "5511990000001")
	defer srv2.Close()
	s2 := whatsappSender{client: srv2.Client()}
	semConfirmacao(t, s2.Send(context.Background(), waCfg(srv2.URL, map[string]any{"to": "11990000001"}), Message{}))
	if (*vistos2)[0] != "5511990000001" {
		t.Errorf("número sem DDI deveria ganhar o 55: %v", *vistos2)
	}
}

func TestWhatsappConfigIncompleta(t *testing.T) {
	s := whatsappSender{client: &http.Client{}}
	if err := s.Send(context.Background(), map[string]any{"base_url": "http://x", "to": "5511999999999"}, Message{}); err == nil {
		t.Error("sem token deveria dar erro")
	}
	if err := s.Send(context.Background(), map[string]any{"base_url": "http://x", "token": "t"}, Message{}); err == nil {
		t.Error("sem destinatário deveria dar erro")
	}
}

func TestWhatsappAssuntoEmNegrito(t *testing.T) {
	var corpo string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Phone, Body string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		corpo = body.Body
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	s := whatsappSender{client: srv.Client()}

	_ = s.Send(context.Background(), waCfg(srv.URL, nil), Message{Subject: "🔴 CRÍTICO — CPU", Text: "host=web1"})
	if !strings.HasPrefix(corpo, "*🔴 CRÍTICO — CPU*") || !strings.Contains(corpo, "host=web1") {
		t.Errorf("assunto em negrito + corpo: %q", corpo)
	}
}

// --- Validação do LID na hora de salvar o canal ---

func TestValidarLIDs(t *testing.T) {
	// O "1" é o caso real: ficou dias salvo no canal de um destinatário, o WhatsApp
	// recusava com 463 e o histórico dizia "enviado". Barrar no formulário é onde
	// custa mais barato.
	casos := []struct {
		nome   string
		lid    string
		valido bool
	}{
		{"vazio é legítimo (nem sempre se sabe o LID)", "", true},
		{"um LID de verdade", "100000000000002", true},
		{"com o sufixo já colado", "100000000000002@lid", true},
		{"vários, um deles em branco", "100000000000002, ", true},
		{"espaços em volta", "  100000000000003  ", true},
		{"o typo que custou dias", "1", false},
		{"curto demais", "1000000000", false},
		{"longo demais", "10000000000000234", false},
		{"letra no meio", "10000000000000a", false},
		{"telefone no lugar do LID", "5511990000001", false},
		{"o segundo é que está errado", "100000000000002, 1", false},
	}
	for _, c := range casos {
		err := validarLIDs(map[string]any{"lid": c.lid})
		if c.valido && err != nil {
			t.Errorf("%s: %q deveria passar, veio %v", c.nome, c.lid, err)
		}
		if !c.valido && err == nil {
			t.Errorf("%s: %q deveria ser recusado", c.nome, c.lid)
		}
	}
	// A recusa tem que ensinar o caminho, não só dizer não.
	err := validarLIDs(map[string]any{"lid": "1"})
	if err == nil || !strings.Contains(err.Error(), "14 a 16 dígitos") || !strings.Contains(err.Error(), "mensagem para o número do painel") {
		t.Errorf("o erro precisa explicar o formato e como descobrir o LID: %v", err)
	}
}

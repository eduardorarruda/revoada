package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/safehttp"
)

// defaultSendTimeout limita o tempo total de um único envio por canal (dial +
// handshake + toda a conversa SMTP/HTTP). Sem isto, um SMTP lento/pendurado
// congela o loop de notificações de todos os canais.
const defaultSendTimeout = 15 * time.Second

// maxRespBody limita a leitura das respostas do provedor: o corpo pode trazer a
// mensagem inteira de volta e não queremos que um texto grande vire alocação sem teto.
const maxRespBody = 256 << 10

// Sender entrega uma mensagem por um tipo de canal. Novos tipos (Slack/Teams)
// só precisam implementar esta interface e registrar em senders.
type Sender interface {
	Send(ctx context.Context, cfg map[string]any, msg Message) error
}

// safeNotifyClient é o *http.Client compartilhado pelos senders HTTP de saída
// (webhook/telegram/whatsapp). Usa o transporte endurecido do safehttp: o guard
// roda após o DNS e recusa link-local/metadata da nuvem (169.254.169.254) SEMPRE,
// e loopback/privado sob REVOADA_SSRF_STRICT=1 — fechando o SSRF por onde um webhook
// admin poderia pivotar para serviços internos. CheckRedirect revalida cada salto.
func safeNotifyClient() *http.Client {
	return &http.Client{
		Timeout:       defaultSendTimeout,
		Transport:     safehttp.Transport(),
		CheckRedirect: safehttp.CheckRedirect,
	}
}

var senders = map[string]Sender{
	"smtp":     smtpSender{},
	"webhook":  webhookSender{client: safeNotifyClient()},
	"telegram": telegramSender{client: safeNotifyClient()},
	"whatsapp": whatsappSender{client: safeNotifyClient()},
}

// sendDeadline devolve o instante-limite para a conversa de rede: usa o deadline
// do contexto (imposto pela cadeia de retry) e, se ausente, cai no timeout padrão.
func sendDeadline(ctx context.Context) time.Time {
	if d, ok := ctx.Deadline(); ok {
		return d
	}
	return time.Now().Add(defaultSendTimeout)
}

// senderFor devolve o Sender para o tipo (ou erro se desconhecido).
func senderFor(typ string) (Sender, error) {
	s, ok := senders[typ]
	if !ok {
		return nil, fmt.Errorf("tipo de canal desconhecido: %s", typ)
	}
	return s, nil
}

func cfgStr(cfg map[string]any, key string) string {
	if v, ok := cfg[key].(string); ok {
		return v
	}
	return ""
}

// --- SMTP ---

type smtpSender struct{}

func (smtpSender) Send(ctx context.Context, cfg map[string]any, msg Message) error {
	host := cfgStr(cfg, "host")
	port := cfgStr(cfg, "port")
	if port == "" {
		port = "587"
	}
	from := cfgStr(cfg, "from")
	to := cfgStr(cfg, "to")
	if host == "" || from == "" || to == "" {
		return fmt.Errorf("smtp: host, from e to obrigatórios")
	}
	recipients := splitList(to)
	raw := buildEmailBody(from, recipients, msg.Subject, msg.Text, msg.HTML)

	addr := host + ":" + port
	var auth smtp.Auth
	if u := cfgStr(cfg, "username"); u != "" {
		auth = smtp.PlainAuth("", u, cfgStr(cfg, "password"), host)
	}
	// Porta 465 = TLS implícito (SMTPS): a conexão já nasce criptografada, então
	// smtp.SendMail (que fala texto e faz STARTTLS) falha com "EOF". Conduzir o SMTP
	// por cima de uma conexão TLS aberta com tls.Dial. As demais portas (587/25) seguem
	// por sendSMTPPlain, que negocia STARTTLS quando disponível.
	if port == "465" {
		return sendSMTPS(ctx, addr, host, auth, from, recipients, raw)
	}
	return sendSMTPPlain(ctx, addr, host, auth, from, recipients, raw)
}

// sendSMTPPlain entrega pelas portas 587/25 com deadline real cobrindo toda a
// conversa. Substitui smtp.SendMail, que não tem timeout: um servidor pendurado
// travaria o loop de notificações indefinidamente. Espelha a semântica do
// SendMail (STARTTLS quando ofertado; AUTH exige suporte do servidor).
func sendSMTPPlain(ctx context.Context, addr, host string, auth smtp.Auth, from string, to []string, body []byte) error {
	deadline := sendDeadline(ctx)
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp: conectando %s: %w", addr, err)
	}
	// Deadline único cobre handshake, AUTH, MAIL/RCPT/DATA e QUIT.
	_ = conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp: %w", err)
	}
	defer func() { _ = c.Close() }()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("smtp: STARTTLS: %w", err)
		}
	}
	if auth != nil {
		if ok, _ := c.Extension("AUTH"); !ok {
			return fmt.Errorf("smtp: servidor não suporta AUTH")
		}
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp: autenticação: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("smtp: MAIL FROM: %w", err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp: RCPT %s: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp: DATA: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("smtp: escrevendo corpo: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp: fechando corpo: %w", err)
	}
	return c.Quit()
}

// buildEmailBody monta a mensagem RFC 822. Com HTML, usa multipart/alternative
// (texto puro + HTML) para o cliente escolher a melhor versão; sem HTML, envia só
// texto. Assunto em RFC 2047 e corpos em base64 (UTF-8 seguro).
func buildEmailBody(from string, to []string, subject, text, htmlBody string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("UTF-8", subject))
	b.WriteString("MIME-Version: 1.0\r\n")

	if htmlBody == "" {
		b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
		b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		b.WriteString(chunk64(text))
		return b.Bytes()
	}

	const boundary = "revoada_alt_5f3c2b1a90ef4d7c"
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%s\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	b.WriteString(chunk64(text))
	fmt.Fprintf(&b, "\r\n--%s\r\n", boundary)
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	b.WriteString(chunk64(htmlBody))
	fmt.Fprintf(&b, "\r\n--%s--\r\n", boundary)
	return b.Bytes()
}

// chunk64 codifica em base64 quebrando em linhas de 76 colunas (RFC 2045).
func chunk64(s string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76])
		b.WriteString("\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc)
	return b.String()
}

// sendSMTPS entrega por TLS implícito (porta 465): abre a conexão já criptografada
// (tls) e conduz o SMTP por cima dela, sem STARTTLS. TLS sempre validado.
func sendSMTPS(ctx context.Context, addr, host string, auth smtp.Auth, from string, to []string, body []byte) error {
	deadline := sendDeadline(ctx)
	raw, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp: conectando %s: %w", addr, err)
	}
	// Deadline cobre a fase pós-handshake (AUTH, MAIL/RCPT/DATA, QUIT), que antes
	// não tinha timeout algum.
	_ = raw.SetDeadline(deadline)
	conn := tls.Client(raw, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err := conn.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return fmt.Errorf("smtp: handshake TLS com %s: %w", addr, err)
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp: %w", err)
	}
	defer func() { _ = c.Close() }()
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp: autenticação: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("smtp: MAIL FROM: %w", err)
	}
	for _, r := range to {
		if err := c.Rcpt(r); err != nil {
			return fmt.Errorf("smtp: RCPT %s: %w", r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp: DATA: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("smtp: escrevendo corpo: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp: fechando corpo: %w", err)
	}
	return c.Quit()
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// NormalizeWhatsAppNumber garante o DDI do Brasil (55) em números nacionais. A Evolution
// API rejeita o jid sem DDI (ex.: "11990000003" → {"exists":false}), então antepomos o 55
// a um número nacional (DDD + telefone: 10 ou 11 dígitos). É idempotente: um número que já
// começa com 55 e tem 12–13 dígitos (DDI + DDD + telefone) é mantido; outros formatos
// (já internacionais de outro país, ou incompletos) voltam só com os dígitos. Aplicada no
// envio (conserta canais legados) e na criação do canal pessoal (guarda já com 55).
func NormalizeWhatsAppNumber(raw string) string {
	d := digitsOnly(raw)
	if len(d) >= 12 && len(d) <= 13 && strings.HasPrefix(d, "55") {
		return d // já tem DDI do Brasil
	}
	if len(d) == 10 || len(d) == 11 {
		return "55" + d // nacional (DDD + telefone) — falta o DDI
	}
	return d
}

// digitsOnly mantém apenas os dígitos de uma string.
func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// --- Webhook genérico ---

type webhookSender struct{ client *http.Client }

func (s webhookSender) Send(ctx context.Context, cfg map[string]any, msg Message) error {
	target := cfgStr(cfg, "url")
	if target == "" {
		return fmt.Errorf("webhook: url obrigatória")
	}
	payload, _ := json.Marshal(msg)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if h, ok := cfg["headers"].(map[string]any); ok {
		for k, v := range h {
			if sv, ok := v.(string); ok {
				req.Header.Set(k, sv)
			}
		}
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook http %d", resp.StatusCode)
	}
	return nil
}

// --- Telegram (Bot API) ---

type telegramSender struct{ client *http.Client }

func (s telegramSender) Send(ctx context.Context, cfg map[string]any, msg Message) error {
	token := cfgStr(cfg, "bot_token")
	chatID := cfgStr(cfg, "chat_id")
	if token == "" || chatID == "" {
		return fmt.Errorf("telegram: bot_token e chat_id obrigatórios")
	}
	api := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	form := url.Values{}
	form.Set("chat_id", chatID)
	// O corpo (msg.Text) não repete o assunto; o cabeçalho vai como primeira linha.
	form.Set("text", msg.Subject+"\n\n"+msg.Text)
	form.Set("disable_web_page_preview", "true")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("telegram http %d", resp.StatusCode)
	}
	return nil
}

// --- WhatsApp (wuzapi, API REST sobre whatsmeow) ---

// whatsappSender entrega por uma instância do wuzapi: POST em
// {base_url}/chat/send/text, header `Token`, corpo {"Phone","Body"}. O assunto vira
// negrito (WhatsApp usa *asteriscos*) e o corpo reaproveita msg.Text (texto puro,
// como o Telegram — nada de HTML).
//
// Substituiu a Evolution API em 29/07/2026. A Evolution respondia HTTP 201 para
// mensagens que o WhatsApp recusava em seguida; o wuzapi devolve o erro na hora —
// mas SÓ quando o WhatsApp recusa. Ele também responde 200 para mensagem que o
// WhatsApp aceita e depois descarta, e é por isso que existe SemConfirmacao abaixo.
type whatsappSender struct{ client *http.Client }

// SemConfirmacao é o envio que o provedor ACEITOU sem que ninguém confirme a
// entrega. Não é sucesso nem falha: é o que o painel realmente sabe.
//
// Nasceu de testes seguidos num canal de produção em que o
// wuzapi respondeu 200, o painel registrou "enviado" e a mensagem nunca apareceu no
// aparelho. O que separa quem recebe de quem não recebe é o LID: o WhatsApp entrega
// a quem já conversou com o número do painel e descarta em silêncio o resto. Então
// o painel só afirma "enviado" quando o endereço aceito foi um LID; aceitação pelo
// número de telefone vira este estado, com a instrução do que fazer.
type SemConfirmacao struct{ Motivo string }

func (e *SemConfirmacao) Error() string { return e.Motivo }

// erroWhatsApp é a recusa devolvida pelo wuzapi, com o código HTTP dele. `recusa`
// marca o erro 463 ("reachout timelocked"): é o servidor do WhatsApp dizendo que
// não vai entregar A ESTE CONTATO — a recusa é da pessoa, não do endereço usado.
type erroWhatsApp struct {
	status int
	motivo string
	recusa bool
}

func (e *erroWhatsApp) Error() string { return fmt.Sprintf("http %d: %s", e.status, e.motivo) }

// erroSemWhatsApp é a resposta do WhatsApp de que o número não tem conta. Como a
// recusa 463, é problema do destinatário: o wuzapi está funcionando.
type erroSemWhatsApp struct{ fone string }

func (e *erroSemWhatsApp) Error() string {
	return fmt.Sprintf("o número %s não tem WhatsApp, confira o cadastro do canal", e.fone)
}

// errSemConexaoWhatsApp é o canal sem base_url/token — configuração, não queda.
var errSemConexaoWhatsApp = errors.New("whatsapp: base_url e token obrigatórios")

// FalhaWhatsApp é a recusa de um envio, destinatário por destinatário. O texto é o
// mesmo de sempre (é ele que vai para o histórico); o que muda é que quem lê o erro
// não precisa mais adivinhar a causa procurando "463" no meio de números de telefone.
type FalhaWhatsApp struct{ destinos []falhaDestino }

// falhaDestino é a falha de um destinatário. doDestinatario = TODAS as tentativas
// falharam por motivo da pessoa (recusa 463 ou número sem WhatsApp).
type falhaDestino struct {
	texto          string
	doDestinatario bool
}

func (e *FalhaWhatsApp) Error() string {
	partes := make([]string, len(e.destinos))
	for i, d := range e.destinos {
		partes[i] = d.texto
	}
	return "whatsapp: " + strings.Join(partes, " | ")
}

// soDoDestinatario diz se todos os destinatários falharam por motivo deles. Basta
// um com falha de infraestrutura para o envio contar como falha do wuzapi.
func (e *FalhaWhatsApp) soDoDestinatario() bool {
	for _, d := range e.destinos {
		if !d.doDestinatario {
			return false
		}
	}
	return len(e.destinos) > 0
}

// falhaDaPessoa diz se o erro de UMA tentativa é do destinatário.
func falhaDaPessoa(err error) bool {
	var recusa *erroWhatsApp
	var semConta *erroSemWhatsApp
	return (errors.As(err, &recusa) && recusa.recusa) || errors.As(err, &semConta)
}

// destino é um destinatário e as formas de endereçá-lo, na ordem em que serão
// tentadas.
type destino struct {
	rotulo     string
	tentativas []string
}

// Send entrega a mensagem a cada destinatário do canal e devolve o que o painel
// realmente sabe sobre cada um: nil (aceito por LID), *SemConfirmacao (aceito só
// pelo telefone) ou erro (recusado).
//
// DOIS ENDEREÇAMENTOS POR DESTINATÁRIO: um contato pode ser endereçado pelo LID ou
// pelo número de telefone, e nem sempre dá para saber de antemão qual vale. O LID
// vem primeiro porque é o único endereçamento com entrega comprovada.
//
// O 463 INTERROMPE O DESTINATÁRIO. Ele significa "não vou entregar a esta pessoa",
// não "endereço errado" — insistir pelo telefone só gasta outra abordagem recusada
// e, pior, costuma render um 200 que nunca chega. Era exatamente assim que uma
// recusa virava "enviado" no histórico: o 463 do LID era engolido pelo 200 do
// telefone. Uma recusa nunca mais é mascarada por uma tentativa seguinte.
func (s whatsappSender) Send(ctx context.Context, cfg map[string]any, msg Message) error {
	baseURL, token, err := whatsappConn(cfg)
	if err != nil {
		return err
	}
	destinos := destinosDe(cfg)
	if len(destinos) == 0 {
		return fmt.Errorf("whatsapp: nenhum destinatário válido em to")
	}
	api := baseURL + "/chat/send/text"
	text := "*" + msg.Subject + "*\n\n" + msg.Text

	var falhos []falhaDestino
	var semLID []string
	for _, d := range destinos {
		var falhas []string
		aceito := ""
		daPessoa := true
		for _, endereco := range d.tentativas {
			alvo, err := s.enderecoReal(ctx, baseURL, token, endereco)
			if err != nil {
				falhas = append(falhas, fmt.Sprintf("%s: %v", endereco, err))
				daPessoa = daPessoa && falhaDaPessoa(err)
				continue
			}
			err = s.sendOne(ctx, api, token, alvo, text)
			if err == nil {
				aceito = alvo
				break
			}
			falhas = append(falhas, fmt.Sprintf("%s: %v", alvo, err))
			daPessoa = daPessoa && falhaDaPessoa(err)
			var recusa *erroWhatsApp
			if errors.As(err, &recusa) && recusa.recusa {
				break
			}
		}
		switch {
		case aceito == "":
			falhos = append(falhos, falhaDestino{
				texto:          fmt.Sprintf("%s (%s)", d.rotulo, strings.Join(falhas, "; ")),
				doDestinatario: daPessoa && len(falhas) > 0,
			})
		case !entregaConfirmavel(aceito):
			semLID = append(semLID, d.rotulo)
		}
	}
	if len(falhos) > 0 {
		return &FalhaWhatsApp{destinos: falhos}
	}
	if len(semLID) > 0 {
		return &SemConfirmacao{Motivo: fmt.Sprintf(
			"o wuzapi aceitou a mensagem para %s, mas sem LID não dá para confirmar que ela chegou, "+
				"o WhatsApp descarta em silêncio mensagem para quem nunca conversou com o número do painel. "+
				"Peça à pessoa que envie uma mensagem para o número do painel e cadastre o LID dela neste canal",
			strings.Join(semLID, ", "))}
	}
	return nil
}

// entregaConfirmavel diz se a aceitação por este endereço vale como envio. O LID é o
// endereçamento com entrega comprovada, e o grupo (…@g.us) é conversa da qual o
// número do painel já participa. Telefone solto é o caso sem garantia nenhuma — é
// dele que trata SemConfirmacao. Os dois primeiros trazem sufixo; o telefone, não.
func entregaConfirmavel(endereco string) bool { return strings.Contains(endereco, "@") }

// enderecoReal devolve o endereço a usar de fato no envio. Endereço que já traz
// sufixo (LID ou grupo …@g.us) vai como está; telefone passa pela conferência.
func (s whatsappSender) enderecoReal(ctx context.Context, baseURL, token, endereco string) (string, error) {
	if strings.Contains(endereco, "@") {
		return endereco, nil
	}
	real, err := s.resolverNumero(ctx, baseURL, token, endereco)
	if err != nil {
		return "", err
	}
	if real == "" {
		return endereco, nil
	}
	return real, nil
}

// resolverNumero pergunta ao WhatsApp qual é o JID REAL de um telefone
// (POST /user/check) e devolve só os dígitos dele. Vazio = não deu para conferir.
//
// É o que outras integrações de WhatsApp já fazem e o painel não fazia. Celular brasileiro tem o nono
// dígito, mas contas antigas seguem registradas SEM ele: o cadastro guarda
// 5511990000001 e o WhatsApp conhece a mesma pessoa como 551190000001. Mandar para
// o número "certo" cai num JID que não existe — e o WhatsApp ACEITA, devolve id de
// mensagem e HTTP 200. Já houve canais de produção nessa situação que só não
// perdiam alerta porque tinham LID cadastrado.
//
// A postura em cada caso é deliberada:
//   - respondeu que NÃO está no WhatsApp → erro, com o número no texto. Enviar
//     seria fabricar um "aceito" para mensagem que ninguém recebe.
//   - a checagem em si falhou (rede, timeout, versão sem a rota) → segue com o
//     número original. Problema de infraestrutura na conferência não pode calar o
//     alerta: no pior caso volta a ser o que já era antes desta função existir.
func (s whatsappSender) resolverNumero(ctx context.Context, baseURL, token, fone string) (string, error) {
	body, _ := json.Marshal(map[string]any{"Phone": []string{fone}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/user/check", bytes.NewReader(body))
	if err != nil {
		return "", nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Token", token)
	resp, err := s.client.Do(req)
	if err != nil {
		return "", nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxRespBody))
	if resp.StatusCode >= 300 {
		return "", nil
	}
	var out struct {
		Success bool `json:"success"`
		Data    struct {
			Users []struct {
				IsInWhatsapp bool   `json:"IsInWhatsapp"`
				JID          string `json:"JID"`
			} `json:"Users"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || !out.Success || len(out.Data.Users) == 0 {
		return "", nil
	}
	u := out.Data.Users[0]
	if !u.IsInWhatsapp {
		return "", &erroSemWhatsApp{fone: fone}
	}
	// "551190000001@s.whatsapp.net" e "5511...:3@s.whatsapp.net" → só os dígitos.
	// O corte antes do ':' importa: o sufixo é o número do aparelho, e digitsOnly
	// sozinho o grudaria no fim do telefone.
	jid := u.JID
	if i := strings.IndexAny(jid, ":@"); i >= 0 {
		jid = jid[:i]
	}
	return digitsOnly(jid), nil
}

// destinosDe pareia `to` (telefones) com `lid` (identificadores internos do
// WhatsApp) pela posição: o LID da 2ª pessoa é o 2º da lista. Um LID sem telefone
// correspondente ainda é um destino válido; um telefone sem LID também.
func destinosDe(cfg map[string]any) []destino {
	fones := splitList(cfgStr(cfg, "to"))
	lids := splitList(cfgStr(cfg, "lid"))
	n := max(len(fones), len(lids))
	out := make([]destino, 0, n)
	for i := 0; i < n; i++ {
		var d destino
		if i < len(lids) && lids[i] != "" {
			d.tentativas = append(d.tentativas, normalizeLID(lids[i]))
		}
		if i < len(fones) && fones[i] != "" {
			// Endereço com sufixo (grupo …@g.us, ou JID já pronto) vai inteiro: o
			// normalizador só entende telefone e apagaria o sufixo, transformando um
			// grupo válido em um número que não existe.
			fone := strings.TrimSpace(fones[i])
			if !strings.Contains(fone, "@") {
				// Normaliza o número (DDI 55) para consertar canais salvos sem o
				// código do país.
				fone = NormalizeWhatsAppNumber(fone)
			}
			d.rotulo = fone
			d.tentativas = append(d.tentativas, fone)
		}
		if d.rotulo == "" && len(d.tentativas) > 0 {
			d.rotulo = d.tentativas[0]
		}
		if len(d.tentativas) > 0 {
			out = append(out, d)
		}
	}
	return out
}

// normalizeLID aceita tanto "100000000000002" quanto "100000000000002@lid": o
// sufixo é o que o wuzapi usa para distinguir de um número de telefone, então
// acrescentamos quando o usuário colar só os dígitos.
func normalizeLID(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.Contains(v, "@") {
		return v
	}
	return v + "@lid"
}

func (s whatsappSender) sendOne(ctx context.Context, api, token, destino, text string) error {
	body, _ := json.Marshal(map[string]any{"Phone": destino, "Body": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Token", token)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxRespBody))
	// O wuzapi devolve {"code":500,"error":"...","success":false} com HTTP 500 na
	// recusa; o campo `success` é a fonte da verdade, o status é confirmação.
	var out struct {
		Error   string `json:"error"`
		Success bool   `json:"success"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode >= 300 || !out.Success {
		motivo := strings.TrimSpace(out.Error)
		if motivo == "" {
			motivo = strings.TrimSpace(string(raw))
		}
		return &erroWhatsApp{
			status: resp.StatusCode,
			motivo: explicaErroWhatsApp(motivo),
			recusa: strings.Contains(motivo, "463"),
		}
	}
	return nil
}

// explicaErroWhatsApp troca o código cru do WhatsApp por uma frase que diz o que
// fazer. O 463 é o erro que derrubou 165 alertas em silêncio: vale explicá-lo em
// vez de repetir o número.
func explicaErroWhatsApp(motivo string) string {
	if strings.Contains(motivo, "463") {
		return motivo + ", o WhatsApp recusou a abordagem. Peça à pessoa que salve o número do painel no celular dela e envie uma mensagem; depois confirme o LID no cadastro do canal"
	}
	return motivo
}

// whatsappConn extrai e valida os campos de conexão do wuzapi.
func whatsappConn(cfg map[string]any) (baseURL, token string, err error) {
	baseURL = strings.TrimRight(cfgStr(cfg, "base_url"), "/")
	token = cfgStr(cfg, "token")
	if baseURL == "" || token == "" {
		return "", "", errSemConexaoWhatsApp
	}
	return baseURL, token, nil
}

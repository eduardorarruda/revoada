package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

// O vigia do WhatsApp avisa quando o próprio canal de avisos para.
//
// Ele existe por causa de dois silêncios. Em agosto/2026 o wuzapi ficou dois dias
// sem enviar (o WhatsApp recusava a versão do cliente, "Client outdated"). Em
// 15/09/2026 dez alertas falharam por queda da conexão com o WhatsApp. Nas duas
// vezes ninguém soube — o aviso de que o WhatsApp caiu teria de sair pelo WhatsApp.
//
// Por isso o vigia fala por DOIS caminhos: e-mail (o que funciona com o wuzapi
// fora) e WhatsApp (que funciona quando a falha é parcial). E por isso ele avisa
// só quem está no ambiente do servidor, nunca os canais de alerta: é um recado de
// operação para quem cuida do painel, não um alerta de cliente.
//
// Limite conhecido: o vigia roda no mesmo servidor que o wuzapi. Se a máquina
// inteira cair, ele cai junto — isso só uma checagem de fora enxerga.

const (
	// vigiaIntervalo é o passo entre checagens da sessão.
	vigiaIntervalo = time.Minute
	// vigiaChecagensParaAbrir: uma checagem ruim isolada é reconexão de rotina do
	// whatsmeow; duas seguidas (≈2 min) já são queda.
	vigiaChecagensParaAbrir = 2
	// vigiaEnviosParaAbrir: cada entrega que falha já passou por 3 tentativas com
	// backoff; duas seguidas descartam o soluço de uma só.
	vigiaEnviosParaAbrir = 2
	// vigiaLembrete é o intervalo do "continua fora" enquanto o problema durar.
	vigiaLembrete = 3 * time.Hour
	// vigiaTesteDeVolta espaça as tentativas de provar a volta com um envio real.
	vigiaTesteDeVolta = 5 * time.Minute
	// vigiaTimeoutSessao limita a consulta de status: wuzapi pendurado é queda.
	vigiaTimeoutSessao = 10 * time.Second
)

// VigiaConfig diz para quem o vigia avisa. Os dois mapas usam o mesmo formato do
// config de canal do sender correspondente; nil desliga aquele caminho.
type VigiaConfig struct {
	WhatsApp map[string]any // "to" e "lid"; a conexão vem da integração WhatsApp
	Email    map[string]any // config SMTP completo, com "to"
}

// Ativo diz se há ao menos um caminho configurado.
func (c VigiaConfig) Ativo() bool { return c.WhatsApp != nil || c.Email != nil }

// VigiaConfigDoAmbiente lê a configuração das variáveis de ambiente do servidor.
// Destinatários e senha SMTP moram no ambiente (e não numa tela) de propósito: o
// vigia avisa quem opera o painel, e senha de e-mail não entra no banco.
func VigiaConfigDoAmbiente(getenv func(string) string) VigiaConfig {
	env := func(k string) string { return strings.TrimSpace(getenv(k)) }
	var cfg VigiaConfig
	if to := env("REVOADA_VIGIA_WHATSAPP"); to != "" || env("REVOADA_VIGIA_WHATSAPP_LID") != "" {
		cfg.WhatsApp = map[string]any{"to": to, "lid": env("REVOADA_VIGIA_WHATSAPP_LID")}
	}
	if to, host := env("REVOADA_VIGIA_EMAIL"), env("REVOADA_SMTP_HOST"); to != "" && host != "" {
		from := env("REVOADA_SMTP_FROM")
		if from == "" {
			from = env("REVOADA_SMTP_USER")
		}
		cfg.Email = map[string]any{
			"host":     host,
			"port":     env("REVOADA_SMTP_PORT"),
			"username": env("REVOADA_SMTP_USER"),
			"password": getenv("REVOADA_SMTP_PASSWORD"),
			"from":     from,
			"to":       to,
		}
	}
	return cfg
}

type integracaoWhatsApp interface {
	GetWhatsAppIntegration(ctx context.Context) (store.WhatsAppIntegration, error)
}

type registroDeEnvio interface {
	LogNotification(ctx context.Context, channelID *int64, channelType, channelName, destination, routeName, subject string, count int, status, detail string) error
}

// Vigia checa a sessão do wuzapi e acompanha o resultado das entregas reais.
type Vigia struct {
	cfg    VigiaConfig
	integ  integracaoWhatsApp
	reg    registroDeEnvio
	log    *slog.Logger
	client *http.Client
	wa     Sender
	mail   Sender
	agora  func() time.Time

	mu              sync.Mutex
	falhasSessao    int
	motivoSessao    string
	falhasEnvio     int
	ultimoErroEnvio string
	aberto          bool
	causa           string // causaSessao | causaEnvio
	entregouDepois  bool   // um alerta real saiu depois da abertura
	abertoEm        time.Time
	ultimoAviso     time.Time
	ultimoTeste     time.Time
}

// NewVigia monta o vigia. `reg` pode ser nil (sem registro no histórico).
func NewVigia(cfg VigiaConfig, integ integracaoWhatsApp, reg registroDeEnvio, log *slog.Logger) *Vigia {
	return &Vigia{
		cfg:    cfg,
		integ:  integ,
		reg:    reg,
		log:    log,
		client: safeNotifyClient(),
		wa:     senders["whatsapp"],
		mail:   senders["smtp"],
		agora:  time.Now,
	}
}

// Run checa a sessão a cada minuto até o contexto acabar.
func (v *Vigia) Run(ctx context.Context) {
	if !v.cfg.Ativo() {
		v.log.Info("vigia do WhatsApp desligado: defina REVOADA_VIGIA_WHATSAPP e/ou REVOADA_VIGIA_EMAIL")
		return
	}
	if v.cfg.WhatsApp != nil && v.cfg.WhatsApp["lid"] == "" {
		v.log.Warn("vigia do WhatsApp sem LID: sem ele o WhatsApp pode aceitar e descartar o aviso, defina REVOADA_VIGIA_WHATSAPP_LID")
	}
	if v.cfg.Email == nil {
		v.log.Warn("vigia do WhatsApp sem e-mail: se o wuzapi cair, o aviso não tem por onde sair, defina REVOADA_VIGIA_EMAIL e REVOADA_SMTP_*")
	}
	t := time.NewTicker(vigiaIntervalo)
	defer t.Stop()
	for {
		v.Checar(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RegistrarEnvio recebe o resultado de cada entrega real de alerta. Só conta o que
// é falha do wuzapi: recusa do contato (463), número sem WhatsApp, aceitação sem
// confirmação e canal sem conexão configurada não são o wuzapi fora do ar.
func (v *Vigia) RegistrarEnvio(tipo, canal string, err error) {
	if v == nil || tipo != "whatsapp" {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	var semConf *SemConfirmacao
	var falha *FalhaWhatsApp
	switch {
	case err == nil || errors.As(err, &semConf):
		v.falhasEnvio, v.ultimoErroEnvio = 0, ""
		if v.aberto {
			v.entregouDepois = true
		}
	case errors.Is(err, errSemConexaoWhatsApp):
	case errors.As(err, &falha) && falha.soDoDestinatario():
	default:
		v.falhasEnvio++
		v.ultimoErroEnvio = fmt.Sprintf("canal %s, %v", canal, err)
	}
}

// resultadoSessao é o que a consulta de status mediu. `medido=false` quer dizer
// que o vigia não conseguiu olhar (integração não configurada, banco fora) — isso
// não é prova de queda nem de saúde, e a checagem é ignorada.
type resultadoSessao struct {
	medido bool
	ok     bool
	motivo string
}

// Checar faz uma rodada: consulta a sessão, decide se abre, lembra, testa a volta.
func (v *Vigia) Checar(ctx context.Context) {
	res := v.consultarSessao(ctx)

	v.mu.Lock()
	if res.medido {
		if res.ok {
			v.falhasSessao, v.motivoSessao = 0, ""
		} else {
			v.falhasSessao++
			v.motivoSessao = res.motivo
		}
	}
	agora := v.agora()
	var aviso *Message
	var volta *Message
	testarVolta := false
	switch {
	case !v.aberto && v.falhasSessao >= vigiaChecagensParaAbrir:
		v.abrirLocked(causaSessao, agora)
		m := mensagemQueda("🔴 WhatsApp do painel fora do ar · wuzapi no servidor do painel", v.motivoSessao, dicaDaSessao(v.motivoSessao), agora)
		aviso = &m
	case !v.aberto && v.falhasEnvio >= vigiaEnviosParaAbrir:
		v.abrirLocked(causaEnvio, agora)
		m := mensagemQueda("🔴 WhatsApp do painel parou de enviar · wuzapi no servidor do painel",
			fmt.Sprintf("as %d últimas entregas de alerta falharam. Último erro: %s", v.falhasEnvio, v.ultimoErroEnvio),
			"a sessão pode estar conectada e mesmo assim não entregar. Veja `docker logs wuzapi --tail 50` em 203.0.113.10.", agora)
		aviso = &m
	case v.aberto && v.falhasSessao == 0 && v.entregouDepois:
		// Um alerta real saiu depois da abertura: é a melhor prova de volta que existe.
		m := mensagemVolta(agora.Sub(v.abertoEm))
		v.fecharLocked()
		volta = &m
	case v.aberto && agora.Sub(v.ultimoAviso) >= vigiaLembrete:
		v.ultimoAviso = agora
		m := Message{
			Subject: "⏰ WhatsApp do painel continua fora há " + humanDuration(agora.Sub(v.abertoEm)) + " · wuzapi no servidor do painel",
			Text: fmt.Sprintf("O problema começou às %s e ainda não passou.\nÚltima causa medida: %s\n\nEnquanto isso, nenhum alerta do painel chega por WhatsApp.",
				horaLocal(v.abertoEm), v.causaAtualLocked()),
		}
		aviso = &m
	case v.aberto && v.falhasSessao == 0 && agora.Sub(v.ultimoTeste) >= vigiaTesteDeVolta:
		v.ultimoTeste = agora
		switch {
		case v.cfg.WhatsApp != nil:
			testarVolta = true
		case v.causa == causaSessao:
			// Sem WhatsApp para testar, a sessão saudável é a melhor prova de que a
			// queda da SESSÃO passou. Falhas de entrega anteriores não seguram o aviso.
			m := mensagemVolta(agora.Sub(v.abertoEm))
			v.fecharLocked()
			volta = &m
		}
	}
	abertoEm := v.abertoEm
	v.mu.Unlock()

	switch {
	case aviso != nil:
		_ = v.enviar(ctx, v.mail, v.cfg.Email, "smtp", *aviso)
		_ = v.enviar(ctx, v.wa, v.cfg.WhatsApp, "whatsapp", *aviso)
	case volta != nil:
		_ = v.enviar(ctx, v.mail, v.cfg.Email, "smtp", *volta)
		_ = v.enviar(ctx, v.wa, v.cfg.WhatsApp, "whatsapp", *volta)
	case testarVolta:
		v.testarVolta(ctx, abertoEm)
	}
}

// testarVolta tenta provar a volta com uma mensagem real: em 15/09 a sessão dizia
// "conectada" e nada era entregue. A própria mensagem de volta é o teste — aceita
// sem confirmação também vale, porque o wuzapi aceitar já prova que ele entrega.
func (v *Vigia) testarVolta(ctx context.Context, abertoEm time.Time) {
	volta := mensagemVolta(v.agora().Sub(abertoEm))
	err := v.enviar(ctx, v.wa, v.cfg.WhatsApp, "whatsapp", volta)
	var semConf *SemConfirmacao
	v.mu.Lock()
	if err != nil && !errors.As(err, &semConf) {
		v.ultimoErroEnvio = "o teste de volta pelo WhatsApp falhou, " + err.Error()
		v.mu.Unlock()
		return
	}
	v.fecharLocked()
	v.mu.Unlock()
	_ = v.enviar(ctx, v.mail, v.cfg.Email, "smtp", volta)
}

const (
	causaSessao = "sessao"
	causaEnvio  = "envio"
)

func (v *Vigia) abrirLocked(causa string, agora time.Time) {
	v.aberto, v.causa, v.entregouDepois = true, causa, false
	v.abertoEm, v.ultimoAviso, v.ultimoTeste = agora, agora, agora
}

func (v *Vigia) fecharLocked() {
	v.aberto, v.causa, v.entregouDepois = false, "", false
	v.falhasEnvio, v.ultimoErroEnvio = 0, ""
}

// causaAtualLocked é a última causa medida, para o lembrete nunca sair em branco.
func (v *Vigia) causaAtualLocked() string {
	switch {
	case v.motivoSessao != "":
		return v.motivoSessao
	case v.ultimoErroEnvio != "":
		return v.ultimoErroEnvio
	}
	return "a sessão voltou, mas nenhuma mensagem saiu desde então"
}

// consultarSessao pergunta ao wuzapi o estado da sessão (GET /session/status).
func (v *Vigia) consultarSessao(ctx context.Context) resultadoSessao {
	integ, err := v.integ.GetWhatsAppIntegration(ctx)
	if err != nil {
		v.log.Warn("vigia: lendo integração WhatsApp", "err", err)
		return resultadoSessao{}
	}
	base := strings.TrimRight(integ.BaseURL, "/")
	if base == "" || integ.Token == "" {
		return resultadoSessao{}
	}
	ctx, cancel := context.WithTimeout(ctx, vigiaTimeoutSessao)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/session/status", nil)
	if err != nil {
		return resultadoSessao{}
	}
	req.Header.Set("Token", integ.Token)
	resp, err := v.client.Do(req)
	if err != nil {
		return resultadoSessao{medido: true, motivo: "o wuzapi não respondeu (" + err.Error() + ")"}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxRespBody))
	if resp.StatusCode == http.StatusUnauthorized {
		return resultadoSessao{medido: true, motivo: "o wuzapi recusou o token do painel (HTTP 401)"}
	}
	if resp.StatusCode >= 300 {
		return resultadoSessao{medido: true, motivo: fmt.Sprintf("o wuzapi respondeu HTTP %d ao pedido de status", resp.StatusCode)}
	}
	// json casa as chaves sem diferenciar maiúsculas: vale para "connected" (wuzapi
	// atual) e "Connected" (versões antigas).
	var out struct {
		Data struct {
			Connected bool `json:"connected"`
			LoggedIn  bool `json:"loggedIn"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return resultadoSessao{medido: true, motivo: "o wuzapi respondeu algo que não é o status da sessão"}
	}
	// Deslogado vem antes de desconectado: sessão deslogada também aparece como
	// desconectada, e esperar a reconexão não resolve — só ler o QR code de novo.
	switch {
	case !out.Data.LoggedIn:
		return resultadoSessao{medido: true, motivo: "o número saiu da sessão (deslogado)"}
	case !out.Data.Connected:
		return resultadoSessao{medido: true, motivo: "a sessão está desconectada do WhatsApp"}
	}
	return resultadoSessao{medido: true, ok: true}
}

// enviar entrega por um caminho e registra no histórico de notificações.
func (v *Vigia) enviar(ctx context.Context, s Sender, destino map[string]any, tipo string, msg Message) error {
	if destino == nil || s == nil {
		return errors.New("caminho não configurado")
	}
	cfg := make(map[string]any, len(destino)+2)
	for k, val := range destino {
		cfg[k] = val
	}
	if tipo == "whatsapp" {
		if integ, err := v.integ.GetWhatsAppIntegration(ctx); err == nil {
			cfg["base_url"], cfg["token"] = integ.BaseURL, integ.Token
		}
	}
	// O prazo vale só para o envio: o histórico é gravado com o contexto de fora,
	// senão justamente o envio que estourou o prazo sumiria do histórico.
	sendCtx, cancel := context.WithTimeout(ctx, deliverTotalBudget)
	err := s.Send(sendCtx, cfg, msg)
	cancel()
	status, detail := "sent", ""
	var semConf *SemConfirmacao
	switch {
	case errors.As(err, &semConf):
		status, detail = statusSemConfirmacao, err.Error()
	case err != nil:
		status, detail = "error", err.Error()
		v.log.Warn("vigia: aviso não saiu", "caminho", tipo, "err", err)
	}
	if v.reg != nil {
		if lerr := v.reg.LogNotification(ctx, nil, tipo, "Vigia do WhatsApp", DestinoDo(tipo, cfg), "vigia", msg.Subject, 1, status, detail); lerr != nil {
			v.log.Warn("vigia: gravando histórico", "err", lerr)
		}
	}
	return err
}

func mensagemQueda(assunto, causa, dica string, agora time.Time) Message {
	return Message{
		Subject: assunto,
		Text: fmt.Sprintf("Desde %s: %s.\n\nEnquanto isso, nenhum alerta do painel chega por WhatsApp.\n\nO que fazer: %s",
			horaLocal(agora), causa, dica),
	}
}

func mensagemVolta(fora time.Duration) Message {
	return Message{
		Subject: "✅ WhatsApp do painel voltou · wuzapi no servidor do painel",
		Text:    "Esta mensagem saiu pelo wuzapi, o envio está funcionando de novo. Ficou fora por " + humanDuration(fora) + ".",
	}
}

// dicaDaSessao traduz a causa medida no próximo passo concreto.
func dicaDaSessao(motivo string) string {
	switch {
	case strings.Contains(motivo, "deslogado"):
		return "é preciso ler o QR code de novo no wuzapi com o celular do número do painel."
	case strings.Contains(motivo, "401"):
		return "o token do wuzapi em Integrações › WhatsApp não confere com o do wuzapi."
	case strings.Contains(motivo, "desconectada"):
		return "o wuzapi tenta reconectar sozinho. Se passar de 10 min, veja `docker logs wuzapi --tail 50` em 203.0.113.10 (\"Client outdated\" = atualizar a imagem)."
	default:
		return "confira se o container está de pé: `ssh root@203.0.113.10 'docker ps -a | grep wuzapi'`."
	}
}

// fusoBrasilia é fixo em -03:00: o Brasil não tem horário de verão desde 2019, e
// a imagem do servidor não traz a base de fusos — LoadLocation falharia calado e o
// aviso sairia em UTC, três horas "adiantado" para quem lê.
var fusoBrasilia = time.FixedZone("BRT", -3*60*60)

// horaLocal formata no fuso de Brasília, que é o de quem lê o aviso.
func horaLocal(t time.Time) string { return t.In(fusoBrasilia).Format("02/01 15:04") }

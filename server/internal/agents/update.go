package agents

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/installer"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// Rota que responde ao agente "qual versão você deveria estar rodando".
//
// # Por que a decisão mora aqui
//
// O agente também confere por conta própria que a versão anunciada é mais nova
// (ele nunca faz downgrade sozinho), mas quem decide POLÍTICA é o painel: é aqui
// que o operador desliga a auto-atualização, fixa a frota numa versão e segura
// um host específico. Política no agente seria política espalhada por N
// máquinas, e "segurar a frota" viraria um mutirão de SSH — exatamente o que
// esta função existe para acabar.
//
// # Autenticação
//
// Pela serverkey, no cabeçalho X-Revoada-Key — a mesma credencial e a mesma
// convenção do caminho de ingestão. Chave revogada não recebe resposta: um host
// que perdeu a credencial não deve continuar recebendo binários do painel.
//
// # A pergunta e o relato viajam juntos
//
// O mesmo POST carrega o que o agente conseguiu (ou não) fazer na tentativa
// anterior. Sem isso a frota volta a ser invisível: o painel saberia a versão em
// uso (que já chega pelo inventário) mas não saberia distinguir "está na versão
// certa" de "tentou atualizar quatro vezes e falhou em todas".

// PoliticaAuto é a política já resolvida para UM agente.
type PoliticaAuto struct {
	// Desligada segura este agente onde ele está.
	Desligada bool
	// Pin fixa a versão. Vazio = sem pin.
	Pin string
	// Motivo explica ao operador (via log e via resposta) por que o agente não
	// vai atualizar. "Não atualizou" sem motivo é indistinguível de "o
	// atualizador está quebrado".
	Motivo string
}

// pedidoUpdate é o corpo que o agente envia: a pergunta ("estou na versão X,
// sou linux/amd64") e o relato da tentativa anterior, juntos.
type pedidoUpdate struct {
	VersaoAtual string `json:"versao_atual"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	Hostname    string `json:"hostname"`
	Ultimo      *struct {
		Estado         string    `json:"estado"`
		Motivo         string    `json:"motivo"`
		Erro           string    `json:"erro"`
		VersaoDesejada string    `json:"versao_desejada"`
		Quando         time.Time `json:"quando"`
	} `json:"ultimo"`
}

// RelatoAgente é o que o painel guarda sobre a última tentativa de um agente.
type RelatoAgente struct {
	VersaoAtual string `json:"versao_atual"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	// Hostname é o nome que o agente diz ter. Ver store.AgentUpdateReport.Hostname:
	// é a única ponte confiável entre uma serverkey e uma linha da tabela `hosts`,
	// porque `agents.hostname` é apelido digitado por gente, não identidade de
	// máquina. Vazio = o agente não informou (a frota 0.7.0 não informa).
	Hostname       string    `json:"hostname,omitempty"`
	Estado         string    `json:"estado,omitempty"`
	Motivo         string    `json:"motivo,omitempty"`
	Erro           string    `json:"erro,omitempty"`
	VersaoDesejada string    `json:"versao_desejada,omitempty"`
	Quando         time.Time `json:"quando,omitempty"`
}

// relatoDe achata o pedido num relato. O `ultimo` é opcional: na primeira
// consulta de um agente recém-instalado ele não existe, e isso não é erro.
// maxHostname corta o hostname relatado. É um nome de máquina (limite prático de
// 253 bytes no DNS); qualquer coisa maior é engano ou tentativa de encher o jsonb
// do relato — que é gravado a cada hora por agente.
const maxHostname = 253

func relatoDe(p pedidoUpdate) RelatoAgente {
	host := strings.TrimSpace(p.Hostname)
	if len(host) > maxHostname {
		host = host[:maxHostname]
	}
	r := RelatoAgente{VersaoAtual: p.VersaoAtual, OS: p.OS, Arch: p.Arch, Hostname: host}
	if p.Ultimo != nil {
		r.Estado, r.Motivo, r.Erro = p.Ultimo.Estado, p.Ultimo.Motivo, p.Ultimo.Erro
		r.VersaoDesejada, r.Quando = p.Ultimo.VersaoDesejada, p.Ultimo.Quando
	}
	return r
}

// FonteDePolitica é de onde sai a política de atualização e para onde vai o
// relato do agente.
//
// É uma interface, e não uma chamada direta ao store, porque a persistência
// (chave em app_settings + colunas na tabela agents) fica fora dos arquivos
// desta frente. Com nil, o handler funciona com a política padrão — atualizar
// sempre que houver versão nova — e o relato do agente vai só para o log. Ver
// docs/auto-atualizacao-agentes.md para o recorte que falta ligar.
type FonteDePolitica interface {
	// PoliticaDeAtualizacao resolve global + por agente numa resposta só.
	PoliticaDeAtualizacao(ctx context.Context, serverkey string) (PoliticaAuto, error)
	// RegistrarRelato guarda o que o agente contou. Falhar aqui não pode impedir
	// a resposta: observabilidade não é pré-requisito da atualização.
	RegistrarRelato(ctx context.Context, serverkey string, r RelatoAgente) error
}

// Autenticador é o pedaço do store que esta rota usa. Interface estreita para
// que o teste não precise de um Postgres de pé para provar que chave revogada
// não recebe binário.
type Autenticador interface {
	AgentKeyActive(ctx context.Context, serverkey string) (bool, error)
}

// OrdensDeRemocao é a fonte das ordens de auto-desinstalação. Interface própria (e
// não o *store.Store inteiro) para o handler poder ser testado sem Postgres.
type OrdensDeRemocao interface {
	PendingAgentUninstall(ctx context.Context, serverkey string) (*store.AgentUninstall, error)
	MarkAgentUninstallSent(ctx context.Context, serverkey string) error
	MarkAgentUninstallReported(ctx context.Context, serverkey, result string) error
}

// UpdateHandler serve a consulta de versão do agente.
type UpdateHandler struct {
	auth     Autenticador
	art      *installer.Artefatos
	politica FonteDePolitica
	remocoes OrdensDeRemocao
	// baseURL é a URL pública do painel. A URL do binário é montada A PARTIR
	// dela, nunca a partir de algo que veio no pedido: o agente exige que o
	// download seja da mesma origem do painel configurado nele, e é esta linha
	// que faz os dois lados concordarem.
	baseURL string
	log     *slog.Logger
}

func NewUpdateHandler(auth Autenticador, art *installer.Artefatos, baseURL string, pol FonteDePolitica, rem OrdensDeRemocao, log *slog.Logger) *UpdateHandler {
	return &UpdateHandler{auth: auth, art: art, politica: pol, remocoes: rem,
		baseURL: strings.TrimRight(baseURL, "/"), log: log}
}

type respostaUpdate struct {
	Atualizar bool   `json:"atualizar"`
	Versao    string `json:"versao,omitempty"`
	URL       string `json:"url,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Tamanho   int64  `json:"tamanho,omitempty"`
	Motivo    string `json:"motivo,omitempty"`
	// Desinstalar manda o agente se REMOVER da máquina. Nunca vem junto com
	// `atualizar`: quem vai embora não precisa de versão nova.
	Desinstalar bool `json:"desinstalar,omitempty"`
}

// maxCorpoUpdate limita o pedido. É um punhado de campos curtos; qualquer coisa
// maior é engano ou abuso, e ler sem teto é entregar memória do painel a quem
// tiver uma serverkey.
const maxCorpoUpdate = 8 << 10

// Check responde qual versão este agente deve rodar.
func (h *UpdateHandler) Check(w http.ResponseWriter, r *http.Request) {
	chave := strings.TrimSpace(r.Header.Get("X-Revoada-Key"))
	if chave == "" {
		http.Error(w, "chave de ingestão ausente", http.StatusUnauthorized)
		return
	}
	ativa, err := h.auth.AgentKeyActive(r.Context(), chave)
	if err != nil {
		h.log.Error("auto-atualização: falha ao validar a chave do agente", "err", err)
		http.Error(w, "erro interno", http.StatusInternalServerError)
		return
	}
	// A ORDEM DE REMOÇÃO É CONSULTADA ANTES DA AUTENTICAÇÃO NORMAL, e é a ÚNICA coisa
	// que uma chave revogada ainda recebe.
	//
	// Quando o operador manda apagar o servidor, a chave é revogada no mesmo instante
	// (senão o host volta a ingerir e reaparece no inventário). Se a revogação também
	// calasse este canal, a ordem nunca chegaria ao agente e ele ficaria rodando na
	// máquina para sempre — que é exatamente o problema que a feature resolve. A
	// exceção é estreita de propósito: uma chave revogada recebe "desinstale-se" e
	// mais nada. Nunca um binário, nunca uma versão.
	if ordem := h.ordemDeRemocao(r.Context(), chave); ordem != nil {
		var pedido pedidoUpdate
		_ = json.NewDecoder(io.LimitReader(r.Body, maxCorpoUpdate)).Decode(&pedido)
		h.registrarRemocao(r.Context(), chave, pedido)
		escreverJSON(w, http.StatusOK, respostaUpdate{
			Desinstalar: true,
			Motivo:      "o servidor foi removido do painel; o agente deve se desinstalar",
		})
		return
	}

	if !ativa {
		// Chave inexistente e chave revogada recebem a MESMA resposta: quem chama
		// aqui não está autenticado, e separar os dois casos só ajudaria quem
		// adivinha chave. Mesma decisão da rota de inscrição.
		http.Error(w, "chave inválida ou revogada", http.StatusUnauthorized)
		return
	}

	var pedido pedidoUpdate
	if err := json.NewDecoder(io.LimitReader(r.Body, maxCorpoUpdate)).Decode(&pedido); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	relato := relatoDe(pedido)
	h.registrar(r.Context(), chave, relato)

	art, err := h.art.Para(pedido.OS, pedido.Arch)
	if err != nil {
		if errors.Is(err, installer.ErrArtefatoAusente) {
			// Não é erro do agente e não é erro de servidor: é a verdade sobre o
			// dist. 200 com `atualizar: false` e o motivo — assim o host reporta
			// "não há binário para mim" em vez de "o painel está com problema", que
			// é a diferença entre um chamado certo e uma caçada errada.
			escreverJSON(w, http.StatusOK, respostaUpdate{Atualizar: false,
				Motivo: "o painel não publica binário para " + pedido.OS + "/" + pedido.Arch})
			return
		}
		h.log.Error("auto-atualização: falha ao resolver o artefato", "os", pedido.OS, "arch", pedido.Arch, "err", err)
		http.Error(w, "erro ao consultar os artefatos publicados", http.StatusInternalServerError)
		return
	}

	pol := h.resolverPolitica(r.Context(), chave)
	if resp, parar := aplicarPolitica(pol, art, pedido.VersaoAtual); parar {
		escreverJSON(w, http.StatusOK, resp)
		return
	}

	escreverJSON(w, http.StatusOK, respostaUpdate{
		Atualizar: true,
		Versao:    art.Versao,
		URL:       h.baseURL + "/" + art.Nome,
		SHA256:    art.SHA256,
		Tamanho:   art.Tamanho,
	})
}

// aplicarPolitica decide se o agente deve ficar onde está. Devolve a resposta e
// `true` quando a conversa acaba aqui. Separada do handler para ser testável sem
// HTTP — é a função onde mora a regra que o operador usa para segurar a frota.
func aplicarPolitica(pol PoliticaAuto, art installer.Artefato, versaoAtual string) (respostaUpdate, bool) {
	if pol.Desligada {
		motivo := pol.Motivo
		if motivo == "" {
			motivo = "auto-atualização desligada no painel"
		}
		return respostaUpdate{Atualizar: false, Versao: art.Versao, Motivo: motivo}, true
	}

	// Pin com versão que o painel NÃO publica é o caso perigoso.
	//
	// O dist é plano: existe um binário por plataforma, sem subpasta por versão.
	// Então "fixado em 0.8.5" enquanto o dist publica 0.9.0 não pode virar uma URL
	// — a única URL disponível serviria 0.9.0 com o rótulo 0.8.5. O agente pegaria
	// a divergência na checagem de sanidade e recusaria, mas a frota inteira
	// gastaria a banda e reportaria erro toda hora. Segurar é responder "não
	// atualize", que é literalmente o que o operador pediu ao fixar a versão.
	if pin := strings.TrimSpace(pol.Pin); pin != "" && pin != art.Versao {
		return respostaUpdate{Atualizar: false, Versao: art.Versao,
			Motivo: "frota fixada em " + pin + "; o painel publica " + art.Versao}, true
	}

	novo, err := installer.VersaoMaisNova(art.Versao, versaoAtual)
	if err != nil {
		// Versão em execução ilegível (agente muito antigo, campo vazio). Não dá
		// para afirmar que é mais velha, e "na dúvida, troque o binário" é a
		// política errada num serviço que roda em servidor de cliente.
		return respostaUpdate{Atualizar: false, Versao: art.Versao,
			Motivo: "não consegui comparar as versões: " + err.Error()}, true
	}
	if !novo {
		return respostaUpdate{Atualizar: false, Versao: art.Versao, Motivo: "já está na versão publicada"}, true
	}
	return respostaUpdate{}, false
}

// resolverPolitica consulta a fonte configurada. Sem fonte (recorte ainda não
// ligado ao store), a política é "atualizar quando houver versão nova".
func (h *UpdateHandler) resolverPolitica(ctx context.Context, chave string) PoliticaAuto {
	if h.politica == nil {
		return PoliticaAuto{}
	}
	pol, err := h.politica.PoliticaDeAtualizacao(ctx, chave)
	if err != nil {
		// Falha ao ler a política é o único caso em que o painel escolhe NÃO
		// atualizar por precaução. Um banco fora do ar não pode virar "atualiza
		// todo mundo porque não consegui ler o pin" — o pin existe justamente para
		// os momentos em que alguém precisa segurar a frota.
		h.log.Warn("auto-atualização: política indisponível, segurando a frota por precaução", "err", err)
		return PoliticaAuto{Desligada: true, Motivo: "o painel não conseguiu ler a política de atualização"}
	}
	return pol
}

func (h *UpdateHandler) registrar(ctx context.Context, chave string, r RelatoAgente) {
	// Log estruturado sempre: mesmo sem persistência, o relato fica no journal do
	// painel e é o suficiente para responder "por que aquele host não atualizou".
	if r.Estado != "" || r.Erro != "" {
		h.log.Info("auto-atualização: relato do agente",
			"versao_atual", r.VersaoAtual, "estado", r.Estado, "erro", r.Erro, "desejada", r.VersaoDesejada)
	}
	if h.politica == nil {
		return
	}
	if err := h.politica.RegistrarRelato(ctx, chave, r); err != nil {
		// Best-effort: perder o relato não pode impedir a atualização de acontecer.
		h.log.Warn("auto-atualização: não consegui registrar o relato do agente", "err", err)
	}
}

func escreverJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// ordemDeRemocao devolve a ordem pendente daquela chave, ou nil. Falha de banco
// devolve nil de propósito: na dúvida, NÃO mandamos desinstalar. Errar para o lado
// de "não removeu" custa um agente órfão; errar para o outro lado apaga o agente de
// um servidor que ninguém mandou apagar.
func (h *UpdateHandler) ordemDeRemocao(ctx context.Context, chave string) *store.AgentUninstall {
	if h.remocoes == nil {
		return nil
	}
	ordem, err := h.remocoes.PendingAgentUninstall(ctx, chave)
	if err != nil {
		h.log.Error("auto-desinstalação: falha ao consultar a ordem pendente", "err", err)
		return nil
	}
	return ordem
}

// registrarRemocao carimba a entrega da ordem e, se o agente já veio contando o que
// aconteceu, guarda o relato dele.
//
// O relato viaja no MESMO campo `ultimo` da auto-atualização — um canal, um formato.
// `estado` vale "desinstalado" (acabou), "desinstalando" (no Linux a remoção só se
// completa no restart seguinte, pelo promotor root, então o agente confirma o início)
// ou o erro que impediu.
func (h *UpdateHandler) registrarRemocao(ctx context.Context, chave string, pedido pedidoUpdate) {
	if h.remocoes == nil {
		return
	}
	if err := h.remocoes.MarkAgentUninstallSent(ctx, chave); err != nil {
		h.log.Warn("auto-desinstalação: falha ao carimbar a entrega da ordem", "err", err)
	}
	if pedido.Ultimo == nil {
		return
	}
	estado := strings.TrimSpace(pedido.Ultimo.Estado)
	if !strings.HasPrefix(estado, "desinstal") {
		return // relato de atualização antiga; não é resposta à ordem
	}
	// Três resultados, e a diferença entre os dois primeiros decide o que a faxina
	// pode apagar. "desinstalando" é o INÍCIO — o pacote selfuninstall diz com todas
	// as letras que o painel não pode tratar isso como removido — e escrevê-lo como
	// "ok" fazia Estado() responder "concluida" para uma remoção que ainda ia
	// acontecer (ou falhar) no restart seguinte.
	//
	// O texto do erro é do agente, então é entrada não confiável num registro nosso:
	// vai truncado e em uma linha só, para não contaminar log nem tela.
	resultado := store.UninstallOK
	if e := strings.TrimSpace(pedido.Ultimo.Erro); e != "" {
		resultado = umaLinha(e, 200)
	} else if estado == "desinstalando" {
		resultado = store.UninstallIniciada
	}
	if err := h.remocoes.MarkAgentUninstallReported(ctx, chave, resultado); err != nil {
		h.log.Warn("auto-desinstalação: falha ao guardar o relato do agente", "err", err)
	}
	h.log.Info("auto-desinstalação: o agente relatou", "estado", estado, "resultado", resultado)
}

// umaLinha deixa um texto vindo do agente em condição de ser gravado e logado: sem
// quebra de linha (que injetaria linhas falsas no log do painel) e com teto de
// tamanho. O corpo da requisição já tem limite, mas 8 KiB de erro num campo de
// resultado é lixo em qualquer tela que venha a mostrá-lo.
func umaLinha(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max]
	}
	return s
}

// Package live provê atualização de painéis em tempo real via WebSocket.
// Cada conexão assina os painéis visíveis; um loop consulta as séries recentes e
// empurra deltas. (Push direto do NATS de ingestão é a otimização futura — o stream
// é WorkQueue e não pode ser co-consumido sem mudança de arquitetura.)
package live

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/authz"
	"github.com/eduardorarruda/revoada/server/internal/config"
	"github.com/eduardorarruda/revoada/server/internal/query"
)

// --- Tetos do canal ao vivo -----------------------------------------------------
//
// O que havia antes: o rate limit por usuário contava só o HANDSHAKE, e a mensagem
// "subscribe" aceitava um número ilimitado de painéis — cada um virando UMA consulta
// ao ClickHouse POR SEGUNDO. Medido em dev: uma única conexão WS autenticada com 300
// painéis levou o ClickHouse a 3,29 GiB e 152% de CPU em 10 s, com o servidor Go a
// 0,01% — quem paga a conta é o banco, e o limitador olhava para o lugar errado.

// maxPanelsPerConn é o teto de painéis assinados por conexão. Os dashboards reais do
// painel têm 7 painéis (o gerador "Visão do Host" produz exatamente 7); 40 é quase
// seis vezes o maior uso conhecido e ainda mantém o pior caso em 8 consultas/s.
// Acima do teto a lista é CORTADA (os primeiros N, que vêm ordenados pela posição na
// grade — o topo do dashboard continua ao vivo) e o cliente recebe um aviso: um
// painel que para de atualizar já aparece na tela com a idade do último dado subindo,
// então nada aqui finge estar fresco.
const maxPanelsPerConn = 40

// maxConnsPerUser é o teto de conexões ao vivo simultâneas por usuário. Uma aba de
// dashboard abre exatamente uma; 5 cobre alguém com várias abas abertas e ainda
// limita o pior caso por conta a 5 × 40 painéis a cada 5 s.
const maxConnsPerUser = 5

// tickInterval é a cadência de reconsulta. Era 1 segundo — cinco vezes mais rápido
// que o polling HTTP do próprio front (usePolling roda a 5 s) sem nenhum ganho de
// leitura: as métricas de host chegam a cada 10–15 s, então quatro em cada cinco
// consultas por segundo reliam exatamente os mesmos pontos. Alinhar em 5 s corta o
// custo do canal ao vivo em 5× e não muda o que o operador enxerga.
const tickInterval = 5 * time.Second

// Subscription é o que o cliente pede para um painel visível.
type Subscription struct {
	PanelID int               `json:"panelId"`
	Metric  string            `json:"metric"`
	Filters map[string]string `json:"filters"`
	GroupBy []string          `json:"group_by"` // chaves de label para agrupar (ex.: ["host"])
	Agg     string            `json:"agg"`
	Step    int               `json:"step"`
	Window  int               `json:"window"` // segundos de janela recente
}

type clientMsg struct {
	Type   string         `json:"type"` // "subscribe"
	Panels []Subscription `json:"panels"`
}

type serverMsg struct {
	Type    string         `json:"type"` // "update"
	PanelID int            `json:"panelId"`
	Data    query.Response `json:"data"`
	At      int64          `json:"at"`
}

// limitMsg avisa o cliente de que parte da assinatura foi recusada por teto. O front
// atual ignora tipos que não conhece, então isto não quebra nada — e deixa a
// informação disponível para a tela mostrar quando quiser.
type limitMsg struct {
	Type     string `json:"type"` // "limit"
	Accepted int    `json:"accepted"`
	Dropped  int    `json:"dropped"`
	Message  string `json:"message"`
}

// Handler serve /api/live.
type Handler struct {
	log    *slog.Logger
	q      *query.Handler
	secret string
	authz  *authz.Resolver

	// conns conta as conexões ao vivo ABERTAS por usuário. O rate limit de HTTP conta
	// requisições; aqui o que custa é a conexão que fica de pé consultando o banco,
	// então o teto tem de ser sobre o que está aberto agora, não sobre o handshake.
	connsMu sync.Mutex
	conns   map[string]int
}

func NewHandler(log *slog.Logger, q *query.Handler, secret string, resolver *authz.Resolver) *Handler {
	return &Handler{log: log, q: q, secret: secret, authz: resolver, conns: map[string]int{}}
}

// acquire reserva uma vaga de conexão para o usuário; devolve false se já estourou.
func (h *Handler) acquire(user string) bool {
	h.connsMu.Lock()
	defer h.connsMu.Unlock()
	if h.conns == nil {
		h.conns = map[string]int{}
	}
	if h.conns[user] >= maxConnsPerUser {
		return false
	}
	h.conns[user]++
	return true
}

func (h *Handler) release(user string) {
	h.connsMu.Lock()
	defer h.connsMu.Unlock()
	if h.conns[user] <= 1 {
		delete(h.conns, user) // não deixa o mapa crescer com usuários sem conexão
		return
	}
	h.conns[user]--
}

// originPatterns restringe o Origin aceito no handshake ao host público do painel.
//
// Antes era `[]string{"*"}`, ou seja, a checagem estava desligada: qualquer página na
// web podia abrir este WebSocket. O impacto hoje é baixo porque a autenticação é por
// token no query-param (não por cookie), então uma página de terceiro não teria o
// token — mas desligar uma defesa "porque outra cobre" é como o painel acaba com uma
// só. A biblioteca já aceita sozinha o Origin igual ao Host da requisição (mesma
// origem) e as conexões sem Origin (clientes que não são browser); isto acrescenta a
// origem do front quando ela é servida de outro host que não o da API.
func originPatterns() []string {
	u, err := url.Parse(config.PublicURL())
	if err != nil || u.Host == "" {
		return nil
	}
	return []string{u.Host}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Auth por query-param (o handshake WS do browser não envia Authorization).
	claims, err := auth.ParseAccessToken(h.secret, r.URL.Query().Get("access"))
	if err != nil {
		http.Error(w, "não autenticado", http.StatusUnauthorized)
		return
	}
	// Teto de conexões por usuário ANTES do upgrade: recusar com 429 dá ao cliente um
	// erro HTTP legível, em vez de um socket que abre e morre sem explicação.
	user := strconv.FormatInt(claims.Sub, 10)
	if !h.acquire(user) {
		http.Error(w, "limite de conexões ao vivo atingido (feche outras abas do painel)", http.StatusTooManyRequests)
		return
	}
	defer h.release(user)

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: originPatterns()})
	if err != nil {
		return
	}
	defer c.CloseNow()

	ctx := r.Context()
	// Enforcement por usuário: resolve o escopo e injeta no contexto para que TODA query
	// deste WS (via QuerySeries) já saia filtrada aos hosts permitidos. Fail-closed: se
	// não conseguir resolver, encerra a conexão em vez de servir dados irrestritos.
	if h.authz != nil {
		scope, err := h.authz.Resolve(ctx, claims.Sub, claims.Role)
		if err != nil {
			return
		}
		ctx = authz.WithScope(ctx, scope)
	}
	var subsMu sync.Mutex
	var subs []group

	// leitor: recebe assinaturas do cliente
	go func() {
		for {
			var msg clientMsg
			if err := wsjson.Read(ctx, c, &msg); err != nil {
				return
			}
			if msg.Type != "subscribe" {
				continue
			}
			panels := msg.Panels
			cortados := 0
			if len(panels) > maxPanelsPerConn {
				cortados = len(panels) - maxPanelsPerConn
				panels = panels[:maxPanelsPerConn]
			}
			next := coalesce(panels)
			subsMu.Lock()
			subs = next
			subsMu.Unlock()
			if cortados > 0 {
				h.log.Warn("assinatura ao vivo acima do teto de painéis",
					"usuario", claims.Sub, "pedidos", len(msg.Panels), "aceitos", maxPanelsPerConn)
				wctx, cancel := context.WithTimeout(ctx, 3*time.Second)
				_ = wsjson.Write(wctx, c, limitMsg{
					Type: "limit", Accepted: maxPanelsPerConn, Dropped: cortados,
					Message: "painéis demais nesta conexão: só os primeiros " +
						strconv.Itoa(maxPanelsPerConn) + " atualizam ao vivo",
				})
				cancel()
			}
		}
	}()

	// escritor: a cada tickInterval consulta e empurra deltas dos painéis assinados
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// snapshot sob o lock; as queries rodam fora do lock.
			subsMu.Lock()
			snapshot := append([]group(nil), subs...)
			subsMu.Unlock()
			for _, g := range snapshot {
				s := g.spec
				resp, err := h.q.QuerySeries(ctx, query.Request{
					Metric:  s.Metric,
					Filters: s.Filters,
					GroupBy: s.GroupBy,
					Agg:     s.Agg,
					Step:    orDefault(s.Step, 60),
					From:    time.Now().Add(-time.Duration(orDefault(s.Window, 3600)) * time.Second).UTC().Format(time.RFC3339),
					To:      time.Now().UTC().Format(time.RFC3339),
				})
				if err != nil || len(resp.TS) == 0 {
					continue
				}
				at := time.Now().Unix()
				// Uma consulta, N painéis: a mesma resposta é entregue a todos os
				// painéis que pediram exatamente a mesma coisa (ver coalesce).
				for _, id := range g.panelIDs {
					wctx, cancel := context.WithTimeout(ctx, 3*time.Second)
					err = wsjson.Write(wctx, c, serverMsg{Type: "update", PanelID: id, Data: resp, At: at})
					cancel()
					if err != nil {
						return
					}
				}
			}
		}
	}
}

// group é uma consulta única servindo um ou mais painéis idênticos.
type group struct {
	spec     Subscription
	panelIDs []int
}

// coalesce agrupa assinaturas IDÊNTICAS numa consulta só.
//
// É comum o mesmo dashboard trazer o mesmo gráfico duas vezes (visão geral + detalhe)
// e, com o seletor de servidor, vários painéis convergirem para o mesmo filtro. Cada
// duplicata era uma varredura extra no ClickHouse a cada tick, pagando de novo por um
// resultado idêntico. Aqui a consulta roda uma vez e a resposta é entregue a todos os
// panelIDs que a pediram.
func coalesce(panels []Subscription) []group {
	byKey := map[string]int{} // chave da assinatura → índice em out
	out := []group{}
	seen := map[int]bool{} // painelID repetido no mesmo subscribe: fica o primeiro
	for _, s := range panels {
		if seen[s.PanelID] {
			continue
		}
		seen[s.PanelID] = true
		k := specKey(s)
		if i, ok := byKey[k]; ok {
			out[i].panelIDs = append(out[i].panelIDs, s.PanelID)
			continue
		}
		byKey[k] = len(out)
		out = append(out, group{spec: s, panelIDs: []int{s.PanelID}})
	}
	return out
}

// specKey serializa tudo que muda o RESULTADO de uma assinatura (o panelId não muda).
func specKey(s Subscription) string {
	var b strings.Builder
	b.WriteString(s.Metric)
	b.WriteByte('|')
	b.WriteString(s.Agg)
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(orDefault(s.Step, 60)))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(orDefault(s.Window, 3600)))
	b.WriteString("|g:")
	b.WriteString(strings.Join(s.GroupBy, ",")) // a ordem importa: muda o map de saída
	b.WriteString("|f:")
	keys := make([]string, 0, len(s.Filters))
	for k := range s.Filters {
		keys = append(keys, k)
	}
	sort.Strings(keys) // mapa não tem ordem; a chave precisa ser estável
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(s.Filters[k])
		b.WriteByte(';')
	}
	return b.String()
}

func orDefault(v, d int) int {
	if v <= 0 {
		return d
	}
	return v
}
